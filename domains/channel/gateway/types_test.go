package gateway

import "testing"

func TestGenerateRoutingKeyTreatsDirectConversationAsSharedRoute(t *testing.T) {
	got := GenerateRoutingKey("matrix", "bot-1", "!room:example.com", "direct", "@alex:example.com")
	if got != "matrix:bot-1:!room:example.com" {
		t.Fatalf("unexpected routing key: %q", got)
	}
}
