// Package sticker owns a bot's sticker library: the stickers it has seen,
// the agent-authored description of each one, and the platform reference
// needed to send it again.
//
// The library lives in the bot's workspace as markdown, not in Postgres. A
// sticker description is agent-authored prose about the bot's own expressive
// vocabulary — the same kind of content as memory files, editable by hand,
// and worthless to anyone but that bot.
package sticker

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Sticker kinds, as reported by the source platform.
const (
	KindStatic   = "static"
	KindAnimated = "animated"
	KindVideo    = "video"
)

// PlatformTelegram is the only platform with native sticker semantics today.
const PlatformTelegram = "telegram"

var (
	ErrNotConfigured  = errors.New("sticker library not configured")
	ErrRefRequired    = errors.New("sticker ref is required")
	ErrDescRequired   = errors.New("sticker description is required")
	errMissingHeader  = errors.New("sticker entry has no frontmatter")
	errUnclosedHeader = errors.New("sticker entry frontmatter is not closed")
)

// Entry is one sticker in the library.
//
// Ref is what actually sends the sticker and UniqueID is what identifies it.
// They are separate because Telegram scopes a file_id to the bot token that
// received it while file_unique_id is stable across bots but cannot be sent —
// so a library keyed on either one alone is either unsendable or unmergeable.
type Entry struct {
	Platform string `yaml:"platform"`
	Ref      string `yaml:"ref"`
	UniqueID string `yaml:"unique_id,omitempty"`
	Pack     string `yaml:"pack,omitempty"`
	Emoji    string `yaml:"emoji,omitempty"`
	Kind     string `yaml:"kind,omitempty"`
	// ContentHash points at the stored preview bytes in the media store. It is
	// the only fallback when a file_id stops working, and it only rescues
	// static stickers: Telegram refuses re-uploaded .tgs and .webm.
	ContentHash string    `yaml:"content_hash,omitempty"`
	Tags        []string  `yaml:"tags,omitempty"`
	SavedAt     time.Time `yaml:"saved_at,omitempty"`
	UpdatedAt   time.Time `yaml:"updated_at,omitempty"`

	// Description is the file body rather than a frontmatter field: it is the
	// part a human or the agent rewrites by hand, and multi-line prose in YAML
	// invites quoting mistakes that would cost the whole entry.
	Description string `yaml:"-"`
	// Path is the workspace path this entry was read from, filled on read and
	// never serialized.
	Path string `yaml:"-"`
}

// Identity returns the strongest stable key for merging two sightings of the
// same sticker.
func (e Entry) Identity() string {
	if id := strings.TrimSpace(e.UniqueID); id != "" {
		return id
	}
	return strings.TrimSpace(e.Ref)
}

// merge folds a fresh sighting into a stored entry. Stored prose wins only
// when the incoming sighting has none: a re-send carries a current file_id but
// no description, while an explicit save carries a description the agent means
// to replace the old one with.
func (stored Entry) merge(incoming Entry, now time.Time) Entry {
	merged := stored
	merged.Platform = firstNonEmpty(incoming.Platform, stored.Platform, PlatformTelegram)
	merged.Ref = firstNonEmpty(incoming.Ref, stored.Ref)
	merged.UniqueID = firstNonEmpty(incoming.UniqueID, stored.UniqueID)
	merged.Pack = firstNonEmpty(incoming.Pack, stored.Pack)
	merged.Emoji = firstNonEmpty(incoming.Emoji, stored.Emoji)
	merged.Kind = firstNonEmpty(incoming.Kind, stored.Kind)
	merged.ContentHash = firstNonEmpty(incoming.ContentHash, stored.ContentHash)
	if len(incoming.Tags) > 0 {
		merged.Tags = normalizeTags(incoming.Tags)
	}
	if desc := strings.TrimSpace(incoming.Description); desc != "" {
		merged.Description = desc
	}
	if merged.SavedAt.IsZero() {
		merged.SavedAt = now
	}
	merged.UpdatedAt = now
	return merged
}

func (e Entry) normalized(now time.Time) Entry {
	e.Platform = strings.TrimSpace(e.Platform)
	if e.Platform == "" {
		e.Platform = PlatformTelegram
	}
	e.Ref = strings.TrimSpace(e.Ref)
	e.UniqueID = strings.TrimSpace(e.UniqueID)
	e.Pack = strings.TrimSpace(e.Pack)
	e.Emoji = strings.TrimSpace(e.Emoji)
	e.Kind = normalizeKind(e.Kind)
	e.ContentHash = strings.TrimSpace(e.ContentHash)
	e.Tags = normalizeTags(e.Tags)
	e.Description = strings.TrimSpace(e.Description)
	if e.SavedAt.IsZero() {
		e.SavedAt = now
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = now
	}
	return e
}

func normalizeKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case KindAnimated:
		return KindAnimated
	case KindVideo:
		return KindVideo
	case KindStatic:
		return KindStatic
	default:
		return ""
	}
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		trimmed := strings.TrimSpace(tag)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// formatEntry renders an entry as frontmatter plus prose.
func formatEntry(entry Entry) (string, error) {
	meta, err := yaml.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("marshal sticker frontmatter: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(meta)
	sb.WriteString("---\n\n")
	sb.WriteString(strings.TrimSpace(entry.Description))
	sb.WriteString("\n")
	return sb.String(), nil
}

// parseEntry reads back what formatEntry wrote. A hand-edited body is the
// expected case, so only the frontmatter is parsed strictly.
func parseEntry(content string) (Entry, error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	rest, ok := strings.CutPrefix(strings.TrimLeft(normalized, "\n"), "---\n")
	if !ok {
		return Entry{}, errMissingHeader
	}
	header, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return Entry{}, errUnclosedHeader
	}
	var entry Entry
	if err := yaml.Unmarshal([]byte(header), &entry); err != nil {
		return Entry{}, fmt.Errorf("parse sticker frontmatter: %w", err)
	}
	entry.Description = strings.TrimSpace(body)
	return entry, nil
}

// matchTokens reports whether every token appears somewhere in the entry's
// searchable text. Tokens are ANDed so a two-word query narrows instead of
// widening, which is what "find the cat sticker that waves" needs.
func matchTokens(entry Entry, tokens []string) bool {
	if len(tokens) == 0 {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		entry.Description,
		entry.Emoji,
		entry.Pack,
		strings.Join(entry.Tags, " "),
	}, " "))
	for _, token := range tokens {
		if !strings.Contains(haystack, token) {
			return false
		}
	}
	return true
}

func queryTokens(query string) []string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
