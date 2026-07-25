package agent

import "testing"

func TestNormalizeConversationType(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":        ConversationTypePrivate,
		"private": ConversationTypePrivate,
		"direct":  ConversationTypePrivate,
		"p2p":     ConversationTypePrivate,
		"group":   ConversationTypeGroup,
		"thread":  ConversationTypeThread,
		"unknown": ConversationTypeGroup,
	}
	for input, want := range tests {
		if got := NormalizeConversationType(input); got != want {
			t.Fatalf("NormalizeConversationType(%q) = %q, want %q", input, got, want)
		}
	}
}
