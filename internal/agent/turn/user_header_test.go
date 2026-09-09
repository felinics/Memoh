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

func TestUnwrapUserMessageEnvelope(t *testing.T) {
	t.Parallel()

	input := UserMessageHeaderInput{
		DisplayName:      "User",
		Channel:          "web",
		ConversationType: "private",
		Target:           "115e7013-dc2a-4437-8e21-b49fbb21dfef",
		AttachmentPaths:  []string{"/data/.memoh/media/b2/b2edf40e.png"},
		Time:             time.Date(2026, 8, 20, 17, 37, 14, 0, time.UTC),
	}

	tests := []struct {
		name string
		text string
		want string
	}{
		{"attachment only", FormatUserHeader(input, ""), ""},
		{"attachment with caption", FormatUserHeader(input, "look at this"), "look at this"},
		{"self closing", `<message sender="User" t="now"/>`, ""},
		{"plain text passthrough", "hello world", "hello world"},
		{"unterminated envelope passthrough", `<message sender="User">hello`, `<message sender="User">hello`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := UnwrapUserMessageEnvelope(tc.text); got != tc.want {
				t.Fatalf("UnwrapUserMessageEnvelope(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}
