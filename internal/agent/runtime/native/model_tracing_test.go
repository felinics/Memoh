package native

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

// spanAttr reports one attribute of a recorded span.
func spanAttr(span sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func spansNamed(recorder *tracetest.SpanRecorder, name string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == name {
			out = append(out, span)
		}
	}
	return out
}

// noopToolAgent returns an agent offering one tool that does nothing, which is
// what makes the SDK continue the loop: a tool call naming a tool it cannot
// execute ends the run instead of starting another round.
func noopToolAgent() *Agent {
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
		Name: "noop",
		Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue("ok"), nil
		},
	}}}})
	return a
}

// A turn is a loop, and the whole point of tracing it is to say where its time
// went. The SDK runs every round inside one call, so a span opened around that
// call can only end on one round: a turn that called two tools showed one
// model span and two unexplained gaps of the same size.
func TestEveryModelRoundGetsItsOwnSpan(t *testing.T) {
	recorder := recordToolSpans(t)

	round := 0
	provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
		round++
		if round <= 2 {
			return closedAgentTestStream(
				&sdk.StartStepPart{},
				&sdk.StreamToolCallPart{ToolCallID: "call-1", ToolName: "noop", Input: toolexec.ArgumentsFromValue(map[string]any{})},
				&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls},
			), nil
		}
		return closedAgentTestStream(
			&sdk.StartStepPart{},
			&sdk.TextDeltaPart{ID: "text-1", Text: "done"},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop},
		), nil
	})

	events := noopToolAgent().Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		SupportsToolCall: true,
	})
	for range events {
	}

	spans := spansNamed(recorder, spanModelStream)
	if len(spans) != 3 {
		t.Fatalf("%s spans = %d, want 3 (one per provider call); got %v",
			spanModelStream, len(spans), spanNames(recorder))
	}
	// The index is what lets a reader tell the rounds apart in a waterfall
	// where all three spans carry the same name and model.
	var indexes []int64
	for _, span := range spans {
		value, ok := spanAttr(span, "agent.model.call_index")
		if !ok {
			t.Fatalf("span has no agent.model.call_index; attrs = %v", span.Attributes())
		}
		indexes = append(indexes, value.AsInt64())
	}
	slices.Sort(indexes)
	if !slices.Equal(indexes, []int64{0, 1, 2}) {
		t.Errorf("call indexes = %v, want 0,1,2", indexes)
	}
}

// Tool spans must stay siblings of the model spans, not children of one.
// The SDK keeps the context it was called with for the whole run, so a model
// span whose context reaches the SDK adopts every tool call the run makes —
// and a waterfall then reads as if the model were still generating while the
// workspace was running a command.
func TestModelSpansDoNotAdoptTheToolSpans(t *testing.T) {
	recorder := recordToolSpans(t)

	round := 0
	provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
		round++
		if round == 1 {
			return closedAgentTestStream(
				&sdk.StartStepPart{},
				&sdk.StreamToolCallPart{ToolCallID: "call-1", ToolName: "noop", Input: toolexec.ArgumentsFromValue(map[string]any{})},
				&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls},
			), nil
		}
		return closedAgentTestStream(
			&sdk.StartStepPart{},
			&sdk.TextDeltaPart{ID: "text-1", Text: "done"},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop},
		), nil
	})

	events := noopToolAgent().Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		SupportsToolCall: true,
	})
	for range events {
	}

	modelSpanIDs := map[trace.SpanID]struct{}{}
	for _, span := range spansNamed(recorder, spanModelStream) {
		modelSpanIDs[span.SpanContext().SpanID()] = struct{}{}
	}
	if len(modelSpanIDs) == 0 {
		t.Fatalf("no %s spans; got %v", spanModelStream, spanNames(recorder))
	}
	tools := spansNamed(recorder, "agent.tool noop")
	if len(tools) == 0 {
		t.Fatalf("no tool span; got %v", spanNames(recorder))
	}
	for _, span := range tools {
		if _, nested := modelSpanIDs[span.Parent().SpanID()]; nested {
			t.Errorf("tool span %s is a child of a model span", span.Name())
		}
	}
}

// How long the provider took to say anything is the pause a user sits
// through, and it is not the duration of the call: an answer that streams tool
// arguments for twenty seconds spends almost none of that waiting. Both
// numbers have to survive, so the wait rides on the call's span rather than
// replacing it.
func TestModelSpanReportsTheWaitBeforeTheFirstPart(t *testing.T) {
	recorder := recordToolSpans(t)

	const wait = 40 * time.Millisecond
	provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
		ch := make(chan sdk.StreamPart)
		go func() {
			defer close(ch)
			ch <- &sdk.StartStepPart{}
			time.Sleep(wait)
			ch <- &sdk.TextDeltaPart{ID: "text-1", Text: "done"}
			ch <- &sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}
		}()
		return ch, nil
	})

	events := New(Deps{}).Stream(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("task")},
		Identity: SessionContext{BotID: "bot-1"},
	})
	for range events {
	}

	spans := spansNamed(recorder, spanModelStream)
	if len(spans) != 1 {
		t.Fatalf("%s spans = %d, want 1; got %v", spanModelStream, len(spans), spanNames(recorder))
	}
	value, ok := spanAttr(spans[0], "agent.model.first_part_ms")
	if !ok {
		t.Fatalf("span has no agent.model.first_part_ms; attrs = %v", spans[0].Attributes())
	}
	// The StartStepPart arrives immediately and must not count: the SDK emits
	// it as soon as it has a goroutine, so timing to it would measure our own
	// plumbing rather than the provider.
	if got := value.AsInt64(); got < wait.Milliseconds() {
		t.Errorf("first_part_ms = %d, want at least %d", got, wait.Milliseconds())
	}
}

// A stream that never produces a part still has to end its span. A span that
// never ends never leaves the process, so the round would vanish from the
// trace in exactly the case an operator is looking into.
func TestModelSpanEndsWhenTheStreamProducesNothing(t *testing.T) {
	recorder := recordToolSpans(t)

	provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
		return closedAgentTestStream(), nil
	})

	events := New(Deps{}).Stream(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("task")},
		Identity: SessionContext{BotID: "bot-1"},
	})
	for range events {
	}

	if spans := spansNamed(recorder, spanModelStream); len(spans) != 1 {
		t.Fatalf("%s spans = %d, want 1; got %v", spanModelStream, len(spans), spanNames(recorder))
	}
}

// A call the provider refuses outright produces no stream to observe, so the
// span has to be closed on the way out rather than in the goroutine that never
// starts.
func TestModelSpanRecordsAProviderThatRefusesTheCall(t *testing.T) {
	recorder := recordToolSpans(t)

	refused := errors.New("provider refused the call")
	provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
		return nil, refused
	})

	events := New(Deps{}).Stream(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("task")},
		Identity: SessionContext{BotID: "bot-1"},
	})
	for range events {
	}

	spans := spansNamed(recorder, spanModelStream)
	if len(spans) == 0 {
		t.Fatalf("no %s span; got %v", spanModelStream, spanNames(recorder))
	}
	span := spans[0]
	if span.Status().Code != codes.Error {
		t.Errorf("status = %v, want error", span.Status().Code)
	}
	if value, ok := spanAttr(span, "agent.model.outcome"); !ok || value.AsString() != "errored" {
		t.Errorf("outcome = %v, want errored", value.AsString())
	}
}

// The non-streaming path runs the same loop and needs the same per-round
// timing. It used to have a span around the whole call, which made every tool
// the run executed a child of one model span.
func TestGenerateGetsAModelSpanPerRound(t *testing.T) {
	recorder := recordToolSpans(t)

	provider := &recordingPromptCacheProvider{}
	result, err := noopToolAgent().Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		SupportsToolCall: true,
	})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if result == nil {
		t.Fatal("Generate() returned no result")
	}

	spans := spansNamed(recorder, spanModelGenerate)
	if len(spans) != provider.calls {
		t.Fatalf("%s spans = %d, want one per provider call (%d); got %v",
			spanModelGenerate, len(spans), provider.calls, spanNames(recorder))
	}
	for _, span := range spans {
		if span.SpanKind() != trace.SpanKindClient {
			t.Errorf("span kind = %v, want client", span.SpanKind())
		}
	}
}

// The conversation must not reach the trace backend. A model span sits right
// next to the prompt and the reply, and recording either would publish the
// user's messages to whoever can read traces — the rule docs/logging.md
// applies to log records.
func TestModelSpansRecordNothingFromTheConversation(t *testing.T) {
	recorder := recordToolSpans(t)

	const secret = "synthetic-conversation-secret"
	provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
		return closedAgentTestStream(
			&sdk.StartStepPart{},
			&sdk.TextDeltaPart{ID: "text-1", Text: secret},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop},
		), nil
	})

	events := New(Deps{}).Stream(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage(secret)},
		System:   secret,
		Identity: SessionContext{BotID: "bot-1"},
	})
	for range events {
	}

	for _, span := range spansNamed(recorder, spanModelStream) {
		for _, kv := range span.Attributes() {
			if kv.Value.Emit() == secret {
				t.Errorf("attribute %s carries the conversation: %s", kv.Key, kv.Value.Emit())
			}
		}
	}
}
