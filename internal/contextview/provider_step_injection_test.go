package contextview

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

func appendToolCycles(messages []sdk.Message, ids []string, resultSize int) []sdk.Message {
	for _, id := range ids {
		messages = append(messages,
			assistantToolCallMessage(id, "lookup", ""),
			toolResultMessage(id, "lookup", strings.Repeat("x", resultSize)),
		)
	}
	return messages
}

func selectionHasUserText(messages []sdk.Message, text string) bool {
	for _, msg := range messages {
		if msg.Role != sdk.MessageRoleUser {
			continue
		}
		for _, part := range msg.Content {
			if tp, ok := part.(sdk.TextPart); ok && strings.Contains(tp.Text, text) {
				return true
			}
		}
	}
	return false
}

func TestProviderStepBudgetEnvelopeNeverCarriesTurnStartPlan(t *testing.T) {
	t.Parallel()

	envelope := providerStepBudgetEnvelope(agentpkg.ContextStepSelectionInput{
		BudgetMaxTokens: 123,
	})

	if envelope.Plan != nil {
		t.Fatalf("step envelope Plan = %#v, want nil so system selection runs only at turn start", envelope.Plan)
	}
	if envelope.MaxTokens != 123 {
		t.Fatalf("step MaxTokens = %d, want suffix budget 123", envelope.MaxTokens)
	}
	if !envelope.EnforceProtectedBudget {
		t.Fatal("step envelope must include protected suffix content in its hard allowance")
	}
}

func TestMarkInjectedLoopUserFragsTypesAndProtectsOnlyUserMessages(t *testing.T) {
	t.Parallel()

	messages := appendToolCycles(nil, []string{"call-a"}, 40)
	messages = append(messages, sdk.UserMessage("<message sender=\"alice\">injected</message>"))

	frags, err := (&HistoryMessagesCollector{}).Collect(context.Background(), CollectRequest{
		Scope:  contextfrag.Scope{BotID: "bot-1"},
		Intent: contextfrag.IntentRunConfigPreProvider,
		Config: HistoryMessagesConfig{Messages: messages, TrimmablePrefix: len(messages)},
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	before := make([]contextfrag.ContextFrag, len(frags))
	copy(before, frags)

	marked := markInjectedLoopUserFrags(frags)
	for i, frag := range marked {
		msg := discussFragMessage(frag)
		if msg != nil && msg.Role == sdk.MessageRoleUser {
			if frag.Kind != contextfrag.KindInjectedMessage {
				t.Fatalf("injected frag kind = %q, want %q", frag.Kind, contextfrag.KindInjectedMessage)
			}
			if frag.Budget.Overflow != contextfrag.OverflowKeep {
				t.Fatalf("injected frag overflow = %q, want keep", frag.Budget.Overflow)
			}
			continue
		}
		if frag.Kind != before[i].Kind || frag.Budget != before[i].Budget {
			t.Fatalf("non-user frag %d changed: %+v -> %+v", i, before[i], frag)
		}
	}
}

func imageUserMessage(size int, extra ...sdk.MessagePart) sdk.Message {
	parts := append([]sdk.MessagePart{sdk.ImagePart{
		Image:     "data:image/png;base64," + strings.Repeat("A", size),
		MediaType: "image/png",
	}}, extra...)
	return sdk.Message{Role: sdk.MessageRoleUser, Content: parts}
}

func fileUserMessage(size int, extra ...sdk.MessagePart) sdk.Message {
	parts := append([]sdk.MessagePart{sdk.FilePart{
		Data:      strings.Repeat("A", size),
		MediaType: "application/pdf",
		Filename:  "report.pdf",
	}}, extra...)
	return sdk.Message{Role: sdk.MessageRoleUser, Content: parts}
}

func selectionHasImagePart(messages []sdk.Message) bool {
	for _, msg := range messages {
		for _, part := range msg.Content {
			if _, ok := part.(sdk.ImagePart); ok {
				return true
			}
		}
	}
	return false
}

func selectionHasFilePart(messages []sdk.Message) bool {
	for _, msg := range messages {
		for _, part := range msg.Content {
			if _, ok := part.(sdk.FilePart); ok {
				return true
			}
		}
	}
	return false
}

func collectLoopFrags(t *testing.T, messages []sdk.Message) []contextfrag.ContextFrag {
	t.Helper()
	frags, err := (&HistoryMessagesCollector{}).Collect(context.Background(), CollectRequest{
		Scope:  contextfrag.Scope{BotID: "bot-1"},
		Intent: contextfrag.IntentRunConfigPreProvider,
		Config: HistoryMessagesConfig{Messages: messages, TrimmablePrefix: len(messages)},
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return frags
}

func TestMarkInjectedLoopUserFragsLeavesNativeMediaPayloadsDroppable(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{
		imageUserMessage(64),
		imageUserMessage(64, sdk.TextPart{Text: "look at this"}),
		fileUserMessage(64),
		sdk.UserMessage("<message sender=\"alice\">injected</message>"),
		sdk.UserMessage("[Background tasks]\nCurrently running background tasks:\n- [task-1] build"),
	}
	marked := markInjectedLoopUserFrags(collectLoopFrags(t, messages))

	wantKinds := []contextfrag.Kind{
		contextfrag.KindNativeImage,
		contextfrag.KindNativeImage,
		contextfrag.KindNativeImage,
		contextfrag.KindInjectedMessage,
		contextfrag.KindBackgroundSummary,
	}
	for i, want := range wantKinds {
		if marked[i].Kind != want {
			t.Fatalf("frag %d kind = %q, want %q", i, marked[i].Kind, want)
		}
		wantKeep := want != contextfrag.KindNativeImage
		if got := marked[i].Budget.Overflow == contextfrag.OverflowKeep; got != wantKeep {
			t.Fatalf("frag %d overflow keep = %v, want %v", i, got, wantKeep)
		}
	}
}

func TestMarkInjectedLoopUserFragsUsesStrictBackgroundSummaryContract(t *testing.T) {
	t.Parallel()

	decorated := sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{
		Text:         "[Background tasks]\nCurrently running background tasks:\n- [task-1] build",
		CacheControl: &sdk.CacheControl{Type: "ephemeral"},
	}}}
	marked := markInjectedLoopUserFrags(collectLoopFrags(t, []sdk.Message{decorated}))

	// The agent's between-step removal only strips unadorned carriers, so a
	// decorated message must be treated as injected content, not as a summary.
	if marked[0].Kind != contextfrag.KindInjectedMessage {
		t.Fatalf("decorated frag kind = %q, want %q", marked[0].Kind, contextfrag.KindInjectedMessage)
	}
	if marked[0].Budget.Overflow != contextfrag.OverflowKeep {
		t.Fatalf("decorated frag overflow = %q, want keep", marked[0].Budget.Overflow)
	}
}

func TestStepReselectionDropsBulkyImagePayloadsUnderBudgetPressure(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := appendToolCycles(prefix, []string{"call-a"}, 800)
	messages = append(messages, imageUserMessage(40000), imageUserMessage(40000), imageUserMessage(40000))
	messages = appendToolCycles(messages, []string{"call-b"}, 800)
	messages = append(messages, sdk.UserMessage("<message sender=\"alice\" channel=\"telegram\">\ninjected instruction\n</message>"))

	budget := 500
	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:               contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount: len(prefix),
		Messages:            messages,
		BudgetMaxTokens:     budget,
	})
	if selection.Messages == nil || selection.Dropped == 0 {
		t.Fatalf("budget pressure must produce a reselection: %+v", selection)
	}
	if selectionHasImagePart(selection.Messages) {
		t.Fatal("bulky image payloads must stay droppable under step budget pressure")
	}
	if !selectionHasUserText(selection.Messages, "injected instruction") {
		t.Fatal("text injection must survive step budget pressure")
	}
	if !selection.MessageSourceIndexesKnown || len(selection.MessageSourceIndexes) != len(selection.Messages) {
		t.Fatalf("message provenance = %#v/%t, want one source per selected message", selection.MessageSourceIndexes, selection.MessageSourceIndexesKnown)
	}
	if selection.MessageSourceIndexes[0] != 0 {
		t.Fatalf("fixed prefix source = %d, want input index 0", selection.MessageSourceIndexes[0])
	}
	foundSynthetic := false
	for i, sourceIndex := range selection.MessageSourceIndexes {
		if sourceIndex < 0 {
			foundSynthetic = true
			continue
		}
		if sourceIndex >= len(messages) || !reflect.DeepEqual(selection.Messages[i], messages[sourceIndex]) {
			t.Fatalf("selected message %d source = %d, want exact input message", i, sourceIndex)
		}
	}
	if !foundSynthetic {
		t.Fatal("budget drop provenance omitted the synthetic trim notice")
	}
	loopEstimate := 0
	for _, msg := range selection.Messages[len(prefix):] {
		loopEstimate += contextfrag.ResolveProviderBudgetFragTokens(contextfrag.ContextFrag{
			Parts: []contextfrag.Part{{Type: contextfrag.PartSDKMessage, SDKMessage: &msg}},
		})
	}
	if loopEstimate > budget {
		t.Fatalf("loop span estimate = %d tokens, want within budget %d", loopEstimate, budget)
	}
}

func TestStepReselectionDropsBulkyFilePayloadsUnderBudgetPressure(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := appendToolCycles(prefix, []string{"call-a"}, 800)
	messages = append(messages, fileUserMessage(40000), fileUserMessage(40000), fileUserMessage(40000))
	messages = appendToolCycles(messages, []string{"call-b"}, 800)
	messages = append(messages, sdk.UserMessage("<message sender=\"alice\">injected instruction</message>"))

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:               contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount: len(prefix),
		Messages:            messages,
		BudgetMaxTokens:     500,
	})
	if selection.Messages == nil || selection.Dropped == 0 {
		t.Fatalf("budget pressure must produce a reselection: %+v", selection)
	}
	if selectionHasFilePart(selection.Messages) {
		t.Fatal("bulky file payloads must stay droppable under step budget pressure")
	}
	if !selectionHasUserText(selection.Messages, "injected instruction") {
		t.Fatal("text injection must survive step budget pressure")
	}
}

func selectionHasToolResult(messages []sdk.Message, callID string) bool {
	for _, msg := range messages {
		for _, part := range msg.Content {
			if result, ok := part.(sdk.ToolResultPart); ok && result.ToolCallID == callID {
				return true
			}
		}
	}
	return false
}

func TestStepReselectionBackgroundSummaryDoesNotShiftRecentAnchor(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := appendToolCycles(prefix, []string{"call-a", "call-b"}, 800)
	messages = append(messages, sdk.UserMessage("<message sender=\"alice\">injected request</message>"))
	messages = appendToolCycles(messages, []string{"call-c", "call-d"}, 800)
	messages = append(messages, sdk.UserMessage("[Background tasks]\nCurrently running background tasks:\n- [task-1] build (started 3s ago)"))

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:               contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount: len(prefix),
		Messages:            messages,
		// The injected request, its following tool work, the background
		// summary, and the trim notice are protected; older cycles must yield.
		BudgetMaxTokens: 700,
	})
	if selection.Messages == nil || selection.Dropped == 0 {
		t.Fatalf("budget pressure must drop loop span content: %+v", selection)
	}
	for _, callID := range []string{"call-c", "call-d"} {
		if !selectionHasToolResult(selection.Messages, callID) {
			t.Fatalf("tool work after the injected request must stay protected, lost %s", callID)
		}
	}
	if !selectionHasUserText(selection.Messages, "[Background tasks]") {
		t.Fatal("background summary message must survive reselection")
	}
}

func TestStepReselectionKeepsInjectedUserMessagesUnderBudgetPressure(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := appendToolCycles(prefix, []string{"call-a", "call-b"}, 800)
	messages = append(messages, sdk.UserMessage("<message sender=\"alice\" t=\"now\" channel=\"telegram\">\nfirst injected instruction\n</message>"))
	messages = appendToolCycles(messages, []string{"call-c", "call-d"}, 800)
	messages = append(messages, sdk.UserMessage("<message sender=\"alice\" t=\"later\" channel=\"telegram\">\nsecond injected instruction\n</message>"))

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:               contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount: len(prefix),
		Messages:            messages,
		BudgetMaxTokens:     200,
	})
	if selection.Messages == nil || selection.Dropped == 0 {
		t.Fatalf("budget pressure must drop loop span content: %+v", selection)
	}
	if !selectionHasUserText(selection.Messages, "first injected instruction") {
		t.Fatal("older injected user message must survive step budget pressure")
	}
	if !selectionHasUserText(selection.Messages, "second injected instruction") {
		t.Fatal("newest injected user message must survive step budget pressure")
	}
}

func TestStepReselectionFailsClosedWhenProtectedSuffixExceedsBudget(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := append([]sdk.Message(nil), prefix...)
	messages = append(messages, sdk.UserMessage(strings.Repeat("protected injected context ", 200)))

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:               contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount: len(prefix),
		Messages:            messages,
		BudgetMaxTokens:     20,
	})

	if !errors.Is(selection.FatalError, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("step selection error = %v, want %v", selection.FatalError, contextfrag.ErrProtectedContextOverflow)
	}
	if selection.Messages != nil {
		t.Fatalf("fatal step selection returned provider messages: %#v", selection.Messages)
	}
}
