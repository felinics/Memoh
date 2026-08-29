package contextview

import (
	"context"
	"fmt"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

const (
	historyMessagesCollectorName         = "history_messages"
	materializedCurrentUserCollectorName = "materialized_current_user"
)

type HistoryMessagesConfig struct {
	Messages []sdk.Message
	// CurrentUserMessageIndex identifies a current request already carried in
	// Messages. The history collector omits it and the current-user collector
	// retains it in the current-user slot.
	CurrentUserMessageIndex *int
	// MemoryMessageIndex identifies materialized memory recall that must be
	// collected separately without changing its provider message position.
	MemoryMessageIndex *int
	// TokenEstimates carries per-message token estimates computed at the
	// source. Missing entries fall back to fragment-side estimation.
	TokenEstimates []int
	// TrimmablePrefix marks the leading message count that may be dropped.
	// Messages at or after the boundary are protected.
	TrimmablePrefix int
	// RepairToolClosures applies the shared repair when a caller has not
	// already repaired its materialized message stream.
	RepairToolClosures bool
}

type HistoryMessagesCollector struct{}

func (*HistoryMessagesCollector) Name() string {
	return historyMessagesCollectorName
}

func (*HistoryMessagesCollector) Collect(_ context.Context, req CollectRequest) ([]contextfrag.ContextFrag, error) {
	cfg, err := historyMessagesConfig(req.Config)
	if err != nil {
		return nil, err
	}
	if len(cfg.Messages) == 0 {
		return nil, nil
	}

	historyScope := req.Scope
	historyScope.Attention = nil
	memoryIndex, hasMemory := markedMemoryMessageIndex(cfg.Messages, cfg.MemoryMessageIndex)
	currentUserIndex, hasCurrentUser := markedCurrentUserMessageIndex(cfg.Messages, cfg.CurrentUserMessageIndex, optionalIndex(memoryIndex, hasMemory)...)
	frags := make([]contextfrag.ContextFrag, 0, len(cfg.Messages))
	for i, msg := range cfg.Messages {
		if hasCurrentUser && i == currentUserIndex || hasMemory && i == memoryIndex {
			continue
		}
		estimate := 0
		if i < len(cfg.TokenEstimates) {
			estimate = cfg.TokenEstimates[i]
		}
		budget := contextfrag.BudgetPolicy{}
		if i >= cfg.TrimmablePrefix {
			budget.Overflow = contextfrag.OverflowKeep
		}
		frags = append(frags, contextfrag.MessageFrag(contextfrag.MessageFragInput{
			ID:            fmt.Sprintf("message.%03d", i),
			Message:       msg,
			Kind:          kindForSDKMessage(msg),
			Slot:          contextfrag.SlotHistory,
			Priority:      contextfrag.PriorityForMessage(msg),
			CacheClass:    cacheForSDKMessage(msg),
			Trust:         trustForSDKMessage(msg),
			Scope:         historyScope,
			Source:        contextfrag.SourceRunConfig,
			Collector:     historyMessagesCollectorName,
			Index:         i,
			Budget:        budget,
			TokenEstimate: estimate,
		}))
	}
	if cfg.RepairToolClosures {
		frags = contextfrag.RepairToolClosureFrags(frags, historyScope, historyMessagesCollectorName)
	}
	return frags, nil
}

type materializedCurrentUserCollector struct{}

func (*materializedCurrentUserCollector) Name() string {
	return materializedCurrentUserCollectorName
}

func (*materializedCurrentUserCollector) Collect(_ context.Context, req CollectRequest) ([]contextfrag.ContextFrag, error) {
	cfg, err := historyMessagesConfig(req.Config)
	if err != nil {
		return nil, err
	}
	memoryIndex, hasMemory := markedMemoryMessageIndex(cfg.Messages, cfg.MemoryMessageIndex)
	index, ok := markedCurrentUserMessageIndex(cfg.Messages, cfg.CurrentUserMessageIndex, optionalIndex(memoryIndex, hasMemory)...)
	if !ok {
		return nil, nil
	}
	estimate := 0
	if index < len(cfg.TokenEstimates) {
		estimate = cfg.TokenEstimates[index]
	}
	msg := cfg.Messages[index]
	return []contextfrag.ContextFrag{contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID:            fmt.Sprintf("message.%03d", index),
		Message:       msg,
		Kind:          contextfrag.KindCurrentUserMessage,
		Slot:          contextfrag.SlotCurrentUser,
		Priority:      contextfrag.PriorityForMessage(msg),
		CacheClass:    contextfrag.CacheNever,
		Trust:         contextfrag.TrustUser,
		Scope:         req.Scope,
		Source:        contextfrag.SourceRunConfig,
		Collector:     materializedCurrentUserCollectorName,
		Index:         index,
		Budget:        contextfrag.BudgetPolicy{Overflow: contextfrag.OverflowKeep},
		TokenEstimate: estimate,
	})}, nil
}

func markedCurrentUserMessageIndex(messages []sdk.Message, index *int, excluded ...int) (int, bool) {
	if index == nil {
		return 0, false
	}
	if *index >= 0 && *index < len(messages) && messages[*index].Role == sdk.MessageRoleUser && !containsIndex(excluded, *index) {
		return *index, true
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == sdk.MessageRoleUser && !containsIndex(excluded, i) {
			return i, true
		}
	}
	return 0, false
}

func markedMemoryMessageIndex(messages []sdk.Message, index *int) (int, bool) {
	if index == nil || *index < 0 || *index >= len(messages) || messages[*index].Role != sdk.MessageRoleUser {
		return 0, false
	}
	return *index, true
}

func optionalIndex(index int, ok bool) []int {
	if !ok {
		return nil
	}
	return []int{index}
}

func containsIndex(indexes []int, target int) bool {
	for _, index := range indexes {
		if index == target {
			return true
		}
	}
	return false
}

func historyMessagesConfig(config any) (HistoryMessagesConfig, error) {
	return collectorConfig[HistoryMessagesConfig](config, "history_messages config must be HistoryMessagesConfig")
}

func kindForSDKMessage(msg sdk.Message) contextfrag.Kind {
	if msg.Role == sdk.MessageRoleSystem {
		return contextfrag.KindSystemPolicy
	}
	return contextfrag.KindConversationEvent
}

func cacheForSDKMessage(msg sdk.Message) contextfrag.CacheClass {
	if msg.Role == sdk.MessageRoleSystem {
		return contextfrag.CacheDynamic
	}
	switch msg.Role {
	case sdk.MessageRoleUser, sdk.MessageRoleAssistant, sdk.MessageRoleTool:
		return contextfrag.CacheStable
	default:
		return contextfrag.CacheNever
	}
}

func trustForSDKMessage(msg sdk.Message) contextfrag.TrustLevel {
	switch msg.Role {
	case sdk.MessageRoleSystem:
		return contextfrag.TrustSystem
	case sdk.MessageRoleAssistant, sdk.MessageRoleTool:
		return contextfrag.TrustWorkspace
	default:
		return contextfrag.TrustExternal
	}
}
