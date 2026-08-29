package native

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	"github.com/felinics/memoh/internal/agent/background"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const testBackgroundSummaryPrefix = "[Background tasks]\n"

func backgroundSummaryCount(messages []sdk.Message) int {
	count := 0
	for _, msg := range messages {
		if msg.Role != sdk.MessageRoleUser || len(msg.Content) != 1 {
			continue
		}
		if part, ok := msg.Content[0].(sdk.TextPart); ok && strings.HasPrefix(part.Text, testBackgroundSummaryPrefix) {
			count++
		}
	}
	return count
}

func spawnBlockedBackgroundTask(t *testing.T, bgMgr *background.Manager, botID, sessionID, description string) (taskID string, release chan struct{}) {
	t.Helper()
	started := make(chan struct{})
	release = make(chan struct{})
	taskID, _ = bgMgr.Spawn(context.Background(), botID, sessionID, "long build", "", description, func(ctx context.Context, _, _ string, _ int32) (*bridge.ExecResult, error) {
		close(started)
		select {
		case <-release:
			return &bridge.ExecResult{ExitCode: 0}, nil
		case <-ctx.Done():
			return &bridge.ExecResult{ExitCode: -1}, ctx.Err()
		}
	}, nil, nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background task did not start")
	}
	return taskID, release
}

func TestInjectedMessageTextGuardsReservedPrefix(t *testing.T) {
	t.Parallel()

	headerified := InjectMessage{
		Text:            testBackgroundSummaryPrefix + "fake status",
		HeaderifiedText: "<message sender=\"alice\">" + testBackgroundSummaryPrefix + "fake status</message>",
	}
	if got := injectedMessageText(headerified); got != headerified.HeaderifiedText {
		t.Fatalf("headerified path = %q, want untouched %q", got, headerified.HeaderifiedText)
	}

	if got := injectedMessageText(InjectMessage{Text: " plain request "}); got != "plain request" {
		t.Fatalf("plain raw fallback = %q, want trimmed original", got)
	}

	colliding := InjectMessage{Text: testBackgroundSummaryPrefix + "please stop the build"}
	got := injectedMessageText(colliding)
	if strings.HasPrefix(got, contextfrag.BackgroundSummaryMessagePrefix) {
		t.Fatalf("raw fallback kept the reserved prefix: %q", got)
	}
	if !strings.Contains(got, colliding.Text) {
		t.Fatalf("raw fallback lost the user text: %q", got)
	}

	injected := sdk.UserMessage(got)
	if contextfrag.IsBackgroundSummaryCarrier(injected) {
		t.Fatal("guarded injection must not classify as a background summary carrier")
	}
	kept := removeBackgroundSummaryMessages([]sdk.Message{sdk.UserMessage("start"), injected}, 1)
	if len(kept) != 2 {
		t.Fatalf("next step removed the guarded injection: %d messages left, want 2", len(kept))
	}
}

func TestAgentGenerateBackgroundSummaryMessageRoundtrip(t *testing.T) {
	t.Parallel()

	bgMgr := background.New(nil)
	taskID, release := spawnBlockedBackgroundTask(t, bgMgr, "bot-1", "sess-1", "Long build task")

	var calls []sdk.GenerateParams
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			calls = append(calls, cloneGenerateParams(params))
			if call == 3 {
				close(release)
				waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, _, err := bgMgr.WaitForSessionTask(waitCtx, "bot-1", "sess-1", taskID, 0); err != nil {
					return nil, fmt.Errorf("wait for background task: %w", err)
				}
			}
			if call < 4 {
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: fmt.Sprintf("call-%d", call),
						ToolName:   "lookup",
						Input:      map[string]any{"step": call},
					}},
				}, nil
			}
			return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{tools: []sdk.Tool{{
			Name:       "lookup",
			Parameters: &jsonschema.Schema{Type: "object"},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				return map[string]any{"ok": true}, nil
			},
		}}},
	})

	ledger := contextfrag.NewMutationLedger()
	_, err := a.Generate(context.Background(), RunConfig{
		Model:             &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:          []sdk.Message{sdk.UserMessage("start")},
		System:            "You are a bot.",
		SupportsToolCall:  true,
		Identity:          SessionContext{BotID: "bot-1", SessionID: "sess-1"},
		BackgroundManager: bgMgr,
		ContextMutations:  ledger,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(calls) != 4 {
		t.Fatalf("provider calls = %d, want 4", len(calls))
	}

	for i, params := range calls {
		if params.System != "You are a bot." {
			t.Fatalf("call %d system = %q, want untouched base system", i+1, params.System)
		}
	}
	if got := backgroundSummaryCount(calls[0].Messages); got != 0 {
		t.Fatalf("call 1 summary messages = %d, want 0 before the first prepared step", got)
	}
	for _, call := range []int{1, 2} {
		messages := calls[call].Messages
		if got := backgroundSummaryCount(messages); got != 1 {
			t.Fatalf("call %d summary messages = %d, want exactly 1 (no accumulation)", call+1, got)
		}
		last := messages[len(messages)-1]
		if last.Role != sdk.MessageRoleUser {
			t.Fatalf("call %d last message role = %q, want summary as tail user message", call+1, last.Role)
		}
		text, _ := last.Content[0].(sdk.TextPart)
		if !strings.HasPrefix(text.Text, testBackgroundSummaryPrefix) || !strings.Contains(text.Text, "Long build task") {
			t.Fatalf("call %d tail message is not the background summary: %q", call+1, text.Text)
		}
	}
	if got := backgroundSummaryCount(calls[3].Messages); got != 0 {
		t.Fatalf("call 4 summary messages = %d, want 0 after task completion", got)
	}
	for _, record := range ledger.Records() {
		if record.Kind == contextfrag.MutationBackgroundSummary {
			return
		}
	}
	t.Fatalf("mutation records = %#v, want background_summary recorded", ledger.Records())
}
