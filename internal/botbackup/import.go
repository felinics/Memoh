package botbackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/acl"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/botbackup/secure"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	emailpkg "github.com/felinics/memoh/internal/email"
	fetchpkg "github.com/felinics/memoh/internal/fetchproviders"
	"github.com/felinics/memoh/internal/mcp"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	modelpkg "github.com/felinics/memoh/internal/models"
	providerpkg "github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/schedule"
	searchpkg "github.com/felinics/memoh/internal/searchproviders"
	"github.com/felinics/memoh/internal/settings"
)

type importState struct {
	entries  map[string]backupZipEntry
	manifest Manifest
	idMap    map[string]string
	// workdirMap remaps source workdir ids onto the workdirs recreated (or
	// matched by path) in the target bot, so restored sessions keep their
	// bindings. Sessions whose workdir was not restored (remote workdirs,
	// or an import without the workspace/history sections) degrade to no
	// workdir.
	workdirMap map[string]pgtype.UUID
	warnings   []string
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
	var b bots.Bot
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
	if s.queries != nil {
		if msgs, err := s.queries.ListMessages(ctx, optionalUUID(botID)); err == nil {
			out[SectionHistory] = len(msgs)
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
	// settings.json backs both the behavior settings and the model config cards.
	if _, ok := entries["bot/settings.json"]; ok {
		out = append(out, SectionSummary{
			Key:   SectionSettings,
			Count: 1,
			Items: settingsLabels(entries["bot/settings.json"].data),
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
	// Reasoning has two archive shapes: the retired reasoning_enabled flag, and
	// the current effort field where "disable" is off. Prefer the legacy flag when
	// present so old archives preview the state they will actually import as.
	reasoningStr := func() string {
		if v, ok := m["reasoning_enabled"].(bool); ok && !v {
			return "off"
		}
		if v, ok := m["reasoning_effort"].(string); ok && modelpkg.IsReasoningDisabled(v) {
			return "off"
		}
		return "on"
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
	out = append(out, "reasoning: "+reasoningStr())
	compaction := "off"
	if enabled, ok := m["compaction_enabled"].(bool); ok && enabled {
		compaction = "on"
	}
	out = append(out, "compaction: "+compaction)
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
		entries:    entries,
		manifest:   manifest,
		idMap:      map[string]string{},
		workdirMap: map[string]pgtype.UUID{},
		warnings:   append([]string(nil), manifest.Warnings...),
		counts:     map[Section]int{},
	}

	profile, err := readEntry[bots.Bot](state, "bot/profile.json")
	if err != nil {
		return ImportResult{}, err
	}
	profile = scrubImportedProfileACPSecrets(profile, state)
	settingsRaw, err := readRawEntry(state, "bot/settings.json")
	if err != nil {
		return ImportResult{}, err
	}
	cfg, err := decodeBackupSettings(settingsRaw)
	if err != nil {
		return ImportResult{}, fmt.Errorf("read bot/settings.json: %w", err)
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
	var releaseACPRuntimeReset func()
	if shouldGateOverwriteACPRuntimes(opts) && s.acpRuntimes == nil {
		return ImportResult{}, ErrHistoryResetUnavailable
	}
	if shouldGateOverwriteACPRuntimes(opts) {
		target := strings.TrimSpace(opts.TargetBotID)
		ctx, releaseACPRuntimeReset, err = s.acpRuntimes.BeginBotHistoryReset(ctx, target)
		if err != nil {
			return ImportResult{}, errors.Join(
				ErrHistoryResetUnavailable,
				fmt.Errorf("begin ACP runtime reset for overwrite import: %w", err),
			)
		}
		defer releaseACPRuntimeReset()
	}
	targetBotID, created, err := s.restoreBot(ctx, actorUserID, profile, opts)
	if err != nil {
		return ImportResult{}, err
	}
	state.idMap[profile.ID] = targetBotID
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
					s.logger.Warn("import compensation: delete bot failed",
						slog.String("bot_id", targetBotID), slog.Any("error", delErr))
				}
			}
		}()
	}

	// Permanently invalidate every old runtime before any overwrite section can
	// mutate database or workspace state. This short committed transaction is
	// intentionally before applyRestore: a process crash during an external
	// workspace transfer must not roll the epoch invalidation back.
	if _, fenced := runtimefence.ResetFromContext(ctx); releaseACPRuntimeReset != nil && fenced {
		botUUID, parseErr := db.ParseUUID(targetBotID)
		if parseErr != nil {
			return ImportResult{}, parseErr
		}
		bumpErr := runtimefence.InResetTransaction(ctx, s.queries, targetBotID, "", func(queries dbstore.Queries) error {
			_, err := queries.BumpBotRuntimeConfigEpoch(ctx, botUUID)
			return err
		})
		if bumpErr != nil {
			return ImportResult{}, errors.Join(
				ErrHistoryResetUnavailable,
				fmt.Errorf("advance ACP runtime config generation: %w", bumpErr),
			)
		}
	}
	if err := s.applyRestore(ctx, actorUserID, targetBotID, cfg, deps, opts, state); err != nil {
		return ImportResult{}, err
	}

	committed = true
	return ImportResult{BotID: targetBotID, Created: created, Warnings: state.warnings, Imported: state.counts}, nil
}

func scrubImportedProfileACPSecrets(profile bots.Bot, state *importState) bots.Bot {
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
func (s *Service) applyRestore(ctx context.Context, actorUserID, targetBotID string, cfg settings.Settings, deps dependencyMap, opts ImportOptions, state *importState) error {
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
	// Workdirs must restore before history: restored sessions reference the
	// recreated workdir rows through a real foreign key.
	if (opts.wants(SectionWorkspace) || opts.wants(SectionHistory)) && hasEntry(state.entries, "bot/workdirs.json") {
		if err := restore("workdir import failed", func() error {
			return s.restoreWorkdirs(ctx, targetBotID, actorUserID, state)
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
		} else {
			_, fenced := runtimefence.ResetFromContext(ctx)
			var transferErr, guardErr error
			if fenced {
				transferErr, guardErr = runtimefence.PublishBotRuntimeConfig(
					ctx,
					s.queries,
					targetBotID,
					workspaceRestoreRetryTimeout,
					func(publishCtx context.Context) error {
						return s.restoreWorkspaceData(publishCtx, targetBotID, archive, state.createMode)
					},
				)
			} else {
				transferErr = s.restoreWorkspaceData(ctx, targetBotID, archive, state.createMode)
			}
			switch {
			case guardErr != nil:
				return guardErr
			case transferErr == nil:
				state.counts[SectionWorkspace] = countWorkspaceFiles(state.entries)
			default:
				state.warnings = append(state.warnings, "workspace restore failed: "+transferErr.Error())
			}
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
	ID         string               `json:"id"`
	ModelID    string               `json:"model_id"`
	Name       string               `json:"name"`
	ProviderID string               `json:"provider_id"`
	Type       modelpkg.ModelType   `json:"type"`
	Enable     *bool                `json:"enable,omitempty"`
	Config     modelpkg.ModelConfig `json:"config"`
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
	memoryProviders, _ := readEntry[[]memprovider.ProviderGetResponse](state, "dependencies/memory_providers.json")
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

func (s *Service) restoreBot(ctx context.Context, actorUserID string, profile bots.Bot, opts ImportOptions) (string, bool, error) {
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
		_, err := s.bots.UpdateReplacingMetadata(ctx, target, bots.UpdateBotRequest{
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
	created, err := s.bots.Create(ctx, actorUserID, bots.CreateBotRequest{
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

func shouldGateOverwriteACPRuntimes(opts ImportOptions) bool {
	if normalizeImportMode(opts.Mode) != ImportModeOverwrite {
		return false
	}
	return opts.wants(SectionProfile) || opts.wants(SectionWorkspace) ||
		(opts.wants(SectionHistory) && opts.strategyFor(SectionHistory) == StrategyReplace)
}

// restoreSettings writes the bot settings, importing the behavior group
// (importSettings) and/or the model-config group (importModels) independently.
// In overwrite mode a skipped group keeps the target's current values, so a
// single UpsertBot never blanks fields the user chose not to import.
func (s *Service) restoreSettings(ctx context.Context, botID string, cfg settings.Settings, deps dependencyMap, importSettings, importModels bool) error {
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
				eff.ReasoningEffort = current.ReasoningEffort
				eff.CompactionEnabled = current.CompactionEnabled
				eff.CompactionThreshold = current.CompactionThreshold
				eff.CompactionTargetPercent = current.CompactionTargetPercent
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
	compactionEnabled := eff.CompactionEnabled
	compactionThreshold := eff.CompactionThreshold
	compactionTargetPercent := 0
	if eff.CompactionTargetPercent != nil {
		compactionTargetPercent = *eff.CompactionTargetPercent
	}
	persistFullToolResults := eff.PersistFullToolResults
	showToolCalls := eff.ShowToolCallsInIM
	toolApproval := eff.ToolApprovalConfig
	displayEnabled := eff.DisplayEnabled
	overlayEnabled := eff.OverlayEnabled
	overlayProvider := eff.OverlayProvider
	fetchProviderID := modelID(eff.FetchProviderID, deps.fetchProviders)
	_, err := s.settings.UpsertBot(ctx, botID, settings.UpsertRequest{
		ChatModelID:             ptrStringAllowEmpty(modelID(eff.ChatModelID, deps.models)),
		ChatRuntime:             ptrStringAllowEmpty(eff.ChatRuntime),
		ChatACPAgentID:          ptrStringAllowEmpty(eff.ChatACPAgentID),
		ChatACPProjectPath:      ptrStringAllowEmpty(eff.ChatACPProjectPath),
		ChatACPProjectMode:      ptrStringAllowEmpty(eff.ChatACPProjectMode),
		ImageModelID:            ptrStringAllowEmpty(modelID(eff.ImageModelID, deps.models)),
		SearchProviderID:        ptrStringAllowEmpty(modelID(eff.SearchProviderID, deps.searchProviders)),
		FetchProviderID:         &fetchProviderID,
		MemoryProviderID:        ptrStringAllowEmpty(modelID(eff.MemoryProviderID, deps.memoryProviders)),
		TtsModelID:              ptrStringAllowEmpty(modelID(eff.TtsModelID, deps.models)),
		TranscriptionModelID:    ptrStringAllowEmpty(modelID(eff.TranscriptionModelID, deps.models)),
		Language:                ptrStringAllowEmpty(eff.Language),
		AclDefaultEffect:        eff.AclDefaultEffect,
		Timezone:                &timezone,
		ReasoningEffort:         &reasoningEffort,
		CompactionEnabled:       &compactionEnabled,
		CompactionThreshold:     &compactionThreshold,
		CompactionTargetPercent: &compactionTargetPercent,
		CompactionModelID:       ptrString(modelID(eff.CompactionModelID, deps.models)),
		DiscussProbeModelID:     modelID(eff.DiscussProbeModelID, deps.models),
		PersistFullToolResults:  &persistFullToolResults,
		ShowToolCallsInIM:       &showToolCalls,
		ToolApprovalConfig:      &toolApproval,
		DisplayEnabled:          &displayEnabled,
		OverlayEnabled:          &overlayEnabled,
		OverlayProvider:         &overlayProvider,
		OverlayConfig:           eff.OverlayConfig,
	})
	return err
}

// restoreWorkdirs recreates the backup's native-workspace workdirs and
// fills state.workdirMap for the session remap in restoreHistory. Directory
// existence is deliberately not validated here: the workspace may not be
// running yet (or the files section may not be part of this import), and a
// workdir pointing at a missing directory fails visibly on the first agent
// turn, which the user can fix by recreating the directory.
func (s *Service) restoreWorkdirs(ctx context.Context, botID, actorUserID string, state *importState) error {
	if s.workdirs == nil {
		return errors.New("workdir store not configured")
	}
	workdirs, err := readEntry[[]backupWorkdir](state, "bot/workdirs.json")
	if err != nil {
		return err
	}
	if len(workdirs) == 0 {
		return nil
	}
	existing, err := s.workdirs.ListWorkdirs(ctx, botID, true)
	if err != nil {
		return fmt.Errorf("list existing workdirs: %w", err)
	}
	nativeByPath := map[string]dbstore.BotWorkdirRecord{}
	for _, record := range existing {
		if record.RemoteBindingID == "" && record.ArchivedAt.IsZero() {
			nativeByPath[record.Path] = record
		}
	}
	restored := 0
	for _, item := range workdirs {
		if strings.TrimSpace(item.Path) == "" || strings.TrimSpace(item.Name) == "" {
			state.warnings = append(state.warnings, "workdir entry with empty name or path skipped")
			continue
		}
		if match, ok := nativeByPath[item.Path]; ok {
			// Merge mode: the target bot already has a live workdir for this
			// directory — bind restored sessions onto it instead of failing
			// the unique path constraint.
			state.workdirMap[item.ID] = optionalUUID(match.ID)
			continue
		}
		created, err := s.workdirs.CreateWorkdir(ctx, dbstore.CreateBotWorkdirInput{
			BotID:           botID,
			Name:            item.Name,
			TargetKind:      "native",
			Path:            item.Path,
			CreatedByUserID: strings.TrimSpace(actorUserID),
		})
		if err != nil {
			return fmt.Errorf("workdir %q: %w", item.Name, err)
		}
		archived := false
		if item.Archived {
			if err := s.workdirs.ArchiveWorkdir(ctx, botID, created.ID); err != nil {
				state.warnings = append(state.warnings, "workdir archive flag restore failed for "+item.Name+": "+err.Error())
			} else {
				archived = true
			}
		}
		state.workdirMap[item.ID] = optionalUUID(created.ID)
		// nativeByPath is the LIVE path index — it is what makes a later
		// backup entry for the same directory reuse this workdir instead of
		// tripping the live-path unique constraint. A workdir we just
		// archived is not live: registering it would make a later live entry
		// for the same directory look already-restored, silently remapping
		// its sessions onto the archived workdir and never recreating the
		// live one. Live-path uniqueness ignores archived rows, so the later
		// entry creates cleanly.
		if !archived {
			nativeByPath[item.Path] = created
		}
		restored++
	}
	if restored > 0 {
		state.counts[SectionWorkspace] += restored
	}
	return nil
}

func (s *Service) restoreWorkspaceResourceLimits(ctx context.Context, botID string, state *importState) error {
	if s.queries == nil {
		return errors.New("queries not configured")
	}
	limits, err := readEntry[backupWorkspaceResourceLimits](state, "bot/workspace_resource_limits.json")
	if err != nil {
		return err
	}
	if limits.CPUMillicores < 0 || limits.MemoryBytes < 0 || limits.StorageBytes < 0 {
		return errors.New("resource limits must be non-negative")
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	if _, err := s.queries.UpsertBotWorkspaceResourceLimits(ctx, sqlc.UpsertBotWorkspaceResourceLimitsParams{
		BotID:         pgBotID,
		CpuMillicores: limits.CPUMillicores,
		MemoryBytes:   limits.MemoryBytes,
		StorageBytes:  limits.StorageBytes,
	}); err != nil {
		return err
	}
	return nil
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
	configs, err := readEntry[[]channel.ChannelConfig](state, "bot/channel_configs.json")
	if err != nil {
		return err
	}
	for _, cfg := range configs {
		disabled := cfg.Disabled
		verifiedAt := cfg.VerifiedAt
		_, err := s.channels.UpsertConfig(ctx, botID, cfg.ChannelType, channel.UpsertConfigRequest{
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

// restoreHistory recreates sessions, messages and (optionally) assets in a
// single transaction so a failure leaves no partial history. When replace is
// set, existing conversation state is cleared first so the import is a real
// replacement, not a partial append.
func (s *Service) restoreHistory(ctx context.Context, actorUserID, botID string, state *importState, includeAssets, replace bool) (retErr error) {
	if s.queries == nil {
		return nil
	}
	_, resetProtected := runtimefence.ResetFromContext(ctx)
	if resetProtected {
		defer func() { retErr = runtimefence.NormalizeResetError(ctx, retErr) }()
	}
	if resetProtected && s.db == nil {
		return runtimefence.ErrTransactionsUnsupported
	}
	q := s.queries
	var tx pgx.Tx
	if s.db != nil {
		begun, err := s.db.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin history tx: %w", err)
		}
		tx = begun
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		q = s.queries.WithTx(tx)
	}

	pgBotID := optionalUUID(botID)
	if resetProtected {
		// Keep the parent lock and the fresh token/expiry validation in the
		// transaction that performs the destructive replace and restore. An old
		// reset owner therefore cannot commit after a successor takes over.
		if _, err := q.LockBotForRuntimeReset(ctx, pgBotID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return runtimefence.ErrResetLeaseLost
			}
			return fmt.Errorf("lock history restore reset: %w", err)
		}
		if err := runtimefence.ValidateResetLocked(ctx, q, botID, ""); err != nil {
			return err
		}
	}
	if replace {
		routes, err := q.ListChatRoutes(ctx, pgBotID)
		if err != nil {
			return fmt.Errorf("list routes for history replace: %w", err)
		}
		for _, route := range routes {
			if err := q.SetRouteActiveSession(ctx, sqlc.SetRouteActiveSessionParams{
				ID:              route.ID,
				ActiveSessionID: pgtype.UUID{},
			}); err != nil {
				return fmt.Errorf("clear active route session: %w", err)
			}
		}
		if err := q.DeleteSessionDiscussCursorsByBot(ctx, pgBotID); err != nil {
			return fmt.Errorf("clear discuss cursors: %w", err)
		}
		if err := q.DeleteSessionEventsByBot(ctx, pgBotID); err != nil {
			return fmt.Errorf("clear session events: %w", err)
		}
		if err := q.ClearHistoryByBot(ctx, pgBotID); err != nil {
			return fmt.Errorf("clear history: %w", err)
		}
		if err := q.SoftDeleteSessionsByBot(ctx, pgBotID); err != nil {
			return fmt.Errorf("clear sessions: %w", err)
		}
	}

	sessionMap := map[string]pgtype.UUID{}
	sessionDescriptors := map[string]restoredHistoryDescriptor{}
	sessionMetadata := map[string][]byte{}
	sessions, err := readEntry[[]sqlc.ListSessionsByBotRow](state, "history/sessions.json")
	if err != nil {
		return err
	}
	retiredSessionIDs := make(map[string]struct{})
	for _, item := range sessions {
		if isRetiredAutomationSession(item.Type, item.SessionMode) {
			retiredSessionIDs[item.ID.String()] = struct{}{}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, item := range sessions {
			if !item.ParentSessionID.Valid {
				continue
			}
			if _, parentRetired := retiredSessionIDs[item.ParentSessionID.String()]; !parentRetired {
				continue
			}
			if _, alreadyRetired := retiredSessionIDs[item.ID.String()]; !alreadyRetired {
				retiredSessionIDs[item.ID.String()] = struct{}{}
				changed = true
			}
		}
	}
	if len(retiredSessionIDs) > 0 {
		state.warnings = appendWarningOnce(state.warnings, "retired automation history was skipped")
	}
	createRestoredSession := func(item sqlc.ListSessionsByBotRow, parentSessionID pgtype.UUID) error {
		legacyType, sessionMode, runtimeType, err := restoredSessionDescriptor(item.Type, item.SessionMode, item.RuntimeType)
		if err != nil {
			return fmt.Errorf("session descriptor: %w", err)
		}
		metadata := defaultJSONMap(item.Metadata)
		runtimeMetadata := defaultJSONMap(item.RuntimeMetadata)
		if runtimeType == sessionpkg.RuntimeACPAgent {
			metadata = rebindRestoredRuntimeOwner(metadata, actorUserID)
			runtimeMetadata = rebindRestoredRuntimeOwner(runtimeMetadata, actorUserID)
		}
		// Remap the source workdir binding onto the recreated workdir. A
		// missing map entry (remote workdir, or an import without workdirs)
		// degrades the session to no workdir rather than tripping the FK.
		workdirID := pgtype.UUID{}
		if item.WorkdirID.Valid {
			workdirID = state.workdirMap[item.WorkdirID.String()]
		}
		created, err := q.CreateSession(ctx, sqlc.CreateSessionParams{
			BotID:       pgBotID,
			ChannelType: item.ChannelType,
			Type:        legacyType,
			SessionMode: sessionMode,
			RuntimeType: runtimeType,
			// Archives from before the visibility column carry an empty
			// string; normalize against the session mode's default.
			Visibility:      string(sessionpkg.NormalizeVisibility(item.Visibility, sessionMode)),
			RuntimeMetadata: runtimeMetadata,
			Title:           item.Title,
			Metadata:        metadata,
			ParentSessionID: parentSessionID,
			CreatedByUserID: optionalUUID(actorUserID),
			WorkdirID:       workdirID,
		})
		if err != nil {
			return fmt.Errorf("session: %w", err)
		}
		sessionMap[item.ID.String()] = created.ID
		sessionDescriptors[item.ID.String()] = restoredHistoryDescriptor{sessionMode: sessionMode, runtimeType: runtimeType}
		sessionMetadata[item.ID.String()] = created.Metadata
		return nil
	}
	pendingSessions := make([]sqlc.ListSessionsByBotRow, 0, len(sessions))
	for _, item := range sessions {
		if _, retired := retiredSessionIDs[item.ID.String()]; !retired {
			pendingSessions = append(pendingSessions, item)
		}
	}
	for len(pendingSessions) > 0 {
		progressed := false
		next := make([]sqlc.ListSessionsByBotRow, 0, len(pendingSessions))
		for i := len(pendingSessions) - 1; i >= 0; i-- {
			item := pendingSessions[i]
			parentSessionID := pgtype.UUID{}
			if item.ParentSessionID.Valid {
				parentSessionID = sessionMap[item.ParentSessionID.String()]
				if !parentSessionID.Valid {
					next = append(next, item)
					continue
				}
			}
			if err := createRestoredSession(item, parentSessionID); err != nil {
				return err
			}
			progressed = true
		}
		if !progressed {
			for i := len(next) - 1; i >= 0; i-- {
				if err := createRestoredSession(next[i], pgtype.UUID{}); err != nil {
					return err
				}
			}
			break
		}
		pendingSessions = next
	}

	if hasEntry(state.entries, "history/discuss_cursors.json") {
		cursors, err := readEntry[[]sqlc.BotSessionDiscussCursor](state, "history/discuss_cursors.json")
		if err != nil {
			return err
		}
		for _, cursor := range cursors {
			sessionID := sessionMap[cursor.SessionID.String()]
			if !sessionID.Valid || strings.TrimSpace(cursor.ScopeKey) == "" {
				continue
			}
			if _, err := q.UpsertSessionDiscussCursor(ctx, restoredDiscussCursorParams(sessionID, cursor)); err != nil {
				return fmt.Errorf("discuss cursor: %w", err)
			}
		}
	}

	eventMap := map[string]pgtype.UUID{}
	if hasEntry(state.entries, "history/session_events.json") {
		events, err := readEntry[[]sqlc.BotSessionEvent](state, "history/session_events.json")
		if err != nil {
			return err
		}
		for _, item := range events {
			sessionID := pgtype.UUID{}
			if item.SessionID.Valid {
				sessionID = sessionMap[item.SessionID.String()]
			}
			if !sessionID.Valid {
				continue
			}
			created, err := q.CreateSessionEvent(ctx, restoredSessionEventParams(pgBotID, sessionID, item))
			if err != nil {
				return fmt.Errorf("session event: %w", err)
			}
			eventMap[item.ID.String()] = created
		}
	}

	messages, err := readEntry[[]sqlc.ListAllMessagesForBackupRow](state, "history/messages.json")
	if err != nil {
		return err
	}
	messageMap := map[string]pgtype.UUID{}
	restoredMessages := make([]restoredHistoryMessage, 0, len(messages))
	for _, item := range messages {
		_, sessionRetired := retiredSessionIDs[item.SessionID.String()]
		if sessionRetired || isRetiredAutomationSession("", item.SessionMode) {
			state.warnings = appendWarningOnce(state.warnings, "retired automation history was skipped")
			continue
		}
		sessionID := pgtype.UUID{}
		var descriptor restoredHistoryDescriptor
		if item.SessionID.Valid {
			sessionID = sessionMap[item.SessionID.String()]
			descriptor = sessionDescriptors[item.SessionID.String()]
		}
		sessionMode := strings.TrimSpace(item.SessionMode)
		if sessionMode == "" {
			sessionMode = descriptor.sessionMode
		}
		if sessionMode == "" {
			sessionMode = sessionpkg.TypeChat
		}
		runtimeType := strings.TrimSpace(item.RuntimeType)
		if runtimeType == "" {
			runtimeType = descriptor.runtimeType
		}
		if runtimeType == "" {
			runtimeType = sessionpkg.RuntimeModel
		}
		// Bundles carry no ACP publication heads or snapshots, so imported ACP
		// sessions always start a fresh native session.
		metadata := item.Metadata
		if runtimeType == sessionpkg.RuntimeACPAgent {
			state.warnings = appendWarningOnce(state.warnings, acpCheckpointBackupWarning)
		}
		eventID := pgtype.UUID{}
		if item.EventID.Valid {
			eventID = eventMap[item.EventID.String()]
		}
		created, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
			BotID:                  pgBotID,
			SessionID:              sessionID,
			ExternalMessageID:      item.ExternalMessageID,
			SourceReplyToMessageID: item.SourceReplyToMessageID,
			Role:                   item.Role,
			Content:                item.Content,
			Metadata:               metadata,
			Usage:                  item.Usage,
			SessionMode:            sessionMode,
			RuntimeType:            runtimeType,
			EventID:                eventID,
			DisplayText:            item.DisplayText,
		})
		if err != nil {
			return fmt.Errorf("message: %w", err)
		}
		messageMap[item.ID.String()] = created.ID
		if created.ID.Valid && sessionID.Valid {
			restoredMessages = append(restoredMessages, restoredHistoryMessage{
				id:        created.ID,
				sessionID: sessionID,
				role:      created.Role,
			})
		}
		state.counts[SectionHistory]++
	}
	if err := restoreHistoryTurnReadModelFromMessages(ctx, q, pgBotID, messages, sessionMap, messageMap, restoredMessages); err != nil {
		return err
	}
	if err := rebindRestoredSessionMetadata(ctx, q, sessions, sessionMap, sessionMetadata, messageMap); err != nil {
		return err
	}

	if includeAssets {
		assets, err := readEntry[[]sqlc.ListMessageAssetsBatchRow](state, "assets/message_assets.json")
		if err != nil {
			return err
		}
		for _, asset := range assets {
			messageID := messageMap[asset.MessageID.String()]
			if !messageID.Valid {
				continue
			}
			if _, err := q.CreateMessageAsset(ctx, sqlc.CreateMessageAssetParams{
				MessageID:   messageID,
				Role:        asset.Role,
				Ordinal:     asset.Ordinal,
				ContentHash: asset.ContentHash,
				Mime:        asset.Mime,
				SizeBytes:   asset.SizeBytes,
				StorageKey:  asset.StorageKey,
				Name:        asset.Name,
				Metadata:    asset.Metadata,
			}); err != nil {
				return fmt.Errorf("message asset: %w", err)
			}
			state.counts[SectionAssets]++
		}
	}

	if tx != nil {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit history tx: %w", err)
		}
	}
	return nil
}

type restoredHistoryMessage struct {
	id        pgtype.UUID
	sessionID pgtype.UUID
	role      string
}

type restoredHistoryTurnQueries interface {
	CreateHistoryTurn(ctx context.Context, arg sqlc.CreateHistoryTurnParams) (dbstore.HistoryTurn, error)
	BindHistoryTurnAssistantByRequest(ctx context.Context, arg sqlc.BindHistoryTurnAssistantByRequestParams) (dbstore.HistoryTurn, error)
	LinkMessageToHistoryTurn(ctx context.Context, arg sqlc.LinkMessageToHistoryTurnParams) (pgtype.UUID, error)
}

type restoredHistoryMessageLinker interface {
	LinkMessageToHistoryTurn(ctx context.Context, arg sqlc.LinkMessageToHistoryTurnParams) (pgtype.UUID, error)
}

type restoredMessageTurnReadModelQueries interface {
	restoredHistoryTurnQueries
	CreateHistoryTurnWithIDAtPosition(ctx context.Context, arg sqlc.CreateHistoryTurnWithIDAtPositionParams) (dbstore.HistoryTurn, error)
	LinkMessageToHistoryTurn(ctx context.Context, arg sqlc.LinkMessageToHistoryTurnParams) (pgtype.UUID, error)
	SupersedeHistoryTurn(ctx context.Context, arg sqlc.SupersedeHistoryTurnParams) (dbstore.HistoryTurn, error)
	HideMessagesByHistoryTurn(ctx context.Context, turnID pgtype.UUID) error
	SetSessionNextTurnPosition(ctx context.Context, arg sqlc.SetSessionNextTurnPositionParams) error
}

type restoredSessionMetadataQueries interface {
	UpdateSessionMetadata(ctx context.Context, arg sqlc.UpdateSessionMetadataParams) (sqlc.BotSession, error)
}

type restoredTurnState struct {
	turnID         pgtype.UUID
	requestID      pgtype.UUID
	assistantBound bool
	seq            int64
}

type restoredMessageTurnGroup struct {
	oldTurnID             pgtype.UUID
	newTurnID             pgtype.UUID
	sessionID             pgtype.UUID
	position              int64
	requestMessageID      pgtype.UUID
	assistantMessageID    pgtype.UUID
	rows                  []restoredMessageTurnRow
	visible               bool
	supersededByOldTurnID pgtype.UUID
	supersededAt          pgtype.Timestamptz
	supersededReason      pgtype.Text
}

type restoredMessageTurnRow struct {
	messageID pgtype.UUID
	seq       int64
	createdAt pgtype.Timestamptz
}

func restoreHistoryTurnReadModelFromMessages(
	ctx context.Context,
	q restoredMessageTurnReadModelQueries,
	botID pgtype.UUID,
	messages []sqlc.ListAllMessagesForBackupRow,
	sessionMap map[string]pgtype.UUID,
	messageMap map[string]pgtype.UUID,
	fallback []restoredHistoryMessage,
) error {
	if q == nil || !botID.Valid || len(messages) == 0 {
		return nil
	}
	groupsByOldTurnID := make(map[string]*restoredMessageTurnGroup)
	for _, item := range messages {
		if !item.ID.Valid || !item.SessionID.Valid || !item.TurnID.Valid || !item.TurnPosition.Valid || !item.TurnMessageSeq.Valid || item.TurnMessageSeq.Int64 <= 0 {
			continue
		}
		sessionID := sessionMap[item.SessionID.String()]
		messageID := messageMap[item.ID.String()]
		if !sessionID.Valid || !messageID.Valid {
			continue
		}
		oldTurnKey := item.TurnID.String()
		group := groupsByOldTurnID[oldTurnKey]
		position := item.TurnPosition.Int64
		if position <= 0 {
			continue
		}
		if group == nil {
			group = &restoredMessageTurnGroup{
				oldTurnID: item.TurnID,
				newTurnID: newRestoredPGUUID(),
				sessionID: sessionID,
				position:  position,
			}
			groupsByOldTurnID[oldTurnKey] = group
		}
		group.visible = group.visible || item.TurnVisible
		switch {
		case item.TurnMessageSeq.Int64 == 1 && strings.EqualFold(strings.TrimSpace(item.Role), "user") && !group.requestMessageID.Valid:
			group.requestMessageID = messageID
		case item.TurnMessageSeq.Int64 == 2 && strings.EqualFold(strings.TrimSpace(item.Role), "assistant") && !group.assistantMessageID.Valid:
			group.assistantMessageID = messageID
		}
		if item.TurnSupersededAt.Valid && (!group.supersededAt.Valid || item.TurnSupersededAt.Time.After(group.supersededAt.Time)) {
			group.supersededAt = item.TurnSupersededAt
			group.supersededByOldTurnID = item.TurnSupersededByTurnID
			group.supersededReason = item.TurnSupersededReason
		}
		group.rows = append(group.rows, restoredMessageTurnRow{
			messageID: messageID,
			seq:       item.TurnMessageSeq.Int64,
			createdAt: item.CreatedAt,
		})
	}
	if len(groupsByOldTurnID) == 0 {
		return rebuildRestoredHistoryTurns(ctx, q, botID, fallback)
	}
	groups := make([]*restoredMessageTurnGroup, 0, len(groupsByOldTurnID))
	for _, group := range groupsByOldTurnID {
		if !group.requestMessageID.Valid && !group.assistantMessageID.Valid && len(group.rows) > 0 {
			group.assistantMessageID = group.rows[0].messageID
		}
		groups = append(groups, group)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].sessionID.String() != groups[j].sessionID.String() {
			return groups[i].sessionID.String() < groups[j].sessionID.String()
		}
		if groups[i].position != groups[j].position {
			return groups[i].position < groups[j].position
		}
		return groups[i].oldTurnID.String() < groups[j].oldTurnID.String()
	})

	turnMap := make(map[string]pgtype.UUID, len(groups))
	sessionNextPosition := map[string]int64{}
	for _, group := range groups {
		if _, err := q.CreateHistoryTurnWithIDAtPosition(ctx, sqlc.CreateHistoryTurnWithIDAtPositionParams{
			TurnID:             group.newTurnID,
			BotID:              botID,
			SessionID:          group.sessionID,
			TurnPosition:       group.position,
			RequestMessageID:   group.requestMessageID,
			AssistantMessageID: group.assistantMessageID,
		}); err != nil {
			return fmt.Errorf("restore message turn read model: %w", err)
		}
		turnMap[group.oldTurnID.String()] = group.newTurnID
		sort.SliceStable(group.rows, func(i, j int) bool {
			if group.rows[i].seq != group.rows[j].seq {
				return group.rows[i].seq < group.rows[j].seq
			}
			if !group.rows[i].createdAt.Time.Equal(group.rows[j].createdAt.Time) {
				return group.rows[i].createdAt.Time.Before(group.rows[j].createdAt.Time)
			}
			return group.rows[i].messageID.String() < group.rows[j].messageID.String()
		})
		for _, row := range group.rows {
			if err := linkRestoredHistoryMessage(ctx, q, group.newTurnID, row.messageID, row.seq); err != nil {
				return err
			}
		}
		if next := group.position + 1; next > sessionNextPosition[group.sessionID.String()] {
			sessionNextPosition[group.sessionID.String()] = next
		}
	}

	for _, group := range groups {
		if group.supersededAt.Valid {
			if _, err := q.SupersedeHistoryTurn(ctx, sqlc.SupersedeHistoryTurnParams{
				OldTurnID:          group.newTurnID,
				SessionID:          group.sessionID,
				SupersededByTurnID: mappedPGUUID(turnMap, group.supersededByOldTurnID),
				SupersededAt:       group.supersededAt,
				SupersededReason:   group.supersededReason,
			}); err != nil {
				return fmt.Errorf("restore superseded message turn: %w", err)
			}
			continue
		}
		if !group.visible {
			if err := q.HideMessagesByHistoryTurn(ctx, group.newTurnID); err != nil {
				return fmt.Errorf("hide restored message turn: %w", err)
			}
		}
	}

	for sessionKey, nextPosition := range sessionNextPosition {
		if nextPosition <= 0 {
			continue
		}
		sessionID, err := db.ParseUUID(sessionKey)
		if err != nil {
			return err
		}
		if err := q.SetSessionNextTurnPosition(ctx, sqlc.SetSessionNextTurnPositionParams{
			SessionID:        sessionID,
			NextTurnPosition: nextPosition,
		}); err != nil {
			return fmt.Errorf("restore session next turn position: %w", err)
		}
	}
	return nil
}

func mappedPGUUID(idMap map[string]pgtype.UUID, oldID pgtype.UUID) pgtype.UUID {
	if !oldID.Valid {
		return pgtype.UUID{}
	}
	return idMap[oldID.String()]
}

func newRestoredPGUUID() pgtype.UUID {
	id := uuid.New()
	return pgtype.UUID{Bytes: id, Valid: true}
}

func rebuildRestoredHistoryTurns(ctx context.Context, q restoredHistoryTurnQueries, botID pgtype.UUID, messages []restoredHistoryMessage) error {
	if q == nil || !botID.Valid || len(messages) == 0 {
		return nil
	}
	states := make(map[string]*restoredTurnState)
	for _, msg := range messages {
		if !msg.id.Valid || !msg.sessionID.Valid {
			continue
		}
		sessionKey := msg.sessionID.String()
		state := states[sessionKey]
		role := strings.ToLower(strings.TrimSpace(msg.role))
		switch role {
		case "user":
			turn, err := q.CreateHistoryTurn(ctx, sqlc.CreateHistoryTurnParams{
				BotID:            botID,
				SessionID:        msg.sessionID,
				RequestMessageID: msg.id,
			})
			if err != nil {
				return fmt.Errorf("create restored history turn: %w", err)
			}
			state = &restoredTurnState{turnID: turn.ID, requestID: msg.id, seq: 1}
			states[sessionKey] = state
		case "assistant":
			if state == nil || !state.turnID.Valid {
				turn, err := q.CreateHistoryTurn(ctx, sqlc.CreateHistoryTurnParams{
					BotID:              botID,
					SessionID:          msg.sessionID,
					AssistantMessageID: msg.id,
				})
				if err != nil {
					return fmt.Errorf("create restored orphan assistant turn: %w", err)
				}
				state = &restoredTurnState{turnID: turn.ID, assistantBound: true, seq: 2}
				states[sessionKey] = state
			} else {
				if !state.assistantBound && state.requestID.Valid {
					if _, err := q.BindHistoryTurnAssistantByRequest(ctx, sqlc.BindHistoryTurnAssistantByRequestParams{
						SessionID:          msg.sessionID,
						RequestMessageID:   state.requestID,
						AssistantMessageID: msg.id,
					}); err != nil {
						return fmt.Errorf("bind restored assistant turn: %w", err)
					}
					state.assistantBound = true
				}
				state.seq++
			}
		default:
			if state == nil || !state.turnID.Valid {
				turn, err := q.CreateHistoryTurn(ctx, sqlc.CreateHistoryTurnParams{
					BotID:              botID,
					SessionID:          msg.sessionID,
					AssistantMessageID: msg.id,
				})
				if err != nil {
					return fmt.Errorf("create restored message turn: %w", err)
				}
				state = &restoredTurnState{turnID: turn.ID, assistantBound: true, seq: 2}
				states[sessionKey] = state
			} else {
				state.seq++
			}
		}
		if err := linkRestoredHistoryMessage(ctx, q, state.turnID, msg.id, state.seq); err != nil {
			return err
		}
	}
	return nil
}

func linkRestoredHistoryMessage(ctx context.Context, q restoredHistoryMessageLinker, turnID pgtype.UUID, messageID pgtype.UUID, seq int64) error {
	if !turnID.Valid || !messageID.Valid || seq <= 0 {
		return nil
	}
	if _, err := q.LinkMessageToHistoryTurn(ctx, sqlc.LinkMessageToHistoryTurnParams{
		TurnID:         turnID,
		MessageID:      messageID,
		TurnMessageSeq: pgtype.Int8{Int64: seq, Valid: true},
	}); err != nil {
		return fmt.Errorf("link restored history message: %w", err)
	}
	return nil
}

func rebindRestoredSessionMetadata(
	ctx context.Context,
	q restoredSessionMetadataQueries,
	sessions []sqlc.ListSessionsByBotRow,
	sessionMap map[string]pgtype.UUID,
	sessionMetadata map[string][]byte,
	messageMap map[string]pgtype.UUID,
) error {
	if q == nil || len(sessions) == 0 {
		return nil
	}
	for _, item := range sessions {
		oldSessionID := item.ID.String()
		newSessionID := sessionMap[oldSessionID]
		if !newSessionID.Valid {
			continue
		}
		raw := sessionMetadata[oldSessionID]
		if len(raw) == 0 {
			raw = item.Metadata
		}
		metadata, changed := rebindRestoredForkMetadata(raw, sessionMap, messageMap)
		if !changed {
			continue
		}
		if _, err := q.UpdateSessionMetadata(ctx, sqlc.UpdateSessionMetadataParams{
			ID:       newSessionID,
			Metadata: metadata,
		}); err != nil {
			return fmt.Errorf("restore session metadata: %w", err)
		}
	}
	return nil
}

func rebindRestoredForkMetadata(raw []byte, sessionMap map[string]pgtype.UUID, messageMap map[string]pgtype.UUID) ([]byte, bool) {
	var metadata map[string]any
	if err := json.Unmarshal(defaultJSONMap(raw), &metadata); err != nil || metadata == nil {
		return raw, false
	}
	forkRaw, ok := metadata["forked_from"]
	if !ok {
		return raw, false
	}
	fork, ok := forkRaw.(map[string]any)
	if !ok || fork == nil {
		return raw, false
	}
	changed := remapForkMetadataID(fork, "session_id", sessionMap)
	if remapForkMetadataID(fork, "message_id", messageMap) {
		changed = true
	}
	if remapForkMetadataID(fork, "fork_message_id", messageMap) {
		changed = true
	}
	if !changed {
		return raw, false
	}
	metadata["forked_from"] = fork
	out, err := json.Marshal(metadata)
	if err != nil {
		return raw, false
	}
	return out, true
}

func remapForkMetadataID(fork map[string]any, key string, idMap map[string]pgtype.UUID) bool {
	value, ok := fork[key].(string)
	if !ok {
		return false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	mapped := idMap[value]
	if !mapped.Valid {
		return false
	}
	next := mapped.String()
	if next == value {
		return false
	}
	fork[key] = next
	return true
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

func (s *Service) ensureMemoryProvider(ctx context.Context, item memprovider.ProviderGetResponse) (string, error) {
	if s.memoryProviders == nil {
		return item.ID, errors.New("memory provider service not configured")
	}
	list, _ := s.memoryProviders.List(ctx)
	for _, existing := range list {
		if existing.Name == item.Name {
			return existing.ID, nil
		}
	}
	created, err := s.memoryProviders.Create(ctx, memprovider.ProviderCreateRequest{
		Name:     item.Name,
		Provider: memprovider.ProviderType(item.Provider),
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

type restoredHistoryDescriptor struct {
	sessionMode string
	runtimeType string
}

func restoredSessionDescriptor(legacyType, sessionMode, runtimeType string) (string, string, string, error) {
	sessionMode = strings.TrimSpace(sessionMode)
	runtimeType = strings.TrimSpace(runtimeType)
	if !sessionpkg.IsKnownSessionMode(sessionMode) || !sessionpkg.IsKnownRuntimeType(runtimeType) {
		derivedMode, derivedRuntime := sessionpkg.DescriptorFromLegacyType(legacyType)
		if !sessionpkg.IsKnownSessionMode(sessionMode) {
			sessionMode = derivedMode
		}
		if !sessionpkg.IsKnownRuntimeType(runtimeType) {
			runtimeType = derivedRuntime
		}
	}
	return sessionpkg.ResolveDescriptor(legacyType, sessionMode, runtimeType)
}

func isRetiredAutomationSession(legacyType, sessionMode string) bool {
	return strings.EqualFold(strings.TrimSpace(legacyType), "heartbeat") ||
		strings.EqualFold(strings.TrimSpace(sessionMode), "heartbeat")
}

func defaultJSONMap(raw []byte) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte(`{}`)
	}
	return raw
}

func rebindRestoredRuntimeOwner(raw []byte, ownerUserID string) []byte {
	raw = defaultJSONMap(raw)
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil || meta == nil {
		return []byte(`{}`)
	}
	delete(meta, "runtime_owner_account_id")
	if ownerUserID = strings.TrimSpace(ownerUserID); ownerUserID != "" {
		meta["runtime_owner_account_id"] = ownerUserID
	}
	out, err := json.Marshal(meta)
	if err != nil {
		return []byte(`{}`)
	}
	return out
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

// restoredSessionEventParams builds the insert for one restored event. The
// source deployment's cursors are instance-local coordinates that cannot be
// compared against this deployment's sequence, so they are dropped and the
// restored history gates in the source-time domain.
func restoredSessionEventParams(botID, sessionID pgtype.UUID, item sqlc.BotSessionEvent) sqlc.CreateSessionEventParams {
	return sqlc.CreateSessionEventParams{
		BotID:                   botID,
		SessionID:               sessionID,
		EventKind:               item.EventKind,
		EventData:               sanitizeRestoredEventData(defaultJSONMap(item.EventData)),
		ExternalMessageID:       item.ExternalMessageID,
		SenderChannelIdentityID: pgtype.UUID{},
		ReceivedAtMs:            item.ReceivedAtMs,
	}
}

// restoredDiscussCursorParams keeps the source-time watermark and drops the
// instance-local event watermark for the same reason.
func restoredDiscussCursorParams(sessionID pgtype.UUID, cursor sqlc.BotSessionDiscussCursor) sqlc.UpsertSessionDiscussCursorParams {
	return sqlc.UpsertSessionDiscussCursorParams{
		SessionID:           sessionID,
		ScopeKey:            cursor.ScopeKey,
		RouteID:             pgtype.UUID{},
		Source:              cursor.Source,
		ConsumedCursor:      cursor.ConsumedCursor,
		ConsumedEventCursor: 0,
	}
}

// sanitizeRestoredEventData strips instance-local event cursors from restored
// payloads: source-instance coordinates would race or poison the local
// sequence, and cursor-less events gate correctly in the source-time domain.
func sanitizeRestoredEventData(eventData []byte) []byte {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(eventData, &payload); err != nil {
		return eventData
	}
	if _, ok := payload["event_cursor"]; !ok {
		return eventData
	}
	delete(payload, "event_cursor")
	sanitized, err := json.Marshal(payload)
	if err != nil {
		return eventData
	}
	return sanitized
}
