package native

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/step"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

func TestStreamSteerInterruptsOnlyInvocation(t *testing.T) {
	for _, mode := range []string{"text", "reasoning", "headers", "consecutive", "retry", "checkpoint_failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			wake := make(chan struct{}, 1)
			started := make(chan int, 3)
			var calls, disconnected atomic.Int32
			var pending atomic.Bool
			var checkpoints, starts, terminals int
			var steps []int
			var finalInput []sdk.Message
			interruptions := 1
			retryAttempts := 0
			if mode == "retry" {
				retryAttempts = 1
			}
			if mode == "consecutive" {
				interruptions = 2
			}
			provider := agentStreamTestProvider(func(ctx context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
				call := int(calls.Add(1))
				if mode == "retry" && call == 1 {
					return closedAgentTestStream(&sdk.ErrorPart{Error: errors.New("unexpected EOF")}), nil
				}
				call -= retryAttempts
				if call > interruptions {
					finalInput = cloneProviderMessages(params.Messages)
					return closedAgentTestStream(&sdk.TextDeltaPart{Text: "done"}, &sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}), nil
				}
				if mode == "headers" {
					started <- call
					<-ctx.Done()
					disconnected.Add(1)
					return nil, ctx.Err()
				}
				parts := make(chan sdk.StreamPart)
				go func() {
					defer close(parts)
					var part sdk.StreamPart = &sdk.TextDeltaPart{Text: fmt.Sprintf("partial-%d", call)}
					if mode == "reasoning" {
						part = &sdk.ReasoningDeltaPart{Text: "unfinished thinking"}
					}
					select {
					case parts <- part:
					case <-ctx.Done():
					}
					<-ctx.Done()
					disconnected.Add(1)
				}()
				return parts, nil
			})
			events := New(Deps{}).Stream(ctx, RunConfig{
				Model: &sdk.Model{ID: "mock", Provider: provider}, Messages: []sdk.Message{sdk.UserMessage("original")},
				SteerWake: wake, PendingSteer: func(context.Context) (bool, error) { return pending.Load(), nil },
				// The checkpoint claims the queued input and hands it back as the
				// directive the loop applies at its next step boundary.
				OnSteer: func(_ context.Context, index int, _ *step.Record) (StepDirective, error) {
					if index != checkpoints {
						return StepDirective{}, fmt.Errorf("step %d, want %d", index, checkpoints)
					}
					checkpoints++
					if mode == "checkpoint_failure" {
						return StepDirective{}, errors.New("SECRET database diagnostic")
					}
					pending.Store(false)
					return StepDirective{NextInputs: []DirectiveInput{{
						ID:   fmt.Sprintf("item-%d", checkpoints),
						Text: fmt.Sprintf("steer-%d", checkpoints),
					}}}, nil
				},
			})
			for events != nil {
				select {
				case <-started:
					pending.Store(true)
					wake <- struct{}{}
				case e, ok := <-events:
					if !ok {
						events = nil
						continue
					}
					if e.Type == EventTextDelta && strings.HasPrefix(e.Delta, "partial-") || e.Type == EventReasoningDelta {
						pending.Store(true)
						wake <- struct{}{}
					}
					if e.Type == EventAgentStart {
						starts++
					}
					if e.Type == EventStepEnd {
						steps = append(steps, e.StepNumber)
					}
					if strings.Contains(e.Error, "SECRET") || (e.Type == EventError && mode != "checkpoint_failure" && mode != "retry") {
						t.Fatalf("unexpected public error: %+v", e)
					}
					if e.IsTerminal() {
						terminals++
						if mode != "checkpoint_failure" && e.Type != EventAgentEnd {
							t.Fatalf("steer terminated run: %+v", e)
						}
					}
				case <-ctx.Done():
					t.Fatal("steer failed to continue the blocked invocation")
				}
			}
			if mode == "checkpoint_failure" {
				if calls.Load() != 1 {
					t.Fatal("continued after failed persistence")
				}
				return
			}
			if calls.Load() != int32(interruptions+retryAttempts+1) || disconnected.Load() != int32(interruptions) || starts != 1 || terminals != 1 {
				t.Fatalf("calls=%d disconnected=%d starts=%d terminals=%d", calls.Load(), disconnected.Load(), starts, terminals)
			}
			// Every steered checkpoint advances the durable cursor, so the
			// final answer lands on the step after the last interruption.
			if len(steps) != interruptions+1 {
				t.Fatalf("step cursor: %v, want %d checkpointed steps", steps, interruptions+1)
			}
			for i, index := range steps {
				if index != i {
					t.Fatalf("step cursor: %v", steps)
				}
			}
			var transcript strings.Builder
			for _, message := range finalInput {
				transcript.WriteString(messageContentText(message))
				for _, part := range message.Content {
					if _, ok := part.(sdk.ReasoningPart); ok {
						t.Fatal("replayed unfinished provider reasoning")
					}
				}
			}
			text := transcript.String()
			if !strings.Contains(text, "original") || strings.Count(text, "steer-1") != 1 || mode == "consecutive" && strings.Count(text, "steer-2") != 1 {
				t.Fatalf("continuation input: %q", text)
			}
		})
	}
}

func TestSteerGatePreservesToolAndCommitBoundaries(t *testing.T) {
	for _, part := range []sdk.StreamPart{&sdk.ToolInputStartPart{}, &sdk.StreamToolCallPart{}, &sdk.FinishStepPart{}} {
		t.Run(fmt.Sprintf("%T", part), func(t *testing.T) {
			g := &modelSteerGate{ready: make(chan struct{}, 1)}

			boundaryCtx, cancelBoundary := context.WithCancelCause(context.Background())
			defer cancelBoundary(nil)
			g.arm(cancelBoundary)
			g.begin()
			g.observe(part)
			if g.interrupt(g.generation()) || boundaryCtx.Err() != nil {
				t.Fatal("interrupted tool/commit boundary")
			}

			// The call that follows owns its own cancellation: a steer stops it
			// and fences the output it may still emit.
			steeredCtx, cancelSteered := context.WithCancelCause(context.Background())
			defer cancelSteered(nil)
			g.arm(cancelSteered)
			g.begin()
			if !g.interrupt(g.generation()) || !errors.Is(context.Cause(steeredCtx), errModelSteered) ||
				g.observe(&sdk.FinishStepPart{}) {
				t.Fatal("next model invocation failed to fence late output")
			}
			if boundaryCtx.Err() != nil {
				t.Fatal("steer cancelled an earlier model invocation")
			}

			// A pending-steer probe that started before the next call armed
			// carries the earlier generation and must not cancel that call.
			stale := g.generation()
			nextCtx, cancelNext := context.WithCancelCause(context.Background())
			defer cancelNext(nil)
			g.arm(cancelNext)
			g.begin()
			if g.interrupt(stale) || nextCtx.Err() != nil {
				t.Fatal("stale steer decision cancelled the following model invocation")
			}
		})
	}
}

func TestSteerPreservesToolsAndEarlierInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	wake := make(chan struct{}, 1)
	var calls, executions atomic.Int32
	var pending atomic.Bool
	var immediateInput atomic.Bool
	var finalInput []sdk.Message
	provider := agentStreamTestProvider(func(ctx context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
		call := calls.Add(1)
		if call <= 2 {
			if call == 2 {
				for _, message := range params.Messages {
					if messageContentText(message) == "change direction" {
						immediateInput.Store(true)
					}
				}
			}
			return closedAgentTestStream(
				&sdk.StreamToolCallPart{ToolCallID: fmt.Sprintf("call-%d", call), ToolName: "held_tool", Input: toolexec.ArgumentsFromValue(map[string]any{})},
				&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls},
			), nil
		}
		if call == 3 {
			parts := make(chan sdk.StreamPart, 1)
			parts <- &sdk.TextDeltaPart{Text: "after-tools"}
			go func() { <-ctx.Done(); close(parts) }()
			return parts, nil
		}
		finalInput = cloneProviderMessages(params.Messages)
		return closedAgentTestStream(&sdk.TextDeltaPart{Text: "done"}, &sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}), nil
	})
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
		Name: "held_tool", Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(ctx *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
			if executions.Add(1) > 1 {
				return toolexec.OutputFromValue("completed second tool result"), nil
			}
			started <- ctx
			select {
			case <-release:
				return toolexec.OutputFromValue("completed tool result"), nil
			case <-ctx.Done():
				return sdk.ToolOutput{}, ctx.Err()
			}
		},
	}}}})
	events := a.Stream(ctx, RunConfig{
		Model: &sdk.Model{ID: "mock", Provider: provider}, Messages: []sdk.Message{sdk.UserMessage("original")},
		SupportsToolCall: true, SteerWake: wake,
		PendingSteer: func(context.Context) (bool, error) { return pending.Load(), nil },
		OnSteer: func(_ context.Context, index int, _ *step.Record) (StepDirective, error) {
			if index != 2 {
				return StepDirective{}, errors.New("must not preempt a tool")
			}
			pending.Store(false)
			return StepDirective{NextInputs: []DirectiveInput{{ID: "steer", Text: "second change"}}}, nil
		},
		OnStepCommitted: func(_ context.Context, index int, _ *step.Record) (StepDirective, error) {
			if index == 0 {
				pending.Store(false)
				return StepDirective{NextInputs: []DirectiveInput{{ID: "commit", Text: "change direction"}}}, nil
			}
			return StepDirective{}, nil
		},
	})
	var releaseTimer <-chan time.Time
	var toolCtx context.Context
	for events != nil {
		select {
		case toolCtx = <-started:
			pending.Store(true)
			wake <- struct{}{}
			releaseTimer = time.After(25 * time.Millisecond)
		case <-releaseTimer:
			if toolCtx.Err() != nil {
				t.Fatal("steer cancelled the tool")
			}
			close(release)
			releaseTimer = nil
		case e, ok := <-events:
			switch {
			case !ok:
				events = nil
			case e.Type == EventError || e.Type == EventAgentAbort:
				t.Fatalf("tool continuation failed: %+v", e)
			case e.Type == EventTextDelta && e.Delta == "after-tools":
				pending.Store(true)
				wake <- struct{}{}
			}
		case <-ctx.Done():
			t.Fatal("tool continuation timed out")
		}
	}
	users, secondUsers, results := 0, 0, 0
	for _, message := range finalInput {
		if message.Role == sdk.MessageRoleUser && messageContentText(message) == "change direction" {
			users++
		}
		if message.Role == sdk.MessageRoleTool {
			results++
		}
		if message.Role == sdk.MessageRoleUser && messageContentText(message) == "second change" {
			secondUsers++
		}
	}
	if calls.Load() != 4 || executions.Load() != 2 || users != 1 || secondUsers != 1 || results != 2 || !immediateInput.Load() {
		t.Fatalf("calls=%d tools=%d steer inputs=%d,%d results=%d immediate=%v", calls.Load(), executions.Load(), users, secondUsers, results, immediateInput.Load())
	}
}

// A claimed steer is named before its row exists (SR-TURN-001), and the step
// that consumes it files its user row under that name by taking the last
// unnamed user row it carries. That only holds while the loop drains its
// boundary inputs in this order: read_media's image-only row and mid-turn
// platform injects first, the steer directive last. Reordering the drains in
// the engine would silently file the steer's turn onto somebody else's row, so
// pin the ordering here rather than in the persistence layer that relies on it.
func TestQueuedSteerIsAppendedAfterEveryOtherPreparedMessage(t *testing.T) {
	t.Parallel()

	imageBase64 := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n\x00payload"))
	wrapped, readMedia := decorateReadMediaTools(&sdk.Model{ID: "mock-model"}, []toolexec.Tool{{
		Name: agenttools.ReadMediaToolName().String(),
		Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue(agenttools.ReadMediaToolOutput{
				Public:         agenttools.ReadMediaToolResult{OK: true, Path: "/data/image.png", Mime: "image/png"},
				ImageBase64:    imageBase64,
				ImageMediaType: "image/png",
			}), nil
		},
	}})
	if readMedia == nil || len(wrapped) != 1 {
		t.Fatalf("decorateReadMediaTools did not wrap read tool: state=%v tools=%d", readMedia, len(wrapped))
	}
	if _, err := wrapped[0].Execute(&toolexec.ToolExecContext{
		Context:    context.Background(),
		ToolCallID: "call-1",
		ToolName:   agenttools.ReadMediaToolName().String(),
	}, toolexec.ArgumentsFromValue(map[string]any{"path": "/data/image.png"})); err != nil {
		t.Fatalf("wrapped read execute returned error: %v", err)
	}

	ledger := contextfrag.NewMutationLedger()
	dynamic := newLoopDynamicInputs(0)
	dynamic.beginBoundary(1)
	convo := []sdk.Message{sdk.UserMessage("original query")}
	before := len(convo)
	convo = drainReadMediaMessage(readMedia, dynamic, ledger, 1, convo)
	convo = appendDirectiveInputs(RunConfig{ContextMutations: ledger}, dynamic, convo, []DirectiveInput{{ID: "steer-1", Text: "steer text"}})

	appended := convo[before:]
	if len(appended) != 2 {
		t.Fatalf("boundary block = %d messages, want the read_media row and the steer", len(appended))
	}
	if appended[0].Role != sdk.MessageRoleUser || textOfMessage(appended[0]) != "" {
		t.Fatalf("appended[0] = %s %q, want read_media's image-only user row",
			appended[0].Role, textOfMessage(appended[0]))
	}
	last := appended[len(appended)-1]
	if last.Role != sdk.MessageRoleUser || textOfMessage(last) != "steer text" {
		t.Fatalf("last boundary message = %s %q, want the steer input",
			last.Role, textOfMessage(last))
	}
}
