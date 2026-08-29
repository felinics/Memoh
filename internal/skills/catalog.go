package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/slash"
)

type SafeCatalogItem struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	SourceKind  string `json:"source_kind"`
	State       string `json:"state"`
}

type ResolvedSkill struct {
	Name           string
	Description    string
	Content        string
	SourceKind     string
	OpaqueSourceID string
	ContentHash    string
	Identity       string
}

// Default requested-skill limits. These are fail-closed safety valves, not
// product knobs — they are deliberately NOT configurable (a zero-valued
// ResolveLimits field falls back to its default, so callers can't
// accidentally run uncapped).
const (
	DefaultMaxRequestedSkills         = 5
	DefaultMaxSingleSkillContextBytes = 64 * 1024
	DefaultMaxTotalSkillContextBytes  = 256 * 1024
)

type ResolveLimits struct {
	MaxCount       int
	MaxSingleBytes int
	MaxTotalBytes  int
}

func (l ResolveLimits) withDefaults() ResolveLimits {
	if l.MaxCount <= 0 {
		l.MaxCount = DefaultMaxRequestedSkills
	}
	if l.MaxSingleBytes <= 0 {
		l.MaxSingleBytes = DefaultMaxSingleSkillContextBytes
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = DefaultMaxTotalSkillContextBytes
	}
	return l
}

func BuildSafeCatalog(entries []Entry) ([]SafeCatalogItem, error) {
	entries = normalizeRuntimeUsabilityEntries(entries)
	groups := groupEntriesByName(entries)
	out := make([]SafeCatalogItem, 0, len(entries))
	for _, entry := range entries {
		if !isRuntimeUsableCandidate(entry, groups[entry.Name]) {
			continue
		}
		out = append(out, SafeCatalogItem{
			Name:        entry.Name,
			DisplayName: entry.Name,
			Description: entry.Description,
			SourceKind:  entry.SourceKind,
			State:       StateEffective,
		})
	}
	return out, nil
}

func ResolveTextRequestedSkills(entries []Entry, names []string, limits ResolveLimits) ([]ResolvedSkill, error) {
	limits = limits.withDefaults()
	entries = normalizeRuntimeUsabilityEntries(entries)
	deduped, err := dedupeRequestedNames(names)
	if err != nil {
		return nil, err
	}
	if len(deduped) > limits.MaxCount {
		return nil, slash.NewError(slash.CodeTooManyRequestedSkills)
	}
	groups := groupEntriesByName(entries)
	out := make([]ResolvedSkill, 0, len(deduped))
	totalBytes := 0
	for _, name := range deduped {
		if !IsValidName(name) {
			return nil, slash.NewError(slash.CodeInvalidSkillSlashSyntax)
		}
		group := groups[name]
		if len(group) == 0 {
			return nil, slash.NewError(slash.CodeRequestedSkillNotFound)
		}
		entry, code := resolveTextCandidate(group)
		if code != "" {
			return nil, slash.NewError(code)
		}
		contentBytes := len([]byte(entry.Content))
		totalBytes += contentBytes
		if contentBytes > limits.MaxSingleBytes {
			return nil, slash.NewError(slash.CodeRequestedSkillContextTooLarge)
		}
		if totalBytes > limits.MaxTotalBytes {
			return nil, slash.NewError(slash.CodeRequestedSkillContextTooLarge)
		}
		out = append(out, resolvedSkill(entry))
	}
	return out, nil
}

func groupEntriesByName(entries []Entry) map[string][]Entry {
	groups := make(map[string][]Entry, len(entries))
	for _, entry := range entries {
		groups[entry.Name] = append(groups[entry.Name], entry)
	}
	return groups
}

func resolveTextCandidate(group []Entry) (Entry, string) {
	if len(group) == 0 {
		return Entry{}, slash.CodeRequestedSkillNotFound
	}
	effective := make([]Entry, 0, 1)
	for _, entry := range group {
		if entry.State == StateEffective {
			effective = append(effective, entry)
		}
	}
	if len(effective) > 1 {
		return Entry{}, slash.CodeRequestedSkillAmbiguous
	}
	if len(effective) == 0 {
		for _, entry := range group {
			if entry.State == StateDisabled {
				return Entry{}, slash.CodeRequestedSkillDisabled
			}
		}
		return Entry{}, slash.CodeRequestedSkillAmbiguous
	}
	entry := effective[0]
	if entry.State == StateDisabled {
		return Entry{}, slash.CodeRequestedSkillDisabled
	}
	if code := runtimeRejectCode(entry, group); code != "" {
		return Entry{}, code
	}
	return entry, ""
}

func runtimeRejectCode(entry Entry, group []Entry) string {
	if entry.State == StateShadowed {
		return slash.CodeRequestedSkillAmbiguous
	}
	if entry.State == StateDisabled {
		return slash.CodeRequestedSkillDisabled
	}
	effectiveCount := 0
	for _, item := range group {
		if item.State == StateEffective {
			effectiveCount++
		}
	}
	if effectiveCount > 1 {
		return slash.CodeRequestedSkillAmbiguous
	}
	if !isRuntimeUsableEntry(entry) {
		return slash.CodeRequestedSkillNotRuntimeUsable
	}
	return ""
}

func isRuntimeUsableCandidate(entry Entry, group []Entry) bool {
	return runtimeRejectCode(entry, group) == ""
}

func isRuntimeUsableEntry(entry Entry) bool {
	if !entry.RuntimeUsabilityChecked {
		entry = NormalizeRuntimeUsability(entry)
	}
	return entry.RuntimeUsable
}

func resolvedSkill(entry Entry) ResolvedSkill {
	contentHash := contentHashForEntry(entry)
	identity := strings.Join([]string{entry.Name, entry.SourceKind, entry.SourceRoot, entry.SourcePath, contentHash}, "|")
	return ResolvedSkill{
		Name:           entry.Name,
		Description:    entry.Description,
		Content:        entry.Content,
		SourceKind:     entry.SourceKind,
		OpaqueSourceID: "",
		ContentHash:    contentHash,
		Identity:       identity,
	}
}

func dedupeRequestedNames(names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, slash.NewError(slash.CodeInvalidSkillSlashSyntax)
		}
		if !IsValidName(name) {
			return nil, slash.NewError(slash.CodeInvalidSkillSlashSyntax)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, nil
}

func contentHashForEntry(entry Entry) string {
	content := entry.Raw
	if content == "" {
		content = entry.Content
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// RequestedSkillContexts converts resolved skills into the conversation
// request shape. Both public entry points (the web WS handler and the channel
// inbound processor) call this one converter, so they can never diverge on
// which fields a requested skill carries into the model context.
func RequestedSkillContexts(resolved []ResolvedSkill) []turn.RequestedSkillContext {
	out := make([]turn.RequestedSkillContext, 0, len(resolved))
	for _, item := range resolved {
		out = append(out, turn.RequestedSkillContext{
			Name:           item.Name,
			Description:    item.Description,
			Content:        item.Content,
			SourceKind:     item.SourceKind,
			OpaqueSourceID: item.OpaqueSourceID,
			ContentHash:    item.ContentHash,
			Identity:       item.Identity,
		})
	}
	return out
}
