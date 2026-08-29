package mcp

import (
	"testing"
)

func TestToolSessionContextStoreUnregisterStopsToolEvents(t *testing.T) {
	store := NewToolSessionContextStore()
	session := ToolSessionContext{BotID: "bot-1", SessionID: "session-1", RunID: "run-1"}
	unregister := store.RegisterToolEventSink(session, func(ToolStreamEvent) {})

	unregister()

	if delivered := store.AppendToolEvent(session, ToolStreamEvent{
		Type:       "tool_call_start",
		ToolCallID: "call-1",
		ToolName:   "schedule_list",
	}); delivered {
		t.Fatal("AppendToolEvent delivered=true after unregister")
	}
}

func TestToolSessionContextStoreRegisteredSinkReceivesToolEvents(t *testing.T) {
	store := NewToolSessionContextStore()
	session := ToolSessionContext{BotID: "bot-1", SessionID: "session-1", RunID: "run-1"}
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
	session := ToolSessionContext{BotID: "bot-1", SessionID: "session-1", RunID: "run-1"}
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
	delivered := store.AppendToolEvent(ToolSessionContext{BotID: "bot-1", SessionID: "session-1", RunID: "run-1"}, ToolStreamEvent{
		Type:       "tool_call_start",
		ToolCallID: "call-1",
		ToolName:   "schedule_list",
	})
	if delivered {
		t.Fatal("AppendToolEvent delivered=true without a registered sink")
	}
}
