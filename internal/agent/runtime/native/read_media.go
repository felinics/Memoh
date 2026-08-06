package native

import (
	"fmt"
	"strings"
	"sync"

	sdk "github.com/memohai/twilight-ai/sdk"

	agenttools "github.com/memohai/memoh/internal/agent/tool"
	"github.com/memohai/memoh/internal/models"
)

func decorateReadMediaTools(model *sdk.Model, tools []sdk.Tool) ([]sdk.Tool, *readMediaDecorationState) {
	if len(tools) == 0 {
		return tools, nil
	}

	clientType := models.ResolveClientType(model)
	state := &readMediaDecorationState{
		pendingMedia: make(map[string]sdk.MessagePart),
	}
	wrapped := make([]sdk.Tool, 0, len(tools))
	found := false

	for _, tool := range tools {
		if tool.Name != agenttools.ReadMediaToolName().String() || tool.Execute == nil {
			wrapped = append(wrapped, tool)
			continue
		}

		found = true
		originalExecute := tool.Execute
		toolCopy := tool
		toolCopy.Execute = func(ctx *sdk.ToolExecContext, input any) (any, error) {
			output, err := originalExecute(ctx, input)
			if err != nil {
				return output, err
			}

			publicResult, media, ok := normalizeReadMediaOutput(output, clientType)
			if !ok {
				return output, nil
			}
			if ctx != nil && strings.TrimSpace(ctx.ToolCallID) != "" && mediaPartHasContent(media) {
				state.mu.Lock()
				if _, exists := state.pendingMedia[ctx.ToolCallID]; !exists {
					state.pendingOrder = append(state.pendingOrder, ctx.ToolCallID)
				}
				state.pendingMedia[ctx.ToolCallID] = media
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
	mu           sync.Mutex
	pendingOrder []string
	pendingMedia map[string]sdk.MessagePart
	prepareCalls int
	injections   []readMediaInjection
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
		media, ok := s.pendingMedia[toolCallID]
		delete(s.pendingMedia, toolCallID)
		if !ok || !mediaPartHasContent(media) {
			continue
		}
		parts = append(parts, media)
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

func normalizeReadMediaOutput(output any, clientType string) (any, sdk.MessagePart, bool) {
	switch value := output.(type) {
	case agenttools.ReadMediaToolOutput:
		return value.Public, buildReadMediaPart(clientType, value), true
	case *agenttools.ReadMediaToolOutput:
		if value == nil {
			return nil, nil, false
		}
		return value.Public, buildReadMediaPart(clientType, *value), true
	default:
		return nil, nil, false
	}
}

// buildReadMediaPart converts a read-media tool output into the message part
// injected on the next step: documents become FilePart (bare base64 by the
// twilight convention — provider framing is the adapters' job), images keep
// the legacy per-client data URL shaping.
func buildReadMediaPart(clientType string, value agenttools.ReadMediaToolOutput) sdk.MessagePart {
	if strings.TrimSpace(value.FileBase64) != "" {
		return sdk.FilePart{
			Data:      strings.TrimSpace(value.FileBase64),
			MediaType: strings.TrimSpace(value.FileMediaType),
			Filename:  strings.TrimSpace(value.Filename),
		}
	}
	return buildReadMediaImagePart(clientType, value.ImageBase64, value.ImageMediaType)
}

// mediaPartHasContent reports whether an injected media part carries a payload.
func mediaPartHasContent(part sdk.MessagePart) bool {
	switch p := part.(type) {
	case sdk.ImagePart:
		return strings.TrimSpace(p.Image) != ""
	case sdk.FilePart:
		return strings.TrimSpace(p.Data) != ""
	default:
		return false
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
	if clientType != string(models.ClientTypeAnthropicMessages) {
		image = fmt.Sprintf("data:%s;base64,%s", mediaType, imageBase64)
	}
	return sdk.ImagePart{
		Image:     image,
		MediaType: mediaType,
	}
}
