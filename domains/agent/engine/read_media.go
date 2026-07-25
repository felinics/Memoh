package engine

import (
	"fmt"
	"strings"
	"sync"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/domains/agent/tool"
	modeldomain "github.com/memohai/memoh/domains/model"
	modelexecution "github.com/memohai/memoh/domains/model/execution"
)

func decorateReadMediaTools(model *sdk.Model, tools []sdk.Tool) ([]sdk.Tool, *readMediaDecorationState) {
	if len(tools) == 0 {
		return tools, nil
	}

	clientType := modelexecution.ResolveClientType(model)
	state := &readMediaDecorationState{
		pendingImages: make(map[string]sdk.ImagePart),
	}
	wrapped := make([]sdk.Tool, 0, len(tools))
	found := false

	for _, toolProvider := range tools {
		if toolProvider.Name != tool.ReadMediaToolName().String() || toolProvider.Execute == nil {
			wrapped = append(wrapped, toolProvider)
			continue
		}

		found = true
		originalExecute := toolProvider.Execute
		toolCopy := toolProvider
		toolCopy.Execute = func(ctx *sdk.ToolExecContext, input any) (any, error) {
			output, err := originalExecute(ctx, input)
			if err != nil {
				return output, err
			}

			publicResult, image, ok := normalizeReadMediaOutput(output, clientType)
			if !ok {
				return output, nil
			}
			if ctx != nil && strings.TrimSpace(ctx.ToolCallID) != "" && strings.TrimSpace(image.Image) != "" {
				state.mu.Lock()
				if _, exists := state.pendingImages[ctx.ToolCallID]; !exists {
					state.pendingOrder = append(state.pendingOrder, ctx.ToolCallID)
				}
				state.pendingImages[ctx.ToolCallID] = image
				state.mu.Unlock()
			}
			return publicResult, nil
		}
		wrapped = append(wrapped, toolCopy)
	}

	if !found {
		return tools, nil
	}

	return wrapped, state
}

type readMediaDecorationState struct {
	mu            sync.Mutex
	pendingOrder  []string
	pendingImages map[string]sdk.ImagePart
	prepareCalls  int
	injections    []readMediaInjection
}

type readMediaInjection struct {
	afterStep int
	message   sdk.Message
}

func (s *readMediaDecorationState) prepareStep(params *sdk.GenerateParams) *sdk.GenerateParams {
	if s == nil || params == nil {
		return nil
	}

	afterStep := s.prepareCalls
	s.prepareCalls++

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pendingOrder) == 0 {
		return nil
	}

	parts := make([]sdk.MessagePart, 0, len(s.pendingOrder))
	for _, toolCallID := range s.pendingOrder {
		image, ok := s.pendingImages[toolCallID]
		delete(s.pendingImages, toolCallID)
		if !ok || strings.TrimSpace(image.Image) == "" {
			continue
		}
		parts = append(parts, image)
	}
	s.pendingOrder = s.pendingOrder[:0]

	if len(parts) == 0 {
		return nil
	}

	message := sdk.Message{
		Role:    sdk.MessageRoleUser,
		Content: parts,
	}
	s.injections = append(s.injections, readMediaInjection{
		afterStep: afterStep,
		message:   message,
	})

	next := *params
	next.Messages = append(append([]sdk.Message(nil), params.Messages...), message)
	return &next
}

func (s *readMediaDecorationState) mergeMessages(steps []sdk.StepResult, fallback []sdk.Message) []sdk.Message {
	if s == nil || len(s.injections) == 0 {
		return fallback
	}
	if len(steps) == 0 {
		merged := append([]sdk.Message(nil), fallback...)
		for _, injection := range s.injections {
			merged = append(merged, injection.message)
		}
		return merged
	}

	merged := make([]sdk.Message, 0, len(fallback)+len(s.injections))
	injectionIndex := 0
	for stepIndex, step := range steps {
		merged = append(merged, step.Messages...)
		for injectionIndex < len(s.injections) && s.injections[injectionIndex].afterStep == stepIndex {
			merged = append(merged, s.injections[injectionIndex].message)
			injectionIndex++
		}
	}
	for injectionIndex < len(s.injections) {
		merged = append(merged, s.injections[injectionIndex].message)
		injectionIndex++
	}
	return merged
}

func normalizeReadMediaOutput(output any, clientType string) (any, sdk.ImagePart, bool) {
	switch value := output.(type) {
	case tool.ReadMediaToolOutput:
		return value.Public, buildReadMediaImagePart(clientType, value.ImageBase64, value.ImageMediaType), true
	case *tool.ReadMediaToolOutput:
		if value == nil {
			return nil, sdk.ImagePart{}, false
		}
		return value.Public, buildReadMediaImagePart(clientType, value.ImageBase64, value.ImageMediaType), true
	default:
		return nil, sdk.ImagePart{}, false
	}
}

func publicReadMediaToolResult(output any) any {
	publicResult, _, ok := normalizeReadMediaOutput(output, "")
	if !ok {
		return output
	}
	return publicResult
}

func buildReadMediaImagePart(clientType, imageBase64, mediaType string) sdk.ImagePart {
	imageBase64 = strings.TrimSpace(imageBase64)
	mediaType = strings.TrimSpace(mediaType)
	if imageBase64 == "" {
		return sdk.ImagePart{}
	}
	if mediaType == "" {
		mediaType = "image/png"
	}

	image := imageBase64
	if clientType != string(modeldomain.ClientTypeAnthropicMessages) {
		image = fmt.Sprintf("data:%s;base64,%s", mediaType, imageBase64)
	}
	return sdk.ImagePart{
		Image:     image,
		MediaType: mediaType,
	}
}
