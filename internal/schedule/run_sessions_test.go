package schedule

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	messageevent "github.com/felinics/memoh/internal/chat/event"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	runTestBotID      = "11111111-1111-1111-1111-111111111111"
	runTestOtherBotID = "99999999-9999-9999-9999-999999999999"
	runTestScheduleID = "66666666-6666-6666-6666-666666666666"
	runTestOwnerID    = "77777777-7777-7777-7777-777777777777"
	runTestLogID      = "88888888-8888-8888-8888-888888888888"
)

// runQueries fakes the queries ListRunSessions and runSchedule perform.
type runQueries struct {
	dbstore.Queries
	schedule   sqlc.Schedule
	rows       []sqlc.ListScheduleRunSessionsRow
	listParams []sqlc.ListScheduleRunSessionsParams
}

func (q *runQueries) GetScheduleByID(_ context.Context, id pgtype.UUID) (sqlc.Schedule, error) {
	if !q.schedule.ID.Valid || q.schedule.ID != id {
		return sqlc.Schedule{}, pgx.ErrNoRows
	}
	return q.schedule, nil
}

func (q *runQueries) ListScheduleRunSessions(_ context.Context, arg sqlc.ListScheduleRunSessionsParams) ([]sqlc.ListScheduleRunSessionsRow, error) {
	q.listParams = append(q.listParams, arg)
	rows := q.rows
	if int(arg.LimitCount) < len(rows) {
		rows = rows[:arg.LimitCount]
	}
	return rows, nil
}

func (q *runQueries) IncrementScheduleCalls(_ context.Context, _ pgtype.UUID) (sqlc.Schedule, error) {
	return q.schedule, nil
}

func (*runQueries) GetBotByID(_ context.Context, id pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return sqlc.GetBotByIDRow{ID: id, OwnerUserID: mustParse(runTestOwnerID)}, nil
}

func (*runQueries) CreateScheduleLog(_ context.Context, arg sqlc.CreateScheduleLogParams) (sqlc.CreateScheduleLogRow, error) {
	return sqlc.CreateScheduleLogRow{ID: mustParse(runTestLogID), ScheduleID: arg.ScheduleID, BotID: arg.BotID, SessionID: arg.SessionID}, nil
}

func (q *runQueries) CompleteScheduleLog(_ context.Context, arg sqlc.CompleteScheduleLogParams) (sqlc.ScheduleLog, error) {
	return sqlc.ScheduleLog{ID: arg.ID, ScheduleID: q.schedule.ID, BotID: q.schedule.BotID, Status: arg.Status}, nil
}

func mustParse(id string) pgtype.UUID {
	var out pgtype.UUID
	if err := out.Scan(id); err != nil {
		panic(err)
	}
	return out
}

func runTestSchedule() sqlc.Schedule {
	return sqlc.Schedule{
		ID:        mustParse(runTestScheduleID),
		BotID:     mustParse(runTestBotID),
		Name:      "daily",
		Pattern:   "0 0 * * *",
		Enabled:   true,
		Command:   "report",
		RunTarget: RunTargetNewSession,
	}
}

func runRow(id string, started time.Time, sessionID, title, status string, completed bool) sqlc.ListScheduleRunSessionsRow {
	row := sqlc.ListScheduleRunSessionsRow{
		ID:           mustParse(id),
		Status:       status,
		StartedAt:    pgtype.Timestamptz{Time: started, Valid: true},
		SessionTitle: title,
	}
	if sessionID != "" {
		row.SessionID = mustParse(sessionID)
	}
	if completed {
		row.CompletedAt = pgtype.Timestamptz{Time: started.Add(time.Minute), Valid: true}
	}
	return row
}

func TestListRunSessionsForeignScheduleIsNotFound(t *testing.T) {
	queries := &runQueries{schedule: runTestSchedule()}
	svc := &Service{queries: queries, logger: slog.Default()}

	_, err := svc.ListRunSessions(t.Context(), runTestOtherBotID, runTestScheduleID, 0, "")
	if !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("foreign bot error = %v, want ErrScheduleNotFound", err)
	}
	_, unknownErr := svc.ListRunSessions(t.Context(), runTestBotID, "12121212-1212-1212-1212-121212121212", 0, "")
	if !errors.Is(unknownErr, ErrScheduleNotFound) {
		t.Fatalf("unknown schedule error = %v, want ErrScheduleNotFound", unknownErr)
	}
	if len(queries.listParams) != 0 {
		t.Fatalf("listed runs of a schedule the bot does not own: %+v", queries.listParams)
	}
}

func TestListRunSessionsMapsRowsAndPages(t *testing.T) {
	base := time.Date(2026, 10, 1, 9, 0, 0, 123456000, time.UTC)
	queries := &runQueries{schedule: runTestSchedule(), rows: []sqlc.ListScheduleRunSessionsRow{
		runRow("a0000000-0000-0000-0000-000000000003", base.Add(2*time.Hour), "b0000000-0000-0000-0000-000000000003", "third", "ok", false),
		runRow("a0000000-0000-0000-0000-000000000002", base.Add(time.Hour), "", "", "error", true),
		runRow("a0000000-0000-0000-0000-000000000001", base, "b0000000-0000-0000-0000-000000000001", "first", "ok", true),
	}}
	svc := &Service{queries: queries, logger: slog.Default()}

	page, err := svc.ListRunSessions(t.Context(), runTestBotID, runTestScheduleID, 2, "")
	if err != nil {
		t.Fatalf("ListRunSessions() error = %v", err)
	}
	if got := queries.listParams[0]; got.LimitCount != 3 || got.UseCursor {
		t.Fatalf("first page params = %+v, want a limit+1 probe and no cursor", got)
	}
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("page = %+v, want two items and a next cursor", page)
	}
	running, failed := page.Items[0], page.Items[1]
	if running.Status != RunStatusRunning || running.CompletedAt != nil || running.Title != "third" ||
		running.SessionID != "b0000000-0000-0000-0000-000000000003" || running.LogID != "a0000000-0000-0000-0000-000000000003" {
		t.Fatalf("running item = %+v", running)
	}
	if failed.Status != RunStatusError || failed.SessionID != "" || failed.CompletedAt == nil {
		t.Fatalf("failed item = %+v", failed)
	}

	queries.rows = queries.rows[2:]
	next, err := svc.ListRunSessions(t.Context(), runTestBotID, runTestScheduleID, 2, page.NextCursor)
	if err != nil {
		t.Fatalf("second page error = %v", err)
	}
	cursor := queries.listParams[1]
	if !cursor.UseCursor || !cursor.CursorStartedAt.Time.Equal(base.Add(time.Hour)) || cursor.CursorID.String() != "a0000000-0000-0000-0000-000000000002" {
		t.Fatalf("second page cursor params = %+v, want the last item of page one", cursor)
	}
	if len(next.Items) != 1 || next.NextCursor != "" {
		t.Fatalf("second page = %+v, want the last item and no cursor", next)
	}
}

func TestListRunSessionsClampsLimitAndRejectsBadCursor(t *testing.T) {
	queries := &runQueries{schedule: runTestSchedule()}
	svc := &Service{queries: queries, logger: slog.Default()}

	for _, tc := range []struct{ in, want int32 }{{0, 21}, {-5, 21}, {500, 101}, {7, 8}} {
		queries.listParams = nil
		page, err := svc.ListRunSessions(t.Context(), runTestBotID, runTestScheduleID, int(tc.in), "")
		if err != nil {
			t.Fatalf("limit %d: %v", tc.in, err)
		}
		if got := queries.listParams[0].LimitCount; got != tc.want {
			t.Fatalf("limit %d probed %d rows, want %d", tc.in, got, tc.want)
		}
		if page.Items == nil {
			t.Fatal("empty page must serialize items as []")
		}
	}

	_, err := svc.ListRunSessions(t.Context(), runTestBotID, runTestScheduleID, 10, "not a cursor")
	var invalid InvalidRequestError
	if !errors.As(err, &invalid) || invalid.Field() != "cursor" {
		t.Fatalf("bad cursor error = %v, want an invalid cursor field", err)
	}
}

type recordingPublisher struct{ events []messageevent.Event }

func (p *recordingPublisher) Publish(event messageevent.Event) {
	p.events = append(p.events, event)
}

type fakeTriggerer struct{ err error }

func (f fakeTriggerer) TriggerSchedule(context.Context, string, TriggerPayload) (TriggerResult, error) {
	if f.err != nil {
		return TriggerResult{}, f.err
	}
	return TriggerResult{Status: "ok"}, nil
}

type fakeSessionCreator struct{ err error }

func (fakeSessionCreator) CreateSession(context.Context, string, string) (string, error) {
	return "", errors.New("not used")
}

func (f fakeSessionCreator) CreateScheduleSession(context.Context, SessionSpec) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "b0000000-0000-0000-0000-00000000000f", nil
}

// Each fire announces itself when its log row (and session) exists and again
// when the row completes, so a schedule's run list can refresh live.
func TestRunSchedulePublishesWhenTheRunStartsAndEnds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		trigger   error
		session   error
		wantError bool
	}{
		{name: "success"},
		{name: "trigger fails", trigger: errors.New("upstream down"), wantError: true},
		{name: "session creation fails", session: errors.New("no session"), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &recordingPublisher{}
			svc := &Service{
				queries:        &runQueries{schedule: runTestSchedule()},
				triggerer:      fakeTriggerer{err: tc.trigger},
				sessionCreator: fakeSessionCreator{err: tc.session},
				events:         publisher,
				logger:         slog.Default(),
			}
			err := svc.runSchedule(t.Context(), toSchedule(runTestSchedule()))
			if (err != nil) != tc.wantError {
				t.Fatalf("runSchedule() error = %v, wantError %v", err, tc.wantError)
			}
			if len(publisher.events) != 3 {
				t.Fatalf("published %d events, want 3 (calls counted, run started, run completed)", len(publisher.events))
			}
			for _, event := range publisher.events {
				if event.Type != messageevent.EventTypeScheduleChanged || event.BotID != runTestBotID {
					t.Fatalf("event = %+v, want schedule_changed for the bot", event)
				}
			}
		})
	}
}
