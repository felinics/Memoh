package contextview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

type SDKRenderedPayload struct {
	System       string
	Messages     []sdk.Message
	Query        string
	InlineImages []sdk.ImagePart
}

type SDKMessagesRenderer struct{}

func (*SDKMessagesRenderer) Target() contextfrag.RenderTarget {
	return contextfrag.RenderSDKMessages
}

func (*SDKMessagesRenderer) Render(_ context.Context, input RenderInput) (RenderedPayload, error) {
	ordered, err := orderedSelectedFrags(input.Selected, input.Placement)
	if err != nil {
		return RenderedPayload{}, err
	}
	payload := renderSDKPayloadFromFrags(ordered)
	hash, err := sdkRenderedPayloadHash(payload)
	if err != nil {
		return RenderedPayload{}, err
	}
	return RenderedPayload{Target: contextfrag.RenderSDKMessages, ContentHash: hash, Data: payload}, nil
}

func renderSDKPayloadFromFrags(ordered []contextfrag.ContextFrag) *SDKRenderedPayload {
	payload := &SDKRenderedPayload{}
	var previousSystemRender contextfrag.RenderPolicy
	hasSystemFrag := false
	for _, frag := range ordered {
		switch frag.Slot {
		case contextfrag.SlotSystem:
			renderSystemFrag(payload, frag, previousSystemRender, hasSystemFrag)
			previousSystemRender = frag.Render
			hasSystemFrag = true
		case contextfrag.SlotCurrentUser:
			renderCurrentUserFrag(payload, frag)
		default:
			renderMessageFrag(payload, frag)
		}
	}
	materializeRenderedCurrent(payload)
	return payload
}

func renderSystemFrag(
	payload *SDKRenderedPayload,
	frag contextfrag.ContextFrag,
	previous contextfrag.RenderPolicy,
	hasPrevious bool,
) {
	if hasPrevious {
		payload.System += contextfrag.RenderSeparator(previous, frag.Render)
	}
	for _, part := range frag.Parts {
		if part.Type == contextfrag.PartText {
			payload.System += part.Text
		}
	}
}

func orderedSelectedFrags(selected []contextfrag.ContextFrag, placement PlacementPlan) ([]contextfrag.ContextFrag, error) {
	if len(placement.Items) == 0 {
		if len(selected) != 0 {
			return nil, fmt.Errorf("placement is empty for %d selected fragments", len(selected))
		}
		return nil, nil
	}
	if len(placement.Items) != len(selected) {
		return nil, fmt.Errorf("placement item count %d does not match selected fragment count %d", len(placement.Items), len(selected))
	}
	byID := make(map[string]contextfrag.ContextFrag, len(selected))
	for _, frag := range selected {
		if _, exists := byID[frag.ID]; exists {
			return nil, fmt.Errorf("selected fragments contain duplicate id %q", frag.ID)
		}
		byID[frag.ID] = frag
	}
	ordered := make([]contextfrag.ContextFrag, 0, len(selected))
	seen := make(map[string]bool, len(selected))
	for _, item := range placement.Items {
		if seen[item.FragID] {
			return nil, fmt.Errorf("placement contains duplicate fragment %q", item.FragID)
		}
		seen[item.FragID] = true
		frag, ok := byID[item.FragID]
		if !ok {
			return nil, fmt.Errorf("placement references unknown fragment %q", item.FragID)
		}
		ordered = append(ordered, frag)
	}
	return ordered, nil
}

func renderCurrentUserFrag(payload *SDKRenderedPayload, frag contextfrag.ContextFrag) {
	for _, part := range frag.Parts {
		switch part.Type {
		case contextfrag.PartText:
			payload.Query = part.Text
		case contextfrag.PartImage:
			if image := sdkImagePart(part); image != nil && strings.TrimSpace(image.Image) != "" {
				payload.InlineImages = append(payload.InlineImages, *image)
			}
		case contextfrag.PartSDKMessage:
			if msg := sdkMessagePart(part); msg != nil {
				payload.Messages = append(payload.Messages, cloneSDKMessage(*msg))
			}
		}
	}
}

func renderMessageFrag(payload *SDKRenderedPayload, frag contextfrag.ContextFrag) {
	for _, part := range frag.Parts {
		if part.Type == contextfrag.PartSDKMessage {
			if msg := sdkMessagePart(part); msg != nil {
				payload.Messages = append(payload.Messages, cloneSDKMessage(*msg))
			}
		}
	}
}

func materializeRenderedCurrent(payload *SDKRenderedPayload) {
	images := make([]sdk.MessagePart, 0, len(payload.InlineImages))
	for _, image := range payload.InlineImages {
		if strings.TrimSpace(image.Image) != "" {
			images = append(images, image)
		}
	}
	if payload.Query != "" {
		payload.Messages = append(payload.Messages, sdk.UserMessage(payload.Query, images...))
		payload.Query = ""
		payload.InlineImages = nil
		return
	}
	if len(images) == 0 {
		return
	}
	for i := len(payload.Messages) - 1; i >= 0; i-- {
		if payload.Messages[i].Role == sdk.MessageRoleUser {
			payload.Messages[i].Content = append(payload.Messages[i].Content, images...)
			payload.InlineImages = nil
			return
		}
	}
	payload.Messages = append(payload.Messages, sdk.UserMessage("", images...))
	payload.InlineImages = nil
}

func sdkMessagePart(part contextfrag.Part) *sdk.Message {
	if part.SDKMessage != nil {
		return part.SDKMessage
	}
	return part.Message
}

func sdkImagePart(part contextfrag.Part) *sdk.ImagePart {
	if part.SDKImage != nil {
		return part.SDKImage
	}
	return part.ImagePart
}

func cloneSDKMessage(msg sdk.Message) sdk.Message {
	out := msg
	out.Content = append([]sdk.MessagePart(nil), msg.Content...)
	return out
}

func sdkRenderedPayloadHash(payload *SDKRenderedPayload) (string, error) {
	raw, err := json.Marshal(struct {
		System       string          `json:"system"`
		Messages     []sdk.Message   `json:"messages"`
		Query        string          `json:"query"`
		InlineImages []sdk.ImagePart `json:"inline_images"`
	}{payload.System, payload.Messages, payload.Query, payload.InlineImages})
	if err != nil {
		return "", fmt.Errorf("marshal sdk rendered payload: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func sortSystemFragsByPriority(ordered []contextfrag.ContextFrag) []contextfrag.ContextFrag {
	systemFrags := make([]contextfrag.ContextFrag, 0, 4)
	nonSystemFrags := make([]contextfrag.ContextFrag, 0, len(ordered))
	for _, frag := range ordered {
		if frag.Slot == contextfrag.SlotSystem {
			systemFrags = append(systemFrags, frag)
		} else {
			nonSystemFrags = append(nonSystemFrags, frag)
		}
	}
	if len(systemFrags) == 0 {
		return ordered
	}
	sort.SliceStable(systemFrags, func(i, j int) bool {
		return systemFrags[i].Priority < systemFrags[j].Priority
	})
	out := make([]contextfrag.ContextFrag, 0, len(ordered))
	out = append(out, systemFrags...)
	out = append(out, nonSystemFrags...)
	return out
}
