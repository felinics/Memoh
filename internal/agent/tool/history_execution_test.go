package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/stretchr/testify/require"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func callHistoryRead(t *testing.T, reader *fakeHistoryMessageReader, args map[string]any) (map[string]any, error) {
	t.Helper()
	provider := NewHistoryProvider(nil, nil, reader, nil)
	available, err := provider.Tools(context.Background(), SessionContext{BotID: "bot-1", SessionID: "session-current"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range available {
		if tool.Name == ToolGetMessages().String() {
			got, err := tool.Execute(&sdk.ToolExecContext{Context: context.Background()}, args)
			if err != nil {
				return nil, err
			}
			return got.(map[string]any), nil
		}
	}
	t.Fatal("get_messages missing")
	return nil, nil
}

func TestHistoryExecutionReadsStoredToolEvidence(t *testing.T) {
	t.Parallel()
	reader := &fakeHistoryMessageReader{exactMessage: messagepkg.Message{
		ID: "message-1", SessionID: "session-current", Role: "assistant", TurnID: "turn-1",
		Content: json.RawMessage(`{"role":"assistant","content":[{"type":"reasoning","text":"PRIVATE_REASONING"},{"type":"text","text":"checking"},{"type":"tool-call","toolCallId":"call-1","toolName":"exec","input":{"command":"pytest exact_test.py"},"providerMetadata":{"private":"PRIVATE_PROVIDER"}},{"type":"tool-result","toolCallId":"call-1","toolName":"exec","result":{"stderr":"AssertionError: evidence-731","exit_code":1},"isError":true}]}`),
	}}
	got, err := callHistoryRead(t, reader, map[string]any{"message_id": "message-1", "view": "execution"})
	if err != nil {
		t.Fatal(err)
	}
	rows := got["messages"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("messages = %v", rows)
	}
	row := rows[0]
	content, ok := row["content"].(string)
	if !ok || !json.Valid([]byte(content)) {
		t.Fatalf("execution content must be serialized JSON: %v", row)
	}
	for _, want := range []string{"pytest exact_test.py", "AssertionError: evidence-731", "call-1", `"isError":true`} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q in %s", want, content)
		}
	}
	for _, hidden := range []string{"PRIVATE_REASONING", "PRIVATE_PROVIDER", "providerMetadata"} {
		if strings.Contains(content, hidden) {
			t.Errorf("leaked %q", hidden)
		}
	}
	if row["turn_id"] != "turn-1" || row["source"] != "persisted_message" || row["has_more"] != false {
		t.Fatalf("source metadata = %v", row)
	}
}

func TestHistoryExecutionPagingPreservesUTF8AndRejectsChangedSource(t *testing.T) {
	t.Parallel()
	content, _ := json.Marshal(map[string]any{"role": "tool", "content": strings.Repeat("错误☃\n", 300), "tool_call_id": "call-2"})
	reader := &fakeHistoryMessageReader{exactMessage: messagepkg.Message{ID: "message-2", Role: "tool", SessionID: "session-current", Content: content}}
	args := map[string]any{"message_id": "message-2", "view": "execution", "max_bytes": 256}
	var recovered strings.Builder
	for i := 0; i < 100; i++ {
		got, err := callHistoryRead(t, reader, args)
		if err != nil {
			t.Fatal(err)
		}
		row := got["messages"].([]map[string]any)[0]
		part, ok := row["content"].(string)
		if !ok || len(part) > 256 || !utf8.ValidString(part) {
			t.Fatalf("invalid byte window: %v", row)
		}
		recovered.WriteString(part)
		if row["has_more"] == false {
			break
		}
		args["content_offset"] = row["next_content_offset"]
		args["content_version"] = row["content_version"]
	}
	var decoded struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(recovered.String()), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Content != strings.Repeat("错误☃\n", 300) {
		t.Fatal("paged content lost or duplicated bytes")
	}
	reader.exactMessage.Content = json.RawMessage(`{"role":"tool","content":"changed"}`)
	if _, err := callHistoryRead(t, reader, args); err == nil {
		t.Fatal("changed source must reject an old content page")
	}
}

func TestHistoryExecutionPreservesLegacyEvidence(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, role, raw, want string }{
		{"bare text", "user", `{"type":"text","text":"original"}`, `{"role":"user","content":[{"type":"text","text":"original"}]}`},
		{"legacy result object", "tool", `{"role":"tool","tool_call_id":"legacy-1","name":"lookup","content":{"status":"failed","error":"E_731"}}`, `{"role":"tool","tool_call_id":"legacy-1","name":"lookup","content":{"status":"failed","error":"E_731"}}`},
		{"legacy result array", "tool", `{"role":"tool","tool_call_id":"legacy-1","content":[{"status":"failed"}]}`, `{"role":"tool","tool_call_id":"legacy-1","content":[{"status":"failed"}]}`},
		{"legacy call", "assistant", `{"role":"assistant","tool_calls":[{"id":"legacy-1","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"exact\"}"}}]}`, `{"role":"assistant","tool_calls":[{"id":"legacy-1","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"exact\"}"}}]}`},
		{"legacy output", "tool", `{"role":"tool","content":[{"type":"tool-result","toolCallId":"legacy-1","output":{"type":"error-json","value":{"code":"E_731","_memoh_truncated":true}}}]}`, `{"role":"tool","content":[{"type":"tool-result","toolCallId":"legacy-1","output":{"type":"error-json","value":{"code":"E_731","_memoh_truncated":true}}}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader := &fakeHistoryMessageReader{exactMessage: messagepkg.Message{ID: "old", Role: tt.role, Content: json.RawMessage(tt.raw)}}
			got, err := callHistoryRead(t, reader, map[string]any{"message_id": "old", "view": "execution"})
			require.NoError(t, err)
			require.JSONEq(t, tt.want, got["messages"].([]map[string]any)[0]["content"].(string))
		})
	}
}

func TestHistoryExecutionContinuationBindsRenderedProjection(t *testing.T) {
	t.Parallel()
	content, _ := json.Marshal(map[string]any{"role": "assistant", "content": strings.Repeat("evidence", 100)})
	reader := &fakeHistoryMessageReader{exactMessage: messagepkg.Message{ID: "source", Role: "assistant", Content: content}}
	args := map[string]any{"message_id": "source", "view": "execution", "max_bytes": 256}
	out, err := callHistoryRead(t, reader, args)
	require.NoError(t, err)
	row := out["messages"].([]map[string]any)[0]
	args["content_offset"], args["content_version"] = row["next_content_offset"], row["content_version"]
	reader.exactMessage.Role = "user"
	_, err = callHistoryRead(t, reader, args)
	require.Error(t, err, "continuation must reject changed projected bytes even when stored content is unchanged")
}
