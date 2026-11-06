package compaction

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/models"
)

func TestFoldShrinksRejectedSummaryAndCommitsOnlyAcceptedAnswer(t *testing.T) {
	s := newFoldSession(t)
	r := s.request()
	r.messages[3] = resultMessage("call_1", strings.Repeat("parser.go:12 unsafe cast of int64 to int32\n", 12000))
	original := r.messages[3]
	ev := s.event(r, &scriptedSummarizer{})
	ev.Window, ev.Allowance, ev.MaxOutputTokens = 200000, 180000, 8000
	ev.FixedOutput = true
	ev.Request.MaxTokens = &ev.MaxOutputTokens
	calls, rejectedTokens := 0, 0
	ev.Summarizer = func(_ context.Context, req sdk.Request) (sdk.ModelResult, error) {
		calls++
		if calls == 1 {
			rejectedTokens = forkTokens(req)
			if rejectedTokens+8000 > 200000 {
				t.Fatal("first fork must pass local admission")
			}
			return sdk.ModelResult{}, &sdk.APIError{StatusCode: 400, Code: "context_length_exceeded"}
		}
		if forkTokens(req) >= rejectedTokens {
			t.Fatal("summary retry did not shrink")
		}
		if req.MaxTokens == nil || *req.MaxTokens != 8000 {
			t.Fatal("recovery changed the run's output settings")
		}
		return summaryAnswer("The parser audit found an unsafe int64 cast at parser.go:12. Fix and verify it."), nil
	}
	passes, err := s.svc.Fold(context.Background(), ev)
	if err != nil || len(passes) == 0 || calls < 2 {
		t.Fatalf("passes=%d calls=%d err=%v", len(passes), calls, err)
	}
	if !messagesEqual(original, r.messages[3]) {
		t.Fatal("recovery mutated the original tool result")
	}
}

// An attempt that exhausts the window with an empty completion recovers
// through the real planner: the retry shrinks history and keeps the whole
// current input. A callback that folds blindly would not show whether the
// planner leaves the protected input room.
func TestEmptyContextRecoveryKeepsRoomForProtectedInput(t *testing.T) {
	s := newFoldSession(t)
	history := sdk.UserMessage(strings.Repeat("history ", 100000))
	input := sdk.UserMessage(strings.Repeat("current ", 60000))
	calls, summaries, firstBytes := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			firstBytes = len(body)
			_, _ = fmt.Fprint(w, `{"id":"exhausted","model":"usage-model","choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":1048570,"completion_tokens":0,"total_tokens":1048570}}`)
			return
		}
		if len(body) >= firstBytes || strings.Count(string(body), "current ") != 60000 {
			t.Error("retry must shrink history while preserving the entire current input")
		}
		_, _ = fmt.Fprint(w, `{"id":"recovered","model":"usage-model","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101}}`)
	}))
	defer server.Close()
	toSources := func(provenance []*native.MessageProvenance) []Source {
		sources := make([]Source, len(provenance))
		for i, p := range provenance {
			if p != nil {
				sources[i] = Source{Sources: p.Sources, Anchor: p.Anchor}
			}
		}
		return sources
	}
	cfg := native.RunConfig{
		Model:    models.NewSDKChatModel(models.SDKModelConfig{ModelID: "usage-model", ClientType: "openai-completions", BaseURL: server.URL, HTTPClient: server.Client()}),
		Messages: []sdk.Message{history, input}, ContextBudgetMaxTokens: 1000000, ModelMaxOutputTokens: 8192,
		Identity: native.SessionContext{BotID: s.botID, SessionID: s.session},
		ContextProvenance: []native.MessageProvenance{
			{Message: history, Sources: []turn.ContextMessageSource{s.row()}},
			{Message: input, Sources: []turn.ContextMessageSource{s.row()}, Anchor: true},
		},
		SwitchContext: func(ctx context.Context, sw native.ContextSwitch) (string, error) {
			tokens := sw.UsageTokens
			_, err := s.svc.Fold(ctx, Event{
				BotID: s.botID, SessionID: s.session, Request: sw.Request, Sources: toSources(sw.Provenance),
				Window: sw.WindowTokens, MaxOutputTokens: sw.OutputTokens, Allowance: sw.AllowanceTokens, Pressure: sw.UsageTokens, FixedOutput: true,
				Summarizer: func(context.Context, sdk.Request) (sdk.ModelResult, error) {
					summaries++
					return summaryAnswer("History condensed."), nil
				},
				Apply: func(pass Pass) (sdk.Request, []Source) {
					next := sw.Fold(native.ContextFold{SummaryID: pass.SummaryID, Summary: SummaryMessage(pass.Summary), Order: pass.Order})
					tokens = next.Tokens
					return next.Request, toSources(next.Provenance)
				},
			})
			if err == nil && tokens > sw.AllowanceTokens {
				return "", fmt.Errorf("%w: splice exceeds planner allowance", contextfrag.ErrBudgetUnsatisfied)
			}
			return "", err
		},
	}
	result, err := native.New(native.Deps{}).Generate(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "done" || calls != 2 || summaries == 0 {
		t.Fatalf("answer=%q calls=%d summaries=%d", result.Text, calls, summaries)
	}
}

func TestFoldStopsRepeatedSummaryOverflowWithoutCommitting(t *testing.T) {
	s := newFoldSession(t)
	r := s.request()
	r.messages[3] = resultMessage("call_1", strings.Repeat("parser.go:12 unsafe cast of int64 to int32\n", 12000))
	ev := s.event(r, &scriptedSummarizer{})
	ev.Window, ev.Allowance, ev.MaxOutputTokens = 200000, 180000, 8000
	calls, lastInput := 0, 0
	ev.Summarizer = func(_ context.Context, req sdk.Request) (sdk.ModelResult, error) {
		calls++
		input := forkTokens(req)
		if lastInput > 0 && input >= lastInput {
			t.Fatal("repeated the same rejected summary")
		}
		lastInput = input
		return sdk.ModelResult{}, &sdk.APIError{Code: "context_length_exceeded"}
	}
	ev.Apply = func(Pass) (sdk.Request, []Source) { t.Fatal("committed a rejected summary"); return sdk.Request{}, nil }
	passes, err := s.svc.Fold(context.Background(), ev)
	if err == nil || len(passes) != 0 || calls < 2 || calls > 4 {
		t.Fatalf("passes=%d calls=%d err=%v", len(passes), calls, err)
	}
}
