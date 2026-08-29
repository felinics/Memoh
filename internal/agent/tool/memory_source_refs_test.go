package tools

import (
	"context"
	"fmt"
	"testing"

	session "github.com/felinics/memoh/internal/chat/thread"
)

func TestMemoryProviderFiltersSourceRefsByHistoryVisibility(t *testing.T) {
	t.Parallel()

	provider := NewMemoryProvider(nil, nil, nil, fakeHistorySessionLister{
		sessions: []session.Thread{
			{ID: "session-current", BotID: "bot-1", RouteID: "route-current"},
			{ID: "session-same-route", BotID: "bot-1", RouteID: "route-current"},
			{ID: "session-same-user", BotID: "bot-1", CreatedByUserID: "user-1"},
			{ID: "session-bob", BotID: "bot-1", RouteID: "route-bob", CreatedByUserID: "user-2"},
		},
	})
	output := map[string]any{
		"results": []map[string]any{{
			"memory": "shared fact",
			"source_refs": []map[string]any{
				{"session_id": "session-current", "message_id": "message-current"},
				{"session_id": "session-same-route", "message_id": "message-route"},
				{"session_id": "session-same-user", "message_id": "message-user"},
				{"session_id": "session-bob", "message_id": "message-bob"},
				{"message_id": "legacy-unscoped"},
			},
		}},
	}

	filtered := provider.filterSourceRefs(context.Background(), SessionContext{
		BotID: "bot-1", SessionID: "session-current", UserID: "user-1",
	}, output).(map[string]any)
	refs := filtered["results"].([]map[string]any)[0]["source_refs"].([]map[string]any)
	if len(refs) != 3 {
		t.Fatalf("source refs = %v, want only current, same-route, and same-user refs", refs)
	}
	for _, ref := range refs {
		if ref["session_id"] == "session-bob" || ref["message_id"] == "legacy-unscoped" {
			t.Fatalf("inaccessible source ref leaked: %v", ref)
		}
	}
}

func TestVisibleSourceRefsValidatesBeforeCapping(t *testing.T) {
	t.Parallel()
	refs := make([]map[string]any, 0, maxVisibleMemorySourceRefs+2)
	for i := 0; i < maxVisibleMemorySourceRefs; i++ {
		refs = append(refs, map[string]any{
			"session_id": "session-current", "message_id": fmt.Sprintf("message-%02d", i), "private": "drop-me",
		})
	}
	refs = append(refs,
		map[string]any{"session_id": "session-current", "message_id": ""},
		map[string]any{"session_id": "session-current"},
	)
	got := visibleSourceRefs(refs, map[string]struct{}{"session-current": {}})
	if len(got) != maxVisibleMemorySourceRefs {
		t.Fatalf("visibleSourceRefs() length = %d, want %d: %v", len(got), maxVisibleMemorySourceRefs, got)
	}
	if got[0]["message_id"] != "message-00" || got[len(got)-1]["message_id"] != "message-07" {
		t.Fatalf("visibleSourceRefs() = %v, want all valid refs", got)
	}
	if _, leaked := got[0]["private"]; leaked {
		t.Fatalf("visibleSourceRefs() leaked provider-controlled fields: %v", got[0])
	}
}

func TestMemoryProviderOmitsSourceRefsWithoutScopeLister(t *testing.T) {
	t.Parallel()

	provider := NewMemoryProvider(nil, nil, nil, nil)
	output := map[string]any{
		"results": []map[string]any{{
			"memory":      "shared fact",
			"source_refs": []map[string]any{{"session_id": "session-other", "message_id": "message-other"}},
		}},
	}

	filtered := provider.filterSourceRefs(context.Background(), SessionContext{
		BotID: "bot-1", SessionID: "session-current",
	}, output).(map[string]any)
	if _, ok := filtered["results"].([]map[string]any)[0]["source_refs"]; ok {
		t.Fatalf("unverified source refs must be omitted: %v", filtered)
	}
}

func TestMemoryProviderFiltersJSONDecodedSourceRefs(t *testing.T) {
	t.Parallel()

	provider := NewMemoryProvider(nil, nil, nil, nil)
	output := map[string]any{
		"results": []any{map[string]any{
			"memory": "shared fact",
			"source_refs": []any{
				map[string]any{"session_id": "session-current", "message_id": "message-current"},
				map[string]any{"session_id": "session-other", "message_id": "message-other"},
			},
		}},
	}

	filtered := provider.filterSourceRefs(context.Background(), SessionContext{
		BotID: "bot-1", SessionID: "session-current",
	}, output).(map[string]any)
	result := filtered["results"].([]any)[0].(map[string]any)
	refs := result["source_refs"].([]map[string]any)
	if len(refs) != 1 || refs[0]["message_id"] != "message-current" {
		t.Fatalf("JSON-decoded source refs were not scoped: %v", refs)
	}
}
