package tools

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func TestHistoryExecutionRejectsInvalidArguments(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		args map[string]any
	}{
		{"unknown view", map[string]any{"view": "raw"}},
		{"view type", map[string]any{"view": true}},
		{"missing message", map[string]any{"message_id": ""}},
		{"before conflicts", map[string]any{"before": "2026-09-15T12:00:00Z"}},
		{"chat offset", map[string]any{"view": "chat", "content_offset": 0}},
		{"chat version", map[string]any{"view": "chat", "content_version": "abc"}},
		{"chat bytes", map[string]any{"view": "chat", "max_bytes": 256}},
		{"offset missing version", map[string]any{"content_offset": 1}},
		{"negative offset", map[string]any{"content_offset": -1}},
		{"fractional offset", map[string]any{"content_offset": 0.5}},
		{"string offset", map[string]any{"content_offset": "0"}},
		{"negative bytes", map[string]any{"max_bytes": -1}},
		{"below minimum bytes", map[string]any{"max_bytes": 255}},
		{"above maximum bytes", map[string]any{"max_bytes": 8193}},
		{"fractional bytes", map[string]any{"max_bytes": 256.5}},
		{"string bytes", map[string]any{"max_bytes": "256"}},
		{"boolean bytes", map[string]any{"max_bytes": true}},
		{"nan bytes", map[string]any{"max_bytes": math.NaN()}},
		{"infinite offset", map[string]any{"content_offset": math.Inf(1)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args := map[string]any{"message_id": "message-1", "view": "execution"}
			for key, value := range tt.args {
				args[key] = value
			}
			reader := &fakeHistoryMessageReader{exactMessage: messagepkg.Message{
				ID: "message-1", Role: "assistant", SessionID: "session-current",
				Content: json.RawMessage(`{"role":"assistant","content":"evidence"}`),
			}}
			if _, err := callHistoryRead(t, reader, args); err == nil {
				t.Fatal("invalid arguments were accepted")
			}
		})
	}
}

func TestHistoryExecutionRejectsInvalidByteBoundaries(t *testing.T) {
	t.Parallel()
	reader := &fakeHistoryMessageReader{exactMessage: messagepkg.Message{
		ID: "message-1", Role: "assistant", SessionID: "session-current",
		Content: json.RawMessage(`{"role":"assistant","content":"准确证据"}`),
	}}
	got, err := callHistoryRead(t, reader, map[string]any{"message_id": "message-1", "view": "execution"})
	if err != nil {
		t.Fatal(err)
	}
	row := got["messages"].([]map[string]any)[0]
	content := row["content"].(string)
	for _, offset := range []int{strings.Index(content, "准") + 1, len(content) + 1} {
		if _, err := callHistoryRead(t, reader, map[string]any{
			"message_id": "message-1", "view": "execution", "content_offset": offset,
			"content_version": row["content_version"],
		}); err == nil {
			t.Errorf("invalid byte offset %d was accepted", offset)
		}
	}
}

func TestHistoryExecutionOmitsEnvelopePrivateDataAndPreservesToolPayload(t *testing.T) {
	t.Parallel()
	reader := &fakeHistoryMessageReader{exactMessage: messagepkg.Message{
		ID: "message-1", Role: "assistant", SessionID: "session-current",
		Content: json.RawMessage(`{"role":"assistant","providerMetadata":{"private":"ENVELOPE_PRIVATE"},"content":[{"type":"text","text":"evidence","providerMetadata":{"private":"TEXT_PRIVATE"}},{"type":"reasoning","text":"REASONING_PRIVATE"},{"type":"image","image":"IMAGE_PRIVATE","url":"IMAGE_URL_PRIVATE","mediaType":"image/png"},{"type":"file","data":"FILE_PRIVATE","filename":"evidence.pdf","mediaType":"application/pdf"},{"type":"tool-result","toolCallId":"call-1","toolName":"inspect","result":{"providerMetadata":{"opaque":"PAYLOAD_PROVIDER"},"reasoning":"PAYLOAD_REASONING","data":"PAYLOAD_DATA","output":"[memoh pruned] [tool result pruned: 2000 bytes]"},"providerMetadata":{"private":"RESULT_PRIVATE"}}]}`),
	}}
	got, err := callHistoryRead(t, reader, map[string]any{"message_id": "message-1", "view": "execution"})
	if err != nil {
		t.Fatal(err)
	}
	content := got["messages"].([]map[string]any)[0]["content"].(string)
	for _, hidden := range []string{"ENVELOPE_PRIVATE", "TEXT_PRIVATE", "REASONING_PRIVATE", "IMAGE_PRIVATE", "IMAGE_URL_PRIVATE", "FILE_PRIVATE", "RESULT_PRIVATE"} {
		if strings.Contains(content, hidden) {
			t.Errorf("private envelope data %q leaked", hidden)
		}
	}
	for _, preserved := range []string{"evidence.pdf", "image/png", "application/pdf", "PAYLOAD_PROVIDER", "PAYLOAD_REASONING", "PAYLOAD_DATA", "[memoh pruned]", "[tool result pruned: 2000 bytes]"} {
		if !strings.Contains(content, preserved) {
			t.Errorf("stored evidence %q was lost", preserved)
		}
	}
}

func TestHistoryExecutionPreservesOpaqueLegacyTypedResults(t *testing.T) {
	t.Parallel()
	reader := &fakeHistoryMessageReader{exactMessage: messagepkg.Message{
		ID: "message-1", Role: "tool", SessionID: "session-current",
		Content: json.RawMessage(`{"role":"tool","tool_call_id":"call-1","name":"inspect","content":[{"type":"record","value":"OPAQUE_RECORD"},{"type":"reasoning","text":"OPAQUE_RESULT_TEXT"}]}`),
	}}
	got, err := callHistoryRead(t, reader, map[string]any{"message_id": "message-1", "view": "execution"})
	if err != nil {
		t.Fatal(err)
	}
	content := got["messages"].([]map[string]any)[0]["content"].(string)
	for _, want := range []string{"OPAQUE_RECORD", "OPAQUE_RESULT_TEXT", "call-1"} {
		if !strings.Contains(content, want) {
			t.Errorf("opaque legacy tool result %q was lost: %s", want, content)
		}
	}
}

func TestHistoryExecutionProjectsSingletonToolResults(t *testing.T) {
	t.Parallel()
	for _, legacyID := range []bool{false, true} {
		stored := map[string]any{
			"role": "tool",
			"content": map[string]any{
				"type": "tool-result", "toolCallId": "call-1", "toolName": "inspect", "isError": true,
				"result":           map[string]any{"evidence": "PUBLIC_RESULT", "providerMetadata": "PAYLOAD_PROVIDER"},
				"providerMetadata": map[string]any{"private": "PRIVATE_PROVIDER"},
			},
		}
		if legacyID {
			stored["tool_call_id"] = "call-1"
		}
		raw, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		reader := &fakeHistoryMessageReader{exactMessage: messagepkg.Message{
			ID: "message-1", Role: "tool", SessionID: "session-current", Content: raw,
		}}
		got, err := callHistoryRead(t, reader, map[string]any{"message_id": "message-1", "view": "execution"})
		if err != nil {
			t.Errorf("legacyID=%t: %v", legacyID, err)
			continue
		}
		content := got["messages"].([]map[string]any)[0]["content"].(string)
		if strings.Contains(content, "PRIVATE_PROVIDER") {
			t.Errorf("legacyID=%t exposed private provider metadata", legacyID)
		}
		for _, value := range []string{"PUBLIC_RESULT", "PAYLOAD_PROVIDER", `"isError":true`} {
			if !strings.Contains(content, value) {
				t.Errorf("legacyID=%t lost stored evidence %q", legacyID, value)
			}
		}
	}
}

func TestHistoryExecutionMalformedLegacyPartsDoNotExposePrivateData(t *testing.T) {
	t.Parallel()
	reader := &fakeHistoryMessageReader{exactMessage: messagepkg.Message{
		ID: "message-1", Role: "tool", SessionID: "session-current",
		Content: json.RawMessage(`{"role":"tool","tool_call_id":"call-1","content":[{"type":"tool-result","toolCallId":"call-1","toolName":"inspect","result":"evidence","providerMetadata":{"private":"PRIVATE_PROVIDER"}},{"type":"reasoning","text":"PRIVATE_REASONING"},{}]}`),
	}}
	for _, malformed := range []string{"{}", "17"} {
		reader.exactMessage.Content = json.RawMessage(strings.Replace(string(reader.exactMessage.Content), ",{}]}", ","+malformed+"]}", 1))
		got, err := callHistoryRead(t, reader, map[string]any{"message_id": "message-1", "view": "execution"})
		if err != nil {
			continue
		}
		content := got["messages"].([]map[string]any)[0]["content"].(string)
		for _, hidden := range []string{"PRIVATE_PROVIDER", "PRIVATE_REASONING"} {
			if strings.Contains(content, hidden) {
				t.Errorf("malformed legacy part %s exposed %q", malformed, hidden)
			}
		}
	}
}

func TestHistoryExecutionPagesSurviveDefaultToolOutputWrapper(t *testing.T) {
	t.Parallel()
	want := strings.Repeat("\"\\\n\t<&错误", 3000)
	raw, err := json.Marshal(map[string]any{"role": "tool", "tool_call_id": "call-1", "content": want})
	if err != nil {
		t.Fatal(err)
	}
	reader := &fakeHistoryMessageReader{exactMessage: messagepkg.Message{
		ID: "message-1", Role: "tool", SessionID: "session-current", Content: raw,
	}}
	provider := NewHistoryProvider(nil, nil, reader, nil)
	available, err := provider.Tools(context.Background(), SessionContext{BotID: "bot-1", SessionID: "session-current"})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := WrapToolOutputLimits(available, ToolOutputLimit{MaxBytes: 64 * 1024, MaxLines: 2000})
	var read sdk.Tool
	for _, tool := range wrapped {
		if tool.Name == ToolGetMessages().String() {
			read = tool
		}
	}
	if read.Execute == nil {
		t.Fatal("get_messages missing")
	}
	args := map[string]any{"message_id": "message-1", "view": "execution", "max_bytes": 8192}
	var recovered strings.Builder
	complete := false
	for page := 0; page < 100; page++ {
		got, err := read.Execute(&sdk.ToolExecContext{Context: context.Background()}, args)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > 64*1024 {
			t.Fatalf("wrapped output exceeds default limit: %d bytes", len(encoded))
		}
		var decoded struct {
			Messages []struct {
				Content           string `json:"content"`
				ContentVersion    string `json:"content_version"`
				ContentOffset     int    `json:"content_offset"`
				NextContentOffset int    `json:"next_content_offset"`
				HasMore           bool   `json:"has_more"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded.Messages) != 1 {
			t.Fatalf("wrapper removed retrieval envelope: %s", encoded)
		}
		row := decoded.Messages[0]
		if len(row.Content) > 8192 || !utf8.ValidString(row.Content) || row.ContentOffset != recovered.Len() {
			t.Fatalf("invalid wrapped page: offset=%d bytes=%d", row.ContentOffset, len(row.Content))
		}
		recovered.WriteString(row.Content)
		if !row.HasMore {
			complete = true
			break
		}
		if row.NextContentOffset != recovered.Len() || row.ContentVersion == "" {
			t.Fatal("wrapper altered pagination metadata")
		}
		args["content_offset"] = row.NextContentOffset
		args["content_version"] = row.ContentVersion
	}
	if !complete {
		t.Fatal("retrieval did not terminate within 100 pages")
	}
	var restored struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(recovered.String()), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Content != want {
		t.Fatal("wrapper truncated or modified stored evidence")
	}
}
