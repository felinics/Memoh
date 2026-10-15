package acp

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

type unconfirmedPrompter struct{}

func (unconfirmedPrompter) Prompt(context.Context, PromptInput) (client.PromptResult, error) {
	return client.PromptResult{Text: "partial", TerminationUnconfirmed: true}, context.Canceled
}

func TestDriverPreservesUnconfirmedTerminationOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := NewDriver(unconfirmedPrompter{}).Prompt(ctx, external.PromptInput{Sink: external.EventSinkFunc(func(event.StreamEvent) {})})
	if !result.TerminationUnconfirmed || !errors.Is(err, external.ErrTerminationUnconfirmed) || result.Text != "partial" || len(result.Output) == 0 {
		t.Fatalf("unknown execution was swallowed as cancellation: result=%+v err=%v", result, err)
	}
}
