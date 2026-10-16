package native

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/step"
)

// refusedConnection is the error net/http returns for a request nobody
// listens for.
func refusedConnection() error {
	return &url.Error{Op: "Post", URL: "http://127.0.0.1:1/v1/chat/completions", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}
}

// The non-streaming loop marks a failed model call as the streaming loop does,
// whether the call was retried until the attempts ran out or failed for good
// at once, leaving the text and the chain of the failure as they were.
func TestAgentGenerateMarksModelCallFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		err  error
		text string
	}{
		{refusedConnection(), "model call retries exhausted: generate: " + refusedConnection().Error()},
		{rejectedErr(), "generate: " + rejectedErr().Error()},
	} {
		want := tc.err
		provider := &atomicMockProvider{handler: func(int, sdk.Request) (sdk.ModelResult, error) {
			return sdk.ModelResult{}, want
		}}
		_, err := New(Deps{}).Generate(context.Background(), RunConfig{
			Model:            &sdk.Model{ID: "mock-model", Provider: provider},
			Messages:         []sdk.Message{sdk.UserMessage("task")},
			Identity:         SessionContext{BotID: "bot-1"},
			ContextMutations: contextfrag.NewMutationLedger(),
			Retry:            fastRetry,
		})
		if !IsModelCallFailure(err) {
			t.Fatalf("IsModelCallFailure(%v) = false, want true", err)
		}
		if !errors.Is(err, want) || err.Error() != tc.text {
			t.Fatalf("Generate() error = %v, want %s", err, tc.text)
		}
	}
}

// A run that ends on its model call reports a model call failure, whether the
// call was retried until the attempts ran out or failed for good at once. The
// mark leaves the text and the chain of the failure as they were.
func TestAgentStreamMarksModelCallFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"retried until the attempts ran out", refusedConnection()},
		{"rejected at once", rejectedErr()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
				return nil, tc.err
			})
			var failures []error
			for event := range New(Deps{}).Stream(context.Background(), RunConfig{
				Model:            &sdk.Model{ID: "mock-model", Provider: provider},
				Messages:         []sdk.Message{sdk.UserMessage("task")},
				Identity:         SessionContext{BotID: "bot-1"},
				ContextMutations: contextfrag.NewMutationLedger(),
				Retry:            fastRetry,
			}) {
				if event.Type == EventError {
					if event.Code != "" {
						t.Fatalf("model call failure code = %q, want none: the application names it", event.Code)
					}
					failures = append(failures, event.Cause)
				}
			}
			if len(failures) != 1 {
				t.Fatalf("error events = %v, want exactly one", failures)
			}
			cause := failures[0]
			if !IsModelCallFailure(cause) {
				t.Fatalf("IsModelCallFailure(%v) = false, want true", cause)
			}
			if !errors.Is(cause, tc.err) || !strings.Contains(cause.Error(), "model stream: "+tc.err.Error()) {
				t.Fatalf("error cause = %v, want the model call's failure %v", cause, tc.err)
			}
		})
	}
}

// A failure of the runtime's own work names runtime_run_failed, so it is not
// reported as an interrupted model response. Its cause is not a model call
// failure.
func TestAgentStreamNamesItsOwnFailures(t *testing.T) {
	t.Parallel()

	commitErr := errors.New("private commit failure")
	for _, tc := range []struct {
		name string
		cfg  func() RunConfig
	}{
		{"no model", func() RunConfig { return RunConfig{} }},
		{"no provider", func() RunConfig { return RunConfig{Model: &sdk.Model{ID: "mock-model"}} }},
		{"step commit", func() RunConfig {
			var invocations atomic.Int32
			provider := agentStreamTestProvider(streamScript(&invocations, scriptText("answer")))
			return RunConfig{
				Model:            &sdk.Model{ID: "mock-model", Provider: provider},
				Messages:         []sdk.Message{sdk.UserMessage("task")},
				ContextMutations: contextfrag.NewMutationLedger(),
				OnStepCommitted: func(context.Context, int, *step.Record) (StepDirective, error) {
					return StepDirective{}, commitErr
				},
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var failures []StreamEvent
			for event := range New(Deps{}).Stream(context.Background(), tc.cfg()) {
				if event.Type == EventError {
					failures = append(failures, event)
				}
			}
			if len(failures) != 1 || failures[0].Code != "runtime_run_failed" || failures[0].Error != "" ||
				failures[0].Cause == nil || IsModelCallFailure(failures[0].Cause) {
				t.Fatalf("error events = %#v, want one runtime_run_failed event with a local cause", failures)
			}
		})
	}
}
