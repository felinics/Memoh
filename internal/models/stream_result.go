package models

import (
	"context"
	"errors"
	"fmt"
	"time"

	sdk "github.com/felinics/twilight/sdk"
)

// ErrStreamSilent marks a stream that sent nothing for as long as its caller
// allows.
var ErrStreamSilent = errors.New("model stream sent nothing within its silence window")

// StreamResult answers req through model as a stream and returns what the
// stream completed with. A long answer is bounded by silence rather than by a
// deadline on its first byte: a stream that sends nothing for silence is cut
// off as an incomplete one, which a retry may ask again for.
//
//nolint:gocritic // hugeParam: the request is the model call's value DTO.
func StreamResult(ctx context.Context, model *sdk.Model, req sdk.Request, silence time.Duration) (sdk.ModelResult, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stream, err := model.Stream(ctx, req)
	if err != nil {
		return sdk.ModelResult{}, err
	}
	quiet := time.AfterFunc(silence, func() { cancel(ErrStreamSilent) })
	defer quiet.Stop()
	for range stream.Parts {
		quiet.Reset(silence)
	}
	result, err := stream.Result()
	if errors.Is(context.Cause(ctx), ErrStreamSilent) {
		// A silent stream is cut off, as one the provider dropped is.
		return sdk.ModelResult{}, fmt.Errorf("%w: %w", sdk.ErrStreamIncomplete, ErrStreamSilent)
	}
	if err != nil {
		return sdk.ModelResult{}, err
	}
	if result == nil {
		return sdk.ModelResult{}, sdk.ErrStreamIncomplete
	}
	return *result, nil
}
