package native

import (
	"context"
	"errors"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/models/modelretry"
)

// A fork request streams through the run's model without the run's call
// observer: none of its parts reach the run's part stream, and its answer is
// the stream's result.
func TestForkSummarizerStreamsPastTheRunsObserver(t *testing.T) {
	t.Parallel()

	provider := &atomicMockProvider{handler: func(int, sdk.Request) (sdk.ModelResult, error) {
		return sdk.ModelResult{Text: "the summary", FinishReason: sdk.FinishReasonStop}, nil
	}}
	var observed []StreamEvent
	model := modelWithProviderCallObserver(&sdk.Model{ID: "mock-model", Provider: provider}, func(event StreamEvent) {
		observed = append(observed, event)
	}, nil)

	result, err := forkSummarizer(model, time.Minute)(context.Background(), sdk.Request{Messages: []sdk.Message{sdk.UserMessage("summarize")}})
	if err != nil || result.Text != "the summary" || result.FinishReason != sdk.FinishReasonStop {
		t.Fatalf("result=%+v err=%v, want the streamed summary", result, err)
	}
	if len(observed) != 0 || provider.calls.Load() != 1 {
		t.Fatalf("observed %d parts over %d calls, want one call the run's observer never saw", len(observed), provider.calls.Load())
	}
}

// A fork stream that sends nothing for the run's model window is cut off as
// an incomplete stream, which the event asks again for, rather than waited on
// while the run's own watchdog is paused.
func TestForkSummarizerCutsOffASilentStream(t *testing.T) {
	t.Parallel()

	provider := &atomicMockProvider{stream: func(ctx context.Context, _ sdk.Request) (<-chan sdk.StreamPart, error) {
		parts := make(chan sdk.StreamPart, 1)
		parts <- &sdk.StartPart{}
		go func() {
			<-ctx.Done()
			close(parts)
		}()
		return parts, nil
	}}

	_, err := forkSummarizer(&sdk.Model{ID: "mock-model", Provider: provider}, 20*time.Millisecond)(context.Background(), sdk.Request{})
	if !errors.Is(err, sdk.ErrStreamIncomplete) || !errors.Is(err, models.ErrStreamSilent) {
		t.Fatalf("err = %v, want an incomplete stream that fell silent", err)
	}
	if reason, ok := modelretry.Retryable(err); !ok || reason != modelretry.ReasonStreamIncomplete {
		t.Fatalf("retryable = %q %v, want a silent fork asked again", reason, ok)
	}
}
