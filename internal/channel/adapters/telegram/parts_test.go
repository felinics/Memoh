package telegram

import (
	"testing"

	tele "gopkg.in/telebot.v4"

	"github.com/felinics/memoh/internal/channel"
)

func TestExtractTelegramMessageParts_PlainTextNoEntities(t *testing.T) {
	t.Parallel()
	msg := &tele.Message{Text: "hello world"}
	parts := extractTelegramMessageParts(msg)
	if len(parts) != 0 {
		t.Fatalf("expected no parts for plain text, got %+v", parts)
	}
}

func TestExtractTelegramMessageParts_BoldSpanInMiddle(t *testing.T) {
	t.Parallel()
	msg := &tele.Message{
		Text: "hi shout bye",
		Entities: tele.Entities{
			{Type: tele.EntityBold, Offset: 3, Length: 5},
		},
	}
	parts := extractTelegramMessageParts(msg)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts (text, bold, text), got %d: %+v", len(parts), parts)
	}
	if parts[0].Type != channel.MessagePartText || parts[0].Text != "hi " {
		t.Fatalf("part 0 wrong: %+v", parts[0])
	}
	if parts[1].Type != channel.MessagePartText || parts[1].Text != "shout" || len(parts[1].Styles) != 1 || parts[1].Styles[0] != channel.MessageStyleBold {
		t.Fatalf("part 1 wrong: %+v", parts[1])
	}
	if parts[2].Type != channel.MessagePartText || parts[2].Text != " bye" {
		t.Fatalf("part 2 wrong: %+v", parts[2])
	}
}

func TestExtractTelegramMessageParts_ItalicStrikeCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		entity tele.EntityType
		style  channel.MessageTextStyle
	}{
		{"italic", tele.EntityItalic, channel.MessageStyleItalic},
		{"strikethrough", tele.EntityStrikethrough, channel.MessageStyleStrikethrough},
		{"inline_code", tele.EntityCode, channel.MessageStyleCode},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := &tele.Message{
				Text: "xx",
				Entities: tele.Entities{
					{Type: tc.entity, Offset: 0, Length: 2},
				},
			}
			parts := extractTelegramMessageParts(msg)
			if len(parts) != 1 || parts[0].Type != channel.MessagePartText {
				t.Fatalf("expected single text part, got %+v", parts)
			}
			if len(parts[0].Styles) != 1 || parts[0].Styles[0] != tc.style {
				t.Fatalf("expected style %q, got %+v", tc.style, parts[0].Styles)
			}
		})
	}
}

func TestExtractTelegramMessageParts_CodeBlockWithLanguage(t *testing.T) {
	t.Parallel()
	msg := &tele.Message{
		Text: "fn main(){}",
		Entities: tele.Entities{
			{Type: tele.EntityCodeBlock, Offset: 0, Length: 11, Language: "rust"},
		},
	}
	parts := extractTelegramMessageParts(msg)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %+v", parts)
	}
	if parts[0].Type != channel.MessagePartCodeBlock || parts[0].Language != "rust" || parts[0].Text != "fn main(){}" {
		t.Fatalf("expected pre with language=rust, got %+v", parts[0])
	}
}

func TestExtractTelegramMessageParts_TextLinkWithURL(t *testing.T) {
	t.Parallel()
	msg := &tele.Message{
		Text: "see Memoh now",
		Entities: tele.Entities{
			{Type: tele.EntityTextLink, Offset: 4, Length: 5, URL: "https://example.com"},
		},
	}
	parts := extractTelegramMessageParts(msg)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %+v", parts)
	}
	if parts[1].Type != channel.MessagePartLink || parts[1].URL != "https://example.com" || parts[1].Text != "Memoh" {
		t.Fatalf("link part wrong: %+v", parts[1])
	}
}

func TestExtractTelegramMessageParts_BareURLEntity(t *testing.T) {
	t.Parallel()
	msg := &tele.Message{
		Text: "go https://example.com",
		Entities: tele.Entities{
			{Type: tele.EntityURL, Offset: 3, Length: 19},
		},
	}
	parts := extractTelegramMessageParts(msg)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %+v", parts)
	}
	if parts[1].Type != channel.MessagePartLink || parts[1].URL != "https://example.com" || parts[1].Text != "https://example.com" {
		t.Fatalf("url part wrong: %+v", parts[1])
	}
}

func TestExtractTelegramMessageParts_MentionPreserved(t *testing.T) {
	t.Parallel()
	msg := &tele.Message{
		Text: "hi @bot ok",
		Entities: tele.Entities{
			{Type: tele.EntityMention, Offset: 3, Length: 4},
		},
	}
	parts := extractTelegramMessageParts(msg)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %+v", parts)
	}
	if parts[1].Type != channel.MessagePartMention || parts[1].Text != "@bot" {
		t.Fatalf("mention part wrong: %+v", parts[1])
	}
}

func TestExtractTelegramMessageParts_TextMentionCarriesUserMetadata(t *testing.T) {
	t.Parallel()
	msg := &tele.Message{
		Text: "hi Alice",
		Entities: tele.Entities{
			{
				Type:   tele.EntityTMention,
				Offset: 3,
				Length: 5,
				User:   &tele.User{ID: 7, FirstName: "Alice", Username: "ali"},
			},
		},
	}
	parts := extractTelegramMessageParts(msg)
	if len(parts) < 2 {
		t.Fatalf("expected at least 2 parts, got %+v", parts)
	}
	mention := parts[len(parts)-1]
	if mention.Type != channel.MessagePartMention {
		t.Fatalf("expected mention, got %+v", mention)
	}
	// The visible text the sender wrote (the entity slice) is preserved; the
	// user identity is surfaced through Metadata + ChannelIdentityID rather
	// than overwriting the displayed label.
	if mention.Text != "Alice" {
		t.Fatalf("expected slice 'Alice' preserved, got %q", mention.Text)
	}
	if mention.ChannelIdentityID != "7" {
		t.Fatalf("expected channel_identity_id=7, got %q", mention.ChannelIdentityID)
	}
	if got := mention.Metadata["user_id"]; got != "7" {
		t.Fatalf("expected user_id=7, got %v", got)
	}
	if got := mention.Metadata["username"]; got != "ali" {
		t.Fatalf("expected username=ali, got %v", got)
	}
}

func TestExtractTelegramMessageParts_TextMentionPreservesSenderLabel(t *testing.T) {
	t.Parallel()
	// A text_mention can anchor a profile link onto an arbitrary label such as
	// "the reviewer". The displayed slice is what the LLM should see, not the
	// linked profile's first name.
	msg := &tele.Message{
		Text: "ask the reviewer please",
		Entities: tele.Entities{
			{
				Type:   tele.EntityTMention,
				Offset: 4,
				Length: 12,
				User:   &tele.User{ID: 42, FirstName: "Alice", Username: "ali"},
			},
		},
	}
	parts := extractTelegramMessageParts(msg)
	var mention *channel.MessagePart
	for i := range parts {
		if parts[i].Type == channel.MessagePartMention {
			mention = &parts[i]
			break
		}
	}
	if mention == nil {
		t.Fatalf("expected mention, got %+v", parts)
	}
	if mention.Text != "the reviewer" {
		t.Fatalf("expected sender-typed label preserved, got %q", mention.Text)
	}
	if got := mention.Metadata["user_id"]; got != "42" {
		t.Fatalf("expected user_id=42, got %v", got)
	}
}

func TestExtractTelegramMessageParts_UnsupportedEntityKeepsTextAsPlain(t *testing.T) {
	t.Parallel()
	msg := &tele.Message{
		Text: "see #news today",
		Entities: tele.Entities{
			{Type: tele.EntityHashtag, Offset: 4, Length: 5},
		},
	}
	parts := extractTelegramMessageParts(msg)
	if len(parts) != 0 {
		t.Fatalf("expected no parts when only hashtag entity (whole text plain), got %+v", parts)
	}
}

func TestExtractTelegramMessageParts_OverlappingEntityDropped(t *testing.T) {
	t.Parallel()
	msg := &tele.Message{
		Text: "bold italic",
		Entities: tele.Entities{
			{Type: tele.EntityBold, Offset: 0, Length: 11},
			{Type: tele.EntityItalic, Offset: 5, Length: 6},
		},
	}
	parts := extractTelegramMessageParts(msg)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part (outer bold), got %+v", parts)
	}
	if parts[0].Type != channel.MessagePartText || parts[0].Text != "bold italic" || len(parts[0].Styles) != 1 || parts[0].Styles[0] != channel.MessageStyleBold {
		t.Fatalf("expected single bold-styled text, got %+v", parts[0])
	}
}

func TestExtractTelegramMessageParts_UsesCaptionAndCaptionEntities(t *testing.T) {
	t.Parallel()
	msg := &tele.Message{
		Caption: "x y",
		CaptionEntities: tele.Entities{
			{Type: tele.EntityBold, Offset: 0, Length: 1},
		},
	}
	parts := extractTelegramMessageParts(msg)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts from caption, got %+v", parts)
	}
	if parts[0].Type != channel.MessagePartText || parts[0].Text != "x" || len(parts[0].Styles) != 1 {
		t.Fatalf("part 0 wrong: %+v", parts[0])
	}
}

func TestExtractTelegramMessageParts_HonorsUTF16OffsetsForBMP(t *testing.T) {
	t.Parallel()
	// CJK characters live in the BMP, so one UTF-16 code unit equals one rune;
	// the offsets line up under either indexing strategy. The supplementary-plane
	// case is covered separately below.
	msg := &tele.Message{
		Text: "你好 world",
		Entities: tele.Entities{
			{Type: tele.EntityBold, Offset: 3, Length: 5},
		},
	}
	parts := extractTelegramMessageParts(msg)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %+v", parts)
	}
	if parts[0].Text != "你好 " {
		t.Fatalf("expected leading CJK text, got %q", parts[0].Text)
	}
	if parts[1].Text != "world" || len(parts[1].Styles) != 1 {
		t.Fatalf("expected bold 'world', got %+v", parts[1])
	}
}

func TestExtractTelegramMessageParts_HandlesSupplementaryPlaneEmoji(t *testing.T) {
	t.Parallel()
	// 🎉 (U+1F389) is encoded as a UTF-16 surrogate pair (2 code units) but is a
	// single rune in Go. Telegram entity offsets are documented as UTF-16 code
	// units; rune-based slicing would drift by 1 after each supplementary-plane
	// character.
	msg := &tele.Message{
		Text: "see 🎉 bold here",
		Entities: tele.Entities{
			// "bold" begins at UTF-16 index 7: "see "(0-3) + 🎉(4-5) + " "(6) + "bold"(7-10).
			{Type: tele.EntityBold, Offset: 7, Length: 4},
		},
	}
	parts := extractTelegramMessageParts(msg)
	var bold *channel.MessagePart
	for i := range parts {
		if len(parts[i].Styles) == 1 && parts[i].Styles[0] == channel.MessageStyleBold {
			bold = &parts[i]
			break
		}
	}
	if bold == nil {
		t.Fatalf("expected a bold part, got %+v", parts)
	}
	if bold.Text != "bold" {
		t.Fatalf("expected bold text 'bold' under UTF-16 offsets, got %q (offsets interpreted as runes drift past the surrogate pair)", bold.Text)
	}
}

func TestExtractTelegramMessageParts_NestedLinkInsideBold(t *testing.T) {
	t.Parallel()
	// Telegram sends overlapping entities natively: the bold span covers the
	// whole rendered text and the text_link entity points at a sub-range.
	// Splitting the outer around the inner keeps the URL discoverable; the
	// flat MessagePart schema can't carry both bold and link at the same
	// position, so the link span itself appears without the bold style.
	msg := &tele.Message{
		Text: "Check this out",
		Entities: tele.Entities{
			{Type: tele.EntityBold, Offset: 0, Length: 14},
			{Type: tele.EntityTextLink, Offset: 6, Length: 4, URL: "https://example.com"},
		},
	}
	parts := extractTelegramMessageParts(msg)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts (lead-bold + link + tail-bold), got %+v", parts)
	}
	if parts[0].Type != channel.MessagePartText || parts[0].Text != "Check " || len(parts[0].Styles) != 1 || parts[0].Styles[0] != channel.MessageStyleBold {
		t.Fatalf("part 0 wrong: %+v", parts[0])
	}
	if parts[1].Type != channel.MessagePartLink || parts[1].URL != "https://example.com" || parts[1].Text != "this" {
		t.Fatalf("part 1 wrong: %+v", parts[1])
	}
	if parts[2].Type != channel.MessagePartText || parts[2].Text != " out" || len(parts[2].Styles) != 1 || parts[2].Styles[0] != channel.MessageStyleBold {
		t.Fatalf("part 2 wrong: %+v", parts[2])
	}
}

func TestExtractTelegramMessageParts_LinkCoextensiveWithStyle(t *testing.T) {
	t.Parallel()
	// A fully bold link sends two entities covering the exact same range.
	// The equality exclusion in the parent-search pre-pass treated those as
	// "not nested", so the structural entity (text_link) was dropped by the
	// main loop's cursor guard and the URL never reached the LLM.
	msg := &tele.Message{
		Text: "click here",
		Entities: tele.Entities{
			{Type: tele.EntityBold, Offset: 0, Length: 10},
			{Type: tele.EntityTextLink, Offset: 0, Length: 10, URL: "https://example.com"},
		},
	}
	parts := extractTelegramMessageParts(msg)
	var link *channel.MessagePart
	for i := range parts {
		if parts[i].Type == channel.MessagePartLink {
			link = &parts[i]
			break
		}
	}
	if link == nil {
		t.Fatalf("expected link part for coextensive bold+link, got %+v", parts)
	}
	if link.URL != "https://example.com" || link.Text != "click here" {
		t.Fatalf("link content wrong: %+v", link)
	}
}

func TestExtractTelegramMessageParts_NestedMentionInsideStyle(t *testing.T) {
	t.Parallel()
	// Same shape but with a text_mention nested in italic; identity must
	// reach the LLM even though the italic span covers the mention.
	msg := &tele.Message{
		Text: "hi Alice ok",
		Entities: tele.Entities{
			{Type: tele.EntityItalic, Offset: 0, Length: 11},
			{Type: tele.EntityTMention, Offset: 3, Length: 5, User: &tele.User{ID: 7, FirstName: "Alice"}},
		},
	}
	parts := extractTelegramMessageParts(msg)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %+v", parts)
	}
	if parts[1].Type != channel.MessagePartMention || parts[1].Text != "Alice" || parts[1].ChannelIdentityID != "7" {
		t.Fatalf("nested mention wrong: %+v", parts[1])
	}
}

func TestExtractTelegramMessageParts_NilMessage(t *testing.T) {
	t.Parallel()
	if got := extractTelegramMessageParts(nil); got != nil {
		t.Fatalf("expected nil for nil msg, got %+v", got)
	}
}
