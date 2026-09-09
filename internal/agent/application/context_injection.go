package application

import (
	"encoding/json"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// interleaveInjectedMessages inserts injected user messages at their correct
// positions within the round. Each record's InsertAfter value indicates how
// many output messages preceded the injection.
//
// round layout: [user_A, output_0, output_1, ..., output_N]
// InsertAfter=K → insert after round[K] (i.e. after the K-th output message).
func interleaveInjectedMessages(round []ModelMessage, injections []InjectedMessageRecord) []ModelMessage {
	if len(injections) == 0 {
		return round
	}
	result := make([]ModelMessage, 0, len(round)+len(injections))
	injIdx := 0
	for i, msg := range round {
		result = append(result, msg)
		for injIdx < len(injections) && injections[injIdx].InsertAfter == i {
			result = append(result, steeringMessage(injections[injIdx].HeaderifiedText))
			injIdx++
		}
	}
	for ; injIdx < len(injections); injIdx++ {
		result = append(result, steeringMessage(injections[injIdx].HeaderifiedText))
	}
	return result
}

func steeringMessage(text string) ModelMessage {
	stamped := native.StampContextInjection(sdk.UserMessage(text), event.ContextInjectionSteering)
	if converted := sdkMessagesToModelMessages([]sdk.Message{stamped}); len(converted) == 1 {
		return converted[0]
	}
	return ModelMessage{Role: "user", Content: newTextContent(text)}
}

// contextInjectionKindOf reads the stamp a runtime left on an injected user
// message, so the row keeps its label however the round is filtered.
func contextInjectionKindOf(message ModelMessage) string {
	if !strings.EqualFold(strings.TrimSpace(message.Role), "user") || len(message.Content) == 0 {
		return ""
	}
	var parts []struct {
		ProviderMetadata map[string]any `json:"providerMetadata"`
	}
	if err := json.Unmarshal(message.Content, &parts); err != nil {
		return ""
	}
	for _, part := range parts {
		if kind := event.ContextInjectionKind(part.ProviderMetadata); kind != "" {
			return kind
		}
	}
	return ""
}

func contextInjectionMetadata(kind string) map[string]any {
	return map[string]any{messagepkg.ContextInjectionMetadataKey: messagepkg.ContextInjectionMetadata{Kind: kind}}
}
