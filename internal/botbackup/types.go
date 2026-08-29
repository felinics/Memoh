package botbackup

import (
	"errors"
	"time"
)

// ErrHistoryResetUnavailable means an overwrite that can invalidate ACP state
// could not acquire the mandatory distributed runtime-reset boundary.
var ErrHistoryResetUnavailable = errors.New("bot backup: ACP history reset coordination is unavailable")

const (
	BackupSchemaVersion = 1
	ManifestPath        = "manifest.json"
	// workspaceArchivePath is the single zip entry holding the container's
	// /data tar.gz verbatim (no re-packing on export/import).
	workspaceArchivePath = "workspace/data.tar.gz"
)

// Section identifies a selectable group of data within a backup. On import,
// each section can be individually included or skipped. The bot profile is
// always imported (it creates or updates the bot itself) and is not a section.
type Section string

const (
	SectionProfile   Section = "profile"
	SectionSettings  Section = "settings"
	SectionModels    Section = "models"
	SectionACL       Section = "acl"
	SectionChannels  Section = "channels"
	SectionMCP       Section = "mcp"
	SectionSchedules Section = "schedules"
	SectionEmail     Section = "email"
	SectionHistory   Section = "history"
	SectionAssets    Section = "assets"
	SectionWorkspace Section = "workspace"
)

// AllExportSections lists the sections a backup can contain, in display order.
// profile is always exported (it identifies the bot) and is not user-toggleable.
var AllExportSections = []Section{
	SectionSettings, SectionModels, SectionACL, SectionChannels, SectionMCP,
	SectionSchedules, SectionEmail, SectionHistory, SectionAssets, SectionWorkspace,
}

// isSensitiveSection reports whether a section may carry API keys or other
// credentials, so the UI can warn before export/import. Single source of truth.
func isSensitiveSection(key Section) bool {
	switch key {
	case SectionModels, SectionChannels, SectionMCP, SectionEmail:
		return true
	default:
		return false
	}
}

// ImportStrategy controls how a section is applied to the target bot on import.
type ImportStrategy string

const (
	StrategySkip    ImportStrategy = "skip"
	StrategyMerge   ImportStrategy = "merge"
	StrategyReplace ImportStrategy = "replace"
)

type ExportOptions struct {
	// Sections lists which sections to include. nil/empty ⇒ all sections.
	// profile is always exported regardless.
	Sections []Section `json:"sections,omitempty"`
}

// ExportRequest is the export endpoint body. Passphrase is kept out of
// ExportOptions on purpose so it can never leak into the bundle manifest: when
// set, the resulting bundle is encrypted with it.
type ExportRequest struct {
	Sections   []Section `json:"sections,omitempty"`
	Passphrase string    `json:"passphrase,omitempty"`
}

// wants reports whether the section should be included in the export.
func (o ExportOptions) wants(section Section) bool {
	if section == SectionProfile {
		return true
	}
	if len(o.Sections) == 0 {
		return true
	}
	for _, s := range o.Sections {
		if s == section {
			return true
		}
	}
	return false
}

type ImportMode string

const (
	ImportModeCreate    ImportMode = "create"
	ImportModeOverwrite ImportMode = "overwrite"
)

type ImportOptions struct {
	Mode        ImportMode `json:"mode,omitempty"`
	TargetBotID string     `json:"target_bot_id,omitempty"`
	// Sections maps a section to its import strategy (skip|merge|replace).
	// nil ⇒ every section imported with the default (merge). A section absent
	// from a non-nil map, or mapped to "skip", is not imported.
	Sections map[Section]ImportStrategy `json:"sections,omitempty"`
}

// strategyFor returns the effective strategy for a section.
func (o ImportOptions) strategyFor(section Section) ImportStrategy {
	if o.Sections == nil {
		return StrategyMerge
	}
	s, ok := o.Sections[section]
	if !ok || s == StrategySkip {
		return StrategySkip
	}
	if s != StrategyReplace {
		return StrategyMerge
	}
	return StrategyReplace
}

// wants reports whether the section should be imported at all.
func (o ImportOptions) wants(section Section) bool {
	return o.strategyFor(section) != StrategySkip
}

type Manifest struct {
	SchemaVersion int               `json:"schema_version"`
	App           string            `json:"app"`
	ExportedAt    time.Time         `json:"exported_at"`
	SourceBotID   string            `json:"source_bot_id"`
	SourceBotName string            `json:"source_bot_name"`
	Options       ManifestOptions   `json:"options"`
	Entries       []ManifestEntry   `json:"entries"`
	Warnings      []string          `json:"warnings,omitempty"`
	Checksums     map[string]string `json:"checksums,omitempty"`
}

type ManifestOptions struct {
	Sections []Section `json:"sections,omitempty"`
}

type ManifestEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

type PreviewResult struct {
	Manifest    Manifest         `json:"manifest"`
	Profile     *ProfilePreview  `json:"profile,omitempty"`
	Conflicts   []string         `json:"conflicts"`
	Missing     []string         `json:"missing"`
	Warnings    []string         `json:"warnings"`
	Sections    []SectionSummary `json:"sections"`
	RestorePlan RestorePlan      `json:"restore_plan"`
	// Encrypted reports that the uploaded bundle is passphrase-encrypted. When
	// true and no (or a wrong) passphrase was supplied, the other fields are
	// empty and the UI should prompt for the passphrase. RequiresPassphrase
	// distinguishes "needs a passphrase" from "passphrase was wrong".
	Encrypted          bool `json:"encrypted"`
	RequiresPassphrase bool `json:"requires_passphrase"`
}

// ProfilePreview surfaces the backup's bot identity so the UI can show an
// avatar + name card before importing.
type ProfilePreview struct {
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
	Timezone    string `json:"timezone"`
	IsActive    bool   `json:"is_active"`
}

// SectionSummary reports a restorable section, how many items it holds, and (in
// overwrite mode) how many the target already has. Items lists a sample of the
// contained item labels for an expandable detail view.
type SectionSummary struct {
	Key         Section  `json:"key"`
	Count       int      `json:"count"`
	TargetCount int      `json:"target_count"`
	Conflict    bool     `json:"conflict"`
	Sensitive   bool     `json:"sensitive"`
	Items       []string `json:"items,omitempty"`
}

// SummaryResult describes what a live bot would export, for the export dialog.
type SummaryResult struct {
	Profile  *ProfilePreview  `json:"profile,omitempty"`
	Sections []SectionSummary `json:"sections"`
}

type RestorePlan struct {
	Mode                 ImportMode     `json:"mode"`
	TargetBotID          string         `json:"target_bot_id,omitempty"`
	WillCreateBot        bool           `json:"will_create_bot"`
	WillRestoreWorkspace bool           `json:"will_restore_workspace"`
	DependencyMatches    map[string]int `json:"dependency_matches,omitempty"`
}

type ImportResult struct {
	BotID    string   `json:"bot_id"`
	Created  bool     `json:"created"`
	Warnings []string `json:"warnings,omitempty"`
	// Imported reports how many items were restored per section, powering the
	// post-import summary in the UI.
	Imported map[Section]int `json:"imported,omitempty"`
}

type backupData struct {
	Profile                 any                            `json:"profile,omitempty"`
	Settings                any                            `json:"settings,omitempty"`
	WorkspaceResourceLimits *backupWorkspaceResourceLimits `json:"workspace_resource_limits,omitempty"`
	Workdirs                []backupWorkdir                `json:"workdirs,omitempty"`
	ACLRules                any                            `json:"acl_rules,omitempty"`
	Channels                any                            `json:"channels,omitempty"`
	MCP                     any                            `json:"mcp,omitempty"`
	Schedules               any                            `json:"schedules,omitempty"`
	EmailBindings           any                            `json:"email_bindings,omitempty"`
	Dependencies            backupDependencies             `json:"dependencies,omitempty"`
	History                 backupHistory                  `json:"history,omitempty"`
}

// backupWorkdir is a native-workspace workdir directory. Remote workdirs are
// excluded on purpose: they reference a specific person's computer, which a
// backup restored elsewhere cannot reach. ID is the source row id, kept so
// restored sessions can be remapped onto the recreated workdirs.
type backupWorkdir struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Archived bool   `json:"archived,omitempty"`
}

type backupWorkspaceResourceLimits struct {
	CPUMillicores int64 `json:"cpu_millicores"`
	MemoryBytes   int64 `json:"memory_bytes"`
	StorageBytes  int64 `json:"storage_bytes"`
}

type backupDependencies struct {
	Providers       any `json:"providers,omitempty"`
	Models          any `json:"models,omitempty"`
	SearchProviders any `json:"search_providers,omitempty"`
	FetchProviders  any `json:"fetch_providers,omitempty"`
	MemoryProviders any `json:"memory_providers,omitempty"`
	EmailProviders  any `json:"email_providers,omitempty"`
}

type backupHistory struct {
	Sessions       any `json:"sessions,omitempty"`
	Messages       any `json:"messages,omitempty"`
	DiscussCursors any `json:"discuss_cursors,omitempty"`
	SessionEvents  any `json:"session_events,omitempty"`
	Assets         any `json:"assets,omitempty"`
}
