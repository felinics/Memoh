package bot

import (
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// nameFormat mirrors the bots_name_format_check database constraint:
// lowercase alphanumerics and dashes, starting with an alphanumeric, 2-63 chars.
var nameFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// reservedNames are slugs that must not be used as bot names because they
// collide with reserved frontend routes or have special meaning.
var reservedNames = map[string]struct{}{
	"new":      {},
	"edit":     {},
	"settings": {},
	"admin":    {},
	"bots":     {},
	"bot":      {},
	"chat":     {},
	"team":     {},
	"teams":    {},
	"api":      {},
	"home":     {},
	"login":    {},
	"logout":   {},
	"me":       {},
	"system":   {},
}

// reasons returned by ValidateName / CheckNameAvailability.
const (
	NameReasonInvalid  = "invalid"
	NameReasonReserved = "reserved"
	NameReasonTaken    = "taken"
)

// normalizeName trims and lowercases a candidate bot name.
func normalizeName(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// validateNameFormat checks a normalized name against the slug format and the
// reserved-name blocklist. It returns one of the NameReason* constants, or an
// empty string when the name is well-formed and allowed.
func validateNameFormat(name string) string {
	if !nameFormat.MatchString(name) {
		return NameReasonInvalid
	}
	if _, ok := reservedNames[name]; ok {
		return NameReasonReserved
	}
	return ""
}

// slugify converts an arbitrary display name into a slug candidate that
// satisfies the name format. It lowercases, replaces runs of non-alphanumerics
// with a single dash, trims dashes, and clamps the length. When the result is
// empty it falls back to a unique "bot-<uuid>" slug.
func slugify(displayName string) string {
	lower := strings.ToLower(strings.TrimSpace(displayName))
	var b strings.Builder
	prevDash := false
	for _, r := range lower {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	// Leading character must be alphanumeric; the regex guarantees this once
	// non-empty, but an all-symbol display name yields an empty slug.
	if slug == "" || validateNameFormat(slug) != "" {
		return "bot-" + uuid.NewString()
	}
	return slug
}
