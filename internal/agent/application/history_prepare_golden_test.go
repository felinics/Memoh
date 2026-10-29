package application

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/toolexec"
	turnpkg "github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

const historyPrepareGoldenPath = "testdata/history_prepare.golden.json"

var updateGolden = flag.Bool("update-golden", false, "rewrite "+historyPrepareGoldenPath+" from the current output")

// TestPrepareHistoryRequestGolden pins what resolve derives from stored rows:
// provider messages, their provenance and source rows, token estimates and
// the trimmable prefix. The golden was captured before resolve stopped
// re-converting each message per pass, so any drift in what reaches the
// provider fails here.
func TestPrepareHistoryRequestGolden(t *testing.T) {
	t.Parallel()

	got := prepareHistoryRequest(t, goldenHistoryRows(t), "what changed since the last check?")
	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if *updateGolden {
		if err := os.WriteFile(historyPrepareGoldenPath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(historyPrepareGoldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update-golden to create it)", err)
	}
	if !bytes.Equal(encoded, want) {
		t.Fatalf("prepared history request drifted from %s; diff it against the output of -update-golden", historyPrepareGoldenPath)
	}
}

// goldenHistoryRows covers every rewrite resolve applies to stored history:
// orphan calls and results, a tool row answering one live and one orphan
// call, a decision projection whose result was stored after narration,
// legacy tool_calls and byte-array images, interrupted reasoning, an empty
// failure row, and repeated identical prompts.
func goldenHistoryRows(t *testing.T) []messagepkg.Message {
	t.Helper()
	base := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	var rows []messagepkg.Message
	add := func(role string, content json.RawMessage, metadata string) {
		i := len(rows)
		if metadata == "" {
			metadata = "{}"
		}
		rows = append(rows, messagepkg.Message{
			ID:          fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1),
			BotID:       "bot-1",
			SessionID:   "session-1",
			Role:        role,
			Content:     content,
			RawMetadata: json.RawMessage(metadata),
			Usage:       json.RawMessage(`{"inputTokens":10,"outputTokens":2}`),
			CreatedAt:   base.Add(time.Duration(i) * time.Second),
		})
	}
	stored := func(message sdk.Message) json.RawMessage {
		raw, err := historyfrag.MarshalStoredSDKMessage(message)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	call := func(id, name string, input any, meta sdk.ProviderMetadata) sdk.ToolCallPart {
		return sdk.ToolCallPart{ToolCallID: id, ToolName: name, Input: toolexec.ArgumentsFromValue(input), ProviderMetadata: meta}
	}
	result := func(id, name string, output any) sdk.ToolResultPart {
		return sdk.ToolResultPart{ToolCallID: id, ToolName: name, Result: toolexec.OutputFromValue(output)}
	}

	// Repeated identical prompts, each answered.
	for i := range 2 {
		add("user", stored(sdk.UserMessage("scheduled check: report the dashboard status")), "")
		add("assistant", stored(sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			sdk.TextPart{Text: "Fetching."},
			call(fmt.Sprintf("fetch-%d", i), "web_fetch", map[string]any{"url": "https://example.com", "nested": map[string]any{"a": []int{1, 2}}}, nil),
		}}), "")
		add("tool", stored(sdk.ToolMessage(result(fmt.Sprintf("fetch-%d", i), "web_fetch", map[string]any{"status": "ok", "items": []any{map[string]any{"z": 1, "a": "<b>&</b>"}}}))), "")
		add("assistant", stored(sdk.AssistantMessage("Dashboard is healthy.")), "")
	}

	// An orphan result, then a call left without one.
	add("tool", stored(sdk.ToolMessage(result("ghost", "exec", "orphan output"))), "")
	add("user", stored(sdk.UserMessage("run uptime")), "")
	add("assistant", stored(sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{call("exec-1", "exec", map[string]any{"command": "uptime"}, nil)}}), "")

	// One tool row answering a live call and an orphan one.
	add("user", stored(sdk.UserMessage("check two hosts")), "")
	add("assistant", stored(sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{call("ping-a", "exec", map[string]any{"host": "a"}, nil)}}), "")
	add("tool", stored(sdk.ToolMessage(result("ping-a", "exec", "a is up"), result("ping-z", "exec", "z is up"))), "")

	// A resolved ask_user projection whose real result was stored after
	// narration in the same user turn.
	add("user", stored(sdk.UserMessage("ask me which env")), "")
	add("assistant", stored(sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{call("ask-1", "ask_user", map[string]any{"question": "env?"},
		partmeta.Set(nil, partmeta.KeyUserInput, map[string]any{"status": "submitted", "answers": map[string]any{"env": "prod"}}))}}), "")
	add("assistant", stored(sdk.AssistantMessage("Thanks, using prod.")), "")
	add("tool", stored(sdk.ToolMessage(result("ask-1", "ask_user", map[string]any{"status": "submitted", "answers": map[string]any{"env": "prod"}}))), "")

	// A legacy row carrying tool_calls beside empty text.
	legacy, err := historyfrag.MarshalStoredModelMessage(ModelMessage{
		Role:      "assistant",
		Content:   newTextContent(""),
		ToolCalls: []ToolCall{{ID: "legacy-1", Type: "function", Function: turnpkg.ToolCallFunction{Name: "exec", Arguments: `{"command":"df -h"}`}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	add("user", stored(sdk.UserMessage("disk usage?")), "")
	add("assistant", legacy, "")
	add("tool", json.RawMessage(`{"role":"tool","content":"/dev/sda1 42%","tool_call_id":"legacy-1","name":"exec"}`), "")

	// A user image stored as an indexed byte object.
	add("user", json.RawMessage(`{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image","image":{"0":137,"1":80,"2":78,"3":71},"mediaType":"image/png"}]}`), "")
	add("assistant", stored(sdk.AssistantMessage("A PNG header.")), "")

	// Interrupted reasoning, and an empty failure row.
	add("user", stored(sdk.UserMessage("think about it")), "")
	add("assistant", stored(sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ReasoningPart{Text: "half a thought"}}}),
		fmt.Sprintf(`{%q:true}`, messagepkg.AgentStepInterruptedMetadataKey))
	add("assistant", json.RawMessage(`{"role":"assistant","content":""}`), fmt.Sprintf(`{%q:"timeout"}`, messagepkg.HistoryErrorCodeMetadataKey))
	return rows
}
