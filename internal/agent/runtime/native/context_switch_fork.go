package native

import (
	"context"
	"errors"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/models"
)

// defaultForkSilence is how long a fork request may stay silent when the run
// names no model window.
const defaultForkSilence = 5 * time.Minute

// forkSummarizer answers a compaction event's fork requests through the
// run's own model, streaming, bounded by the run's model window of silence.
// The call is no step of the run: it bypasses the run's call observer.
func forkSummarizer(model *sdk.Model, silence time.Duration) func(context.Context, sdk.Request) (sdk.ModelResult, error) {
	model = unobservedModel(model)
	if silence <= 0 {
		silence = defaultForkSilence
	}
	return func(ctx context.Context, req sdk.Request) (sdk.ModelResult, error) {
		if model == nil {
			return sdk.ModelResult{}, errors.New("compaction fork: the run has no model")
		}
		return models.StreamResult(ctx, model, req, silence)
	}
}

// unobservedModel is model without the run's call observers, which time and
// publish the run's own model calls.
func unobservedModel(model *sdk.Model) *sdk.Model {
	if model == nil {
		return nil
	}
	plain := *model
	for {
		observer, ok := plain.Provider.(providerCallObserver)
		if !ok {
			return &plain
		}
		plain.Provider = observer.Provider
	}
}
