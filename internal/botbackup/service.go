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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/acl"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	emailpkg "github.com/felinics/memoh/internal/email"
	fetchpkg "github.com/felinics/memoh/internal/fetchproviders"
	"github.com/felinics/memoh/internal/mcp"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	modelpkg "github.com/felinics/memoh/internal/models"
	providerpkg "github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/runtimekind"
	"github.com/felinics/memoh/internal/schedule"
	searchpkg "github.com/felinics/memoh/internal/searchproviders"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/version"
)

type WorkspaceData interface {
	ExportData(ctx context.Context, botID string) (io.ReadCloser, error)
	ImportData(ctx context.Context, botID string, r io.Reader) error
	// CountData returns the number of files in the bot's workspace /data. It is
	// best-effort context for the export dialog; an error means "unknown".
	CountData(ctx context.Context, botID string) (int, error)
}

type ACPRuntimeCloser interface {
	BeginBotHistoryReset(ctx context.Context, botID string) (resetCtx context.Context, release func(), err error)
}

type Service struct {
	logger          *slog.Logger
	db              *pgxpool.Pool
	queries         dbstore.Queries
	bots            *bots.Service
	settings        *settings.Service
	acl             *acl.Service
	channels        *channel.Store
	mcp             *mcp.ConnectionService
	schedules       *schedule.Service
	email           *emailpkg.Service
	providers       *providerpkg.Service
	models          *modelpkg.Service
	searchProviders *searchpkg.Service
	fetchProviders  *fetchpkg.Service
	memoryProviders *memprovider.Service
	workspace       WorkspaceData
	acpRuntimes     ACPRuntimeCloser
	workdirs        dbstore.BotWorkdirStore
}

const agentCredentialsWarning = "External Agent credentials were excluded from bot/profile.json; re-enter them after import" // #nosec G101 -- user-facing warning text, not a credential.

// Agent checkpoints are intentionally not part of backup schema v1. They are
// versioned against process-run rows and native session files, while history
// import remaps sessions, turns, and messages without recreating those runs.
// Keeping a promotion watermark without its matching checkpoint would make an
// imported transcript claim resumability that the bundle cannot provide.
const acpCheckpointBackupWarning = "Agent runtime checkpoints are not included in bot backups; imported agent sessions keep their visible history but start a fresh runtime conversation" // #nosec G101 -- user-facing warning text, not a credential.

type Params struct {
	Logger          *slog.Logger
	DB              *pgxpool.Pool
	Queries         dbstore.Queries
	Bots            *bots.Service
	Settings        *settings.Service
	ACL             *acl.Service
	Channels        *channel.Store
	MCP             *mcp.ConnectionService
	Schedules       *schedule.Service
	Email           *emailpkg.Service
	Providers       *providerpkg.Service
	Models          *modelpkg.Service
	SearchProviders *searchpkg.Service
	FetchProviders  *fetchpkg.Service
	MemoryProviders *memprovider.Service
	Workspace       WorkspaceData
	ACPRuntimes     ACPRuntimeCloser
	Workdirs        dbstore.BotWorkdirStore
}

func New(params Params) *Service {
	log := params.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		logger:          log.With(slog.String("service", "bot_backup")),
		db:              params.DB,
		queries:         params.Queries,
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
		acpRuntimes:     params.ACPRuntimes,
		workdirs:        params.Workdirs,
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
	if scrubbed, changed := scrubBotAgentSecretsForBackup(data.Profile); changed {
		data.Profile = scrubbed
		manifest.Warnings = append(manifest.Warnings, agentCredentialsWarning)
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
		if err := writer.writeJSON("bot/settings.json", "bot_settings", data.Settings, opts); err != nil {
			return err
		}
	}
	if opts.wants(SectionSettings) || opts.wants(SectionWorkspace) {
		if err := writer.writeJSON("bot/workspace_resource_limits.json", "bot_workspace_resource_limits", data.WorkspaceResourceLimits, opts); err != nil {
			return err
		}
	}
	// Workdirs ride with workspace data, but history sessions reference them
	// too, so either section brings them along.
	if opts.wants(SectionWorkspace) || opts.wants(SectionHistory) {
		if err := writer.writeJSON("bot/workdirs.json", "bot_workdirs", data.Workdirs, opts); err != nil {
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

func scrubBotAgentSecretsForBackup(profile any) (any, bool) {
	bot, err := roundTripJSON[bots.Bot](profile)
	if err != nil {
		return profile, false
	}
	scrubbed, changed := acpprofile.ScrubMetadataForExport(bot.Metadata)
	if changed {
		bot.Metadata = scrubbed
	}
	return bot, changed
}

func (s *Service) pauseBotForExport(ctx context.Context, botID string) (bots.Bot, func(), error) {
	if s.bots == nil {
		return bots.Bot{}, func() {}, errors.New("bot service not configured")
	}
	bot, err := s.bots.Get(ctx, botID)
	if err != nil {
		return bots.Bot{}, func() {}, fmt.Errorf("get bot before export pause: %w", err)
	}
	if !bot.IsActive {
		return bot, func() {}, nil
	}
	inactive := false
	if _, err := s.bots.Update(ctx, botID, bots.UpdateBotRequest{IsActive: &inactive}); err != nil {
		return bots.Bot{}, func() {}, fmt.Errorf("pause bot for export: %w", err)
	}
	return bot, func() {
		active := true
		if _, err := s.bots.Update(context.WithoutCancel(ctx), botID, bots.UpdateBotRequest{IsActive: &active}); err != nil {
			s.logger.Warn("failed to restore bot active state after export", slog.String("bot_id", botID), slog.Any("error", err))
		}
	}, nil
}

func (s *Service) collect(ctx context.Context, botID string, opts ExportOptions) (backupData, Manifest, error) {
	if s.bots == nil || s.settings == nil {
		return backupData{}, Manifest{}, errors.New("bot backup dependencies not configured")
	}
	bot, err := s.bots.Get(ctx, botID)
	if err != nil {
		return backupData{}, Manifest{}, fmt.Errorf("get bot: %w", err)
	}
	cfg, err := s.settings.GetBot(ctx, botID)
	if err != nil {
		return backupData{}, Manifest{}, fmt.Errorf("get bot settings: %w", err)
	}
	data := backupData{Profile: bot, Settings: cfg}
	warnings := []string(nil)
	if limits, err := s.collectWorkspaceResourceLimits(ctx, botID); err == nil {
		data.WorkspaceResourceLimits = &limits
	} else {
		warnings = append(warnings, "workspace resource limits export failed: "+err.Error())
	}
	if workdirs, skippedRemote, err := s.collectWorkdirs(ctx, botID); err == nil {
		data.Workdirs = workdirs
		if skippedRemote > 0 {
			warnings = append(warnings, fmt.Sprintf("%d remote computer workdir(s) were not exported: they reference machines outside this backup", skippedRemote))
		}
	} else {
		warnings = append(warnings, "workdir export failed: "+err.Error())
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
		SourceBotID:   bot.ID,
		SourceBotName: bot.DisplayName,
		Options:       ManifestOptions(opts),
		Warnings:      warnings,
		Checksums:     map[string]string{},
	}
	return data, manifest, nil
}

// collectWorkdirs exports the bot's native-workspace workdirs. Remote
// workdirs are counted and skipped: they pin a specific person's computer,
// which a backup restored elsewhere cannot reach.
func (s *Service) collectWorkdirs(ctx context.Context, botID string) ([]backupWorkdir, int, error) {
	if s.workdirs == nil {
		return nil, 0, nil
	}
	records, err := s.workdirs.ListWorkdirs(ctx, botID, true)
	if err != nil {
		return nil, 0, err
	}
	workdirs := make([]backupWorkdir, 0, len(records))
	skippedRemote := 0
	for _, record := range records {
		if record.RemoteBindingID != "" {
			skippedRemote++
			continue
		}
		workdirs = append(workdirs, backupWorkdir{
			ID:       record.ID,
			Name:     record.Name,
			Path:     record.Path,
			Archived: !record.ArchivedAt.IsZero(),
		})
	}
	return workdirs, skippedRemote, nil
}

func (s *Service) collectWorkspaceResourceLimits(ctx context.Context, botID string) (backupWorkspaceResourceLimits, error) {
	if s.queries == nil {
		return backupWorkspaceResourceLimits{}, errors.New("queries not configured")
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return backupWorkspaceResourceLimits{}, err
	}
	row, err := s.queries.GetBotWorkspaceResourceLimits(ctx, pgBotID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return backupWorkspaceResourceLimits{}, nil
		}
		return backupWorkspaceResourceLimits{}, err
	}
	return backupWorkspaceResourceLimitsFromRow(row), nil
}

func backupWorkspaceResourceLimitsFromRow(row dbsqlc.BotWorkspaceResourceLimit) backupWorkspaceResourceLimits {
	return backupWorkspaceResourceLimits{
		CPUMillicores: row.CpuMillicores,
		MemoryBytes:   row.MemoryBytes,
		StorageBytes:  row.StorageBytes,
	}
}

func (s *Service) collectDependencies(ctx context.Context, cfg settings.Settings, data backupData) (backupDependencies, []string) {
	var warnings []string
	modelIDs := uniqueStrings([]string{
		cfg.ChatModelID,
		cfg.ImageModelID,
		cfg.TtsModelID,
		cfg.TranscriptionModelID,
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
	memoryProviders := []memprovider.ProviderGetResponse{}
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
	if s.queries == nil {
		return backupHistory{}, []string{"history export skipped: queries not configured"}
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return backupHistory{}, []string{err.Error()}
	}
	var warnings []string
	var history backupHistory
	if sessions, err := s.queries.ListSessionsByBot(ctx, pgBotID); err == nil {
		history.Sessions = sessions
	} else {
		warnings = append(warnings, "sessions export failed: "+err.Error())
	}
	if cursors, err := s.queries.ListSessionDiscussCursorsByBot(ctx, pgBotID); err == nil {
		history.DiscussCursors = cursors
	} else {
		warnings = append(warnings, "discuss cursors export failed: "+err.Error())
	}
	if events, err := s.queries.ListSessionEventsByBot(ctx, pgBotID); err == nil {
		history.SessionEvents = events
	} else {
		warnings = append(warnings, "session events export failed: "+err.Error())
	}
	if messages, err := s.queries.ListAllMessagesForBackup(ctx, pgBotID); err == nil {
		history.Messages = messages
		// Agent runtime publication heads and JSONL snapshots live outside
		// the backup schema, so exported agent history (ACP and direct
		// runtimes alike) is never runtime-resumable.
		for _, message := range messages {
			if runtimekind.IsExternal(message.RuntimeType) {
				warnings = appendWarningOnce(warnings, acpCheckpointBackupWarning)
				break
			}
		}
		if includeAssets {
			messageIDs := make([]pgtype.UUID, 0, len(messages))
			for _, message := range messages {
				messageIDs = append(messageIDs, message.ID)
			}
			if assets, assetErr := s.queries.ListMessageAssetsBatch(ctx, messageIDs); assetErr == nil {
				history.Assets = assets
			} else {
				warnings = append(warnings, "message assets export failed: "+assetErr.Error())
			}
		}
	} else {
		warnings = append(warnings, "messages export failed: "+err.Error())
	}
	return history, warnings
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

func optionalUUID(id string) pgtype.UUID {
	if strings.TrimSpace(id) == "" {
		return pgtype.UUID{}
	}
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return pgtype.UUID{}
	}
	return parsed
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
