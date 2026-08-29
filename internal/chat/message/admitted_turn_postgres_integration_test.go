package message

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

// Admission allocates turn_id and turn_position before any message exists
// (SR-TURN-001, SR-DUR-002). Persisting the request message must file it under
// that turn rather than minting a second one, because the client was already
// told the first at run_accepted and history is read by turn.
func TestPostgresPersistHonoursAdmittedTurnIdentity(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)

	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))

	before := readNextTurnPosition(t, ctx, tx)
	admittedTurnID := "8f1f7c1e-6c3a-4a1e-9b2d-2f8a5c4d1e30"
	admittedRunID := "3b7d9a52-1f44-4c7e-8a91-6d5b2c0e7f18"
	position := before

	user, err := svc.Persist(ctx, PersistInput{
		BotID:        postgresMessageTestBotID,
		SessionID:    postgresMessageTestSessionID,
		Role:         "user",
		Content:      []byte(`{"role":"user","content":"admitted"}`),
		RunID:        admittedRunID,
		TurnID:       admittedTurnID,
		TurnPosition: &position,
	})
	if err != nil {
		t.Fatalf("persist admitted user turn: %v", err)
	}

	gotTurnID, gotPosition, gotRunID := readMessageTurn(t, ctx, tx, user.ID)
	if gotTurnID != admittedTurnID {
		t.Errorf("persisted turn_id = %q, want the admitted %q", gotTurnID, admittedTurnID)
	}
	if gotPosition != position {
		t.Errorf("persisted turn_position = %d, want the admitted %d", gotPosition, position)
	}
	if gotRunID != admittedRunID {
		t.Errorf("persisted run_id = %q, want %q", gotRunID, admittedRunID)
	}

	// Admission already advanced the counter when it drew the position, so
	// persisting must not advance it again: a second bump would leave a hole and
	// put the next turn one slot further out than the ledger believes.
	if after := readNextTurnPosition(t, ctx, tx); after != before {
		t.Errorf("persisting an admitted turn moved next_turn_position from %d to %d", before, after)
	}

	// The assistant reply binds to the same turn and carries the same run.
	assistant, err := svc.Persist(ctx, PersistInput{
		BotID:                postgresMessageTestBotID,
		SessionID:            postgresMessageTestSessionID,
		Role:                 "assistant",
		Content:              []byte(`{"role":"assistant","content":"reply"}`),
		RunID:                admittedRunID,
		TurnRequestMessageID: user.ID,
	})
	if err != nil {
		t.Fatalf("persist assistant reply: %v", err)
	}
	replyTurnID, replyPosition, replyRunID := readMessageTurn(t, ctx, tx, assistant.ID)
	if replyTurnID != admittedTurnID || replyPosition != position {
		t.Errorf("assistant turn = (%q,%d), want the admitted (%q,%d)", replyTurnID, replyPosition, admittedTurnID, position)
	}
	if replyRunID != admittedRunID {
		t.Errorf("assistant run_id = %q, want %q", replyRunID, admittedRunID)
	}
}

// The batched user -> assistant -> tool -> assistant round writes the whole turn
// in one statement, so it is a second place that could mint a turn id and spend
// a second position. It must reuse the admitted turn like the row-at-a-time path.
func TestPostgresToolTailRoundHonoursAdmittedTurnIdentity(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)

	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))

	before := readNextTurnPosition(t, ctx, tx)
	admittedTurnID := "1c2e4b60-77aa-45d2-9f31-0a7c6b8e5d42"
	admittedRunID := "9d0c3a17-45be-4f2a-8c66-1e4b7a9f2c53"
	position := before

	round := []PersistInput{
		{
			Role:         "user",
			Content:      []byte(`{"role":"user","content":"go"}`),
			RunID:        admittedRunID,
			TurnID:       admittedTurnID,
			TurnPosition: &position,
		},
		{Role: "assistant", Content: []byte(`{"role":"assistant","content":"calling"}`), RunID: admittedRunID},
		{Role: "tool", Content: []byte(`{"role":"tool","content":"result"}`), RunID: admittedRunID},
		{Role: "assistant", Content: []byte(`{"role":"assistant","content":"done"}`), RunID: admittedRunID},
	}
	for i := range round {
		round[i].BotID = postgresMessageTestBotID
		round[i].SessionID = postgresMessageTestSessionID
	}

	persisted, handled, err := svc.PersistToolTailRound(ctx, round)
	if err != nil {
		t.Fatalf("persist tool tail round: %v", err)
	}
	if !handled {
		t.Skip("store does not support the batched tool-tail round")
	}
	if len(persisted) != len(round) {
		t.Fatalf("persisted %d messages, want %d", len(persisted), len(round))
	}

	for i, msg := range persisted {
		turnID, gotPosition, runID := readMessageTurn(t, ctx, tx, msg.ID)
		if turnID != admittedTurnID {
			t.Errorf("round[%d] turn_id = %q, want the admitted %q", i, turnID, admittedTurnID)
		}
		if gotPosition != position {
			t.Errorf("round[%d] turn_position = %d, want %d", i, gotPosition, position)
		}
		if runID != admittedRunID {
			t.Errorf("round[%d] run_id = %q, want %q", i, runID, admittedRunID)
		}
	}
	if after := readNextTurnPosition(t, ctx, tx); after != before {
		t.Errorf("batched round moved next_turn_position from %d to %d", before, after)
	}
}

// Entry points with no admission — channel inbound and schedules —
// still allocate their own turn here, so the counter must advance for them.
func TestPostgresPersistAllocatesTurnWhenAdmissionSuppliedNone(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)

	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))

	before := readNextTurnPosition(t, ctx, tx)
	user, err := svc.Persist(ctx, PersistInput{
		BotID:     postgresMessageTestBotID,
		SessionID: postgresMessageTestSessionID,
		Role:      "user",
		Content:   []byte(`{"role":"user","content":"unadmitted"}`),
	})
	if err != nil {
		t.Fatalf("persist unadmitted user turn: %v", err)
	}
	turnID, position, runID := readMessageTurn(t, ctx, tx, user.ID)
	if turnID == "" {
		t.Error("unadmitted turn has no turn_id")
	}
	if position != before {
		t.Errorf("unadmitted turn_position = %d, want %d", position, before)
	}
	if runID != "" {
		t.Errorf("unadmitted turn carries run_id %q, want none", runID)
	}
	if after := readNextTurnPosition(t, ctx, tx); after != before+1 {
		t.Errorf("next_turn_position = %d, want %d", after, before+1)
	}
}

// A turn id without its position (or the reverse) is refused rather than
// half-applied: honouring one alone would either misorder the turn or take a
// slot under a name the client never saw.
func TestPersistRejectsPartialTurnIdentity(t *testing.T) {
	ctx := context.Background()
	svc := NewService(nil, nil)

	position := int64(3)
	if _, err := svc.preparePersistMessage(ctx, PersistInput{
		BotID:     postgresMessageTestBotID,
		SessionID: postgresMessageTestSessionID,
		Role:      "user",
		TurnID:    "8f1f7c1e-6c3a-4a1e-9b2d-2f8a5c4d1e30",
	}); err == nil {
		t.Error("turn id without a position was accepted")
	}
	if _, err := svc.preparePersistMessage(ctx, PersistInput{
		BotID:        postgresMessageTestBotID,
		SessionID:    postgresMessageTestSessionID,
		Role:         "user",
		TurnPosition: &position,
	}); err == nil {
		t.Error("turn position without an id was accepted")
	}
}

func readMessageTurn(t *testing.T, ctx context.Context, tx pgx.Tx, messageID string) (string, int64, string) {
	t.Helper()
	var turnID, runID *string
	var position *int64
	err := tx.QueryRow(ctx, `
		SELECT turn_id::text, turn_position, run_id::text
		FROM bot_history_messages
		WHERE id = $1::uuid`, messageID).Scan(&turnID, &position, &runID)
	if err != nil {
		t.Fatalf("read message turn: %v", err)
	}
	out := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	var pos int64
	if position != nil {
		pos = *position
	}
	return out(turnID), pos, out(runID)
}

func readNextTurnPosition(t *testing.T, ctx context.Context, tx pgx.Tx) int64 {
	t.Helper()
	var position int64
	if err := tx.QueryRow(ctx, `
		SELECT next_turn_position FROM bot_sessions WHERE id = $1::uuid`,
		postgresMessageTestSessionID).Scan(&position); err != nil {
		t.Fatalf("read next_turn_position: %v", err)
	}
	return position
}
