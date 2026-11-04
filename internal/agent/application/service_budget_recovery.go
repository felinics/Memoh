package application

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/contextview"
)

// pauseIdleDuringBudgetRecovery pauses the watchdog while the run recovers
// its context budget, work that owns its bound: a compaction fork request
// ends after as long a silence as the watchdog allows a model request.
func pauseIdleDuringBudgetRecovery(cfg native.RunConfig, idle *idleCancel) native.RunConfig {
	cfg.ModelIdleWindow = idle.modelWindow()
	if switchContext := cfg.SwitchContext; switchContext != nil {
		cfg.SwitchContext = func(ctx context.Context, sw native.ContextSwitch) (string, error) {
			idle.Pause()
			defer idle.Resume()
			return switchContext(ctx, sw)
		}
	}
	if recoverBudget := cfg.RecoverContextBudget; recoverBudget != nil {
		cfg.RecoverContextBudget = func(ctx context.Context, cfg native.RunConfig) (native.RunConfig, bool, error) {
			idle.Pause()
			defer idle.Resume()
			return recoverBudget(ctx, cfg)
		}
	}
	return cfg
}

func (s *Service) recoverDiscussContextBudget(ctx context.Context, cmd turn.StartTurnCommand, admitted []turn.DiscussMessage, modelID string, cfg native.RunConfig, compacted *bool) (native.RunConfig, bool, error) {
	plan := cfg.ContextManifest.BudgetPlan
	if plan == nil || ctx.Err() != nil {
		return cfg, false, nil
	}
	batch := make(map[string]turn.ContextMessageSource)
	newestTokens := 0
	for _, message := range admitted {
		if message.Source != nil && message.Source.Current {
			batch[message.Source.ID] = *message.Source
			newestTokens = discussMessageTokens(message)
		}
	}
	budget := plan.HistoryBudget
	if cfg.SwitchContext != nil {
		// An armed run keeps the batch while it leaves a tenth of the room
		// for the summary of what precedes it.
		budget -= budget / 10
	}
	cfg, shed := shedOlderBatchImages(cfg, batch, budget)
	// An armed run folds its own request before its first step; only input
	// Channel left out of the request is compacted from rows first.
	if *compacted || cfg.SwitchContext != nil && len(cmd.DiscussOmittedSources) == 0 {
		return cfg, shed, nil
	}
	// The batch stays where it is, newest first while it leaves the summary
	// room, and is priced with the rest of what the history renders.
	var protected []turn.ContextMessageSource
	available, pressure := plan.HistoryBudget, 0
	for _, frag := range cfg.ContextSourceFrags {
		source, inBatch := batch[frag.Provenance.SourceID]
		if inBatch {
			protected = append(protected, source)
		}
		switch {
		case frag.Slot == contextfrag.SlotSystem:
		case frag.Slot == contextfrag.SlotCurrentUser || frag.Kind == contextfrag.KindCurrentUserMessage:
			if inBatch {
				available += contextfrag.ResolveProviderBudgetFragTokens(frag)
			}
		case frag.Kind == contextfrag.KindConversationSummary || (frag.Slot == contextfrag.SlotHistory && frag.Kind == contextfrag.KindConversationEvent):
			pressure += contextfrag.ResolveProviderBudgetFragTokens(frag)
		default:
			available -= contextfrag.ResolveProviderBudgetFragTokens(frag)
		}
	}
	originalHistory := max(0, cmd.DiscussContextTokens-newestTokens)
	pressure = max(pressure, contextfrag.ProviderBudgetTokensFromBytes(int(contextfrag.BudgetBytesForTokens(originalHistory))))
	if pressure <= available && len(cmd.DiscussOmittedSources) == 0 {
		return cfg, shed, nil
	}
	if available <= 0 {
		if len(cmd.DiscussOmittedSources) > 0 {
			return cfg, false, fmt.Errorf("%w: no history room for input Channel omitted", contextfrag.ErrBudgetUnsatisfied)
		}
		return cfg, shed, nil
	}
	*compacted = true
	recovery := ChatRequest{BotID: cmd.BotID, ChatID: cmd.BotID, ThreadID: cmd.ThreadID, RunID: cfg.RunID, discussCurrentSources: protected}
	if err := s.compactForBudget(ctx, recovery, pressure, available, modelID); err != nil {
		return cfg, false, fmt.Errorf("%w: %w", contextfrag.ErrBudgetUnsatisfied, err)
	}
	return cfg, false, native.ErrContextRecompose
}

// shedOlderBatchImages strips images of older input of the batch, oldest
// first, until what must stay fits the budget. Answered history before the
// batch is what selection trims, or an armed run folds, first, so it does not
// count; an image costs far more than its message's text and no summary can
// carry it, so the text survives its images. The newest input keeps its own.
// It reports whether any were shed.
func shedOlderBatchImages(cfg native.RunConfig, batch map[string]turn.ContextMessageSource, historyBudget int) (native.RunConfig, bool) {
	excess, total, trimmable, inBatch := -historyBudget, -historyBudget, false, false
	for _, frag := range cfg.ContextSourceFrags {
		if frag.Slot == contextfrag.SlotSystem || frag.Slot == contextfrag.SlotCurrentUser || frag.Kind == contextfrag.KindCurrentUserMessage {
			continue
		}
		cost := contextfrag.ResolveProviderBudgetFragTokens(frag)
		total += cost
		_, member := batch[frag.Provenance.SourceID]
		inBatch = inBatch || member && frag.Kind == contextfrag.KindConversationEvent
		if inBatch || frag.Slot != contextfrag.SlotHistory || frag.Kind != contextfrag.KindConversationEvent {
			excess += cost
		} else {
			trimmable = true
		}
	}
	if total <= 0 {
		return cfg, false
	}
	if trimmable && cfg.SwitchContext == nil {
		excess += contextfrag.ResolveProviderBudgetFragTokens(contextview.TrimNoticeFrag(contextfrag.Scope{}))
	}
	var shed []string
	frags, messages, provenance := slices.Clone(cfg.ContextSourceFrags), slices.Clone(cfg.Messages), slices.Clone(cfg.ContextProvenance)
	for i, frag := range frags {
		if excess <= 0 {
			break
		}
		msg := contextfrag.FragMessage(frag)
		if _, inBatch := batch[frag.Provenance.SourceID]; !inBatch || msg == nil || msg.Role != sdk.MessageRoleUser ||
			frag.Kind != contextfrag.KindConversationEvent || frag.Provenance.Collector != discussContextCollector {
			continue
		}
		// Oldest image first, only as many as the budget needs.
		kept := *msg
		for excess > 0 {
			image := slices.IndexFunc(kept.Content, func(part sdk.MessagePart) bool {
				_, isImage := part.(sdk.ImagePart)
				return isImage
			})
			if image < 0 {
				break
			}
			before := contextfrag.ResolveProviderBudgetFragTokens(frags[i])
			kept.Content = slices.Delete(slices.Clone(kept.Content), image, image+1)
			frags[i] = contextfrag.RebuildFragMessage(frag, kept)
			excess -= before - contextfrag.ResolveProviderBudgetFragTokens(frags[i])
		}
		if len(kept.Content) == len(msg.Content) {
			continue
		}
		if index := frag.Provenance.Index; index >= 0 && index < len(messages) {
			// The input renders the same sources without its images.
			for j := range provenance {
				if reflect.DeepEqual(provenance[j].Message, messages[index]) {
					provenance[j].Message = kept
					break
				}
			}
			messages[index] = kept
		}
		shed = append(shed, frag.Provenance.SourceID)
	}
	if len(shed) == 0 {
		return cfg, false
	}
	cfg.ContextSourceFrags, cfg.Messages, cfg.ContextProvenance = frags, messages, provenance
	if cfg.ContextMutations != nil {
		cfg.ContextMutations.Record(contextfrag.MutationCurrentInputImagesOmitted, "sources="+strings.Join(shed, ","))
	}
	return cfg.RefreshContextFrag(), true
}
