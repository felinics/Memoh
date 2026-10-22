package application

import (
	"context"
	"errors"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/chat/contextepoch"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
)

type continuationTurnRequestResolverService struct {
	messagepkg.Service
	requestMessageID string
	sessionID        string
	runID            string
	rootTurnID       string
}

func (s *continuationTurnRequestResolverService) GetRunTurnRequestMessageID(_ context.Context, sessionID, runID, rootTurnID string) (string, error) {
	s.sessionID, s.runID, s.rootTurnID = sessionID, runID, rootTurnID
	return s.requestMessageID, nil
}

func TestContinuationTurnRequestMessageIDUsesAdmittedTurn(t *testing.T) {
	t.Parallel()

	messages := &continuationTurnRequestResolverService{
		requestMessageID: "request-message-id",
	}
	service := &Service{messageService: messages}
	got, err := service.continuationTurnRequestMessageID(
		context.Background(),
		"session-id",
		sessionruntime.RunHandle{RunID: "run-id", TurnID: "turn-id"},
	)
	if err != nil {
		t.Fatalf("continuationTurnRequestMessageID() error = %v", err)
	}
	if got != "request-message-id" {
		t.Fatalf("continuation request message ID = %q, want %q", got, "request-message-id")
	}
	if messages.sessionID != "session-id" || messages.runID != "run-id" || messages.rootTurnID != "turn-id" {
		t.Fatalf("continuation lookup omitted run/turn identity: %#v", messages)
	}
}

func TestPrepareContinuationRunConfigReplacesStaleContextAndSetsCapabilities(t *testing.T) {
	t.Parallel()

	resolver := &Service{userInput: &userinput.Service{}}
	eventCh := make(chan WSStreamEvent)
	staleIndex := 0
	staleMemoryIndex := 0
	base := native.RunConfig{
		Query:                          "stale query",
		Messages:                       []sdk.Message{sdk.UserMessage("stale context")},
		ContextCurrentUserMessageIndex: &staleIndex,
		ContextMemoryMessageIndex:      &staleMemoryIndex,
		ContextSourceFrags: []contextfrag.ContextFrag{{
			ID:   "stale-source-fragment",
			Kind: contextfrag.KindConversationEvent,
		}},
		ContextFrags: []contextfrag.ContextFrag{{
			ID:   "stale-fragment",
			Kind: contextfrag.KindConversationEvent,
		}},
	}

	got, err := resolver.prepareContinuationRunConfig(
		context.Background(),
		base,
		historyfrag.ScopeFallback{},
		contextfrag.Scope{},
		eventCh,
	)
	if err != nil {
		t.Fatalf("prepareContinuationRunConfig() error = %v", err)
	}
	if got.Query != "" || len(got.Messages) != 0 {
		t.Fatalf("continuation retained stale context: %#v", got)
	}
	for _, frag := range got.ContextFrags {
		if frag.ID == "stale-fragment" {
			t.Fatalf("continuation retained stale fragment: %#v", frag)
		}
	}
	if got.ContextCurrentUserMessageIndex != nil {
		t.Fatalf("continuation retained stale current-user index: %#v", got.ContextCurrentUserMessageIndex)
	}
	if got.ContextMemoryMessageIndex != nil {
		t.Fatalf("continuation retained stale memory index: %#v", got.ContextMemoryMessageIndex)
	}
	if got.ContextTrimmableMessages != len(got.Messages) || len(got.ContextHistoryTokenEstimates) != len(got.Messages) {
		t.Fatalf("budget signals = trimmable:%d estimates:%d messages:%d",
			got.ContextTrimmableMessages, len(got.ContextHistoryTokenEstimates), len(got.Messages))
	}
	for _, frag := range got.ContextSourceFrags {
		if frag.ID == "stale-source-fragment" {
			t.Fatalf("continuation retained stale source fragment: %#v", frag)
		}
	}
	if !got.LiveToolStream || !got.CanRequestUserInput {
		t.Fatalf("continuation capabilities = live:%v input:%v, want true/true", got.LiveToolStream, got.CanRequestUserInput)
	}
}

func TestPrepareContinuationRunConfigPropagatesArtifactProjectionFailure(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("artifact projection unavailable")
	resolver := &Service{queries: &recordingCompactionLogQueries{listErr: sentinel}}
	got, err := resolver.prepareContinuationRunConfig(
		context.Background(),
		native.RunConfig{Query: "must not survive"},
		historyfrag.ScopeFallback{},
		contextfrag.Scope{SessionID: "00000000-0000-0000-0000-00000000f401"},
		nil,
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("prepareContinuationRunConfig() error = %v, want %v", err, sentinel)
	}
	if got.Query != "" || len(got.Messages) != 0 || len(got.ContextFrags) != 0 {
		t.Fatalf("failed continuation returned partial config: %#v", got)
	}
}

func TestPendingRuntimeDecisionsFailsClosedOnRuntimeLookupFailure(t *testing.T) {
	sentinel := errors.New("session unavailable")
	service := &Service{queries: &abortLifecycleQueries{pending: true}, sessionService: &fakeBackgroundSessionService{getFn: func(context.Context, string) (sessionpkg.Thread, error) { return sessionpkg.Thread{}, sentinel }}}
	got, err := service.PendingRuntimeDecisions(t.Context(), "00000000-0000-0000-0000-000000000001")
	if !errors.Is(err, sentinel) || got != nil {
		t.Fatalf("runtime lookup error treated as native: %v %v", got, err)
	}
}

// Continuations (ask_user answers, tool approvals) rebuild the run config
// inside the same epoch: the pinned history budget must apply there exactly
// as at turn start (spec rule 4).
func TestPrepareContinuationRunConfigAppliesPinnedEpochBudget(t *testing.T) {
	t.Parallel()

	const sessionID = "33333333-3333-4333-8333-333333333333"
	queries := &fakeContextEpochQueries{states: map[string]string{}}
	queries.setState(sessionID, contextepoch.State{Epoch: 2, LastReason: contextepoch.ReasonBudget, BudgetTokens: 4000})
	resolver := &Service{
		userInput:     &userinput.Service{},
		contextEpochs: contextepoch.NewStore(queries, nil),
	}

	got, err := resolver.prepareContinuationRunConfig(
		context.Background(),
		native.RunConfig{},
		historyfrag.ScopeFallback{},
		contextfrag.Scope{SessionID: sessionID},
		nil,
	)
	if err != nil {
		t.Fatalf("prepareContinuationRunConfig() error = %v", err)
	}
	if got.PinnedHistoryBudgetTokens != 4000 {
		t.Fatalf("continuation PinnedHistoryBudgetTokens = %d, want the pinned 4000", got.PinnedHistoryBudgetTokens)
	}
	if got.RecordHistoryBudgetPin == nil {
		t.Fatal("continuation lost the pin-recording callback")
	}
}
