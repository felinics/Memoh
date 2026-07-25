package slack

import (
	"strings"
	"unicode/utf8"

	"github.com/kenshaw/emoji"
)

// resolveSlackEmoji converts a Unicode emoji character to its Slack shortcode
// name using the Gemoji dataset. Slack's reactions.add API requires shortcode
// names (e.g. "thumbsup") rather than Unicode characters (e.g. "👍").
// If the input is already a valid shortcode (ASCII text) or cannot be resolved,
// it is returned after stripping any surrounding colons.
func resolveSlackEmoji(raw string) string {
	raw = strings.Trim(raw, ":")
	if raw == "" {
		return raw
	}
	if resolved, ok := resolveSlackEmojiAlias(raw); ok {
		return resolved
	}
	return raw
}

func resolveSlackEmojiAlias(raw string) (string, bool) {
	e := emoji.FromCode(raw)
	if e != nil && len(e.Aliases) > 0 {
		return e.Aliases[0], true
	}

	base, tone, changed := splitEmojiSkinTone(raw)
	if !changed {
		return "", false
	}

	e = emoji.FromCode(base)
	if e == nil || len(e.Aliases) == 0 {
		return "", false
	}

	alias := e.Aliases[0]
	if tone == emoji.Neutral {
		return alias, true
	}
	return alias + "::skin-tone-" + slackSkinToneSuffix(tone), true
}

func splitEmojiSkinTone(raw string) (string, emoji.SkinTone, bool) {
	var (
		tone    emoji.SkinTone
		changed bool
		runes   = make([]rune, 0, utf8.RuneCountInString(raw))
	)

	for _, r := range raw {
		switch emoji.SkinTone(r) {
		case emoji.Light, emoji.MediumLight, emoji.Medium, emoji.MediumDark, emoji.Dark:
			tone = emoji.SkinTone(r)
			changed = true
			continue
		}
		if r == '\uFE0F' {
			changed = true
			continue
		}
		runes = append(runes, r)
	}

	if !changed {
		return raw, emoji.Neutral, false
	}
	return string(runes), tone, true
}

func slackSkinToneSuffix(tone emoji.SkinTone) string {
	switch tone {
	case emoji.Light:
		return "2"
	case emoji.MediumLight:
		return "3"
	case emoji.Medium:
		return "4"
	case emoji.MediumDark:
		return "5"
	case emoji.Dark:
		return "6"
	default:
		return ""
	}
}
