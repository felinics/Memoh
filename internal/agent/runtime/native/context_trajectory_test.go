package native

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/context/trajectory"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

type nativeTrajectorySink struct {
	mu       sync.Mutex
	events   []trajectory.Event
	contents map[string]string
}

func (s *nativeTrajectorySink) Append(_ context.Context, event trajectory.Event, contents []trajectory.Content) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contents == nil {
		s.contents = make(map[string]string)
	}
	for _, content := range contents {
		s.contents[content.Hash] = string(content.Data)
	}
	s.events = append(s.events, event)
	return nil
}

func (s *nativeTrajectorySink) providerInputs() []trajectory.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var events []trajectory.Event
	for _, event := range s.events {
		if event.Stage == "provider_request" {
			events = append(events, event)
		}
	}
	return events
}

func TestAgentTrajectoryRecordsToolOnlyRequestAndNextInput(t *testing.T) {
	sink := &nativeTrajectorySink{}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetTrajectoryRecorder(trajectory.NewRecorder(sink))
	calls := 0
	provider := agentStreamTestProvider(func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
		calls++
		inputs := sink.providerInputs()
		if len(inputs) != calls {
			t.Errorf("provider call %d has %d captured inputs", calls, len(inputs))
		}
		if calls == 1 {
			return closedAgentTestStream(&sdk.StartStepPart{},
				&sdk.StreamToolCallPart{ToolCallID: "call", ToolName: "echo", Input: map[string]any{}},
				&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls}), nil
		}
		return closedAgentTestStream(&sdk.StartStepPart{}, &sdk.TextDeltaPart{ID: "answer", Text: "done"},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}), nil
	})
	agent := New(Deps{})
	agent.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name: "echo", Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*sdk.ToolExecContext, any) (any, error) { return "UNIQUE_TOOL_OUTPUT", nil },
	}}}})
	for event := range agent.Stream(t.Context(), RunConfig{
		RunID: "run", ContextLifecycle: holder, SupportsToolCall: true,
		Identity: SessionContext{BotID: "bot", SessionID: "session"},
		Model:    &sdk.Model{ID: "fixture", Provider: provider}, System: "ORIGINAL_SYSTEM",
		Messages: []sdk.Message{sdk.UserMessage("ORIGINAL_USER")},
	}) {
		if event.Type == EventError {
			t.Fatalf("stream error: %s", event.Error)
		}
	}
	inputs := sink.providerInputs()
	if len(inputs) != 2 {
		t.Fatalf("captured requests = %d", len(inputs))
	}
	for i, input := range inputs {
		if input.StepIndex == nil || *input.StepIndex != i {
			t.Fatalf("request %d = %#v", i, input)
		}
		var texts strings.Builder
		for _, block := range input.Blocks {
			for _, hash := range block.Chunks {
				texts.WriteString(sink.contents[hash])
			}
		}
		if !strings.Contains(texts.String(), "ORIGINAL_USER") || (i == 1 && !strings.Contains(texts.String(), "UNIQUE_TOOL_OUTPUT")) {
			t.Fatalf("request %d lost its user input or tool result", i)
		}
	}
}

func TestAgentGenerateTrajectoryKeepsRejectedProviderInput(t *testing.T) {
	sink := &nativeTrajectorySink{}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetTrajectoryRecorder(trajectory.NewRecorder(sink))
	provider := &atomicMockProvider{handler: func(int, sdk.GenerateParams) (*sdk.GenerateResult, error) {
		return nil, errors.New("fixture provider rejected input")
	}}
	_, err := New(Deps{}).Generate(t.Context(), RunConfig{
		RunID: "run", ContextLifecycle: holder,
		Identity: SessionContext{BotID: "bot", SessionID: "session"},
		Model:    &sdk.Model{ID: "fixture", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("REJECTED_INPUT")},
	})
	if err == nil || len(sink.providerInputs()) == 0 {
		t.Fatal("failed non-streaming request disappeared from trajectory")
	}
	encoded, err := json.Marshal(sink.providerInputs())
	if err != nil || len(encoded) == 0 {
		t.Fatal("request metadata is not serializable")
	}
}

func TestSpawnTrajectoryStartsWithChildTaskAndOwnRequestIdentity(t *testing.T) {
	sink := &nativeTrajectorySink{}
	adapter := NewSpawnAdapter(New(Deps{}))
	adapter.SetLifecycleHolderFactory(func(context.Context, string) *contextfrag.LifecycleHolder {
		holder := contextfrag.NewLifecycleHolder()
		holder.SetTrajectoryRecorder(trajectory.NewRecorder(sink))
		return holder
	})
	provider := &atomicMockProvider{handler: func(int, sdk.GenerateParams) (*sdk.GenerateResult, error) {
		return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
	}}
	_, err := adapter.Generate(trajectory.WithRequest(t.Context(), 41), agenttools.SpawnRunConfig{
		Model: &sdk.Model{ID: "fixture", Provider: provider}, Query: "CHILD_TASK",
		Identity: agenttools.SpawnIdentity{BotID: "bot", SessionID: "child-session"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.events) == 0 || sink.events[0].Stage != "spawn_trigger" || sink.events[0].Request != 0 {
		t.Fatal("child trace omitted its trigger or inherited a parent request")
	}
	if len(sink.providerInputs()) != 1 {
		t.Fatal("child provider request was not captured")
	}
}

type delayedNativeTrajectorySink struct {
	nativeTrajectorySink
	entered chan struct{}
	release chan struct{}
}

func (s *delayedNativeTrajectorySink) Append(ctx context.Context, event trajectory.Event, contents []trajectory.Content) error {
	if event.Stage == "wire_result" {
		close(s.entered)
		<-s.release
	}
	return s.nativeTrajectorySink.Append(ctx, event, contents)
}

func TestTrajectoryFlushesBeforeTerminalObservation(t *testing.T) {
	sink := &delayedNativeTrajectorySink{entered: make(chan struct{}), release: make(chan struct{})}
	recorder := trajectory.NewRecorder(sink)
	holder := contextfrag.NewLifecycleHolder()
	holder.SetTrajectoryRecorder(recorder)
	provider := agentStreamTestProvider(func(ctx context.Context, _ sdk.GenerateParams) (*sdk.StreamResult, error) {
		recorder.RecordAsync(ctx, "wire_result", nil)
		return closedAgentTestStream(&sdk.StartStepPart{}, &sdk.TextDeltaPart{ID: "answer", Text: "done"},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}), nil
	})
	go func() {
		<-sink.entered
		time.Sleep(40 * time.Millisecond)
		close(sink.release)
	}()
	for range New(Deps{}).Stream(t.Context(), RunConfig{
		RunID: "run", ContextLifecycle: holder,
		Identity: SessionContext{BotID: "bot", SessionID: "session"},
		Model:    &sdk.Model{ID: "fixture", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("input")},
		OnAgentEventObserved: func(event StreamEvent) {
			if event.Type == EventAgentEnd && recorder.Stats().Pending != 0 {
				t.Error("terminal observer can release ownership before wire capture is stored")
			}
		},
	}) {
	}
}
