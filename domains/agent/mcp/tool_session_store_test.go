package mcp

import (
	"context"
	"testing"
)

func TestToolSessionContextStoreMergesLatestPromptContext(t *testing.T) {
	store := NewToolSessionContextStore()
	store.Put(ToolSessionContext{
		BotID:            "bot-1",
		SessionID:        "session-1",
		StreamID:         "stream-1",
		CurrentPlatform:  "web",
		ReplyTarget:      "reply-1",
		ConversationType: "private",
	})

	merged := store.Merge(ToolSessionContext{
		BotID:        "bot-1",
		SessionID:    "session-1",
		SessionToken: "header-token",
	})
	if merged.StreamID != "stream-1" || merged.CurrentPlatform != "web" || merged.ReplyTarget != "reply-1" || merged.ConversationType != "private" {
		t.Fatalf("merged context = %#v", merged)
	}
	if merged.SessionToken != "header-token" {
		t.Fatalf("SessionToken = %q, want header fallback", merged.SessionToken)
	}
}

func TestToolSessionContextMergePreservesSupportsImageInput(t *testing.T) {
	base := ToolSessionContext{BotID: "bot-1"}
	merged := MergeToolSessionContext(base, ToolSessionContext{SupportsImageInput: true})
	if !merged.SupportsImageInput {
		t.Fatalf("SupportsImageInput = false, want true")
	}
}

func TestToolSessionContextMergePreservesUserInputCapability(t *testing.T) {
	base := ToolSessionContext{BotID: "bot-1"}
	merged := MergeToolSessionContext(base, ToolSessionContext{CanRequestUserInput: true})
	if !merged.CanRequestUserInput {
		t.Fatalf("CanRequestUserInput = false, want true")
	}
}

func TestToolSessionContextMergePreservesRuntimeLifecycle(t *testing.T) {
	runCtx := context.Background()
	guard := func(context.Context) error { return nil }
	merged := MergeToolSessionContext(ToolSessionContext{BotID: "bot-1"}, ToolSessionContext{
		RunContext: runCtx, RuntimeGuard: guard,
	})
	if merged.RunContext != runCtx || merged.RuntimeGuard == nil {
		t.Fatalf("runtime lifecycle = context:%v guard:%v", merged.RunContext, merged.RuntimeGuard != nil)
	}
}

func TestToolSessionContextStorePutPreservesExistingNonEmptyFields(t *testing.T) {
	store := NewToolSessionContextStore()
	store.Put(ToolSessionContext{BotID: "bot-1", SessionID: "session-1", StreamID: "stream-1"})
	store.Put(ToolSessionContext{BotID: "bot-1", SessionID: "session-1", CurrentPlatform: "web"})

	merged := store.Merge(ToolSessionContext{BotID: "bot-1", SessionID: "session-1"})
	if merged.StreamID != "stream-1" || merged.CurrentPlatform != "web" {
		t.Fatalf("merged context = %#v", merged)
	}
}

func TestToolSessionContextStoreCloseSessionClearsContextAndSinks(t *testing.T) {
	store := NewToolSessionContextStore()
	session := ToolSessionContext{BotID: "bot-1", SessionID: "session-1", StreamID: "stream-1"}
	store.Put(ToolSessionContext{BotID: "bot-1", SessionID: "session-1", CurrentPlatform: "web"})
	store.RegisterToolEventSink(session, func(ToolStreamEvent) {})

	store.CloseSession("session-1")

	merged := store.Merge(ToolSessionContext{BotID: "bot-1", SessionID: "session-1"})
	if merged.CurrentPlatform != "" {
		t.Fatalf("stored context was not cleared: %#v", merged)
	}
	if delivered := store.AppendToolEvent(session, ToolStreamEvent{
		Type:       "tool_call_start",
		ToolCallID: "call-1",
		ToolName:   "schedule_list",
	}); delivered {
		t.Fatal("AppendToolEvent delivered=true after CloseSession")
	}
}

func TestToolSessionContextStoreRegisteredSinkReceivesToolEvents(t *testing.T) {
	store := NewToolSessionContextStore()
	session := ToolSessionContext{BotID: "bot-1", SessionID: "session-1", StreamID: "stream-1"}
	var delivered []ToolStreamEvent
	unregister := store.RegisterToolEventSink(session, func(event ToolStreamEvent) {
		delivered = append(delivered, event)
	})
	defer unregister()

	ok := store.AppendToolEvent(session, ToolStreamEvent{
		Type:       "tool_call_start",
		ToolCallID: "call-1",
		ToolName:   "schedule_list",
	})
	if !ok {
		t.Fatalf("AppendToolEvent delivered=false, want true")
	}
	if len(delivered) != 1 || delivered[0].ToolName != "schedule_list" {
		t.Fatalf("delivered = %#v", delivered)
	}
}

func TestToolSessionContextStoreOldCleanupDoesNotRemoveNewerSink(t *testing.T) {
	store := NewToolSessionContextStore()
	session := ToolSessionContext{BotID: "bot-1", SessionID: "session-1", StreamID: "stream-1"}
	var first, second int
	unregisterFirst := store.RegisterToolEventSink(session, func(ToolStreamEvent) {
		first++
	})
	unregisterSecond := store.RegisterToolEventSink(session, func(ToolStreamEvent) {
		second++
	})
	defer unregisterSecond()

	unregisterFirst()

	ok := store.AppendToolEvent(session, ToolStreamEvent{
		Type:       "tool_call_start",
		ToolCallID: "call-1",
		ToolName:   "schedule_list",
	})
	if !ok {
		t.Fatalf("AppendToolEvent delivered=false, want newer sink to remain")
	}
	if first != 0 || second != 1 {
		t.Fatalf("sink calls: first=%d second=%d, want 0/1", first, second)
	}
}

func TestToolSessionContextStoreDropsToolEventsWithoutSink(t *testing.T) {
	store := NewToolSessionContextStore()
	delivered := store.AppendToolEvent(ToolSessionContext{BotID: "bot-1", SessionID: "session-1", StreamID: "stream-1"}, ToolStreamEvent{
		Type:       "tool_call_start",
		ToolCallID: "call-1",
		ToolName:   "schedule_list",
	})
	if delivered {
		t.Fatal("AppendToolEvent delivered=true without a registered sink")
	}
}
