package application

import (
	"testing"

	agentdomain "github.com/memohai/memoh/domains/agent"
)

// TestUserQueryNeedsHeader pins the attachment-only regression fix: a message
// with no caption but with attachments must still get the user header (it is
// what carries sender/channel/attachment paths to the model and what makes
// the user turn persist with its asset links). A no-prompt skill activation
// stays headerless so its stored user content remains empty.
func TestUserQueryNeedsHeader(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		req         ChatRequest
		attachments int
		want        bool
	}{
		{name: "text message", req: ChatRequest{Query: "hello"}, want: true},
		{name: "text with attachments", req: ChatRequest{Query: "look"}, attachments: 1, want: true},
		{name: "attachment only", req: ChatRequest{}, attachments: 2, want: true},
		{name: "empty message", req: ChatRequest{}, want: false},
		{name: "whitespace query no attachments", req: ChatRequest{Query: "  "}, want: false},
		{
			name:        "no-prompt skill activation with attachments",
			req:         ChatRequest{UserMessageKind: agentdomain.UserMessageKindSkillActivation},
			attachments: 1,
			want:        false,
		},
		{
			name: "skill activation with prompt",
			req:  ChatRequest{Query: "do it", UserMessageKind: agentdomain.UserMessageKindSkillActivation},
			want: true,
		},
	}
	for _, tc := range cases {
		if got := userQueryNeedsHeader(tc.req, tc.attachments); got != tc.want {
			t.Errorf("%s: userQueryNeedsHeader = %v, want %v", tc.name, got, tc.want)
		}
	}
}
