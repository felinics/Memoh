package compaction

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
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
