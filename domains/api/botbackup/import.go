package botbackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/memohai/memoh/domains/api/internal/backup/secure"
	"github.com/memohai/memoh/domains/api/setting"
	channelbackup "github.com/memohai/memoh/domains/channel/backup"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/channel/gateway"
	memcatalog "github.com/memohai/memoh/domains/memory/catalog"
	modeldomain "github.com/memohai/memoh/domains/model"
	modelpkg "github.com/memohai/memoh/domains/model/catalog"
	fetchpkg "github.com/memohai/memoh/domains/model/fetch"
	providerpkg "github.com/memohai/memoh/domains/model/provider"
	searchpkg "github.com/memohai/memoh/domains/model/search"
	"github.com/memohai/memoh/domains/runtime/container"
)

type importState struct {
	entries  map[string]backupZipEntry
	manifest Manifest
	warnings []string
	// counts records how many items each section restored, surfaced to the UI.
	counts map[Section]int
	// createMode is true for a fresh-bot import. In create mode any restore
	// failure is fatal (the caller compensates by deleting the bot); in
	// overwrite mode item failures degrade to warnings.
	createMode bool
}

const (
	workspaceRestoreRetryTimeout  = 2 * time.Minute
	workspaceRestoreRetryInterval = 2 * time.Second
)

// decodeBundle returns the plaintext bundle bytes, transparently decrypting an
// encrypted bundle with the passphrase. The bool reports whether the input was
// encrypted, so callers can prompt for a passphrase when one is missing or wrong.
func decodeBundle(raw []byte, passphrase string) (plaintext []byte, encrypted bool, err error) {
	if !secure.IsEncrypted(raw) {
		return raw, false, nil
	}
	if passphrase == "" {
		return nil, true, secure.ErrPassphraseRequired
	}
	var out bytes.Buffer
	if err := secure.Decrypt(&out, bytes.NewReader(raw), passphrase); err != nil {
		return nil, true, err
	}
	return out.Bytes(), true, nil
}

// itemErr decides how a per-item restore failure is handled: fatal in create
// mode (so the caller rolls back the whole bot), a warning in overwrite mode so
// one bad row does not abort an otherwise-good restore.
func (st *importState) itemErr(label string, err error) error {
	if st.createMode {
		return err
	}
	st.warnings = append(st.warnings, label+" skipped: "+err.Error())
	return nil
}

func (s *Service) Preview(ctx context.Context, raw []byte, opts ImportOptions, passphrase string) (PreviewResult, error) {
	plain, encrypted, decErr := decodeBundle(raw, passphrase)
	if decErr != nil {
		// Encrypted but no/wrong passphrase: return a soft result so the UI can
		// (re-)prompt instead of surfacing a hard error.
		res := PreviewResult{Encrypted: true, RequiresPassphrase: true}
		if !errors.Is(decErr, secure.ErrPassphraseRequired) {
			res.Conflicts = []string{"passphrase incorrect or bundle corrupted"}
		}
		return res, nil
	}
	entries, manifest, err := loadManifest(plain)
	if err != nil {
		return PreviewResult{}, err
	}
	result := PreviewResult{
		Manifest:  manifest,
		Profile:   profilePreview(entries),
		Warnings:  append([]string(nil), manifest.Warnings...),
		Sections:  summarizeSections(entries),
		Encrypted: encrypted,
		RestorePlan: RestorePlan{
			Mode:                 normalizeImportMode(opts.Mode),
			TargetBotID:          strings.TrimSpace(opts.TargetBotID),
			WillCreateBot:        normalizeImportMode(opts.Mode) != ImportModeOverwrite,
			WillRestoreWorkspace: opts.wants(SectionWorkspace) && hasWorkspaceEntries(entries),
			DependencyMatches:    map[string]int{},
		},
	}
	if manifest.SchemaVersion != BackupSchemaVersion {
		result.Conflicts = append(result.Conflicts, fmt.Sprintf("unsupported schema version %d", manifest.SchemaVersion))
	}
	if result.RestorePlan.Mode == ImportModeOverwrite && result.RestorePlan.TargetBotID == "" {
		result.Missing = append(result.Missing, "target_bot_id")
	}
	// In overwrite mode, annotate each section with the target bot's current
	// item count so the UI can flag conflicts and offer skip/merge/replace.
	if result.RestorePlan.Mode == ImportModeOverwrite && result.RestorePlan.TargetBotID != "" {
		counts := s.targetSectionCounts(ctx, result.RestorePlan.TargetBotID)
		for i := range result.Sections {
			tc := counts[result.Sections[i].Key]
			result.Sections[i].TargetCount = tc
			result.Sections[i].Conflict = result.Sections[i].Count > 0 && tc > 0
		}
	}
	return result, nil
}

// profilePreview extracts the backup's bot identity for display.
func profilePreview(entries map[string]backupZipEntry) *ProfilePreview {
	entry, ok := entries["bot/profile.json"]
	if !ok || len(entry.data) == 0 {
		return nil
	}
	var b bot.Bot
	if err := unmarshalJSON(entry.data, &b); err != nil {
		return nil
	}
	return &ProfilePreview{
		DisplayName: b.DisplayName,
		AvatarURL:   b.AvatarURL,
		Timezone:    b.Timezone,
		IsActive:    b.IsActive,
	}
}

// targetSectionCounts returns how many items the target bot currently has per
// section, used to detect overwrite conflicts.
func (s *Service) targetSectionCounts(ctx context.Context, botID string) map[Section]int {
	out := map[Section]int{SectionSettings: 1, SectionModels: 1}
	if s.acl != nil {
		if rows, err := s.acl.ListRules(ctx, botID); err == nil {
			out[SectionACL] = len(rows)
		}
	}
	if s.channels != nil {
		if rows, err := s.channels.ListConfigs(ctx, botID); err == nil {
			out[SectionChannels] = len(rows)
		}
	}
	if s.mcp != nil {
		if rows, err := s.mcp.ListByBot(ctx, botID); err == nil {
			out[SectionMCP] = len(rows)
		}
	}
	if s.schedules != nil {
		if rows, err := s.schedules.List(ctx, botID); err == nil {
			out[SectionSchedules] = len(rows)
		}
	}
	if s.email != nil {
		if rows, err := s.email.ListBindings(ctx, botID); err == nil {
			out[SectionEmail] = len(rows)
		}
	}
	if s.chatBackup != nil {
		if summary, err := s.chatBackup.Summary(ctx, botID); err == nil {
			out[SectionHistory] = summary.Messages
		}
	}
	return out
}

// clear* helpers remove all existing items in a section before a "replace"
// import restores the backup's items.
func (s *Service) clearACL(ctx context.Context, botID string) {
	if s.acl == nil {
		return
	}
	rows, err := s.acl.ListRules(ctx, botID)
	if err != nil {
		return
	}
	for _, r := range rows {
		_ = s.acl.DeleteRule(ctx, r.ID)
	}
}

func (s *Service) clearChannels(ctx context.Context, botID string) {
	if s.channels == nil {
		return
	}
	rows, err := s.channels.ListConfigs(ctx, botID)
	if err != nil {
		return
	}
	for _, c := range rows {
		_ = s.channels.DeleteConfig(ctx, botID, c.ChannelType)
	}
}

func (s *Service) clearMCP(ctx context.Context, botID string) {
	if s.mcp == nil {
		return
	}
	rows, err := s.mcp.ListByBot(ctx, botID)
	if err != nil {
		return
	}
	for _, m := range rows {
		_ = s.mcp.Delete(ctx, botID, m.ID)
	}
}

func (s *Service) clearSchedules(ctx context.Context, botID string) {
	if s.schedules == nil {
		return
	}
	rows, err := s.schedules.List(ctx, botID)
	if err != nil {
		return
	}
	for _, x := range rows {
		_ = s.schedules.Delete(ctx, x.ID)
	}
}

func (s *Service) clearEmailBindings(ctx context.Context, botID string) {
	if s.email == nil {
		return
	}
	rows, err := s.email.ListBindings(ctx, botID)
	if err != nil {
		return
	}
	for _, b := range rows {
		_ = s.email.DeleteBinding(ctx, b.ID)
	}
}

// summarizeSections lists the sections a backup contains (i.e. whose file was
// written at export time), with item counts and a sample of item labels. A
// section is shown even when its count is 0, so import mirrors the section set
// chosen at export.
func summarizeSections(entries map[string]backupZipEntry) []SectionSummary {
	out := []SectionSummary{}
	add := func(key Section, path string, count int, items []string) {
		if _, ok := entries[path]; ok {
			out = append(out, SectionSummary{Key: key, Count: count, Items: items, Sensitive: isSensitiveSection(key)})
		}
	}
	// setting.json backs both the behavior settings and the model config cards.
	if _, ok := entries["bot/setting.json"]; ok {
		out = append(out, SectionSummary{
			Key:   SectionSettings,
			Count: 1,
			Items: settingsLabels(entries["bot/setting.json"].data),
		})
		out = append(out, SectionSummary{
			Key:       SectionModels,
			Count:     countArrayEntry(entries, "dependencies/models.json"),
			Sensitive: true,
			Items:     jsonArrayLabels(entries["dependencies/models.json"].data, sectionItemLimit, "name", "model_id"),
		})
	}
	add(SectionACL, "bot/acl_rules.json", countArrayEntry(entries, "bot/acl_rules.json"),
		jsonArrayLabels(entries["bot/acl_rules.json"].data, sectionItemLimit, "description", "subject_channel_type"))
	add(SectionChannels, "bot/channel_configs.json", countArrayEntry(entries, "bot/channel_configs.json"),
		jsonArrayLabels(entries["bot/channel_configs.json"].data, sectionItemLimit, "channel_type"))
	add(SectionMCP, "bot/mcp_connections.json", countArrayEntry(entries, "bot/mcp_connections.json"),
		jsonArrayLabels(entries["bot/mcp_connections.json"].data, sectionItemLimit, "name"))
	add(SectionSchedules, "bot/schedules.json", countArrayEntry(entries, "bot/schedules.json"),
		jsonArrayLabels(entries["bot/schedules.json"].data, sectionItemLimit, "name"))
	add(SectionEmail, "bot/email_bindings.json", countArrayEntry(entries, "bot/email_bindings.json"),
		jsonArrayLabels(entries["bot/email_bindings.json"].data, sectionItemLimit, "email_address"))
	add(SectionHistory, "history/messages.json", countArrayEntry(entries, "history/messages.json"),
		jsonArrayLabels(entries["history/sessions.json"].data, sectionItemLimit, "title", "type"))
	add(SectionAssets, "assets/message_assets.json", countArrayEntry(entries, "assets/message_assets.json"),
		jsonArrayLabels(entries["assets/message_assets.json"].data, sectionItemLimit, "name"))
	if hasWorkspaceEntries(entries) {
		out = append(out, SectionSummary{
			Key:   SectionWorkspace,
			Count: countWorkspaceFiles(entries),
			Items: workspaceFileList(entries, sectionItemLimit),
		})
	}
	return out
}

const sectionItemLimit = 200

// settingsLabels extracts a human-readable summary of the key behavior settings
// (not model config) from the settings JSON object, for the expandable detail.
func settingsLabels(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := unmarshalJSON(raw, &m); err != nil {
		return nil
	}
	str := func(k string) string {
		if v, ok := m[k].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	boolStr := func(k string) string {
		if v, ok := m[k].(bool); ok && v {
			return "on"
		}
		return "off"
	}
	out := []string{}
	if v := str("language"); v != "" {
		out = append(out, "language: "+v)
	}
	if v := str("timezone"); v != "" {
		out = append(out, "timezone: "+v)
	}
	if v := str("acl_default_effect"); v != "" {
		out = append(out, "acl default: "+v)
	}
	out = append(out, "reasoning: "+boolStr("reasoning_enabled"))
	out = append(out, "heartbeat: "+boolStr("heartbeat_enabled"))
	out = append(out, "compaction: "+boolStr("compaction_enabled"))
	return out
}

// jsonArrayLabels extracts up to `limit` string labels from a JSON array of
// objects, using the first present key in `keys` for each element.
func jsonArrayLabels(raw []byte, limit int, keys ...string) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []map[string]any
	if err := unmarshalJSON(raw, &arr); err != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, m := range arr {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				out = append(out, v)
				break
			}
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

// jsonArrayLen counts elements in a JSON array, returning 0 on any error.
func jsonArrayLen(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	var arr []any
	if err := unmarshalJSON(raw, &arr); err != nil {
		return 0
	}
	return len(arr)
}

// workspaceFileList returns the relative file paths inside the workspace data.
// workspaceFileList returns the file paths inside the workspace tar.gz blob,
// read from the archive headers without extracting contents.
func workspaceFileList(entries map[string]backupZipEntry, limit int) []string {
	names, _ := readTarGzNames(entries[workspaceArchivePath].data, limit)
	return names
}

func countArrayEntry(entries map[string]backupZipEntry, path string) int {
	entry, ok := entries[path]
	if !ok || len(entry.data) == 0 {
		return 0
	}
	var arr []any
	if err := unmarshalJSON(entry.data, &arr); err != nil {
		return 0
	}
	return len(arr)
}

func countWorkspaceFiles(entries map[string]backupZipEntry) int {
	_, n := readTarGzNames(entries[workspaceArchivePath].data, 0)
	return n
}

// readTarGzNames lists regular-file paths in a gzip-compressed tar. When limit
// > 0 the returned slice is capped at limit, but the second return value is the
// full file count.
func readTarGzNames(raw []byte, limit int) ([]string, int) {
	if len(raw) == 0 {
		return nil, 0
	}
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, 0
	}
	defer func() { _ = gr.Close() }()
	tr := tar.NewReader(gr)
	out := []string{}
	count := 0
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			break
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		count++
		if limit <= 0 || len(out) < limit {
			out = append(out, strings.TrimPrefix(filepath.ToSlash(header.Name), "./"))
		}
	}
	sort.Strings(out)
	return out, count
}

func (s *Service) Import(ctx context.Context, actorUserID string, raw []byte, opts ImportOptions, passphrase string) (ImportResult, error) {
	plain, _, decErr := decodeBundle(raw, passphrase)
	if decErr != nil {
		if errors.Is(decErr, secure.ErrPassphraseRequired) {
			return ImportResult{}, errors.New("this backup is encrypted; a passphrase is required")
		}
		return ImportResult{}, fmt.Errorf("decrypt backup: %w", decErr)
	}
	entries, manifest, err := loadManifest(plain)
	if err != nil {
		return ImportResult{}, err
	}
	if manifest.SchemaVersion != BackupSchemaVersion {
		return ImportResult{}, fmt.Errorf("unsupported backup schema version: %d", manifest.SchemaVersion)
	}
	state := &importState{
		entries:  entries,
		manifest: manifest,
		warnings: append([]string(nil), manifest.Warnings...),
		counts:   map[Section]int{},
	}

	profile, err := readEntry[bot.Bot](state, "bot/profile.json")
	if err != nil {
		return ImportResult{}, err
	}
	profile = scrubImportedProfileACPSecrets(profile, state)
	cfg, err := readEntry[setting.Settings](state, "bot/setting.json")
	if err != nil {
		return ImportResult{}, err
	}
	// Dependencies (providers/models/...) are global, idempotent resources; they
	// are created before the bot and are intentionally NOT rolled back, so a
	// retry reuses them by name.
	deps := newDependencyMap()
	if opts.wants(SectionModels) {
		deps, err = s.importDependencies(ctx, state)
		if err != nil {
			return ImportResult{}, err
		}
	}
	targetBotID, created, err := s.restoreBot(ctx, actorUserID, profile, opts)
	if err != nil {
		return ImportResult{}, err
	}
	if shouldCloseOverwriteACPRuntimes(opts, created) {
		s.closeBotACPRuntimes(targetBotID)
	}
	state.createMode = created
	if opts.wants(SectionEmail) {
		if err := s.importEmailDependencies(ctx, state, targetBotID, &deps); err != nil {
			return ImportResult{}, err
		}
	}

	// Compensation: in create mode, undo a partially-imported bot on any fatal
	// failure. Deleting the bot cascades to all its child rows (settings, acl,
	// channels, mcp, schedules, email bindings, sessions, messages, assets,
	// container), leaving no trace. Overwrite mode keeps skip/merge/replace
	// semantics and is not rolled back.
	committed := false
	if created {
		defer func() {
			if !committed {
				if delErr := s.bots.Delete(context.WithoutCancel(ctx), targetBotID); delErr != nil {
					s.logger.WarnContext(ctx, "import compensation: delete bot failed",
						slog.String("bot_id", targetBotID), slog.Any("error", delErr))
				}
			}
		}()
	}

	if err := s.applyRestore(ctx, actorUserID, targetBotID, cfg, deps, opts, state); err != nil {
		return ImportResult{}, err
	}

	committed = true
	return ImportResult{BotID: targetBotID, Created: created, Warnings: state.warnings, Imported: state.counts}, nil
}

func scrubImportedProfileACPSecrets(profile bot.Bot, state *importState) bot.Bot {
	scrubbed, changed := acpprofile.ScrubMetadataForExport(profile.Metadata)
	if !changed {
		return profile
	}
	profile.Metadata = scrubbed
	if state != nil {
		state.warnings = appendWarningOnce(state.warnings, acpManagedSecretsWarning)
	}
	return profile
}

func appendWarningOnce(warnings []string, warning string) []string {
	for _, item := range warnings {
		if item == warning {
			return warnings
		}
	}
	return append(warnings, warning)
}

// applyRestore runs every selected section in order. A returned error is fatal
// (create mode) and triggers compensation in the caller; restore steps that are
// only meaningful in overwrite mode degrade their own item failures to warnings.
func (s *Service) applyRestore(ctx context.Context, actorUserID, targetBotID string, cfg setting.Settings, deps dependencyMap, opts ImportOptions, state *importState) error {
	// restore wraps a section step: fatal in create mode, a warning otherwise.
	restore := func(label string, fn func() error) error {
		if err := fn(); err != nil {
			if state.createMode {
				return fmt.Errorf("%s: %w", label, err)
			}
			state.warnings = append(state.warnings, label+": "+err.Error())
		}
		return nil
	}

	if opts.wants(SectionSettings) || opts.wants(SectionModels) {
		if err := restore("settings import failed", func() error {
			return s.restoreSettings(ctx, targetBotID, cfg, deps, opts.wants(SectionSettings), opts.wants(SectionModels))
		}); err != nil {
			return err
		}
	}
	if (opts.wants(SectionSettings) || opts.wants(SectionWorkspace)) && hasEntry(state.entries, "bot/workspace_resource_limits.json") {
		if err := restore("workspace resource limits import failed", func() error {
			return s.restoreWorkspaceResourceLimits(ctx, targetBotID, state)
		}); err != nil {
			return err
		}
	}
	if opts.wants(SectionACL) {
		if opts.strategyFor(SectionACL) == StrategyReplace {
			s.clearACL(ctx, targetBotID)
		}
		if err := restore("acl import failed", func() error { return s.restoreACL(ctx, targetBotID, actorUserID, state) }); err != nil {
			return err
		}
	}
	if opts.wants(SectionChannels) {
		if opts.strategyFor(SectionChannels) == StrategyReplace {
			s.clearChannels(ctx, targetBotID)
		}
		if err := restore("channels import failed", func() error { return s.restoreChannels(ctx, targetBotID, state) }); err != nil {
			return err
		}
	}
	if opts.wants(SectionMCP) {
		if opts.strategyFor(SectionMCP) == StrategyReplace {
			s.clearMCP(ctx, targetBotID)
		}
		if err := restore("mcp import failed", func() error { return s.restoreMCP(ctx, targetBotID, state) }); err != nil {
			return err
		}
	}
	if opts.wants(SectionSchedules) {
		if opts.strategyFor(SectionSchedules) == StrategyReplace {
			s.clearSchedules(ctx, targetBotID)
		}
		if err := restore("schedules import failed", func() error { return s.restoreSchedules(ctx, targetBotID, state) }); err != nil {
			return err
		}
	}
	if opts.wants(SectionEmail) {
		if opts.strategyFor(SectionEmail) == StrategyReplace {
			s.clearEmailBindings(ctx, targetBotID)
		}
		if err := restore("email import failed", func() error { return s.restoreEmailBindings(ctx, targetBotID, state, deps) }); err != nil {
			return err
		}
	}
	if opts.wants(SectionHistory) {
		replace := opts.strategyFor(SectionHistory) == StrategyReplace
		if err := restore("history import failed", func() error {
			return s.restoreHistory(ctx, actorUserID, targetBotID, state, opts.wants(SectionAssets), replace)
		}); err != nil {
			return err
		}
	}
	// Workspace files are auxiliary: a transfer failure (e.g. container not yet
	// reachable, despite retries) is recorded as a warning rather than discarding
	// an otherwise-complete bot import, even in create mode.
	if opts.wants(SectionWorkspace) && hasWorkspaceEntries(state.entries) {
		if s.workspace == nil {
			state.warnings = append(state.warnings, "workspace restore skipped: workspace manager not configured")
		} else if archive, err := workspaceArchive(state.entries); err != nil {
			state.warnings = append(state.warnings, "workspace restore failed: "+err.Error())
		} else if err := s.restoreWorkspaceData(ctx, targetBotID, archive, state.createMode); err != nil {
			state.warnings = append(state.warnings, "workspace restore failed: "+err.Error())
		} else {
			state.counts[SectionWorkspace] = countWorkspaceFiles(state.entries)
		}
	}
	return nil
}

func (s *Service) restoreWorkspaceData(ctx context.Context, botID string, raw []byte, waitForContainer bool) error {
	if s.workspace == nil {
		return errors.New("workspace manager not configured")
	}
	if !waitForContainer {
		return s.workspace.ImportData(ctx, botID, bytes.NewReader(raw))
	}

	deadline := time.Now().Add(workspaceRestoreRetryTimeout)
	var lastErr error
	for {
		err := s.workspace.ImportData(ctx, botID, bytes.NewReader(raw))
		if err == nil {
			return nil
		}
		lastErr = err
		if !isWorkspaceRestoreRetryable(err) || time.Now().After(deadline) {
			return lastErr
		}
		timer := time.NewTimer(workspaceRestoreRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func isWorkspaceRestoreRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	retryable := []string{
		"not found",
		"no such container",
		"workspace is not reachable",
		"connection refused",
	}
	for _, item := range retryable {
		if strings.Contains(msg, item) {
			return true
		}
	}
	return false
}

type dependencyMap struct {
	providers       map[string]string
	models          map[string]string
	searchProviders map[string]string
	fetchProviders  map[string]string
	memoryProviders map[string]string
	emailProviders  map[string]string
}

func newDependencyMap() dependencyMap {
	return dependencyMap{
		providers:       map[string]string{},
		models:          map[string]string{},
		searchProviders: map[string]string{},
		fetchProviders:  map[string]string{},
		memoryProviders: map[string]string{},
		emailProviders:  map[string]string{},
	}
}

type modelDependency struct {
	ID         string                  `json:"id"`
	ModelID    string                  `json:"model_id"`
	Name       string                  `json:"name"`
	ProviderID string                  `json:"provider_id"`
	Type       modeldomain.ModelType   `json:"type"`
	Enable     *bool                   `json:"enable,omitempty"`
	Config     modeldomain.ModelConfig `json:"config"`
}

func (s *Service) importDependencies(ctx context.Context, state *importState) (dependencyMap, error) {
	deps := newDependencyMap()
	providers, _ := readEntry[[]providerpkg.GetResponse](state, "dependencies/providers.json")
	for _, item := range providers {
		id, err := s.ensureProvider(ctx, item)
		if err != nil {
			state.warnings = append(state.warnings, "provider dependency skipped: "+err.Error())
			continue
		}
		deps.providers[item.ID] = id
	}
	models, _ := readEntry[[]modelDependency](state, "dependencies/models.json")
	for _, item := range models {
		id, err := s.ensureModel(ctx, item, deps)
		if err != nil {
			state.warnings = append(state.warnings, "model dependency skipped: "+err.Error())
			continue
		}
		deps.models[item.ID] = id
	}
	searchProviders, _ := readEntry[[]searchpkg.GetResponse](state, "dependencies/search_providers.json")
	for _, item := range searchProviders {
		id, err := s.ensureSearchProvider(ctx, item)
		if err != nil {
			state.warnings = append(state.warnings, "search provider dependency skipped: "+err.Error())
			continue
		}
		deps.searchProviders[item.ID] = id
	}
	fetchProviders, _ := readEntry[[]fetchpkg.GetResponse](state, "dependencies/fetch_providers.json")
	for _, item := range fetchProviders {
		id, err := s.ensureFetchProvider(ctx, item)
		if err != nil {
			state.warnings = append(state.warnings, "fetch provider dependency skipped: "+err.Error())
			continue
		}
		deps.fetchProviders[item.ID] = id
	}
	memoryProviders, _ := readEntry[[]memcatalog.ProviderGetResponse](state, "dependencies/memory_providers.json")
	for _, item := range memoryProviders {
		id, err := s.ensureMemoryProvider(ctx, item)
		if err != nil {
			state.warnings = append(state.warnings, "memory provider dependency skipped: "+err.Error())
			continue
		}
		deps.memoryProviders[item.ID] = id
	}
	return deps, nil
}

func (s *Service) importEmailDependencies(ctx context.Context, state *importState, targetBotID string, deps *dependencyMap) error {
	if s.email == nil {
		return nil
	}
	if s.bots == nil {
		return errors.New("bot service not configured")
	}
	targetBot, err := s.bots.Get(ctx, targetBotID)
	if err != nil {
		return fmt.Errorf("get target bot owner: %w", err)
	}
	emailProviders, _ := readEntry[[]emailpkg.ProviderResponse](state, "dependencies/email_providers.json")
	for _, item := range emailProviders {
		id, err := s.ensureEmailProvider(ctx, targetBot.OwnerUserID, item)
		if err != nil {
			state.warnings = append(state.warnings, "email provider dependency skipped: "+err.Error())
			continue
		}
		deps.emailProviders[item.ID] = id
	}
	return nil
}

func (s *Service) restoreBot(ctx context.Context, actorUserID string, profile bot.Bot, opts ImportOptions) (string, bool, error) {
	mode := normalizeImportMode(opts.Mode)
	if mode == ImportModeOverwrite {
		target := strings.TrimSpace(opts.TargetBotID)
		if target == "" {
			return "", false, errors.New("target_bot_id is required for overwrite import")
		}
		// Only overwrite the target's identity (name/avatar/timezone) when the
		// profile section is explicitly selected; otherwise keep it intact.
		if !opts.wants(SectionProfile) {
			return target, false, nil
		}
		avatar := profile.AvatarURL
		name := profile.DisplayName
		active := profile.IsActive
		tz := profile.Timezone
		_, err := s.bots.UpdateReplacingMetadata(ctx, target, bot.UpdateBotRequest{
			DisplayName: &name,
			AvatarURL:   &avatar,
			Timezone:    &tz,
			IsActive:    &active,
			Metadata:    profile.Metadata,
		})
		return target, false, err
	}
	tz := profile.Timezone
	active := profile.IsActive
	created, err := s.bots.Create(ctx, actorUserID, bot.CreateBotRequest{
		DisplayName: profile.DisplayName,
		AvatarURL:   profile.AvatarURL,
		Timezone:    &tz,
		IsActive:    &active,
		Metadata:    profile.Metadata,
	})
	if err != nil {
		return "", false, err
	}
	return created.ID, true, nil
}

func shouldCloseOverwriteACPRuntimes(opts ImportOptions, created bool) bool {
	if created || normalizeImportMode(opts.Mode) != ImportModeOverwrite {
		return false
	}
	return opts.wants(SectionProfile) || opts.wants(SectionWorkspace)
}

func (s *Service) closeBotACPRuntimes(botID string) {
	if s == nil || s.acpRuntimes == nil {
		return
	}
	for _, profile := range acpprofile.List() {
		if err := s.acpRuntimes.CloseBotAgentRuntimes(botID, profile.ID); err != nil {
			s.logger.Warn("close ACP runtime after bot backup import failed",
				slog.String("bot_id", botID),
				slog.String("agent_id", profile.ID),
				slog.Any("error", err),
			)
		}
	}
}

// restoreSettings writes the bot settings, importing the behavior group
// (importSettings) and/or the model-config group (importModels) independently.
// In overwrite mode a skipped group keeps the target's current values, so a
// single UpsertBot never blanks fields the user chose not to import.
func (s *Service) restoreSettings(ctx context.Context, botID string, cfg setting.Settings, deps dependencyMap, importSettings, importModels bool) error {
	remap := func(id string, m map[string]string) string {
		if id == "" {
			return ""
		}
		if next := strings.TrimSpace(m[id]); next != "" {
			return next
		}
		return id
	}

	// Start from the backup, then for any skipped group fall back to the target's
	// current values so only the selected group(s) change.
	eff := cfg
	if !importSettings || !importModels {
		if current, err := s.settings.GetBot(ctx, botID); err == nil {
			if !importModels {
				eff.ChatModelID = current.ChatModelID
				eff.ImageModelID = current.ImageModelID
				eff.SearchProviderID = current.SearchProviderID
				eff.FetchProviderID = current.FetchProviderID
				eff.MemoryProviderID = current.MemoryProviderID
				eff.TtsModelID = current.TtsModelID
				eff.TranscriptionModelID = current.TranscriptionModelID
				eff.HeartbeatModelID = current.HeartbeatModelID
				eff.CompactionModelID = current.CompactionModelID
				eff.DiscussProbeModelID = current.DiscussProbeModelID
			}
			if !importSettings {
				eff.Language = current.Language
				eff.AclDefaultEffect = current.AclDefaultEffect
				eff.Timezone = current.Timezone
				eff.ChatRuntime = current.ChatRuntime
				eff.ChatACPAgentID = current.ChatACPAgentID
				eff.ChatACPProjectPath = current.ChatACPProjectPath
				eff.ChatACPProjectMode = current.ChatACPProjectMode
				eff.ReasoningEnabled = current.ReasoningEnabled
				eff.ReasoningEffort = current.ReasoningEffort
				eff.HeartbeatEnabled = current.HeartbeatEnabled
				eff.HeartbeatInterval = current.HeartbeatInterval
				eff.CompactionEnabled = current.CompactionEnabled
				eff.CompactionThreshold = current.CompactionThreshold
				eff.CompactionRatio = current.CompactionRatio
				eff.PersistFullToolResults = current.PersistFullToolResults
				eff.ShowToolCallsInIM = current.ShowToolCallsInIM
				eff.ToolApprovalConfig = current.ToolApprovalConfig
				eff.DisplayEnabled = current.DisplayEnabled
				eff.OverlayEnabled = current.OverlayEnabled
				eff.OverlayProvider = current.OverlayProvider
				eff.OverlayConfig = current.OverlayConfig
			}
		}
	}

	// Model IDs are remapped through imported dependencies only when models are
	// being imported; otherwise they're already the target's own (valid) IDs.
	modelID := func(id string, m map[string]string) string {
		if importModels {
			return remap(id, m)
		}
		return id
	}

	timezone := eff.Timezone
	reasoningEffort := eff.ReasoningEffort
	heartbeatEnabled := eff.HeartbeatEnabled
	heartbeatInterval := eff.HeartbeatInterval
	compactionEnabled := eff.CompactionEnabled
	compactionThreshold := eff.CompactionThreshold
	compactionRatio := eff.CompactionRatio
	persistFullToolResults := eff.PersistFullToolResults
	showToolCalls := eff.ShowToolCallsInIM
	toolApproval := eff.ToolApprovalConfig
	displayEnabled := eff.DisplayEnabled
	overlayEnabled := eff.OverlayEnabled
	overlayProvider := eff.OverlayProvider
	reasoningEnabled := eff.ReasoningEnabled
	fetchProviderID := modelID(eff.FetchProviderID, deps.fetchProviders)
	_, err := s.settings.UpsertBot(ctx, botID, setting.UpsertRequest{
		ChatModelID:            modelID(eff.ChatModelID, deps.models),
		ChatRuntime:            ptrStringAllowEmpty(eff.ChatRuntime),
		ChatACPAgentID:         ptrStringAllowEmpty(eff.ChatACPAgentID),
		ChatACPProjectPath:     ptrStringAllowEmpty(eff.ChatACPProjectPath),
		ChatACPProjectMode:     ptrStringAllowEmpty(eff.ChatACPProjectMode),
		ImageModelID:           modelID(eff.ImageModelID, deps.models),
		SearchProviderID:       modelID(eff.SearchProviderID, deps.searchProviders),
		FetchProviderID:        &fetchProviderID,
		MemoryProviderID:       modelID(eff.MemoryProviderID, deps.memoryProviders),
		TtsModelID:             modelID(eff.TtsModelID, deps.models),
		TranscriptionModelID:   modelID(eff.TranscriptionModelID, deps.models),
		Language:               eff.Language,
		AclDefaultEffect:       eff.AclDefaultEffect,
		Timezone:               &timezone,
		ReasoningEnabled:       &reasoningEnabled,
		ReasoningEffort:        &reasoningEffort,
		HeartbeatEnabled:       &heartbeatEnabled,
		HeartbeatInterval:      &heartbeatInterval,
		HeartbeatModelID:       modelID(eff.HeartbeatModelID, deps.models),
		CompactionEnabled:      &compactionEnabled,
		CompactionThreshold:    &compactionThreshold,
		CompactionRatio:        &compactionRatio,
		CompactionModelID:      ptrString(modelID(eff.CompactionModelID, deps.models)),
		DiscussProbeModelID:    modelID(eff.DiscussProbeModelID, deps.models),
		PersistFullToolResults: &persistFullToolResults,
		ShowToolCallsInIM:      &showToolCalls,
		ToolApprovalConfig:     &toolApproval,
		DisplayEnabled:         &displayEnabled,
		OverlayEnabled:         &overlayEnabled,
		OverlayProvider:        &overlayProvider,
		OverlayConfig:          eff.OverlayConfig,
	})
	return err
}

func (s *Service) restoreWorkspaceResourceLimits(ctx context.Context, botID string, state *importState) error {
	if s.workspace == nil {
		return errors.New("workspace manager not configured")
	}
	limits, err := readEntry[backupWorkspaceResourceLimits](state, "bot/workspace_resource_limits.json")
	if err != nil {
		return err
	}
	if limits.CPUMillicores < 0 || limits.MemoryBytes < 0 || limits.StorageBytes < 0 {
		return errors.New("resource limits must be non-negative")
	}
	_, err = s.workspace.SetResourceLimits(ctx, botID, container.ResourceLimits{
		CPUMillicores: limits.CPUMillicores,
		MemoryBytes:   limits.MemoryBytes,
		StorageBytes:  limits.StorageBytes,
	})
	return err
}

func (s *Service) restoreACL(ctx context.Context, botID, actorUserID string, state *importState) error {
	if s.acl == nil {
		return nil
	}
	rules, err := readEntry[[]acl.Rule](state, "bot/acl_rules.json")
	if err != nil {
		return err
	}
	for _, rule := range rules {
		sourceScope := rule.SourceScope
		if sourceScope == nil {
			sourceScope = &acl.SourceScope{}
		}
		_, err := s.acl.CreateRule(ctx, botID, actorUserID, acl.CreateRuleRequest{
			Enabled:            rule.Enabled,
			Description:        rule.Description,
			Effect:             rule.Effect,
			ChannelIdentityID:  "",
			SubjectChannelType: rule.SubjectChannelType,
			SourceScope:        sourceScope,
		})
		if err != nil {
			if e := state.itemErr("acl rule", err); e != nil {
				return e
			}
			continue
		}
		state.counts[SectionACL]++
	}
	return nil
}

func (s *Service) restoreChannels(ctx context.Context, botID string, state *importState) error {
	if s.channels == nil {
		return nil
	}
	configs, err := readEntry[[]gateway.ChannelConfig](state, "bot/channel_configs.json")
	if err != nil {
		return err
	}
	for _, cfg := range configs {
		disabled := cfg.Disabled
		verifiedAt := cfg.VerifiedAt
		_, err := s.channels.UpsertConfig(ctx, botID, cfg.ChannelType, gateway.UpsertConfigRequest{
			Credentials:      cfg.Credentials,
			ExternalIdentity: cfg.ExternalIdentity,
			SelfIdentity:     cfg.SelfIdentity,
			Routing:          cfg.Routing,
			Disabled:         &disabled,
			VerifiedAt:       &verifiedAt,
		})
		if err != nil {
			if e := state.itemErr("channel config", err); e != nil {
				return e
			}
			continue
		}
		state.counts[SectionChannels]++
	}
	return nil
}

func (s *Service) restoreMCP(ctx context.Context, botID string, state *importState) error {
	if s.mcp == nil {
		return nil
	}
	items, err := readEntry[[]mcp.Connection](state, "bot/mcp_connections.json")
	if err != nil {
		return err
	}
	for _, item := range items {
		req := mcpRequestFromConnection(item)
		if _, err := s.mcp.Create(ctx, botID, req); err != nil {
			if e := state.itemErr("mcp connection", err); e != nil {
				return e
			}
			continue
		}
		state.counts[SectionMCP]++
	}
	return nil
}

func (s *Service) restoreSchedules(ctx context.Context, botID string, state *importState) error {
	if s.schedules == nil {
		return nil
	}
	items, err := readEntry[[]schedule.Schedule](state, "bot/schedules.json")
	if err != nil {
		return err
	}
	for _, item := range items {
		enabled := item.Enabled
		_, err := s.schedules.Create(ctx, botID, schedule.CreateRequest{
			Name:        item.Name,
			Description: item.Description,
			Pattern:     item.Pattern,
			MaxCalls:    schedule.NullableInt{Value: item.MaxCalls, Set: true},
			Command:     item.Command,
			Enabled:     &enabled,
		})
		if err != nil {
			if e := state.itemErr("schedule", err); e != nil {
				return e
			}
			continue
		}
		state.counts[SectionSchedules]++
	}
	return nil
}

func (s *Service) restoreEmailBindings(ctx context.Context, botID string, state *importState, deps dependencyMap) error {
	if s.email == nil {
		return nil
	}
	items, err := readEntry[[]emailpkg.BindingResponse](state, "bot/email_bindings.json")
	if err != nil {
		return err
	}
	for _, item := range items {
		providerID := deps.emailProviders[item.EmailProviderID]
		if providerID == "" {
			providerID = item.EmailProviderID
		}
		canRead := item.CanRead
		canWrite := item.CanWrite
		canDelete := item.CanDelete
		if _, err := s.email.CreateBinding(ctx, botID, emailpkg.CreateBindingRequest{
			EmailProviderID: providerID,
			EmailAddress:    item.EmailAddress,
			CanRead:         &canRead,
			CanWrite:        &canWrite,
			CanDelete:       &canDelete,
			Config:          item.Config,
		}); err != nil {
			if e := state.itemErr("email binding", err); e != nil {
				return e
			}
			continue
		}
		state.counts[SectionEmail]++
	}
	return nil
}

func (s *Service) restoreHistory(ctx context.Context, actorUserID, botID string, state *importState, includeAssets, replace bool) error {
	if s.chatBackup == nil || s.channelBackup == nil {
		return errors.New("history backup owners not configured")
	}
	sessions, err := readEntry[[]chatbackup.Session](state, "history/sessions.json")
	if err != nil {
		return err
	}
	messages, err := readEntry[[]chatbackup.Message](state, "history/messages.json")
	if err != nil {
		return err
	}
	var assets []chatbackup.Asset
	if includeAssets {
		assets, err = readEntry[[]chatbackup.Asset](state, "assets/message_assets.json")
		if err != nil {
			return err
		}
	}
	cursors, err := readEntry[[]channelbackup.DiscussCursor](state, "history/discuss_cursors.json")
	if err != nil {
		return err
	}
	events, err := readEntry[[]channelbackup.SessionEvent](state, "history/session_events.json")
	if err != nil {
		return err
	}
	routeSessions, err := readEntry[[]backupRouteActiveSession](state, "history/route_active_sessions.json")
	if err != nil {
		return err
	}

	chatResult, err := s.chatBackup.Import(ctx, chatbackup.ImportRequest{
		BotID:       botID,
		ActorUserID: actorUserID,
		Replace:     replace,
		Sessions:    sessions,
		Messages:    messages,
		Assets:      assets,
	})
	if err != nil {
		return fmt.Errorf("chat history import: %w", err)
	}

	channelResult, err := s.channelBackup.Import(ctx, channelbackup.ImportRequest{
		BotID:          botID,
		Replace:        replace,
		DiscussCursors: cursors,
		SessionEvents:  events,
		RouteSessions:  ownerRouteSessionsFromBackup(routeSessions),
		SessionIDs:     chatResult.SessionIDs,
	})
	if err != nil {
		compErr := s.chatBackup.Compensate(context.WithoutCancel(ctx), chatResult.Receipt)
		return errors.Join(fmt.Errorf("channel observations import: %w", err), wrapCompensationError("chat", compErr))
	}

	bindings := make([]chatbackup.EventBinding, 0, len(chatResult.EventReferences))
	for _, reference := range chatResult.EventReferences {
		eventID := channelResult.EventIDs[reference.OldEventID]
		if eventID == "" {
			continue
		}
		bindings = append(bindings, chatbackup.EventBinding{
			MessageID: reference.MessageID,
			EventID:   eventID,
		})
	}
	if err := s.chatBackup.BindEventReferences(ctx, chatbackup.BindEventReferencesRequest{
		BotID:    botID,
		Bindings: bindings,
	}); err != nil {
		compCtx := context.WithoutCancel(ctx)
		channelErr := s.channelBackup.Compensate(compCtx, channelResult.Receipt)
		chatErr := s.chatBackup.Compensate(compCtx, chatResult.Receipt)
		return errors.Join(
			fmt.Errorf("bind chat event references: %w", err),
			wrapCompensationError("channel", channelErr),
			wrapCompensationError("chat", chatErr),
		)
	}

	state.counts[SectionHistory] += len(messages)
	state.counts[SectionAssets] += len(assets)
	return nil
}

func wrapCompensationError(owner string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s history compensation: %w", owner, err)
}

func ownerRouteSessionsFromBackup(items []backupRouteActiveSession) []channelbackup.RouteActiveSession {
	result := make([]channelbackup.RouteActiveSession, 0, len(items))
	for _, item := range items {
		result = append(result, channelbackup.RouteActiveSession{
			RouteID:   item.RouteID,
			SessionID: item.SessionID,
		})
	}
	return result
}

func (s *Service) ensureProvider(ctx context.Context, item providerpkg.GetResponse) (string, error) {
	if s.providers == nil {
		return item.ID, errors.New("provider service not configured")
	}
	if existing, err := s.providers.GetByName(ctx, item.Name); err == nil {
		return existing.ID, nil
	}
	created, err := s.providers.Create(ctx, providerpkg.CreateRequest{
		Name:       item.Name,
		ClientType: item.ClientType,
		Icon:       item.Icon,
		Config:     item.Config,
		Metadata:   item.Metadata,
	})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

func (s *Service) ensureModel(ctx context.Context, item modelDependency, deps dependencyMap) (string, error) {
	if s.models == nil {
		return item.ID, errors.New("model service not configured")
	}
	if existing, err := s.models.GetByModelID(ctx, item.ModelID); err == nil {
		return existing.ID, nil
	}
	providerID := deps.providers[item.ProviderID]
	if providerID == "" {
		providerID = item.ProviderID
	}
	created, err := s.models.Create(ctx, modelpkg.AddRequest{
		ModelID:    item.ModelID,
		Name:       item.Name,
		ProviderID: providerID,
		Type:       item.Type,
		Enable:     item.Enable,
		Config:     item.Config,
	})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

func (s *Service) ensureSearchProvider(ctx context.Context, item searchpkg.GetResponse) (string, error) {
	if s.searchProviders == nil {
		return item.ID, errors.New("search provider service not configured")
	}
	list, _ := s.searchProviders.List(ctx, "")
	for _, existing := range list {
		if existing.Name == item.Name {
			return existing.ID, nil
		}
	}
	created, err := s.searchProviders.Create(ctx, searchpkg.CreateRequest{
		Name:     item.Name,
		Provider: searchpkg.ProviderName(item.Provider),
		Config:   item.Config,
	})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

func (s *Service) ensureFetchProvider(ctx context.Context, item fetchpkg.GetResponse) (string, error) {
	if s.fetchProviders == nil {
		return item.ID, errors.New("fetch provider service not configured")
	}
	if item.Provider == string(fetchpkg.ProviderNative) {
		list, _ := s.fetchProviders.List(ctx, string(fetchpkg.ProviderNative))
		for _, existing := range list {
			return existing.ID, nil
		}
		return item.ID, errors.New("native fetch provider is not available")
	}
	list, _ := s.fetchProviders.List(ctx, "")
	for _, existing := range list {
		if existing.Name == item.Name {
			if item.Enable && !existing.Enable {
				enable := true
				if _, err := s.fetchProviders.Update(ctx, existing.ID, fetchpkg.UpdateRequest{Enable: &enable}); err != nil {
					return "", err
				}
			}
			return existing.ID, nil
		}
	}
	created, err := s.fetchProviders.Create(ctx, fetchpkg.CreateRequest{
		Name:     item.Name,
		Provider: fetchpkg.ProviderName(item.Provider),
		Config:   item.Config,
	})
	if err != nil {
		return "", err
	}
	if item.Enable {
		enable := true
		if _, err := s.fetchProviders.Update(ctx, created.ID, fetchpkg.UpdateRequest{Enable: &enable}); err != nil {
			return "", err
		}
	}
	return created.ID, nil
}

func (s *Service) ensureMemoryProvider(ctx context.Context, item memcatalog.ProviderGetResponse) (string, error) {
	if s.memoryProviders == nil {
		return item.ID, errors.New("memory provider service not configured")
	}
	list, _ := s.memoryProviders.List(ctx)
	for _, existing := range list {
		if existing.Name == item.Name {
			return existing.ID, nil
		}
	}
	created, err := s.memoryProviders.Create(ctx, memcatalog.ProviderCreateRequest{
		Name:     item.Name,
		Provider: memcatalog.ProviderType(item.Provider),
		Config:   item.Config,
	})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

func (s *Service) ensureEmailProvider(ctx context.Context, ownerUserID string, item emailpkg.ProviderResponse) (string, error) {
	if s.email == nil {
		return item.ID, errors.New("email service not configured")
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	if ownerUserID == "" {
		return item.ID, errors.New("target bot owner is required")
	}
	list, _ := s.email.ListProviders(ctx, ownerUserID, "")
	for _, existing := range list {
		if existing.Name == item.Name {
			if shouldUpdateImportedEmailProvider(existing, item) {
				updated, err := s.email.UpdateProvider(ctx, ownerUserID, existing.ID, emailpkg.UpdateProviderRequest{
					Config: item.Config,
				})
				if err != nil {
					return "", err
				}
				return updated.ID, nil
			}
			return existing.ID, nil
		}
	}
	created, err := s.email.CreateProvider(ctx, ownerUserID, emailpkg.CreateProviderRequest{
		Name:     item.Name,
		Provider: emailpkg.ProviderName(item.Provider),
		Config:   item.Config,
	})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

func shouldUpdateImportedEmailProvider(existing, imported emailpkg.ProviderResponse) bool {
	if existing.Provider != imported.Provider || imported.Provider != "gmail" {
		return false
	}
	return emailProviderConfigString(existing.Config, "email_address") == "" &&
		emailProviderConfigString(imported.Config, "email_address") != ""
}

func emailProviderConfigString(config map[string]any, key string) string {
	value, ok := config[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func loadManifest(raw []byte) (map[string]backupZipEntry, Manifest, error) {
	entries, err := readZipEntries(raw)
	if err != nil {
		return nil, Manifest{}, err
	}
	manifestEntry, ok := entries[ManifestPath]
	if !ok {
		return nil, Manifest{}, errors.New("manifest.json not found")
	}
	var manifest Manifest
	if err := unmarshalJSON(manifestEntry.data, &manifest); err != nil {
		return nil, Manifest{}, err
	}
	return entries, manifest, nil
}

func readEntry[T any](state *importState, path string) (T, error) {
	var zero T
	raw, err := readRawEntry(state, path)
	if err != nil {
		return zero, err
	}
	if raw == nil {
		return zero, nil
	}
	var out T
	if err := unmarshalJSON(raw, &out); err != nil {
		return zero, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

func readRawEntry(state *importState, path string) ([]byte, error) {
	entry, ok := state.entries[path]
	if !ok {
		return nil, nil
	}
	return entry.data, nil
}

func hasEntry(entries map[string]backupZipEntry, path string) bool {
	_, ok := entries[path]
	return ok
}

func hasWorkspaceEntries(entries map[string]backupZipEntry) bool {
	entry, ok := entries[workspaceArchivePath]
	return ok && len(entry.data) > 0
}

// workspaceArchive returns the workspace tar.gz blob verbatim, ready to feed
// straight to the container's ImportData (no re-packing).
func workspaceArchive(entries map[string]backupZipEntry) ([]byte, error) {
	entry, ok := entries[workspaceArchivePath]
	if !ok || len(entry.data) == 0 {
		return nil, errors.New("workspace archive not found")
	}
	return entry.data, nil
}

func normalizeImportMode(mode ImportMode) ImportMode {
	if mode == ImportModeOverwrite {
		return ImportModeOverwrite
	}
	return ImportModeCreate
}

func ptrString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func ptrStringAllowEmpty(value string) *string {
	return &value
}

func mcpRequestFromConnection(conn mcp.Connection) mcp.UpsertRequest {
	req := mcp.UpsertRequest{Name: conn.Name, Active: &conn.Active, AuthType: conn.AuthType}
	switch conn.Type {
	case "stdio":
		req.Command, _ = conn.Config["command"].(string)
		req.Cwd, _ = conn.Config["cwd"].(string)
		req.Args = stringSliceFromAny(conn.Config["args"])
		req.Env = stringMapFromAny(conn.Config["env"])
	case "sse":
		req.Transport = "sse"
		fallthrough
	default:
		req.URL, _ = conn.Config["url"].(string)
		req.Headers = stringMapFromAny(conn.Config["headers"])
	}
	return req
}

func stringSliceFromAny(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func stringMapFromAny(value any) map[string]string {
	switch v := value.(type) {
	case map[string]string:
		return v
	case map[string]any:
		out := make(map[string]string, len(v))
		for key, item := range v {
			if s, ok := item.(string); ok {
				out[key] = s
			}
		}
		return out
	default:
		return nil
	}
}

