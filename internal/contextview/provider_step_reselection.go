package contextview

import (
	"context"
	"fmt"
	"reflect"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

// providerStepBudgetEnvelope resolves the step selection budget with the same
// recent-protect window semantics as the provider view, so mid-loop
// reselection and resolve-time selection share one envelope shape.
func providerStepBudgetEnvelope(input agentpkg.ContextStepSelectionInput) BudgetEnvelope {
	return BudgetEnvelope{
		MaxTokens:              input.BudgetMaxTokens,
		EnforceProtectedBudget: input.BudgetMaxTokens > 0,
		RecentProtectTokens:    resolveRecentProtectTokens(input.RecentProtectTokens),
	}
}

func SelectProviderStepMessages(ctx context.Context, input agentpkg.ContextStepSelectionInput) agentpkg.ContextStepSelectionResult {
	if input.InitialMessageCount < 0 || input.InitialMessageCount >= len(input.Messages) {
		return agentpkg.ContextStepSelectionResult{}
	}
	loopMessages := input.Messages[input.InitialMessageCount:]
	if len(loopMessages) == 0 {
		return agentpkg.ContextStepSelectionResult{}
	}

	frags, err := (&HistoryMessagesCollector{}).Collect(ctx, CollectRequest{
		Scope:  input.Scope,
		Intent: contextfrag.IntentRunConfigPreProvider,
		Config: HistoryMessagesConfig{
			Messages:        loopMessages,
			TrimmablePrefix: len(loopMessages),
		},
	})
	if err != nil || len(frags) == 0 {
		return agentpkg.ContextStepSelectionResult{}
	}
	frags = markInjectedLoopUserFrags(frags)

	selector := &FragmentSelector{}
	budget := input.BudgetMaxTokens
	for attempt := 0; attempt <= len(frags)+1; attempt++ {
		attemptInput := input
		attemptInput.BudgetMaxTokens = budget
		selection := selector.Select(
			frags,
			selector.ProfileFor(contextfrag.IntentRunConfigPreProvider),
			providerStepBudgetEnvelope(attemptInput),
		)
		if selection.FatalError != nil {
			return agentpkg.ContextStepSelectionResult{FatalError: selection.FatalError}
		}

		selected := selectedProviderStepFrags(selection, input.Scope)
		truncated := 0
		if len(input.Messages) >= input.MinMessages {
			selected, truncated = truncateOldToolResultFrags(selected, input.KeepRecentToolResults)
		}
		changed := len(selection.Dropped) > 0 || truncated > 0
		messages := input.Messages
		var messageSourceIndexes []int
		if changed {
			messages = make([]sdk.Message, 0, input.InitialMessageCount+len(selected))
			messages = append(messages, cloneSDKMessages(input.Messages[:input.InitialMessageCount])...)
			messages = append(messages, sdkMessagesFromFrags(selected)...)
			messageSourceIndexes = providerStepMessageSourceIndexes(input.Messages, input.InitialMessageCount, selected)
		}

		overflow := providerStepEnvelopeOverflow(input, messages)
		if overflow <= 0 {
			if !changed {
				return agentpkg.ContextStepSelectionResult{}
			}
			return agentpkg.ContextStepSelectionResult{
				Messages:                  messages,
				MessageSourceIndexes:      messageSourceIndexes,
				MessageSourceIndexesKnown: true,
				Dropped:                   len(selection.Dropped),
				Truncated:                 truncated,
				DropReasons:               dropReasonHistogram(selection.Summary.DropReasons),
			}
		}

		nextBudget := budget - overflow
		if nextBudget < 1 {
			nextBudget = 1
		}
		if nextBudget >= budget {
			break
		}
		budget = nextBudget
	}
	return agentpkg.ContextStepSelectionResult{FatalError: fmt.Errorf(
		"%w: provider step exceeds input allowance %d",
		contextfrag.ErrBudgetUnsatisfied,
		input.ProviderInputAllowanceTokens,
	)}
}

func providerStepEnvelopeOverflow(input agentpkg.ContextStepSelectionInput, messages []sdk.Message) int {
	if input.ProviderInputAllowanceTokens <= 0 {
		return 0
	}
	return contextfrag.ProviderEnvelopeTokens(input.ProviderSystem, messages, input.ProviderTools) - input.ProviderInputAllowanceTokens
}

// markInjectedLoopUserFrags types user-role messages appended during the tool
// loop. Text-only carriers (InjectCh text, background summaries) hold content
// the run deliberately inserted mid-stream, so step budget pressure must never
// drop them. Media-bearing payloads (read_media injections) stay droppable:
// their sources live at workspace paths the model can re-read.
func markInjectedLoopUserFrags(frags []contextfrag.ContextFrag) []contextfrag.ContextFrag {
	for i := range frags {
		msg := providerStepFragMessage(frags[i])
		if msg == nil || !isRole(msg.Role, sdk.MessageRoleUser) {
			continue
		}
		if messageHasNativeMediaPart(*msg) {
			frags[i].Kind = contextfrag.KindNativeImage
			continue
		}
		if contextfrag.IsBackgroundSummaryCarrier(*msg) {
			frags[i].Kind = contextfrag.KindBackgroundSummary
		} else {
			frags[i].Kind = contextfrag.KindInjectedMessage
		}
		frags[i].Budget.Overflow = contextfrag.OverflowKeep
	}
	return frags
}

func messageHasNativeMediaPart(msg sdk.Message) bool {
	for _, part := range msg.Content {
		switch part.(type) {
		case sdk.ImagePart, sdk.FilePart:
			return true
		}
	}
	return false
}

// truncateOldToolResultFrags keeps the most recent keepRecent complete tool
// cycles intact and replaces older bulky tool results with a size summary,
// preserving the ToolResultPart shape so provider serializers stay happy.
// keepRecent <= 0 disables truncation.
func truncateOldToolResultFrags(frags []contextfrag.ContextFrag, keepRecent int) ([]contextfrag.ContextFrag, int) {
	if keepRecent <= 0 {
		return frags, 0
	}
	recentCycles := 0
	cutoff := -1
	for i := len(frags) - 1; i >= 0; i-- {
		msg := providerStepFragMessage(frags[i])
		if msg == nil || msg.Role != sdk.MessageRoleTool {
			continue
		}
		recentCycles++
		if recentCycles > keepRecent {
			cutoff = i
			break
		}
	}
	if cutoff < 0 {
		return frags, 0
	}
	truncated := 0
	out := make([]contextfrag.ContextFrag, len(frags))
	copy(out, frags)
	for i := 0; i <= cutoff; i++ {
		msg := providerStepFragMessage(out[i])
		if msg == nil || msg.Role != sdk.MessageRoleTool {
			continue
		}
		replaced, changed := contextlimit.TruncateStepToolResult(*msg, contextlimit.StepToolResultTruncateBytes)
		if !changed {
			continue
		}
		out[i] = rebuildMessageFrag(out[i], replaced)
		truncated++
	}
	return out, truncated
}

func providerStepFragMessage(frag contextfrag.ContextFrag) *sdk.Message {
	for _, part := range frag.Parts {
		if part.Type == contextfrag.PartSDKMessage {
			return sdkMessagePart(part)
		}
	}
	return nil
}

func selectedProviderStepFrags(selection SelectionResult, scope contextfrag.Scope) []contextfrag.ContextFrag {
	if !selection.TrimNotice || selection.TrimNoticeIndex < 0 || selection.TrimNoticeIndex > len(selection.Selected) {
		return selection.Selected
	}
	notice := contextfrag.NormalizeContextRefs([]contextfrag.ContextFrag{TrimNoticeFrag(scope)})[0]
	selected := make([]contextfrag.ContextFrag, 0, len(selection.Selected)+1)
	selected = append(selected, selection.Selected[:selection.TrimNoticeIndex]...)
	selected = append(selected, notice)
	selected = append(selected, selection.Selected[selection.TrimNoticeIndex:]...)
	return selected
}

func sdkMessagesFromFrags(frags []contextfrag.ContextFrag) []sdk.Message {
	var messages []sdk.Message
	for _, frag := range frags {
		for _, part := range frag.Parts {
			if part.Type != contextfrag.PartSDKMessage {
				continue
			}
			if msg := sdkMessagePart(part); msg != nil {
				messages = append(messages, cloneSDKMessage(*msg))
			}
		}
	}
	return messages
}

func providerStepMessageSourceIndexes(inputMessages []sdk.Message, prefixCount int, frags []contextfrag.ContextFrag) []int {
	indexes := make([]int, 0, prefixCount+len(frags))
	for i := 0; i < prefixCount; i++ {
		indexes = append(indexes, i)
	}
	for _, frag := range frags {
		for _, part := range frag.Parts {
			if part.Type != contextfrag.PartSDKMessage || sdkMessagePart(part) == nil {
				continue
			}
			sourceIndex := prefixCount + frag.Provenance.Index
			if frag.Provenance.Collector != historyMessagesCollectorName ||
				sourceIndex < prefixCount || sourceIndex >= len(inputMessages) ||
				!reflect.DeepEqual(inputMessages[sourceIndex], *sdkMessagePart(part)) {
				sourceIndex = -1
			}
			indexes = append(indexes, sourceIndex)
		}
	}
	return indexes
}

func cloneSDKMessages(messages []sdk.Message) []sdk.Message {
	out := make([]sdk.Message, len(messages))
	for i, msg := range messages {
		out[i] = cloneSDKMessage(msg)
	}
	return out
}

func dropReasonHistogram(records []DropRecord) map[string]int {
	if len(records) == 0 {
		return nil
	}
	out := make(map[string]int, len(records))
	for _, record := range records {
		out[record.Reason]++
	}
	return out
}
