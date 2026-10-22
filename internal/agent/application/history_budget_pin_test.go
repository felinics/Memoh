package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/chat/contextepoch"
	"github.com/felinics/memoh/internal/contextview"
)

// PR 3 acceptance (docs/design/cache-stable-context-rfc.md): the history
// budget is pinned inside an epoch. A much longer current request on a later
// turn must not shrink the history budget (rule 4), so the provider request
// prefix stays byte-identical across turns of a long session.
func TestChatHistoryBudgetPinnedAcrossTurnsWithGrowingInput(t *testing.T) {
	clock := time.Now()
	queries := &fakeContextEpochQueries{states: map[string]string{}}
	epochStore := contextepoch.NewStore(queries, nil)
	epochStore.SetClock(func() time.Time { return clock })

	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	fixture.service.contextEpochs = epochStore
	// Production installs the context-view compiler; the shared fixture's
	// agent keeps the legacy refresh path, so install it here for the budget
	// pin to reach the selector.
	fixture.service.agent = native.New(native.Deps{
		Logger:             fixture.service.logger,
		ContextViewApplier: contextview.ProviderRunConfigApplier(fixture.service.logger),
	})
	// Clamp the window so the per-turn feedback would bite without the pin:
	// turn 2's current request alone grows by ~1000 tokens.
	fixture.service.SetContextAbsoluteMaxTokens(8000)
	messages := &anchoredHistoryMessageService{recordingMessageService: fixture.messages}
	fixture.service.messageService = messages

	// ~1200 tokens of history: fits turn 1's computed budget, exceeds what
	// the per-turn feedback would leave for turn 2's much larger request.
	base := clock.Add(-time.Hour)
	for i := 1; i <= 6; i++ {
		user := persistedHistoryMessage(t, fmt.Sprintf("pin-user-%d", i), "user", strings.Repeat("u", 400))
		user.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		assistant := persistedHistoryMessage(t, fmt.Sprintf("pin-assistant-%d", i), "assistant", strings.Repeat("a", 400))
		assistant.CreatedAt = base.Add(time.Duration(i)*time.Minute + time.Second)
		messages.history = append(messages.history, user, assistant)
	}

	firstPrompt := "PRIVATE_PIN_FIRST_QUESTION"
	if _, err := fixture.service.Chat(context.Background(), ChatRequest{
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                firstPrompt,
		UserMessagePersisted: true,
	}); err != nil {
		t.Fatalf("first Chat() error = %v", err)
	}
	first := requestByNeedle(t, fixture, firstPrompt)

	// The first turn's computed history budget becomes the epoch pin.
	state := queries.state(lifecycleTestSessionID)
	if state.BudgetTokens <= 0 {
		t.Fatalf("epoch state after turn 1 = %+v, want a pinned history budget", state)
	}
	pinned := state.BudgetTokens

	userOne := persistedHistoryMessage(t, "pin-turn-1-user", "user", firstPrompt)
	userOne.CreatedAt = clock
	assistantOne := persistedHistoryMessage(t, "pin-turn-1-assistant", "assistant", "answer one")
	assistantOne.CreatedAt = clock.Add(time.Second)
	messages.history = append(messages.history, userOne, assistantOne)

	// Turn 2 carries a far larger current request. Without the pin the
	// history budget would shrink by its size and evict the oldest history.
	secondPrompt := "PRIVATE_PIN_SECOND_QUESTION " + strings.Repeat("detail ", 600)
	if _, err := fixture.service.Chat(context.Background(), ChatRequest{
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                secondPrompt,
		UserMessagePersisted: true,
	}); err != nil {
		t.Fatalf("second Chat() error = %v", err)
	}
	second := requestByNeedle(t, fixture, secondPrompt)

	prefixLen := len(first) - 1
	if len(second) != prefixLen+3 {
		t.Fatalf("second request has %d messages, want first-turn prefix (%d) + turn-1 exchange + current", len(second), prefixLen)
	}
	for i := 0; i < prefixLen; i++ {
		before, err := json.Marshal(first[i])
		if err != nil {
			t.Fatalf("marshal first[%d]: %v", i, err)
		}
		after, err := json.Marshal(second[i])
		if err != nil {
			t.Fatalf("marshal second[%d]: %v", i, err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("request prefix drifted under a larger current input at message %d:\nbefore %s\nafter  %s", i, before, after)
		}
	}

	// The pin held: same budget, no epoch advance, no reason change.
	state = queries.state(lifecycleTestSessionID)
	if state.BudgetTokens != pinned {
		t.Fatalf("pinned budget moved within the epoch: %d -> %d", pinned, state.BudgetTokens)
	}
	// The selector records the pin exactly once: turn 1 computed and pinned
	// the budget, turn 2 applied it. A second record attempt would prove the
	// pin was ignored and the budget recomputed per turn (the SQL guard would
	// reject the write, but the regression would stay invisible).
	if queries.pinBudgetCalls != 1 {
		t.Fatalf("pin record calls = %d, want 1 (turn 2 must reuse the pin, not recompute)", queries.pinBudgetCalls)
	}
	if state.Epoch != 1 || state.LastReason != contextepoch.ReasonInit {
		t.Fatalf("epoch advanced on a quiet turn: %+v", state)
	}
}
