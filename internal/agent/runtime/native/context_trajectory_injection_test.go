package native

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/context/trajectory"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

func capturedStageBody(sink *nativeTrajectorySink, event trajectory.Event) string {
	var body strings.Builder
	for _, block := range event.Blocks {
		for _, hash := range block.Chunks {
			body.WriteString(sink.contents[hash])
		}
	}
	return body.String()
}

func TestTrajectoryTracksConcurrentImageAndFileInjection(t *testing.T) {
	sink := &nativeTrajectorySink{}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetTrajectoryRecorder(trajectory.NewRecorder(sink))
	agent := New(Deps{})
	image := base64.StdEncoding.EncodeToString([]byte("ORIGINAL_IMAGE_BYTES"))
	file := base64.StdEncoding.EncodeToString([]byte("ORIGINAL_PDF_BYTES"))
	agent.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name: agenttools.ReadMediaToolName().String(), Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(ctx *sdk.ToolExecContext, _ any) (any, error) {
			output := agenttools.ReadMediaToolOutput{Public: agenttools.ReadMediaToolResult{OK: true, Path: ctx.ToolCallID}}
			if ctx.ToolCallID == "image-call" {
				output.ImageBase64, output.ImageMediaType = image, "image/png"
			} else {
				output.FileBase64, output.FileMediaType, output.Filename = file, "application/pdf", "source.pdf"
			}
			return output, nil
		},
	}}}})
	provider := &atomicMockProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call == 1 {
			return &sdk.GenerateResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{
				{ToolCallID: "image-call", ToolName: agenttools.ReadMediaToolName().String(), Input: map[string]any{}},
				{ToolCallID: "file-call", ToolName: agenttools.ReadMediaToolName().String(), Input: map[string]any{}},
			}}, nil
		}
		return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
	}}
	_, err := agent.Generate(t.Context(), RunConfig{
		RunID: "run", ContextLifecycle: holder, SupportsToolCall: true, SupportsImageInput: true, SupportsFileInput: true,
		Identity: SessionContext{BotID: "bot", SessionID: "session"},
		Model:    &sdk.Model{ID: "fixture", Provider: provider}, Messages: []sdk.Message{sdk.UserMessage("inspect media")},
	})
	if err != nil {
		t.Fatal(err)
	}
	stages := make(map[string]int)
	for _, event := range sink.events {
		stages[event.Stage]++
		if event.Stage == "read_media_injected" {
			body := capturedStageBody(sink, event)
			for _, source := range []string{"image-call", "file-call", image, file, "message_index"} {
				if !strings.Contains(body, source) {
					t.Errorf("injected media lost source %q", source)
				}
			}
			if event.StepIndex == nil || *event.StepIndex != 1 {
				t.Errorf("media injection step = %#v", event.StepIndex)
			}
		}
	}
	if stages["read_media_loaded"] != 2 || stages["read_media_normalized"] != 2 || stages["read_media_injected"] != 1 {
		t.Fatalf("media source stages = %v", stages)
	}
}

func TestTrajectoryTracksSteeringOrderAndImageFiltering(t *testing.T) {
	sink := &nativeTrajectorySink{}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetTrajectoryRecorder(trajectory.NewRecorder(sink))
	inject := make(chan InjectMessage, 2)
	inject <- InjectMessage{Text: "FIRST_STEER", ImageParts: []sdk.ImagePart{{Image: "UNSUPPORTED_IMAGE"}}}
	inject <- InjectMessage{Text: "SECOND_STEER"}
	close(inject)
	agent := New(Deps{})
	agent.SetToolProviders(mockToolLoopTools())
	provider := &atomicMockProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call == 1 {
			return &sdk.GenerateResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{ToolCallID: "steer-call", ToolName: "lookup", Input: map[string]any{}}}}, nil
		}
		return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
	}}
	for range agent.Stream(t.Context(), RunConfig{
		RunID: "run", ContextLifecycle: holder, SupportsToolCall: true, InjectCh: inject,
		Identity: SessionContext{BotID: "bot", SessionID: "session"},
		Model:    &sdk.Model{ID: "fixture", Provider: provider}, Messages: []sdk.Message{sdk.UserMessage("task")},
	}) {
	}
	var bodies []string
	for _, event := range sink.events {
		if event.Stage == "steering_applied" {
			bodies = append(bodies, capturedStageBody(sink, event))
		}
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], "FIRST_STEER") || !strings.Contains(bodies[0], "UNSUPPORTED_IMAGE") || !strings.Contains(bodies[1], "SECOND_STEER") {
		t.Fatalf("steering source order lost: %#v", bodies)
	}
	inputs := sink.providerInputs()
	if len(inputs) != 2 || strings.Contains(capturedStageBody(sink, inputs[1]), "UNSUPPORTED_IMAGE") {
		t.Fatal("unsupported steering image was represented as sent")
	}
	var source struct {
		MessageIndex int `json:"message_index"`
	}
	for i, event := range sink.events {
		if event.Stage == "steering_applied" {
			block := event.Blocks[len(event.Blocks)-1]
			if err := json.Unmarshal([]byte(sink.contents[block.Chunks[0]]), &source); err != nil || source.MessageIndex == 0 {
				t.Fatalf("steering event %d omitted target message: %v", i, err)
			}
		}
	}
}
