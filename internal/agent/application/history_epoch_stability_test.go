package application

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/chat/contextepoch"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// fakeContextEpochQueries is an in-memory contextepoch.Queries with the same
// monotonic guard as the SQL write: the epoch must strictly increase and the
// anchor may not move backward, otherwise the write is a stale no-op.
type fakeContextEpochQueries struct {
	mu             sync.Mutex
	pinBudgetCalls int
	states         map[string]string
}

func (f *fakeContextEpochQueries) GetSessionContextEpoch(_ context.Context, sessionID pgtype.UUID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[sessionID.String()], nil
}

func (f *fakeContextEpochQueries) AdvanceSessionContextEpoch(_ context.Context, arg sqlc.AdvanceSessionContextEpochParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := arg.SessionID.String()
	if existing := f.states[key]; existing != "" {
		var stored, next struct {
			Epoch             int64 `json:"epoch"`
			AnchorCreatedAtMs int64 `json:"anchor_created_at_ms"`
		}
		if err := json.Unmarshal([]byte(existing), &stored); err != nil {
			return "", err
		}
		if err := json.Unmarshal(arg.State, &next); err != nil {
			return "", err
		}
		if stored.Epoch >= next.Epoch || stored.AnchorCreatedAtMs > next.AnchorCreatedAtMs {
			return "", pgx.ErrNoRows
		}
	}
	f.states[key] = string(arg.State)
	return string(arg.State), nil
}

func (f *fakeContextEpochQueries) PinSessionContextEpochBudget(_ context.Context, arg sqlc.PinSessionContextEpochBudgetParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pinBudgetCalls++
	key := arg.SessionID.String()
	existing := f.states[key]
	if existing == "" {
		return "", pgx.ErrNoRows
	}
	var stored struct {
		BudgetTokens int64 `json:"budget_tokens"`
	}
	if err := json.Unmarshal([]byte(existing), &stored); err != nil {
		return "", err
	}
	if stored.BudgetTokens > 0 {
		return "", pgx.ErrNoRows
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(existing), &raw); err != nil {
		return "", err
	}
	raw["budget_tokens"] = arg.BudgetTokens
	out, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	f.states[key] = string(out)
	return string(out), nil
}

func (f *fakeContextEpochQueries) PinSessionContextEpochPrefixHash(_ context.Context, arg sqlc.PinSessionContextEpochPrefixHashParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := arg.SessionID.String()
	existing := f.states[key]
	if existing == "" {
		return "", pgx.ErrNoRows
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(existing), &raw); err != nil {
		return "", err
	}
	if pinned, _ := raw["prefix_hash"].(string); pinned != "" {
		return "", pgx.ErrNoRows
	}
	raw["prefix_hash"] = arg.PrefixHash
	out, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	f.states[key] = string(out)
	return string(out), nil
}

func (f *fakeContextEpochQueries) PinSessionContextEpochModelIdentity(_ context.Context, arg sqlc.PinSessionContextEpochModelIdentityParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := arg.SessionID.String()
	existing := f.states[key]
	if existing == "" {
		return "", pgx.ErrNoRows
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(existing), &raw); err != nil {
		return "", err
	}
	if pinned, _ := raw["model_identity"].(string); pinned != "" {
		return "", pgx.ErrNoRows
	}
	raw["model_identity"] = arg.ModelIdentity
	out, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	f.states[key] = string(out)
	return string(out), nil
}

func (f *fakeContextEpochQueries) setState(sessionID string, state contextepoch.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := state.Encode()
	if err != nil {
		panic(err)
	}
	f.states[sessionID] = string(raw)
}

func (f *fakeContextEpochQueries) state(sessionID string) contextepoch.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw := f.states[sessionID]
	var state contextepoch.State
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			panic(err)
		}
	}
	return state
}

// anchoredHistoryMessageService honors the anchor the loader passes: only
// messages at or after since are visible. A wall-clock regression (loading
// from now − 24h) is caught immediately, because the seeded history is older
// than 24 hours even at the start of the test.
type anchoredHistoryMessageService struct {
	*recordingMessageService
	history []messagepkg.Message
}

func (s *anchoredHistoryMessageService) ListActiveSinceBySessionWithinBytes(_ context.Context, _ string, since time.Time, _ int64) ([]messagepkg.Message, error) {
	out := make([]messagepkg.Message, 0, len(s.history))
	for _, msg := range s.history {
		if !msg.CreatedAt.Before(since) {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (s *anchoredHistoryMessageService) MeasureActiveBySession(_ context.Context, _ string, since time.Time) (messagepkg.ActiveMessagesMeasure, error) {
	measure := messagepkg.ActiveMessagesMeasure{}
	for _, msg := range s.history {
		if !msg.CreatedAt.Before(since) {
			measure.MessageCount++
			measure.ContentBytes += int64(len(msg.Content))
		}
	}
	return measure, nil
}

func requestByNeedle(t *testing.T, fixture directLifecycleFixture, needle string) []chatCompletionsMessage {
	t.Helper()
	for _, body := range fixture.requests.all() {
		if !bytes.Contains(body, []byte(needle)) {
			continue
		}
		var request struct {
			Messages []chatCompletionsMessage `json:"messages"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		return request.Messages
	}
	t.Fatalf("provider never received a request containing %q", needle)
	return nil
}

// TestChatHistoryPrefixStableAcross24hWithAnchoredEpoch is the PR 1 acceptance
// test of the Context Epoch RFC (docs/design/cache-stable-context-rfc.md, D1):
// with the wall clock out of the load path, a session whose history is older
// than the retired 24-hour window keeps a byte-identical provider request
// prefix across turns, no matter how much time passes between them.
func TestChatHistoryPrefixStableAcross24hWithAnchoredEpoch(t *testing.T) {
	clock := time.Now()
	queries := &fakeContextEpochQueries{states: map[string]string{}}
	epochStore := contextepoch.NewStore(queries, nil)
	epochStore.SetClock(func() time.Time { return clock })

	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	fixture.service.contextEpochs = epochStore
	messages := &anchoredHistoryMessageService{recordingMessageService: fixture.messages}
	fixture.service.messageService = messages

	// Seeded history starts 48 hours before the first turn — already outside
	// the retired 24-hour window at turn 1.
	base := clock.Add(-48 * time.Hour)
	messages.history = persistedToolTurn(t, 1, base)

	firstPrompt := "PRIVATE_EPOCH_FIRST_QUESTION"
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
	historyStart(t, first)

	// The first turn's exchange lands in history; the wall clock jumps 25
	// hours — past the retired window — before the second turn.
	userOne := persistedHistoryMessage(t, "epoch-turn-1-user", "user", firstPrompt)
	userOne.CreatedAt = clock
	assistantOne := persistedHistoryMessage(t, "epoch-turn-1-assistant", "assistant", "answer one")
	assistantOne.CreatedAt = clock.Add(time.Second)
	messages.history = append(messages.history, userOne, assistantOne)

	clock = clock.Add(25 * time.Hour)

	secondPrompt := "PRIVATE_EPOCH_SECOND_QUESTION"
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

	// The second request must extend the first: everything the first turn
	// sent except its own current user message is a byte-identical prefix.
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
			t.Fatalf("request prefix drifted at message %d across the 24h boundary:\nbefore %s\nafter  %s", i, before, after)
		}
	}

	// The budget never bound, so the epoch holds its initial state: no
	// recorded advance, anchor still at the beginning.
	state := queries.state(lifecycleTestSessionID)
	if state.Epoch != 1 || state.LastReason != contextepoch.ReasonInit || state.AnchorCreatedAtMs != 0 {
		t.Fatalf("epoch state = %+v, want the initial epoch-1 state with no advance", state)
	}
}

// budgetedVisibleWindowService records the byte budget the required-history
// window is loaded with (CM-ADM-001).
type budgetedVisibleWindowService struct {
	recordingMessageService
	maxBytes int64
	window   []messagepkg.Message
}

func (s *budgetedVisibleWindowService) ListVisibleFromBySessionWithinBytes(_ context.Context, _, _ string, maxBytes int64) ([]messagepkg.Message, error) {
	s.maxBytes = maxBytes
	return append([]messagepkg.Message(nil), s.window...), nil
}

func TestEnsureRequiredHistoryMessageBudgetsTheVisibleWindow(t *testing.T) {
	t.Parallel()

	required := persistedHistoryMessage(t, "user-required", "user", "retry me")
	service := &budgetedVisibleWindowService{window: []messagepkg.Message{required}}
	resolver := &Service{messageService: service}

	got, err := resolver.ensureRequiredHistoryMessage(context.Background(), nil, ChatRequest{
		ChatID:                   "bot-1",
		ThreadID:                 "session-1",
		RequiredHistoryMessageID: "user-required",
	}, 1000)
	if err != nil {
		t.Fatalf("ensureRequiredHistoryMessage() error = %v", err)
	}
	if len(got) != 1 || !got[0].Required {
		t.Fatalf("got %v, want the required message marked", historyRecordIDs(got))
	}
	wantBytes := int64(1000) * contextfrag.EstimateBytesPerToken
	if service.maxBytes != wantBytes {
		t.Fatalf("visible window maxBytes = %d, want the budgeted %d", service.maxBytes, wantBytes)
	}

	// Without a per-turn budget the load falls back to the absolute cap —
	// the window is never unbounded (CM-ADM-001).
	service.maxBytes = 0
	if _, err := resolver.ensureRequiredHistoryMessage(context.Background(), nil, ChatRequest{
		ChatID:                   "bot-1",
		ThreadID:                 "session-1",
		RequiredHistoryMessageID: "user-required",
	}, 0); err != nil {
		t.Fatalf("ensureRequiredHistoryMessage() with zero budget error = %v", err)
	}
	if service.maxBytes <= 0 {
		t.Fatalf("zero token budget must still load with the absolute byte cap, got %d", service.maxBytes)
	}
}
