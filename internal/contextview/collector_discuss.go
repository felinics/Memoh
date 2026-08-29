package contextview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/chat/timeline"
)

const (
	discussContextCollectorName = "discuss_context"
	discussContextSource        = "pipeline_discuss"
)

type DiscussContextConfig struct {
	// ComposedMessages is the authoritative output of timeline composition
	// when non-nil.
	ComposedMessages []timeline.ContextMessage
	// InlineImages are freshly surfaced attachments delivered as native vision
	// input on the latest user message.
	InlineImages []sdk.ImagePart
}

type DiscussContextCollector struct{}

func (*DiscussContextCollector) Name() string {
	return discussContextCollectorName
}

func (*DiscussContextCollector) Collect(_ context.Context, req CollectRequest) ([]contextfrag.ContextFrag, error) {
	cfg, err := discussContextConfig(req.Config)
	if err != nil {
		return nil, err
	}
	if cfg.ComposedMessages == nil {
		return nil, nil
	}

	frags := make([]contextfrag.ContextFrag, 0, len(cfg.ComposedMessages))
	currentUserIndex := latestComposedUserMessageIndex(cfg.ComposedMessages)
	for i, message := range cfg.ComposedMessages {
		frags = append(frags, discussComposedMessageFrag(message, i, i == currentUserIndex, req.Scope))
	}
	if req.Intent == contextfrag.IntentRunConfigPreProvider {
		frags = contextfrag.RepairToolClosureFrags(frags, req.Scope, discussContextCollectorName)
	}
	return injectDiscussImages(frags, cfg.InlineImages), nil
}

func latestComposedUserMessageIndex(messages []timeline.ContextMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].CompactionArtifactID == "" &&
			discussContextMessageToSDK(messages[i]).Role == sdk.MessageRoleUser {
			return i
		}
	}
	return -1
}

func discussComposedMessageFrag(message timeline.ContextMessage, index int, currentUser bool, scope contextfrag.Scope) contextfrag.ContextFrag {
	msg := discussContextMessageToSDK(message)
	input := contextfrag.MessageFragInput{
		ID:         fmt.Sprintf("discuss.message.%03d", index),
		Message:    msg,
		Kind:       contextfrag.KindConversationEvent,
		Slot:       contextfrag.SlotHistory,
		Priority:   contextfrag.PriorityForMessage(msg),
		CacheClass: contextfrag.CacheNever,
		Trust:      trustForDiscussRole(message.Role),
		Scope:      scope,
		Source:     discussContextSource,
		SourceID:   fmt.Sprintf("message.%03d", index),
		Collector:  discussContextCollectorName,
		Index:      index,
	}
	if message.CompactionArtifactID != "" {
		input.Kind = contextfrag.KindConversationSummary
		input.Slot = contextfrag.SlotBeforeHistory
		input.Priority = 10
		input.CacheClass = contextfrag.CacheDynamic
		input.Trust = contextfrag.TrustSystem
		input.Budget = contextfrag.BudgetPolicy{Overflow: contextfrag.OverflowKeep}
	} else if currentUser {
		// Keep the history slot so rendering preserves the authoritative
		// composed order; kind and overflow carry current-request semantics.
		input.Kind = contextfrag.KindCurrentUserMessage
		input.Trust = contextfrag.TrustUser
		input.Budget = contextfrag.BudgetPolicy{Overflow: contextfrag.OverflowKeep}
	}
	return contextfrag.MessageFrag(input)
}

// injectDiscussImages mirrors the legacy inject-into-last-user-message
// behavior at fragment granularity.
func injectDiscussImages(frags []contextfrag.ContextFrag, images []sdk.ImagePart) []contextfrag.ContextFrag {
	extra := make([]sdk.MessagePart, 0, len(images))
	for _, img := range images {
		if strings.TrimSpace(img.Image) != "" {
			extra = append(extra, img)
		}
	}
	if len(extra) == 0 {
		return frags
	}
	for i := len(frags) - 1; i >= 0; i-- {
		msg := contextfrag.FragMessage(frags[i])
		if msg == nil || msg.Role != sdk.MessageRoleUser {
			continue
		}
		enriched := *msg
		enriched.Content = append(append([]sdk.MessagePart(nil), msg.Content...), extra...)
		frags[i] = contextfrag.RebuildFragMessage(frags[i], enriched)
		return frags
	}
	return frags
}

func discussContextConfig(config any) (DiscussContextConfig, error) {
	return collectorConfig[DiscussContextConfig](config, "discuss_context config must be DiscussContextConfig")
}

func discussContextMessageToSDK(message timeline.ContextMessage) sdk.Message {
	if len(message.RawContent) > 0 {
		raw, err := json.Marshal(struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}{
			Role:    message.Role,
			Content: message.RawContent,
		})
		if err == nil {
			var msg sdk.Message
			if json.Unmarshal(raw, &msg) == nil {
				return msg
			}
		}
	}
	if message.Role == "assistant" {
		return sdk.AssistantMessage(message.Content)
	}
	return sdk.UserMessage(message.Content)
}

func trustForDiscussRole(role string) contextfrag.TrustLevel {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant", "tool":
		return contextfrag.TrustWorkspace
	default:
		return contextfrag.TrustExternal
	}
}
