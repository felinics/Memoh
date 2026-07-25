package botbackup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	acpprofile "github.com/memohai/memoh/domains/agent/acp/profile"
	"github.com/memohai/memoh/domains/agent/automation/schedule"
	chatbackup "github.com/memohai/memoh/domains/agent/chat/backup"
	"github.com/memohai/memoh/domains/agent/mcp"
	"github.com/memohai/memoh/domains/api/access/acl"
	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/setting"
	channelbackup "github.com/memohai/memoh/domains/channel/backup"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/channel/gateway"
	memcatalog "github.com/memohai/memoh/domains/memory/catalog"
	modelpkg "github.com/memohai/memoh/domains/model/catalog"
	fetchpkg "github.com/memohai/memoh/domains/model/fetch"
	providerpkg "github.com/memohai/memoh/domains/model/provider"
	searchpkg "github.com/memohai/memoh/domains/model/search"
	"github.com/memohai/memoh/domains/runtime/container"
	"github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/version"
)

type WorkspaceData interface {
	ExportData(ctx context.Context, botID string) (io.ReadCloser, error)
	ImportData(ctx context.Context, botID string, r io.Reader) error
	GetResourceLimits(ctx context.Context, botID string) (*workspace.ResourceLimitsResult, error)
	SetResourceLimits(ctx context.Context, botID string, limits container.ResourceLimits) (*workspace.ResourceLimitsResult, error)
	// CountData returns the number of files in the bot's workspace /data. It is
	// best-effort context for the export dialog; an error means "unknown".
	CountData(ctx context.Context, botID string) (int, error)
}

type ChatBackup interface {
	chatbackup.ExportReader
	chatbackup.ImportWriter
	chatbackup.SummaryReader
}

type ChannelBackup interface {
	channelbackup.ExportReader
	channelbackup.ImportWriter
}

type ACPRuntimeCloser interface {
	CloseBotAgentRuntimes(botID, agentID string) error
}

type Service struct {
	logger          *slog.Logger
	bots            *bot.Service
	settings        *setting.Service
	acl             *acl.Service
	channels        *gateway.Store
	mcp             *mcp.ConnectionService
	schedules       *schedule.Service
	email           *emailpkg.Service
	providers       *providerpkg.Service
	models          *modelpkg.Service
	searchProviders *searchpkg.Service
	fetchProviders  *fetchpkg.Service
	memoryProviders *memcatalog.Service
	workspace       WorkspaceData
	chatBackup      ChatBackup
	channelBackup   ChannelBackup
	acpRuntimes     ACPRuntimeCloser
}

const acpManagedSecretsWarning = "ACP managed secrets were excluded from bot/profile.json; re-enter API keys after import" // #nosec G101 -- user-facing warning text, not a credential.

type Params struct {
	Logger          *slog.Logger
	Bots            *bot.Service
	Settings        *setting.Service
	ACL             *acl.Service
	Channels        *gateway.Store
	MCP             *mcp.ConnectionService
	Schedules       *schedule.Service
	Email           *emailpkg.Service
	Providers       *providerpkg.Service
	Models          *modelpkg.Service
	SearchProviders *searchpkg.Service
	FetchProviders  *fetchpkg.Service
	MemoryProviders *memcatalog.Service
	Workspace       WorkspaceData
	ChatBackup      ChatBackup
	ChannelBackup   ChannelBackup
	ACPRuntimes     ACPRuntimeCloser
}

func New(params Params) *Service {
	log := params.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		logger:          log.With(slog.String("service", "bot_backup")),
		bots:            params.Bots,
		settings:        params.Settings,
		acl:             params.ACL,
		channels:        params.Channels,
		mcp:             params.MCP,
		schedules:       params.Schedules,
		email:           params.Email,
		providers:       params.Providers,
		models:          params.Models,
		searchProviders: params.SearchProviders,
		fetchProviders:  params.FetchProviders,
		memoryProviders: params.MemoryProviders,
		workspace:       params.Workspace,
		chatBackup:      params.ChatBackup,
		channelBackup:   params.ChannelBackup,
		acpRuntimes:     params.ACPRuntimes,
	}
}

func NormalizeExportOptions(opts ExportOptions) ExportOptions {
	if len(opts.Sections) == 0 {
		opts.Sections = append([]Section(nil), AllExportSections...)
	}
	return opts
}

func (s *Service) Export(ctx context.Context, botID string, opts ExportOptions, dst io.Writer) error {
	opts = NormalizeExportOptions(opts)
	originalBot, restoreBot, err := s.pauseBotForExport(ctx, botID)
	if err != nil {
		return err
	}
	defer restoreBot()
	data, manifest, err := s.collect(ctx, botID, opts)
	if err != nil {
		return err
	}
	if originalBot.ID != "" {
		data.Profile = originalBot
		manifest.SourceBotName = originalBot.DisplayName
	}
	if scrubbed, changed := scrubBotACPSecretsForBackup(data.Profile); changed {
		data.Profile = scrubbed
		manifest.Warnings = append(manifest.Warnings, acpManagedSecretsWarning)
	}

	zw := zip.NewWriter(dst)
	defer func() { _ = zw.Close() }()
	writer := &zipBackupWriter{
		zw:       zw,
		manifest: &manifest,
		checksum: map[string]string{},
	}

	if err := writer.writeJSON("bot/profile.json", "bot_profile", data.Profile, opts); err != nil {
		return err
	}
	if opts.wants(SectionSettings) || opts.wants(SectionModels) {
		if err := writer.writeJSON("bot/setting.json", "bot_settings", data.Settings, opts); err != nil {
			return err
		}
	}
	if opts.wants(SectionSettings) || opts.wants(SectionWorkspace) {
		if err := writer.writeJSON("bot/workspace_resource_limits.json", "bot_workspace_resource_limits", data.WorkspaceResourceLimits, opts); err != nil {
			return err
		}
	}
	if opts.wants(SectionACL) {
		if err := writer.writeJSON("bot/acl_rules.json", "bot_acl_rules", data.ACLRules, opts); err != nil {
			return err
		}
	}
	if opts.wants(SectionChannels) {
		if err := writer.writeJSON("bot/channel_configs.json", "bot_channel_configs", data.Channels, opts); err != nil {
			return err
		}
	}
	if opts.wants(SectionMCP) {
		if err := writer.writeJSON("bot/mcp_connections.json", "bot_mcp_connections", data.MCP, opts); err != nil {
			return err
		}
	}
	if opts.wants(SectionSchedules) {
		if err := writer.writeJSON("bot/schedules.json", "bot_schedules", data.Schedules, opts); err != nil {
			return err
		}
	}
	if opts.wants(SectionEmail) {
		if err := writer.writeJSON("bot/email_bindings.json", "bot_email_bindings", data.EmailBindings, opts); err != nil {
			return err
		}
	}
	// Dependencies back the model config (providers/models/search/memory) and
	// email bindings (email providers); include them when either is exported.
	if opts.wants(SectionModels) || opts.wants(SectionEmail) {
		if err := writer.writeJSON("dependencies/providers.json", "providers", data.Dependencies.Providers, opts); err != nil {
			return err
		}
		if err := writer.writeJSON("dependencies/models.json", "models", data.Dependencies.Models, opts); err != nil {
			return err
		}
		if err := writer.writeJSON("dependencies/search_providers.json", "search_providers", data.Dependencies.SearchProviders, opts); err != nil {
			return err
		}
		if err := writer.writeJSON("dependencies/fetch_providers.json", "fetch_providers", data.Dependencies.FetchProviders, opts); err != nil {
			return err
		}
		if err := writer.writeJSON("dependencies/memory_providers.json", "memory_providers", data.Dependencies.MemoryProviders, opts); err != nil {
			return err
		}
		if err := writer.writeJSON("dependencies/email_providers.json", "email_providers", data.Dependencies.EmailProviders, opts); err != nil {
			return err
		}
	}
	if opts.wants(SectionHistory) {
		if err := writer.writeJSON("history/sessions.json", "bot_sessions", data.History.Sessions, opts); err != nil {
			return err
		}
		if err := writer.writeJSON("history/messages.json", "bot_messages", data.History.Messages, opts); err != nil {
			return err
		}
		if err := writer.writeJSON("history/discuss_cursors.json", "bot_session_discuss_cursors", data.History.DiscussCursors, opts); err != nil {
			return err
		}
		if err := writer.writeJSON("history/session_events.json", "bot_session_events", data.History.SessionEvents, opts); err != nil {
			return err
		}
		if err := writer.writeJSON("history/route_active_sessions.json", "bot_route_active_sessions", data.History.RouteActiveSessions, opts); err != nil {
			return err
		}
	}
	if opts.wants(SectionAssets) {
		if err := writer.writeJSON("assets/message_assets.json", "bot_message_assets", data.History.Assets, opts); err != nil {
			return err
		}
	}
	if opts.wants(SectionWorkspace) && s.workspace != nil {
		if err := writer.writeWorkspace(ctx, botID, s.workspace, opts); err != nil {
			manifest.Warnings = append(manifest.Warnings, "workspace export failed: "+err.Error())
		}
	}
	manifest.Checksums = writer.checksum
	return writer.writeManifest()
}

func scrubBotACPSecretsForBackup(profile any) (any, bool) {
	record, err := roundTripJSON[bot.Bot](profile)
	if err != nil {
		return profile, false
	}
	scrubbed, changed := acpprofile.ScrubMetadataForExport(record.Metadata)
	if changed {
		record.Metadata = scrubbed
	}
	return record, changed
}

func (s *Service) pauseBotForExport(ctx context.Context, botID string) (bot.Bot, func(), error) {
	if s.bots == nil {
		return bot.Bot{}, func() {}, errors.New("bot service not configured")
	}
	record, err := s.bots.Get(ctx, botID)
	if err != nil {
		return bot.Bot{}, func() {}, fmt.Errorf("get bot before export pause: %w", err)
	}
	if !record.IsActive {
		return record, func() {}, nil
	}
	inactive := false
	if _, err := s.bots.Update(ctx, botID, bot.UpdateBotRequest{IsActive: &inactive}); err != nil {
		return bot.Bot{}, func() {}, fmt.Errorf("pause bot for export: %w", err)
	}
	return record, func() {
		active := true
		if _, err := s.bots.Update(context.WithoutCancel(ctx), botID, bot.UpdateBotRequest{IsActive: &active}); err != nil {
			s.logger.WarnContext(ctx, "failed to restore bot active state after export", slog.String("bot_id", botID), slog.Any("error", err))
		}
	}, nil
}

func (s *Service) collect(ctx context.Context, botID string, opts ExportOptions) (backupData, Manifest, error) {
	if s.bots == nil || s.settings == nil {
		return backupData{}, Manifest{}, errors.New("bot backup dependencies not configured")
	}
	record, err := s.bots.Get(ctx, botID)
	if err != nil {
		return backupData{}, Manifest{}, fmt.Errorf("get bot: %w", err)
	}
	cfg, err := s.settings.GetBot(ctx, botID)
	if err != nil {
		return backupData{}, Manifest{}, fmt.Errorf("get bot settings: %w", err)
	}
	data := backupData{Profile: record, Settings: cfg}
	warnings := []string(nil)
	if limits, err := s.collectWorkspaceResourceLimits(ctx, botID); err == nil {
		data.WorkspaceResourceLimits = &limits
	} else {
		warnings = append(warnings, "workspace resource limits export failed: "+err.Error())
	}

	if s.acl != nil {
		if rows, err := s.acl.ListRules(ctx, botID); err == nil {
			data.ACLRules = rows
		} else {
			warnings = append(warnings, "acl export failed: "+err.Error())
		}
	}
	if s.channels != nil {
		if rows, err := s.channels.ListConfigs(ctx, botID); err == nil {
			data.Channels = rows
		} else {
			warnings = append(warnings, "channel config export failed: "+err.Error())
		}
	}
	if s.mcp != nil {
		if rows, err := s.mcp.ListByBot(ctx, botID); err == nil {
			data.MCP = rows
		} else {
			warnings = append(warnings, "mcp export failed: "+err.Error())
		}
	}
	if s.schedules != nil {
		if rows, err := s.schedules.List(ctx, botID); err == nil {
			data.Schedules = rows
		} else {
			warnings = append(warnings, "schedule export failed: "+err.Error())
		}
	}
	if s.email != nil {
		if rows, err := s.email.ListBindings(ctx, botID); err == nil {
			data.EmailBindings = rows
		} else {
			warnings = append(warnings, "email binding export failed: "+err.Error())
		}
	}
	deps, depWarnings := s.collectDependencies(ctx, cfg, data)
	data.Dependencies = deps
	warnings = append(warnings, depWarnings...)
	if opts.wants(SectionHistory) || opts.wants(SectionAssets) {
		history, histWarnings := s.collectHistory(ctx, botID, opts.wants(SectionAssets))
		data.History = history
		warnings = append(warnings, histWarnings...)
	}
	manifest := Manifest{
		SchemaVersion: BackupSchemaVersion,
		App:           "memoh/" + version.Version,
		ExportedAt:    time.Now().UTC(),
		SourceBotID:   record.ID,
		SourceBotName: record.DisplayName,
		Options:       ManifestOptions(opts),
		Warnings:      warnings,
		Checksums:     map[string]string{},
	}
	return data, manifest, nil
}

func (s *Service) collectWorkspaceResourceLimits(ctx context.Context, botID string) (backupWorkspaceResourceLimits, error) {
	if s.workspace == nil {
		return backupWorkspaceResourceLimits{}, errors.New("workspace manager not configured")
	}
	result, err := s.workspace.GetResourceLimits(ctx, botID)
	if err != nil {
		return backupWorkspaceResourceLimits{}, err
	}
	if result == nil {
		return backupWorkspaceResourceLimits{}, nil
	}
	return backupWorkspaceResourceLimitsFromContainer(result.Desired), nil
}

func backupWorkspaceResourceLimitsFromContainer(limits container.ResourceLimits) backupWorkspaceResourceLimits {
	return backupWorkspaceResourceLimits{
		CPUMillicores: limits.CPUMillicores,
		MemoryBytes:   limits.MemoryBytes,
		StorageBytes:  limits.StorageBytes,
	}
}

func (s *Service) collectDependencies(ctx context.Context, cfg setting.Settings, data backupData) (backupDependencies, []string) {
	var warnings []string
	modelIDs := uniqueStrings([]string{
		cfg.ChatModelID,
		cfg.ImageModelID,
		cfg.TtsModelID,
		cfg.TranscriptionModelID,
		cfg.HeartbeatModelID,
		cfg.CompactionModelID,
		cfg.DiscussProbeModelID,
	})
	models := make([]modelpkg.GetResponse, 0, len(modelIDs))
	providerIDs := make([]string, 0, len(modelIDs))
	for _, id := range modelIDs {
		if s.models == nil || id == "" {
			continue
		}
		model, err := s.models.GetByID(ctx, id)
		if err != nil {
			warnings = append(warnings, "model dependency missing: "+id)
			continue
		}
		models = append(models, model)
		providerIDs = append(providerIDs, model.ProviderID)
	}
	providers := make([]providerpkg.GetResponse, 0, len(providerIDs))
	for _, id := range uniqueStrings(providerIDs) {
		if s.providers == nil || id == "" {
			continue
		}
		provider, err := s.providers.Get(ctx, id)
		if err != nil {
			warnings = append(warnings, "provider dependency missing: "+id)
			continue
		}
		providers = append(providers, provider)
	}
	searchProviders := []searchpkg.GetResponse{}
	if s.searchProviders != nil && cfg.SearchProviderID != "" {
		if item, err := s.searchProviders.Get(ctx, cfg.SearchProviderID); err == nil {
			searchProviders = append(searchProviders, item)
		} else {
			warnings = append(warnings, "search provider dependency missing: "+cfg.SearchProviderID)
		}
	}
	fetchProviders := []fetchpkg.GetResponse{}
	if s.fetchProviders != nil && cfg.FetchProviderID != "" {
		if item, err := s.fetchProviders.Get(ctx, cfg.FetchProviderID); err == nil {
			fetchProviders = append(fetchProviders, item)
		} else {
			warnings = append(warnings, "fetch provider dependency missing: "+cfg.FetchProviderID)
		}
	}
	memoryProviders := []memcatalog.ProviderGetResponse{}
	if s.memoryProviders != nil && cfg.MemoryProviderID != "" {
		if item, err := s.memoryProviders.Get(ctx, cfg.MemoryProviderID); err == nil {
			memoryProviders = append(memoryProviders, item)
		} else {
			warnings = append(warnings, "memory provider dependency missing: "+cfg.MemoryProviderID)
		}
	}
	emailProviders := s.collectEmailProviderDependencies(ctx, data)
	return backupDependencies{
		Providers:       providers,
		Models:          models,
		SearchProviders: searchProviders,
		FetchProviders:  fetchProviders,
		MemoryProviders: memoryProviders,
		EmailProviders:  emailProviders,
	}, warnings
}

func (s *Service) collectEmailProviderDependencies(ctx context.Context, data backupData) []emailpkg.ProviderResponse {
	bindings, err := roundTripJSON[[]emailpkg.BindingResponse](data.EmailBindings)
	if err != nil || s.email == nil {
		return nil
	}
	out := make([]emailpkg.ProviderResponse, 0, len(bindings))
	seen := map[string]struct{}{}
	for _, binding := range bindings {
		id := strings.TrimSpace(binding.EmailProviderID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if provider, err := s.email.GetProviderInternal(ctx, id); err == nil {
			out = append(out, provider)
		}
	}
	return out
}

func (s *Service) collectHistory(ctx context.Context, botID string, includeAssets bool) (backupHistory, []string) {
	var warnings []string
	var history backupHistory
	if s.chatBackup == nil {
		warnings = append(warnings, "chat history export skipped: chat backup not configured")
	} else {
		snapshot, err := s.chatBackup.Export(ctx, chatbackup.ExportRequest{
			BotID:         botID,
			IncludeAssets: includeAssets,
		})
		if err != nil {
			warnings = append(warnings, "chat history export failed: "+err.Error())
		} else {
			history.Sessions = snapshot.Sessions
			history.Messages = snapshot.Messages
			history.Assets = snapshot.Assets
		}
	}
	if s.channelBackup == nil {
		warnings = append(warnings, "channel observations export skipped: channel backup not configured")
	} else {
		snapshot, err := s.channelBackup.Export(ctx, botID)
		if err != nil {
			warnings = append(warnings, "channel observations export failed: "+err.Error())
		} else {
			history.DiscussCursors = snapshot.DiscussCursors
			history.SessionEvents = snapshot.SessionEvents
			history.RouteActiveSessions = backupRouteSessionsFromOwner(snapshot.RouteActiveSessions)
		}
	}
	return history, warnings
}

func backupRouteSessionsFromOwner(items []channelbackup.RouteActiveSession) []backupRouteActiveSession {
	result := make([]backupRouteActiveSession, 0, len(items))
	for _, item := range items {
		result = append(result, backupRouteActiveSession{
			RouteID:   item.RouteID,
			SessionID: item.SessionID,
		})
	}
	return result
}

type zipBackupWriter struct {
	zw       *zip.Writer
	manifest *Manifest
	checksum map[string]string
}

func (w *zipBackupWriter) writeJSON(path, entryType string, value any, _ ExportOptions) error {
	if value == nil {
		value = []any{}
	}
	raw, err := marshalJSON(value)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := w.writeRaw(path, raw); err != nil {
		return err
	}
	w.appendEntry(path, entryType)
	return nil
}

func (w *zipBackupWriter) writeWorkspace(ctx context.Context, botID string, workspace WorkspaceData, _ ExportOptions) error {
	rc, err := workspace.ExportData(ctx, botID)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	// Store the container's tar.gz verbatim as a single entry. This preserves
	// Unix permissions/symlinks and avoids re-packing the archive on import.
	// Use zip.Store (no compression): the blob is already gzip-compressed, so
	// re-deflating it only burns CPU without shrinking the bundle.
	if err := w.writeStream(workspaceArchivePath, rc, 0o640, time.Time{}, zip.Store); err != nil {
		return err
	}
	w.manifest.Entries = append(w.manifest.Entries, ManifestEntry{
		Path: workspaceArchivePath,
		Type: "workspace_data",
	})
	return nil
}

func (w *zipBackupWriter) writeManifest() error {
	raw, err := marshalJSON(w.manifest)
	if err != nil {
		return err
	}
	return w.writeRaw(ManifestPath, raw)
}

func (w *zipBackupWriter) writeRaw(path string, raw []byte) error {
	return w.writeStream(path, bytes.NewReader(raw), 0o640, time.Time{}, zip.Deflate)
}

// writeStream copies r into a zip entry. method selects the zip compression:
// zip.Deflate for compressible payloads (JSON), zip.Store for content that is
// already compressed (e.g. the workspace tar.gz) to avoid double compression.
func (w *zipBackupWriter) writeStream(path string, r io.Reader, mode os.FileMode, modTime time.Time, method uint16) error {
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return fmt.Errorf("unsafe backup path: %s", path)
	}
	header := &zip.FileHeader{Name: clean, Method: method}
	if !modTime.IsZero() {
		header.Modified = modTime
	}
	if mode == 0 {
		mode = 0o640
	}
	header.SetMode(mode)
	f, err := w.zw.CreateHeader(header)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(f, io.TeeReader(r, hash))
	w.checksum[clean] = hex.EncodeToString(hash.Sum(nil))
	return err
}

func (w *zipBackupWriter) appendEntry(path, entryType string) {
	w.manifest.Entries = append(w.manifest.Entries, ManifestEntry{Path: path, Type: entryType})
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

type backupZipEntry struct {
	data    []byte
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func readZipEntries(raw []byte) (map[string]backupZipEntry, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, err
	}
	out := make(map[string]backupZipEntry, len(zr.File))
	for _, file := range zr.File {
		name, err := normalizeZipPath(file.Name)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(file.Name, "/") && !strings.HasSuffix(name, "/") {
			name += "/"
		}
		entry := backupZipEntry{
			mode:    file.Mode(),
			modTime: file.ModTime(),
			isDir:   strings.HasSuffix(name, "/"),
		}
		if entry.isDir {
			out[name] = entry
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(rc)
		_ = rc.Close()
		if readErr != nil {
			return nil, readErr
		}
		entry.data = data
		out[name] = entry
	}
	return out, nil
}

func normalizeZipPath(path string) (string, error) {
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("unsafe backup path: %s", path)
	}
	return clean, nil
}
