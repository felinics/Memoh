package native

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	agentevent "github.com/felinics/memoh/internal/agent/event"
)

func finishingStream(reason sdk.FinishReason, requests *[]sdk.Request) func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
	return func(_ context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
		*requests = append(*requests, params)
		ch := make(chan sdk.StreamPart, 8)
		ch <- &sdk.StartPart{}
		ch <- &sdk.StartStepPart{}
		ch <- &sdk.TextStartPart{ID: "mock"}
		ch <- &sdk.TextDeltaPart{ID: "mock", Text: "partial answer"}
		ch <- &sdk.TextEndPart{ID: "mock"}
		ch <- &sdk.FinishStepPart{FinishReason: reason}
		ch <- &sdk.FinishPart{FinishReason: reason}
		close(ch)
		return ch, nil
	}
}

func TestAgentStreamNoticesAReplyCutOffAtTheOutputLimit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		reason sdk.FinishReason
		notice bool
	}{
		{name: "length", reason: sdk.FinishReasonLength, notice: true},
		{name: "stop", reason: sdk.FinishReasonStop, notice: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var requests []sdk.Request
			provider := &atomicMockProvider{stream: finishingStream(tc.reason, &requests)}
			a := New(Deps{})

			var notices []StreamEvent
			var text string
			var terminal StreamEvent
			for ev := range a.Stream(context.Background(), RunConfig{
				Model:                &sdk.Model{ID: "mock-model", Provider: provider},
				Messages:             []sdk.Message{sdk.UserMessage("hi")},
				SupportsToolCall:     true,
				Identity:             SessionContext{BotID: "bot-1"},
				ModelMaxOutputTokens: 4096,
			}) {
				switch {
				case ev.Type == EventRuntimeNotice:
					notices = append(notices, ev)
				case ev.Type == EventTextDelta:
					text += ev.Delta
				case ev.IsTerminal():
					terminal = ev
				}
			}

			if tc.notice {
				if len(notices) != 1 || notices[0].NoticeKind != agentevent.NoticeOutputTruncated {
					t.Fatalf("notices = %+v, want one output_truncated", notices)
				}
			} else if len(notices) != 0 {
				t.Fatalf("notices = %+v, want none", notices)
			}
			if text != "partial answer" {
				t.Fatalf("reply text = %q; the notice must not be mixed into it", text)
			}
			if terminal.Type != EventAgentEnd {
				t.Fatalf("terminal = %q, want agent_end", terminal.Type)
			}
			if len(requests) != 1 || requests[0].MaxTokens == nil || *requests[0].MaxTokens != 4096 {
				t.Fatalf("requests = %+v, want max_tokens 4096 from the model cap", requests)
			}
		})
	}
}
