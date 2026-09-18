package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func TestHistoryOptionalArgumentsReplayRealModelSearch(t *testing.T) {
	for _, unset := range []any{"", nil} {
		q := &historySearchQueries{}
		_, err := executeHistorySearch(t, NewHistoryProvider(nil, nil, nil, q), map[string]any{
			"contact_id": unset, "cursor": unset, "end_time": unset, "keyword": "Mercury", "limit": 50,
			"role": "user", "session_id": searchTestSessionID, "start_time": unset,
		})
		if err != nil || q.calls != 1 || q.params.ContactID.Valid || q.params.CursorID.Valid {
			t.Fatalf("unused optional fields blocked a valid real-model search: %v, calls=%d", err, q.calls)
		}
	}
}

func TestHistoryOptionalArgumentsKeepInvalidNumericTypes(t *testing.T) {
	q := &historySearchQueries{}
	_, err := executeHistorySearch(t, NewHistoryProvider(nil, nil, nil, q), map[string]any{"limit": ""})
	if err == nil || q.calls != 0 {
		t.Fatal("blank strings must not become valid numeric defaults")
	}
}

func TestHistoryToolsAcceptNullableUnusedArguments(t *testing.T) {
	reader := &fakeHistoryMessageReader{exactMessage: messagepkg.Message{ID: "source", Role: "assistant", Content: json.RawMessage(`{"role":"assistant","content":"evidence"}`)}}
	provider := NewHistoryProvider(nil, fakeHistorySessionLister{}, reader, &historySearchQueries{})
	registered, err := provider.Tools(context.Background(), SessionContext{BotID: searchTestBotID, SessionID: searchTestSessionID})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range registered {
		raw, _ := json.Marshal(tool.Parameters)
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		args := make(map[string]any)
		for name := range schema.Properties {
			args[name] = nil
		}
		if err := resolved.Validate(args); err != nil {
			t.Errorf("%s optional schema cannot represent unused fields in strict mode: %v", tool.Name, err)
		}
	}
	for _, view := range []string{"chat", "execution"} {
		args := map[string]any{"session_id": nil, "message_id": "source", "view": view, "before": nil, "before_message_id": nil, "content_offset": nil, "content_version": nil, "max_bytes": nil, "limit": nil}
		if _, err := callHistoryRead(t, reader, args); err != nil {
			t.Errorf("%s null options: %v", view, err)
		}
	}
}
