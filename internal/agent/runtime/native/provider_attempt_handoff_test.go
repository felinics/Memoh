package native

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

func TestProviderAttemptHandoffRejectsCanceledDispatch(t *testing.T) {
	t.Parallel()

	for _, stream := range []bool{false, true} {
		stream := stream
		name := "generate"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			oldParams := sdk.GenerateParams{Messages: []sdk.Message{sdk.UserMessage("last dispatched")}}
			oldHash := contextfrag.ProviderPayloadHash(oldParams.System, oldParams.Messages, oldParams.Tools)
			ledger := contextfrag.NewMutationLedger()
			ledger.SetFinalInputHash(oldHash)
			ledger.AppendStepSnapshot(contextfrag.StepSnapshot{StepIndex: 0, PostPrepareInputHash: oldHash})
			fork := agenttools.NewMessageSnapshot(oldParams.Messages)
			attemptState := &providerAttemptState{}
			attemptState.store(&oldParams, 0, false, preparedMessageProvenance{})

			prepare, capture := capturePreparedStepMessages(func(params *sdk.GenerateParams) *sdk.GenerateParams {
				params.Messages = append(params.Messages, sdk.UserMessage("not dispatched"))
				return params
			})
			newParams := sdk.GenerateParams{Messages: []sdk.Message{sdk.UserMessage("next")}}
			newParams = *prepare(&newParams)
			if len(capture.messages(1)) != 1 {
				t.Fatal("test setup did not capture the pending dynamic message")
			}

			cfg := RunConfig{
				ContextMutations:     ledger,
				ForkContext:          fork,
				providerAttemptState: attemptState,
				preparedStepMessages: capture,
			}
			handoff := newProviderAttemptHandoff(cfg)
			handoff.stage(
				contextfrag.StepSnapshot{StepIndex: 1},
				false,
				"dropped=1",
				capture.latestProvenance(newParams.Messages),
			)

			var calls atomic.Int32
			provider := &atomicMockProvider{
				handler: func(int, sdk.GenerateParams) (*sdk.GenerateResult, error) {
					calls.Add(1)
					return nil, errors.New("provider must not be called")
				},
				stream: func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
					calls.Add(1)
					return nil, errors.New("provider must not be called")
				},
			}
			guard := contextBudgetGuardProvider{Provider: provider, handoff: handoff}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			var err error
			if stream {
				_, err = guard.DoStream(ctx, newParams)
			} else {
				_, err = guard.DoGenerate(ctx, newParams)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("guard error = %v, want context canceled", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("provider calls = %d, want 0", calls.Load())
			}
			if got := ledger.FinalInputHash(); got != oldHash {
				t.Fatalf("final input hash = %q, want prior hash %q", got, oldHash)
			}
			if got := ledger.StepSnapshots(); len(got) != 1 || got[0].PostPrepareInputHash != oldHash {
				t.Fatalf("step snapshots = %#v, want only prior dispatched attempt", got)
			}
			if got := ledger.Records(); len(got) != 0 {
				t.Fatalf("mutation records = %#v, want canceled reselection audit unpublished", got)
			}
			forkMessages, err := fork.Messages()
			if err != nil {
				t.Fatalf("read fork snapshot: %v", err)
			}
			if !reflect.DeepEqual(forkMessages, oldParams.Messages) {
				t.Fatalf("fork messages = %#v, want prior dispatched messages %#v", forkMessages, oldParams.Messages)
			}
			retryMessages, ok := attemptState.retryMessages(nil)
			if !ok || !reflect.DeepEqual(retryMessages, oldParams.Messages) {
				t.Fatalf("retry messages = %#v, %t; want prior dispatched messages %#v", retryMessages, ok, oldParams.Messages)
			}
			if got := capture.messages(1); len(got) != 0 {
				t.Fatalf("captured messages = %#v, want rejected admission revoked", got)
			}
			if _, err := guard.DoGenerate(context.Background(), newParams); !errors.Is(err, errProviderAttemptNotPrepared) {
				t.Fatalf("second guard call error = %v, want stale handoff cleared", err)
			}
		})
	}
}

func TestProviderAttemptHandoffPublishesBeforeProviderEntry(t *testing.T) {
	t.Parallel()

	providerErr := errors.New("provider rejected request")
	for _, stream := range []bool{false, true} {
		stream := stream
		name := "generate"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			prepare, capture := capturePreparedStepMessages(func(params *sdk.GenerateParams) *sdk.GenerateParams {
				params.Messages = append(params.Messages, sdk.UserMessage("admitted dynamic input"))
				return params
			})
			params := sdk.GenerateParams{
				System:   "system",
				Messages: []sdk.Message{sdk.UserMessage("task")},
				Tools:    []sdk.Tool{{Name: "lookup"}},
			}
			params = *prepare(&params)
			wantHash := contextfrag.ProviderPayloadHash(params.System, params.Messages, params.Tools)
			ledger := contextfrag.NewMutationLedger()
			fork := agenttools.NewMessageSnapshot(nil)
			attemptState := &providerAttemptState{}
			cfg := RunConfig{
				ContextMutations:     ledger,
				ForkContext:          fork,
				providerAttemptState: attemptState,
				preparedStepMessages: capture,
			}
			handoff := newProviderAttemptHandoff(cfg)
			handoff.stage(
				contextfrag.StepSnapshot{StepIndex: 1},
				false,
				"dropped=1",
				capture.latestProvenance(params.Messages),
			)

			var calls atomic.Int32
			inspect := func(got sdk.GenerateParams) {
				calls.Add(1)
				if ledger.FinalInputHash() != wantHash {
					t.Fatalf("provider entry saw final hash %q, want %q", ledger.FinalInputHash(), wantHash)
				}
				steps := ledger.StepSnapshots()
				if len(steps) != 1 || steps[0].StepIndex != 1 || steps[0].PostPrepareInputHash != wantHash {
					t.Fatalf("provider entry saw step snapshots %#v, want published step 1", steps)
				}
				forkMessages, err := fork.Messages()
				if err != nil {
					t.Fatalf("read fork snapshot: %v", err)
				}
				if !reflect.DeepEqual(forkMessages, got.Messages) {
					t.Fatalf("provider entry saw fork messages %#v, want %#v", forkMessages, got.Messages)
				}
				retryMessages, ok := attemptState.retryMessages(nil)
				if !ok || !reflect.DeepEqual(retryMessages, got.Messages) {
					t.Fatalf("provider entry saw retry messages %#v, %t; want %#v", retryMessages, ok, got.Messages)
				}
				if captured := capture.messages(1); len(captured) != 1 || !reflect.DeepEqual(captured[0], got.Messages[len(got.Messages)-1]) {
					t.Fatalf("provider entry saw captured messages %#v, want admitted dynamic input", captured)
				}
			}
			provider := &atomicMockProvider{
				handler: func(_ int, got sdk.GenerateParams) (*sdk.GenerateResult, error) {
					inspect(got)
					return nil, providerErr
				},
				stream: func(_ context.Context, got sdk.GenerateParams) (*sdk.StreamResult, error) {
					inspect(got)
					return nil, providerErr
				},
			}
			guard := contextBudgetGuardProvider{Provider: provider, handoff: handoff}

			var err error
			if stream {
				_, err = guard.DoStream(context.Background(), params)
			} else {
				_, err = guard.DoGenerate(context.Background(), params)
			}
			if !errors.Is(err, providerErr) {
				t.Fatalf("guard error = %v, want provider error", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", calls.Load())
			}
			if records := ledger.Records(); len(records) != 1 || records[0].Kind != contextfrag.MutationLoopStepReselection {
				t.Fatalf("mutation records = %#v, want published reselection audit", records)
			}
		})
	}
}
