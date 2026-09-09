package telegram

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	tele "gopkg.in/telebot.v4"

	"github.com/felinics/memoh/internal/channel"
)

// extractTelegramMessageParts converts Telegram entities into channel.MessagePart
// slices, interleaving styled spans with the surrounding plain text. Returns nil
// when the resulting slice would carry no rich information (so callers can fall
// back to the plain Text field).
func extractTelegramMessageParts(msg *tele.Message) []channel.MessagePart {
	if msg == nil {
		return nil
	}
	text := msg.Text
	entities := msg.Entities
	if text == "" {
		text = msg.Caption
		entities = msg.CaptionEntities
	}
	if text == "" || len(entities) == 0 {
		return nil
	}

	sorted := append(tele.Entities{}, entities...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Offset != sorted[j].Offset {
			return sorted[i].Offset < sorted[j].Offset
		}
		return sorted[i].Length > sorted[j].Length
	})

	// Telegram entity offsets and lengths are documented as UTF-16 code units,
	// so slice the text through utf16 indices rather than runes — supplementary
	// plane characters (e.g. most emoji) take 2 code units each and would
	// otherwise drift rune-based offsets.
	units := utf16.Encode([]rune(text))
	n := len(units)
	var parts []channel.MessagePart
	appendPlain := func(s string) {
		if s == "" {
			return
		}
		parts = append(parts, channel.MessagePart{Type: channel.MessagePartText, Text: s})
	}

	// Pre-pass: identify nested structural entities (link/mention/text_mention)
	// so the outer span can be split around them. The flat MessagePart schema
	// can't simultaneously style + link the same range, but splitting lets us
	// keep both the styled context and the link/identity signal.
	childrenOf := make([][]int, len(sorted))
	isNestedChild := make([]bool, len(sorted))
	for i, ent := range sorted {
		if !telegramEntityIsStructural(ent.Type) {
			continue
		}
		parent := -1
		parentSize := 0
		for j, candidate := range sorted {
			if i == j {
				continue
			}
			// Only style/format entities can host a structural child; two
			// coextensive structurals (e.g. two text_links on the same span)
			// would otherwise pick each other as parent and both get marked
			// as nested, dropping both.
			if telegramEntityIsStructural(candidate.Type) {
				continue
			}
			if candidate.Offset > ent.Offset {
				continue
			}
			if candidate.Offset+candidate.Length < ent.Offset+ent.Length {
				continue
			}
			if parent == -1 || candidate.Length < parentSize {
				parent = j
				parentSize = candidate.Length
			}
		}
		if parent != -1 {
			childrenOf[parent] = append(childrenOf[parent], i)
			isNestedChild[i] = true
		}
	}

	emitFromEntity := func(ent tele.MessageEntity, slice string) {
		if part, ok := telegramEntityToPart(ent, slice); ok {
			parts = append(parts, part)
		} else {
			appendPlain(slice)
		}
	}

	cursor := 0
	for i, ent := range sorted {
		if ent.Offset < 0 || ent.Length <= 0 || ent.Offset+ent.Length > n {
			continue
		}
		if isNestedChild[i] {
			continue
		}
		if ent.Offset < cursor {
			continue
		}
		if ent.Offset > cursor {
			appendPlain(string(utf16.Decode(units[cursor:ent.Offset])))
		}
		if kids := childrenOf[i]; len(kids) > 0 {
			ordered := append([]int{}, kids...)
			sort.SliceStable(ordered, func(a, b int) bool {
				return sorted[ordered[a]].Offset < sorted[ordered[b]].Offset
			})
			innerCursor := ent.Offset
			for _, ki := range ordered {
				child := sorted[ki]
				if child.Offset > innerCursor {
					emitFromEntity(ent, string(utf16.Decode(units[innerCursor:child.Offset])))
				}
				emitFromEntity(child, string(utf16.Decode(units[child.Offset:child.Offset+child.Length])))
				innerCursor = child.Offset + child.Length
			}
			if innerCursor < ent.Offset+ent.Length {
				emitFromEntity(ent, string(utf16.Decode(units[innerCursor:ent.Offset+ent.Length])))
			}
		} else {
			emitFromEntity(ent, string(utf16.Decode(units[ent.Offset:ent.Offset+ent.Length])))
		}
		cursor = ent.Offset + ent.Length
	}
	if cursor < n {
		appendPlain(string(utf16.Decode(units[cursor:])))
	}

	if onlyPlainText(parts) {
		return nil
	}
	return parts
}

func telegramEntityToPart(ent tele.MessageEntity, slice string) (channel.MessagePart, bool) {
	switch ent.Type {
	case tele.EntityBold:
		return styledText(slice, channel.MessageStyleBold), true
	case tele.EntityItalic:
		return styledText(slice, channel.MessageStyleItalic), true
	case tele.EntityStrikethrough:
		return styledText(slice, channel.MessageStyleStrikethrough), true
	case tele.EntityCode:
		return styledText(slice, channel.MessageStyleCode), true
	case tele.EntityCodeBlock:
		return channel.MessagePart{
			Type:     channel.MessagePartCodeBlock,
			Text:     slice,
			Language: strings.TrimSpace(ent.Language),
		}, true
	case tele.EntityTextLink:
		return channel.MessagePart{
			Type: channel.MessagePartLink,
			Text: slice,
			URL:  strings.TrimSpace(ent.URL),
		}, true
	case tele.EntityURL:
		return channel.MessagePart{
			Type: channel.MessagePartLink,
			Text: slice,
			URL:  slice,
		}, true
	case tele.EntityMention:
		return channel.MessagePart{Type: channel.MessagePartMention, Text: slice}, true
	case tele.EntityTMention:
		if ent.User == nil {
			return channel.MessagePart{}, false
		}
		uid := strconv.FormatInt(ent.User.ID, 10)
		meta := map[string]any{
			"user_id": uid,
		}
		if ent.User.Username != "" {
			meta["username"] = ent.User.Username
		}
		if name := strings.TrimSpace(ent.User.FirstName + " " + ent.User.LastName); name != "" {
			meta["display_name"] = name
		}
		// Preserve the sender-typed label as the display text; identity flows
		// through ChannelIdentityID + Metadata so the LLM still sees who was
		// anchored without overwriting what the user actually wrote.
		return channel.MessagePart{
			Type:              channel.MessagePartMention,
			Text:              slice,
			ChannelIdentityID: uid,
			Metadata:          meta,
		}, true
	default:
		return channel.MessagePart{}, false
	}
}

func telegramEntityIsStructural(t tele.EntityType) bool {
	switch t {
	case tele.EntityMention, tele.EntityTMention, tele.EntityTextLink, tele.EntityURL:
		return true
	}
	return false
}

func styledText(text string, style channel.MessageTextStyle) channel.MessagePart {
	return channel.MessagePart{
		Type:   channel.MessagePartText,
		Text:   text,
		Styles: []channel.MessageTextStyle{style},
	}
}

func onlyPlainText(parts []channel.MessagePart) bool {
	for _, p := range parts {
		if p.Type != channel.MessagePartText || len(p.Styles) > 0 {
			return false
		}
	}
	return true
}
