package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/contextview"
)

type chatHistoryLayout struct {
	forkCount      int
	historyCount   int
	pressureTokens int
	usePipeline    bool
}

func pauseIdleDuringBudgetRecovery(cfg native.RunConfig, idle *idleCancel) native.RunConfig {
	if recoverBudget := cfg.RecoverContextBudget; recoverBudget != nil {
		cfg.RecoverContextBudget = func(ctx context.Context, cfg native.RunConfig) (native.RunConfig, bool, error) {
			idle.Stop()
			defer idle.Reset()
			return recoverBudget(ctx, cfg)
		}
	}
	return cfg
}

func (s *Service) chatBudgetRecovery(req ChatRequest, layout chatHistoryLayout) func(context.Context, native.RunConfig) (native.RunConfig, bool, error) {
	return func(ctx context.Context, cfg native.RunConfig) (native.RunConfig, bool, error) {
		if s.effectiveSyncCompactionMode() == syncCompactionModeOff || ctx.Err() != nil {
			return cfg, false, nil
		}
		budget, pressure := layout.availableHistoryBudget(cfg)
		if budget <= 0 {
			return cfg, false, nil
		}
		compactionReq := req
		compactionReq.RunID = cfg.RunID
		if result := s.runBudgetCompactionSync(ctx, compactionReq, pressure, budget, cfg.CurrentModelUUID); result.Status != compaction.StatusOK && result.Status != compaction.StatusProgress {
			return cfg, false, nil
		}
		var messages []ModelMessage
		var records []historyfrag.HistoryRecord
		if layout.usePipeline {
			messages, _, _ = s.buildMessagesFromPipeline(ctx, req, cfg.ContextBudgetMaxTokens)
		} else {
			prepared, err := s.prepareHistoryContext(ctx, req, historyScopeFallbackFromChatRequest(req), cfg.ContextBudgetMaxTokens)
			if err != nil {
				return cfg, false, err
			}
			messages, records = prepared.messages, prepared.records
		}
		recovered, err := layout.replaceHistory(ctx, cfg, req, messages, records)
		if err != nil {
			return cfg, false, err
		}
		if reflect.DeepEqual(recovered.Messages, cfg.Messages) {
			return cfg, false, nil
		}
		layout.historyCount = recovered.ContextTrimmableMessages
		layout.pressureTokens = 0
		return recovered, true, nil
	}
}

func (l chatHistoryLayout) owns(frag contextfrag.ContextFrag) bool {
	index := frag.Provenance.Index
	return frag.Slot == contextfrag.SlotHistory && index >= l.forkCount && index < l.historyCount &&
		frag.ID == fmt.Sprintf("message.%03d", index)
}

func (l chatHistoryLayout) availableHistoryBudget(cfg native.RunConfig) (int, int) {
	plan := cfg.ContextManifest.BudgetPlan
	if plan == nil {
		return 0, 0
	}
	available, raw, summaries := plan.HistoryBudget, 0, 0
	for _, frag := range cfg.ContextSourceFrags {
		if frag.Slot == contextfrag.SlotSystem || frag.Slot == contextfrag.SlotCurrentUser || frag.Kind == contextfrag.KindCurrentUserMessage {
			continue
		}
		if l.owns(frag) {
			if frag.Kind == contextfrag.KindConversationSummary {
				summaries += contextfrag.ResolveProviderBudgetFragTokens(frag)
			} else {
				raw += contextfrag.ResolveProviderBudgetFragTokens(frag)
			}
		} else {
			available -= contextfrag.ResolveProviderBudgetFragTokens(frag)
		}
	}
	pressure := max(contextfrag.ProviderBudgetTokensFromBytes(int(contextfrag.BudgetBytesForTokens(l.pressureTokens))), raw+summaries)
	if pressure <= available {
		return 0, pressure
	}
	return max(0, available), pressure
}

func (l chatHistoryLayout) replaceHistory(ctx context.Context, cfg native.RunConfig, req ChatRequest, history []ModelMessage, records []historyfrag.HistoryRecord) (native.RunConfig, error) {
	if l.forkCount < 0 || l.historyCount < l.forkCount || l.historyCount > len(cfg.Messages) {
		return cfg, errors.New("context recovery history boundary is invalid")
	}
	old := cfg
	history, _, _ = normalizeContextMessages(history, nil, nil)
	messages := sdkMessagesToModelMessages(cfg.Messages[:l.forkCount])
	messages = append(messages, history...)
	newHistoryCount := len(messages)
	cfg.Messages = make([]sdk.Message, 0, newHistoryCount+len(old.Messages)-l.historyCount)
	cfg.Messages = append(cfg.Messages, old.Messages[:l.forkCount]...)
	cfg.Messages = append(cfg.Messages, modelMessagesToSDKMessages(history)...)
	cfg.Messages = append(cfg.Messages, old.Messages[l.historyCount:]...)
	delta := newHistoryCount - l.historyCount
	cfg.ContextMemoryMessageIndex = shiftRecoveryIndex(cfg.ContextMemoryMessageIndex, l.historyCount, delta)
	cfg.ContextCurrentUserMessageIndex = shiftRecoveryIndex(cfg.ContextCurrentUserMessageIndex, l.historyCount, delta)
	cfg.ContextFrags = historyContextFragsForMessages(messages, records)
	cfg.ForkContextSourceMessageIDs = make([]string, l.forkCount, len(cfg.Messages))
	cfg.ForkContextSourceMessageIDs = append(cfg.ForkContextSourceMessageIDs, historySourceMessageIDsForMessages(history, records)...)
	copy(cfg.ForkContextSourceMessageIDs[:l.forkCount], old.ForkContextSourceMessageIDs[:min(l.forkCount, len(old.ForkContextSourceMessageIDs))])
	for i := l.historyCount; i < len(old.Messages); i++ {
		source := ""
		if i < len(old.ForkContextSourceMessageIDs) {
			source = old.ForkContextSourceMessageIDs[i]
		}
		cfg.ForkContextSourceMessageIDs = append(cfg.ForkContextSourceMessageIDs, source)
	}
	if index := old.ContextCurrentUserMessageIndex; index != nil && *index >= l.forkCount && *index < l.historyCount {
		if l.usePipeline {
			cfg.ContextCurrentUserMessageIndex = latestModelUserMessageIndex(messages)
		} else if req.ReusePersistedUserMessage {
			cfg.ContextCurrentUserMessageIndex = nil
			markRequiredHistoryMessageCurrent(&cfg, req.RequiredHistoryMessageID)
		}
		current := cfg.ContextCurrentUserMessageIndex
		if current == nil || *current < l.forkCount || *current >= newHistoryCount {
			return old, errors.New("context recovery lost the current request")
		}
		candidate, original := cfg.Messages[*current], old.Messages[*index]
		if candidate.Role != original.Role || len(candidate.Content) > len(original.Content) ||
			!reflect.DeepEqual(candidate.Content, original.Content[:len(candidate.Content)]) {
			return old, errors.New("context recovery changed the current request")
		}
		cfg.Messages[*current] = old.Messages[*index]
		cfg.ForkContextSourceMessageIDs[*current] = ""
		if *index < len(old.ForkContextSourceMessageIDs) {
			cfg.ForkContextSourceMessageIDs[*current] = old.ForkContextSourceMessageIDs[*index]
		}
	}
	cfg.ContextHistoryTokenEstimates = make([]int, len(cfg.Messages))
	for i, message := range cfg.Messages {
		cfg.ContextHistoryTokenEstimates[i] = contextfrag.EstimateSDKMessageTokens(message)
	}
	cfg.ContextTrimmableMessages = newHistoryCount
	cfg.ContextSourceFrags = l.replaceSourceFrags(ctx, old, cfg, newHistoryCount)
	if cfg.ForkContext != nil {
		if err := cfg.ForkContext.StoreWithSources(cfg.Messages, cfg.ForkContextSourceMessageIDs); err != nil {
			return old, err
		}
	}
	return cfg.RefreshContextFrag(), nil
}

func shiftRecoveryIndex(index *int, boundary, delta int) *int {
	if index == nil {
		return nil
	}
	value := *index
	if value >= boundary {
		value += delta
	}
	return &value
}

func (l chatHistoryLayout) replaceSourceFrags(ctx context.Context, old, cfg native.RunConfig, newHistoryCount int) []contextfrag.ContextFrag {
	nextLayout := l
	nextLayout.historyCount = newHistoryCount
	var history []contextfrag.ContextFrag
	for _, frag := range contextview.CollectNonSystemProviderSourceFrags(ctx, cfg) {
		if nextLayout.owns(frag) {
			history = append(history, frag)
		}
	}
	out := make([]contextfrag.ContextFrag, 0, len(old.ContextSourceFrags)+len(history))
	inserted := false
	for _, frag := range old.ContextSourceFrags {
		if !inserted && frag.Slot != contextfrag.SlotSystem &&
			(l.owns(frag) || frag.Provenance.Index >= l.historyCount || frag.Kind == contextfrag.KindMemoryRecall || frag.Kind == contextfrag.KindHookContext) {
			out = append(out, history...)
			inserted = true
		}
		if l.owns(frag) {
			if !inserted {
				out = append(out, history...)
				inserted = true
			}
			continue
		}
		oldIndex, newIndex := frag.Provenance.Index, frag.Provenance.Index
		if frag.Slot != contextfrag.SlotSystem {
			if oldIndex >= l.historyCount {
				newIndex += newHistoryCount - l.historyCount
			} else if old.ContextCurrentUserMessageIndex != nil && cfg.ContextCurrentUserMessageIndex != nil &&
				frag.ID == fmt.Sprintf("message.%03d", *old.ContextCurrentUserMessageIndex) {
				newIndex = *cfg.ContextCurrentUserMessageIndex
			}
		}
		if newIndex != oldIndex {
			frag.Provenance.Index = newIndex
			if frag.ID == fmt.Sprintf("message.%03d", oldIndex) {
				if frag.Ref.ID == frag.ID && frag.Ref.Durability != contextfrag.RefDurable {
					frag.Ref.ID = fmt.Sprintf("message.%03d", newIndex)
				}
				frag.ID = fmt.Sprintf("message.%03d", newIndex)
			}
		}
		out = append(out, frag)
	}
	if !inserted {
		out = append(out, history...)
	}
	return out
}

func (s *Service) recoverDiscussContextBudget(ctx context.Context, cmd turn.StartTurnCommand, modelID string, cfg native.RunConfig) (native.RunConfig, bool, error) {
	plan := cfg.ContextManifest.BudgetPlan
	if s.effectiveSyncCompactionMode() == syncCompactionModeOff || plan == nil || ctx.Err() != nil {
		return cfg, false, nil
	}
	available, pressure := plan.HistoryBudget, 0
	for _, frag := range cfg.ContextSourceFrags {
		if frag.Slot == contextfrag.SlotSystem || frag.Slot == contextfrag.SlotCurrentUser || frag.Kind == contextfrag.KindCurrentUserMessage {
			continue
		}
		cost := contextfrag.ResolveProviderBudgetFragTokens(frag)
		if frag.Kind == contextfrag.KindConversationSummary || (frag.Slot == contextfrag.SlotHistory && frag.Kind == contextfrag.KindConversationEvent) {
			pressure += cost
		} else {
			available -= cost
		}
	}
	originalHistory := max(0, cmd.DiscussContextTokens-cmd.DiscussCurrentTokens)
	pressure = max(pressure, contextfrag.ProviderBudgetTokensFromBytes(int(contextfrag.BudgetBytesForTokens(originalHistory))))
	if pressure <= available || available <= 0 {
		return cfg, false, nil
	}
	result := s.runBudgetCompactionSync(ctx, ChatRequest{BotID: cmd.BotID, ChatID: cmd.BotID, ThreadID: cmd.ThreadID, RunID: cfg.RunID}, pressure, available, modelID)
	if result.Status != compaction.StatusOK && result.Status != compaction.StatusProgress {
		return cfg, false, nil
	}
	return cfg, false, native.ErrContextRecompose
}
