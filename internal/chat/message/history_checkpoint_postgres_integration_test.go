package message

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

// testCheckpointItem is a row a test checkpoint keeps by reference or a
// summary it holds.
type testCheckpointItem struct {
	row, summary string
}

// testCheckpoint is a history checkpoint of the fixture session.
type testCheckpoint struct {
	parent   string
	position HistoryPosition
	items    []testCheckpointItem
}

// placeTestCheckpoint writes the checkpoint and makes it the fixture
// session's active one in place of its parent.
func placeTestCheckpoint(t *testing.T, ctx context.Context, tx pgx.Tx, c testCheckpoint) string {
	t.Helper()
	items := make([]map[string]string, 0, len(c.items))
	for _, item := range c.items {
		if item.row != "" {
			items = append(items, map[string]string{"row": item.row})
		} else {
			items = append(items, map[string]string{"summary": item.summary})
		}
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	params := dbsqlc.CreateHistoryCheckpointParams{
		ParentID:       dbpkg.ParseUUIDOrEmpty(c.parent),
		TurnPosition:   c.position.Turn,
		TurnMessageSeq: c.position.Seq,
		Items:          encoded,
		SessionID:      mustTestUUID(t, postgresMessageTestSessionID),
	}
	queries := dbsqlc.New(tx)
	checkpoint, err := queries.CreateHistoryCheckpoint(ctx, params)
	if err != nil {
		t.Fatalf("create history checkpoint: %v", err)
	}
	moved, err := queries.SetHistoryCheckpoint(ctx, dbsqlc.SetHistoryCheckpointParams{
		CheckpointID:         checkpoint.ID,
		SessionID:            params.SessionID,
		ExpectedCheckpointID: params.ParentID,
	})
	if err != nil || moved != 1 {
		t.Fatalf("activate history checkpoint: moved=%d err=%v", moved, err)
	}
	return checkpoint.ID.String()
}

// activeTestCheckpoint is the fixture session's active checkpoint, "" when
// its whole history is context.
func activeTestCheckpoint(t *testing.T, ctx context.Context, tx pgx.Tx) string {
	t.Helper()
	var active string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(history_checkpoint_id::text, '') FROM bot_sessions WHERE id = $1`, postgresMessageTestSessionID).Scan(&active); err != nil {
		t.Fatalf("read active history checkpoint: %v", err)
	}
	return active
}

// insertTestSummary commits an ok summary of the fixture session.
func insertTestSummary(t *testing.T, ctx context.Context, tx pgx.Tx, summary string) string {
	t.Helper()
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO bot_history_message_compacts (bot_id, session_id, status, summary, completed_at)
		VALUES ($1, $2, 'ok', $3, now())
		RETURNING id::text`, postgresMessageTestBotID, postgresMessageTestSessionID, summary).Scan(&id); err != nil {
		t.Fatalf("insert summary: %v", err)
	}
	return id
}

func testRowPosition(t *testing.T, ctx context.Context, svc *DBService, row Message) HistoryPosition {
	t.Helper()
	position, err := svc.HistoryPositionByID(ctx, postgresMessageTestSessionID, row.ID)
	if err != nil {
		t.Fatalf("read the position of %s: %v", row.ID, err)
	}
	return position
}

type rewindFixture struct {
	earlier, latest                            HistoryTurn
	latestRequest, earlierAnswer, latestAnswer string
	// earlierCheckpoint's summary folds the earlier turn; the rolling one,
	// on top of it, folds the latest turn as well, as a run that compacted
	// its own turn leaves them.
	earlierCheckpoint, rollingCheckpoint string
}

func setupRewindFixture(t *testing.T, ctx context.Context, tx pgx.Tx) rewindFixture {
	t.Helper()
	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	_, earlierAnswer, earlier := persistPostgresTurn(t, ctx, svc)
	latestUser, latestAnswer, latest := persistPostgresTurn(t, ctx, svc)
	f := rewindFixture{
		earlier: earlier, latest: latest, latestRequest: latestUser.ID,
		earlierAnswer: earlierAnswer.ID, latestAnswer: latestAnswer.ID,
	}
	f.earlierCheckpoint = placeTestCheckpoint(t, ctx, tx, testCheckpoint{
		position: testRowPosition(t, ctx, svc, earlierAnswer),
		items:    []testCheckpointItem{{summary: insertTestSummary(t, ctx, tx, "earlier summary")}},
	})
	f.rollingCheckpoint = placeTestCheckpoint(t, ctx, tx, testCheckpoint{
		parent:   f.earlierCheckpoint,
		position: testRowPosition(t, ctx, svc, latestAnswer),
		items:    []testCheckpointItem{{summary: insertTestSummary(t, ctx, tx, "rolling summary")}},
	})
	return f
}

// Replacing or hiding a turn, or deleting a row of it, moves the session back
// to the newest checkpoint that does not depend on the change, and so does an
// edit or retry that compacts before its first step. A checkpoint that holds
// a summary depends on every row at or before its position: a change to the
// latest turn moves the session from the rolling summary back to the earlier
// one, which ends before the turn, and a change to the earlier turn leaves no
// checkpoint at all. Linking an asset to a row changes no context row and
// moves nothing. The summaries stay either way; the checkpoints the session
// moves back past go, since each depends on a row the change removed.
func TestPostgresHistoryMutationsRewindTheHistoryCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		// mutate changes the earlier turn when early, else the latest one.
		early  bool
		mutate func(context.Context, pgx.Tx, rewindFixture, HistoryTurn) error
		// want names the checkpoint the session is left on.
		want func(rewindFixture) string
	}{
		{name: "supersede turn", want: earlierCheckpoint, mutate: func(ctx context.Context, tx pgx.Tx, _ rewindFixture, turn HistoryTurn) error {
			_, err := dbsqlc.New(tx).SupersedeHistoryTurn(ctx, dbsqlc.SupersedeHistoryTurnParams{
				SupersededByTurnID: mustTestUUID(t, uuid.NewString()),
				SupersededAt:       pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
				SupersededReason:   pgtype.Text{String: "test", Valid: true},
				OldTurnID:          mustTestUUID(t, turn.ID),
				SessionID:          mustTestUUID(t, postgresMessageTestSessionID),
			})
			return err
		}},
		{name: "hide turn", want: earlierCheckpoint, mutate: func(ctx context.Context, tx pgx.Tx, _ rewindFixture, turn HistoryTurn) error {
			return dbsqlc.New(tx).HideMessagesByHistoryTurn(ctx, mustTestUUID(t, turn.ID))
		}},
		{name: "retry turn", want: earlierCheckpoint, mutate: func(ctx context.Context, tx pgx.Tx, f rewindFixture, turn HistoryTurn) error {
			replacement, err := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx))).Persist(ctx, PersistInput{
				BotID: postgresMessageTestBotID, SessionID: postgresMessageTestSessionID, Role: "assistant",
				Content: []byte(`{"role":"assistant","content":"retried answer"}`), SkipHistoryTurn: true,
			})
			if err != nil {
				return err
			}
			_, err = dbsqlc.New(tx).ReplaceHistoryTurn(ctx, dbsqlc.ReplaceHistoryTurnParams{
				OldTurnID:           mustTestUUID(t, turn.ID),
				SessionID:           mustTestUUID(t, postgresMessageTestSessionID),
				ReplacementTurnID:   mustTestUUID(t, uuid.NewString()),
				ReplacementPosition: turn.Position + 1,
				RequestMessageID:    mustTestUUID(t, f.latestRequest),
				AssistantMessageID:  mustTestUUID(t, replacement.ID),
				SupersededAt:        pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
				SupersededReason:    pgtype.Text{String: "retry", Valid: true},
			})
			return err
		}},
		{name: "bound retry", want: earlierCheckpoint, mutate: func(ctx context.Context, tx pgx.Tx, _ rewindFixture, turn HistoryTurn) error {
			// A retried run writes its own copy of the input as the new
			// turn's request before it selects the replacement.
			position := turn.Position + 1
			replacementTurnID := uuid.NewString()
			if _, err := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx))).Persist(ctx, PersistInput{
				BotID: postgresMessageTestBotID, SessionID: postgresMessageTestSessionID, Role: "user",
				Content: []byte(`{"role":"user","content":"hello"}`), TurnID: replacementTurnID, TurnPosition: &position,
			}); err != nil {
				return err
			}
			_, err := dbsqlc.New(tx).SelectBoundHistoryTurnReplacement(ctx, dbsqlc.SelectBoundHistoryTurnReplacementParams{
				BotID: mustTestUUID(t, postgresMessageTestBotID), SessionID: mustTestUUID(t, postgresMessageTestSessionID),
				OldTurnID: mustTestUUID(t, turn.ID), ReplacementTurnID: mustTestUUID(t, replacementTurnID),
				ReplacementPosition: position, Reason: pgtype.Text{String: "retry", Valid: true},
			})
			return err
		}},
		{name: "compact before a replacement's first step", want: earlierCheckpoint, mutate: func(ctx context.Context, tx pgx.Tx, _ rewindFixture, turn HistoryTurn) error {
			rewound, err := dbsqlc.New(tx).RewindHistoryCheckpointBeforeTurn(ctx, dbsqlc.RewindHistoryCheckpointBeforeTurnParams{
				SessionID: mustTestUUID(t, postgresMessageTestSessionID), BotID: mustTestUUID(t, postgresMessageTestBotID),
				TurnID: mustTestUUID(t, turn.ID),
			})
			if err == nil && !rewound {
				err = errors.New("the active checkpoint depends on the turn, but the session was not rewound")
			}
			return err
		}},
		{name: "delete a row", want: earlierCheckpoint, mutate: func(ctx context.Context, tx pgx.Tx, f rewindFixture, _ HistoryTurn) error {
			return dbsqlc.New(tx).DeleteMessagesByIDs(ctx, []pgtype.UUID{mustTestUUID(t, f.latestAnswer)})
		}},
		{name: "link an asset", want: rollingCheckpoint, mutate: func(ctx context.Context, tx pgx.Tx, f rewindFixture, _ HistoryTurn) error {
			_, err := dbsqlc.New(tx).CreateMessageAsset(ctx, dbsqlc.CreateMessageAssetParams{
				MessageID: mustTestUUID(t, f.latestAnswer), Role: "attachment", ContentHash: "sha256:rewind-asset",
				Name: "note.txt", Metadata: []byte("{}"),
			})
			return err
		}},
		{name: "hide an earlier turn", early: true, want: noCheckpoint, mutate: func(ctx context.Context, tx pgx.Tx, _ rewindFixture, turn HistoryTurn) error {
			return dbsqlc.New(tx).HideMessagesByHistoryTurn(ctx, mustTestUUID(t, turn.ID))
		}},
		{name: "delete an earlier row", early: true, want: noCheckpoint, mutate: func(ctx context.Context, tx pgx.Tx, f rewindFixture, _ HistoryTurn) error {
			return dbsqlc.New(tx).DeleteMessagesByIDs(ctx, []pgtype.UUID{mustTestUUID(t, f.earlierAnswer)})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tx := beginPostgresMessageTestTx(t, ctx)
			setupPostgresMessageTestFixtures(t, ctx, tx)
			f := setupRewindFixture(t, ctx, tx)

			turn := f.latest
			if tc.early {
				turn = f.earlier
			}
			if err := tc.mutate(ctx, tx, f, turn); err != nil {
				t.Fatalf("mutate history: %v", err)
			}
			if got, want := activeTestCheckpoint(t, ctx, tx), tc.want(f); got != want {
				t.Fatalf("active checkpoint = %q, want %q (earlier %s, rolling %s)", got, want, f.earlierCheckpoint, f.rollingCheckpoint)
			}
			var checkpoints, summaries int
			if err := tx.QueryRow(ctx, `
				SELECT
				  (SELECT count(*) FROM bot_history_checkpoints WHERE session_id = $1),
				  (SELECT count(*) FROM bot_history_message_compacts WHERE session_id = $1 AND status = 'ok')
			`, postgresMessageTestSessionID).Scan(&checkpoints, &summaries); err != nil {
				t.Fatal(err)
			}
			// The session's checkpoint and its ancestors remain.
			remaining := map[string]int{f.rollingCheckpoint: 2, f.earlierCheckpoint: 1, "": 0}[tc.want(f)]
			if checkpoints != remaining || summaries != 2 {
				t.Fatalf("checkpoints = %d, summaries = %d, want %d checkpoints and both summaries kept", checkpoints, summaries, remaining)
			}
		})
	}
}

func earlierCheckpoint(f rewindFixture) string { return f.earlierCheckpoint }
func rollingCheckpoint(f rewindFixture) string { return f.rollingCheckpoint }
func noCheckpoint(rewindFixture) string        { return "" }

// historyFixture is six turns of two rows each, created a second apart, each
// user row recording the inbound message inbound-<turn>.
type historyFixture struct {
	turns          []HistoryTurn
	users, answers []Message
	positions      map[string]HistoryPosition
	created        map[string]time.Time
}

func (f historyFixture) at(row Message) time.Time             { return f.created[row.ID] }
func (f historyFixture) position(row Message) HistoryPosition { return f.positions[row.ID] }

func setupHistoryFixture(t *testing.T, ctx context.Context, tx pgx.Tx) historyFixture {
	t.Helper()
	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	f := historyFixture{positions: map[string]HistoryPosition{}, created: map[string]time.Time{}}
	for i := range 6 {
		user, answer, turn := persistPostgresTurn(t, ctx, svc)
		f.users, f.answers, f.turns = append(f.users, user), append(f.answers, answer), append(f.turns, turn)
		for j, row := range []Message{user, answer} {
			at := base.Add(time.Duration(2*i+j) * time.Second)
			f.created[row.ID] = at
			if _, err := tx.Exec(ctx, `UPDATE bot_history_messages SET created_at = $2 WHERE id = $1`, row.ID, at); err != nil {
				t.Fatal(err)
			}
			f.positions[row.ID] = testRowPosition(t, ctx, svc, row)
		}
		if _, err := tx.Exec(ctx, `UPDATE bot_history_messages SET source_message_id = $2 WHERE id = $1`, user.ID, fmt.Sprintf("inbound-%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// summaryThrough is a checkpoint after parent whose summary folds the turns
// after parent through last, as compaction commits one, with the rows it
// keeps by reference before its summary.
func (f historyFixture) summaryThrough(t *testing.T, ctx context.Context, tx pgx.Tx, parent string, last int, kept ...testCheckpointItem) testCheckpoint {
	t.Helper()
	kept = append(kept, testCheckpointItem{summary: insertTestSummary(t, ctx, tx, fmt.Sprintf("through turn %d", last+1))})
	return testCheckpoint{parent: parent, position: f.position(f.answers[last]), items: kept}
}

// checkpointStack names the checkpoints a test stacks on a session.
type checkpointStack struct{ base, summary string }

// A checkpoint depends on every row at or before its position, which its
// summary may have read, so removing one of them moves the session back to
// the newest checkpoint before it; a change after the checkpoint leaves it
// standing. A row it keeps by reference is one of those rows.
func TestPostgresRewindKeepsACheckpointThatDoesNotDependOnTheChange(t *testing.T) {
	for _, tc := range []struct {
		name string
		// place stacks the session's checkpoints.
		place  func(*testing.T, context.Context, pgx.Tx, historyFixture) checkpointStack
		mutate func(*testing.T, context.Context, pgx.Tx, historyFixture) error
		want   func(checkpointStack) string
	}{
		{
			name:   "a row both summaries stand for goes",
			place:  placeSummaryOverBase,
			mutate: deleteTestRow(func(f historyFixture) Message { return f.answers[1] }),
			want:   func(checkpointStack) string { return "" },
		},
		{
			name:   "a turn only the later summary folds is hidden",
			place:  placeSummaryOverBase,
			mutate: hideTestTurn(4),
			want:   func(s checkpointStack) string { return s.base },
		},
		{
			name:   "a row after the summary goes",
			place:  placeSummaryOverBase,
			mutate: deleteTestRow(func(f historyFixture) Message { return f.answers[5] }),
			want:   func(s checkpointStack) string { return s.summary },
		},
		{
			name: "a kept row goes",
			place: func(t *testing.T, ctx context.Context, tx pgx.Tx, f historyFixture) checkpointStack {
				return checkpointStack{base: placeTestCheckpoint(t, ctx, tx, f.summaryThrough(t, ctx, tx, "", 3, testCheckpointItem{row: f.users[2].ID}))}
			},
			mutate: deleteTestRow(func(f historyFixture) Message { return f.users[2] }),
			want:   func(checkpointStack) string { return "" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tx := beginPostgresMessageTestTx(t, ctx)
			setupPostgresMessageTestFixtures(t, ctx, tx)
			f := setupHistoryFixture(t, ctx, tx)
			placed := tc.place(t, ctx, tx, f)
			if err := tc.mutate(t, ctx, tx, f); err != nil {
				t.Fatalf("mutate history: %v", err)
			}
			if got, want := activeTestCheckpoint(t, ctx, tx), tc.want(placed); got != want {
				t.Fatalf("active checkpoint = %q, want %q (placed %+v)", got, want, placed)
			}
		})
	}
}

// placeSummaryOverBase places a checkpoint whose summary folds turns 1-4 and
// one above it whose summary folds turn 5.
func placeSummaryOverBase(t *testing.T, ctx context.Context, tx pgx.Tx, f historyFixture) checkpointStack {
	placed := checkpointStack{base: placeTestCheckpoint(t, ctx, tx, f.summaryThrough(t, ctx, tx, "", 3))}
	placed.summary = placeTestCheckpoint(t, ctx, tx, f.summaryThrough(t, ctx, tx, placed.base, 4))
	return placed
}

func deleteTestRow(row func(historyFixture) Message) func(*testing.T, context.Context, pgx.Tx, historyFixture) error {
	return func(t *testing.T, ctx context.Context, tx pgx.Tx, f historyFixture) error {
		return dbsqlc.New(tx).DeleteMessagesByIDs(ctx, []pgtype.UUID{mustTestUUID(t, row(f).ID)})
	}
}

func hideTestTurn(turn int) func(*testing.T, context.Context, pgx.Tx, historyFixture) error {
	return func(t *testing.T, ctx context.Context, tx pgx.Tx, f historyFixture) error {
		return dbsqlc.New(tx).HideMessagesByHistoryTurn(ctx, mustTestUUID(t, f.turns[turn].ID))
	}
}

// What the active checkpoint stands for reaches no read path: the history
// load reads the rows after its position and the rows it keeps by reference,
// the turn responses read what comes after it, compaction
// reads those rows as its candidates, and the Discuss event replay leaves out
// the messages it folds. Clearing the history drops the session's
// checkpoints.
func TestPostgresHistoryCheckpointBoundsEveryReadPath(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)
	f := setupHistoryFixture(t, ctx, tx)
	q := dbsqlc.New(tx)
	svc := NewService(nil, postgresstore.NewQueries(q))
	sessionID := mustTestUUID(t, postgresMessageTestSessionID)

	for _, event := range []struct {
		id string
		at time.Time
	}{
		{"inbound-1", f.at(f.users[0])},
		{"inbound-3", f.at(f.users[2])},
		{"inbound-4", f.at(f.users[3])},
		{"inbound-6", f.at(f.users[5])},
		// Events without a context row: one received while turn 2 was
		// answered, one while turn 4 was.
		{"orphan-early", f.at(f.users[1]).Add(500 * time.Millisecond)},
		{"orphan-late", f.at(f.users[3]).Add(500 * time.Millisecond)},
	} {
		if _, err := q.CreateSessionEvent(ctx, dbsqlc.CreateSessionEventParams{
			BotID: mustTestUUID(t, postgresMessageTestBotID), SessionID: sessionID,
			EventKind: "message", EventData: []byte(`{}`), ExternalMessageID: pgtype.Text{String: event.id, Valid: true},
			ReceivedAtMs: event.at.UnixMilli(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(messages []Message) []string {
		out := make([]string, 0, len(messages))
		for _, message := range messages {
			out = append(out, message.ID)
		}
		return out
	}
	rowIDs := func(rows ...Message) []string { return ids(rows) }
	type readPaths struct {
		history, responses, candidates, replay []string
		kept                                   map[string]bool
	}
	read := func(after HistoryPosition, keptRows []string) readPaths {
		t.Helper()
		var got readPaths
		history, err := svc.ListHistoryWithinBytes(ctx, postgresMessageTestSessionID, HistoryWindow{After: after, MaxBytes: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		byID, err := svc.ListHistoryByIDs(ctx, postgresMessageTestSessionID, keptRows)
		if err != nil {
			t.Fatal(err)
		}
		got.history = append(ids(byID), ids(history.Messages)...)
		responses, err := svc.ListTurnResponseSourcesWithinBytes(ctx, postgresMessageTestSessionID, after, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		got.responses = ids(responses.Messages)
		pgKept := make([]pgtype.UUID, 0, len(keptRows))
		for _, id := range keptRows {
			pgKept = append(pgKept, mustTestUUID(t, id))
		}
		candidates, err := q.ListHistoryCheckpointCandidatesWithinBytes(ctx, dbsqlc.ListHistoryCheckpointCandidatesWithinBytesParams{
			SessionID: sessionID, AfterTurnPosition: after.Turn, AfterTurnMessageSeq: after.Seq, KeptIds: pgKept, MaxBytes: 1 << 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		counted, err := q.CountHistoryCheckpointCandidates(ctx, dbsqlc.CountHistoryCheckpointCandidatesParams{
			SessionID: sessionID, AfterTurnPosition: after.Turn, AfterTurnMessageSeq: after.Seq, KeptIds: pgKept,
		})
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(candidates)) != counted {
			t.Fatalf("listed %d candidates, counted %d", len(candidates), counted)
		}
		for _, candidate := range candidates {
			got.candidates = append(got.candidates, candidate.ID.String())
		}
		events, err := q.ListSessionEventsBySessionPageBeforeWithinBytes(ctx, dbsqlc.ListSessionEventsBySessionPageBeforeWithinBytesParams{
			AfterTurnPosition: after.Turn, AfterTurnMessageSeq: after.Seq, KeptIds: pgKept, SessionID: sessionID,
			MaxBytes: 1 << 20, PageSize: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			got.replay = append(got.replay, event.ExternalMessageID.String)
		}
		slices.Sort(got.replay)
		classified, err := q.ClassifyHistoryCheckpointMessages(ctx, dbsqlc.ClassifyHistoryCheckpointMessagesParams{
			AfterTurnPosition: after.Turn, AfterTurnMessageSeq: after.Seq, KeptIds: pgKept, SessionID: sessionID,
			ExternalMessageIds: []string{"inbound-1", "inbound-2", "inbound-3", "inbound-4", "inbound-5", "inbound-6", "orphan-late"},
		})
		if err != nil {
			t.Fatal(err)
		}
		got.kept = map[string]bool{}
		for _, row := range classified {
			got.kept[row.ExternalMessageID] = row.Kept
		}
		return got
	}

	var all []Message
	for i := range f.users {
		all = append(all, f.users[i], f.answers[i])
	}
	without := read(HistoryPosition{}, nil)
	if !slices.Equal(without.history, ids(all)) || !slices.Equal(without.responses, ids(all)) || !slices.Equal(without.candidates, ids(all)) {
		t.Fatalf("without a checkpoint: history=%d responses=%d candidates=%d, want all 12 rows each", len(without.history), len(without.responses), len(without.candidates))
	}
	if len(without.replay) != 6 {
		t.Fatalf("replay without a checkpoint = %v, want every event", without.replay)
	}

	// A summary folds turns 1-4 but keeps the input of turn 3 by reference.
	placeTestCheckpoint(t, ctx, tx, f.summaryThrough(t, ctx, tx, "", 3, testCheckpointItem{row: f.users[2].ID}))

	after := f.position(f.answers[3])
	got := read(after, []string{f.users[2].ID})
	if want := rowIDs(f.users[2], f.users[4], f.answers[4], f.users[5], f.answers[5]); !slices.Equal(got.history, want) || !slices.Equal(got.candidates, want) {
		t.Fatalf("history = %v, candidates = %v, want the kept input and turns 5 and 6: %v", got.history, got.candidates, want)
	}
	if want := rowIDs(f.users[4], f.answers[4], f.users[5], f.answers[5]); !slices.Equal(got.responses, want) {
		t.Fatalf("responses = %v, want the 4 rows of turns 5 and 6", got.responses)
	}
	if want := []string{"inbound-3", "inbound-6", "orphan-early", "orphan-late"}; !slices.Equal(got.replay, want) {
		t.Fatalf("replay = %v, want %v", got.replay, want)
	}
	if want := map[string]bool{"inbound-1": false, "inbound-2": false, "inbound-3": true, "inbound-4": false, "inbound-5": true, "inbound-6": true}; !maps.Equal(got.kept, want) {
		t.Fatalf("classified = %v, want %v", got.kept, want)
	}
	byIDs, err := svc.ListTurnResponseSourcesByIDs(ctx, postgresMessageTestSessionID, []string{f.users[2].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(byIDs) != 1 || byIDs[0].ID != f.users[2].ID || byIDs[0].ExternalMessageID != "inbound-3" {
		t.Fatalf("kept turn response = %+v, want the input of turn 3 with its inbound message", byIDs)
	}

	if err := q.ClearHistoryBySession(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	var checkpoints int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM bot_history_checkpoints WHERE session_id = $1`, postgresMessageTestSessionID).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if active := activeTestCheckpoint(t, ctx, tx); active != "" || checkpoints != 0 {
		t.Fatalf("after clearing the history: active=%q checkpoints=%d, want none", active, checkpoints)
	}
}

// Clearing a bot's history drops the checkpoints of every one of its
// sessions along with the rows they stand for.
func TestPostgresClearingABotsHistoryDropsItsCheckpoints(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)
	f := setupRewindFixture(t, ctx, tx)
	if err := dbsqlc.New(tx).ClearHistoryByBot(ctx, mustTestUUID(t, postgresMessageTestBotID)); err != nil {
		t.Fatal(err)
	}
	var checkpoints int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM bot_history_checkpoints WHERE id = ANY($1::uuid[])`,
		[]string{f.earlierCheckpoint, f.rollingCheckpoint}).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if active := activeTestCheckpoint(t, ctx, tx); active != "" || checkpoints != 0 {
		t.Fatalf("after clearing the bot's history: active=%q checkpoints=%d, want none", active, checkpoints)
	}
}
