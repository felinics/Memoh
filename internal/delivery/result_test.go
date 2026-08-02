package delivery

import "testing"

func TestIsSameConversationDefaultsOnlyToCurrentRoute(t *testing.T) {
	t.Parallel()

	if !IsSameConversation("telegram", "chat-1", "", "") {
		t.Fatal("omitted route should mean the current conversation")
	}
	if IsSameConversation("telegram", "chat-1", "telegram", "chat-2") ||
		IsSameConversation("telegram", "chat-1", "discord", "chat-1") {
		t.Fatal("cross-conversation route was accepted")
	}
}

func TestSuccessfulCurrentDeliveryAcceptsCommittedTextOnly(t *testing.T) {
	t.Parallel()

	partial := map[string]any{
		"ok": false, "text_delivered": true,
		"platform": "telegram", "target": "chat-1",
	}
	if !IsSuccessfulCurrentDelivery(partial, "telegram", "chat-1") {
		t.Fatal("committed text should terminate the current delivery loop")
	}
	if IsSuccessfulCurrentDelivery(partial, "telegram", "chat-2") {
		t.Fatal("cross-conversation partial delivery was accepted")
	}
	if IsSuccessfulCurrentDelivery(map[string]any{"ok": false}, "telegram", "chat-1") {
		t.Fatal("failed delivery without committed text was accepted")
	}
}
