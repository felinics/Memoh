package turn

import (
	"strings"
	"testing"
	"time"
)

func TestFormatUserHeaderIncludesAttachments(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC)
	header := FormatUserHeader(UserMessageHeaderInput{
		MessageID:         "msg_1",
		ChannelIdentityID: "cid_1",
		DisplayName:       "Alice",
		Channel:           "feishu",
		ConversationType:  "group",
		ConversationName:  "Team Chat",
		AttachmentPaths:   []string{"/tmp/a.txt"},
		Time:              now,
		Timezone:          "UTC",
	}, "hello")

	if !strings.Contains(header, "<attachment path=\"/tmp/a.txt\"/>") {
		t.Fatalf("expected attachment tag in header: %s", header)
	}
}

func TestFormatUserHeaderWithoutAttachmentsUsesEmptyList(t *testing.T) {
	t.Parallel()

	header := FormatUserHeader(UserMessageHeaderInput{
		ChannelIdentityID: "cid_1",
		DisplayName:       "Alice",
		Channel:           "feishu",
		ConversationType:  "group",
		ConversationName:  "Team Chat",
		Time:              time.Now().UTC(),
	}, "hello")

	if strings.Contains(header, "<attachment ") {
		t.Fatalf("expected no attachment tag in header: %s", header)
	}
}

func TestProjectUserMessageHeaderCompactPrivate(t *testing.T) {
	t.Parallel()

	full := `<message id="42" sender="Alice &amp; Bob" t="2026-08-02T10:00:00Z" channel="telegram" conversation="Direct" type="private" target="99">
hello
</message>`
	want := `<message id="42">
hello
</message>`
	if got := ProjectUserMessageHeader(full, "compact"); got != want {
		t.Fatalf("compact private header mismatch:\nwant: %s\ngot:  %s", want, got)
	}
}

func TestProjectUserMessageHeaderCompactGroupRetainsSender(t *testing.T) {
	t.Parallel()

	full := `<message id="43" sender="Alice &amp; Bob" t="2026-08-02T10:00:00Z" channel="telegram" conversation="Team" type="group">
hello
</message>`
	want := `<message id="43" sender="Alice &amp; Bob">
hello
</message>`
	if got := ProjectUserMessageHeader(full, "compact"); got != want {
		t.Fatalf("compact group header mismatch:\nwant: %s\ngot:  %s", want, got)
	}
}

func TestProjectUserMessageHeaderCompactPreservesDynamicAndNestedMetadata(t *testing.T) {
	t.Parallel()

	full := `<message id="44" sender="Alice" mentions_me="true" t="2026-08-02T10:00:00Z" channel="telegram" conversation="Team" type="group" target="99">
<in-reply-to><message id="43" sender="Bob" forwarded_from="Carol" t="2026-08-02T09:59:00Z" channel="telegram" conversation="Team" type="group" target="99">earlier</message></in-reply-to>
hello
</message>`
	want := `<message id="44" sender="Alice" mentions_me="true">
<in-reply-to><message id="43" sender="Bob" forwarded_from="Carol">earlier</message></in-reply-to>
hello
</message>`
	if got := ProjectUserMessageHeader(full, "compact"); got != want {
		t.Fatalf("compact nested header mismatch:\nwant: %s\ngot:  %s", want, got)
	}
}

func TestProjectUserMessageHeaderCompactSelfClosingMessage(t *testing.T) {
	t.Parallel()

	full := `<message id="45" sender="Alice" t="now" channel="telegram" type="group" deleted="true"/>`
	want := `<message id="45" sender="Alice" deleted="true"/>`
	if got := ProjectUserMessageHeader(full, "compact"); got != want {
		t.Fatalf("compact self-closing header mismatch: want %q, got %q", want, got)
	}
}

func TestProjectUserMessageHeaderLeavesFullAndMalformedInputUntouched(t *testing.T) {
	t.Parallel()

	full := `<message id="42" sender="Alice" type="private">hello</message>`
	if got := ProjectUserMessageHeader(full, "full"); got != full {
		t.Fatalf("full projection changed content: %s", got)
	}
	malformed := `<message id="42" sender="Alice" type="private" hello`
	if got := ProjectUserMessageHeader(malformed, "compact"); got != malformed {
		t.Fatalf("malformed content changed: %s", got)
	}
}
