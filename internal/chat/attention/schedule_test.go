package attention

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

var testRun = uuid.MustParse("00000000-0000-0000-0000-0000000000d1")

func TestQuietCompletion(t *testing.T) {
	cases := []struct {
		name   string
		row    dbsqlc.GetScheduleNotifyPolicyForRunRow
		err    error
		quiet  bool
		failed bool
	}{
		{name: "user run", row: dbsqlc.GetScheduleNotifyPolicyForRunRow{Scheduled: false, NotifyPolicy: NotifyImportant}},
		{name: "scheduled important", row: dbsqlc.GetScheduleNotifyPolicyForRunRow{Scheduled: true, NotifyPolicy: NotifyImportant}, quiet: true},
		{name: "scheduled every run", row: dbsqlc.GetScheduleNotifyPolicyForRunRow{Scheduled: true, NotifyPolicy: NotifyEveryRun}},
		{name: "run unknown", err: pgx.ErrNoRows},
		{name: "read fails", err: errors.New("boom"), failed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestService(&fakeQueries{policy: tc.row, policyErr: tc.err}, &recordingPublisher{})
			quiet, err := svc.QuietCompletion(context.Background(), testRun.String())
			if (err != nil) != tc.failed {
				t.Fatalf("err = %v, want failure %v", err, tc.failed)
			}
			if quiet != tc.quiet {
				t.Fatalf("quiet = %v, want %v", quiet, tc.quiet)
			}
		})
	}
}

func TestActiveScheduleFire(t *testing.T) {
	started := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	schedule := uuid.MustParse("00000000-0000-0000-0000-0000000000e1")
	row := func(kind string, at time.Time) dbsqlc.GetActiveScheduleFireForSessionRow {
		r := dbsqlc.GetActiveScheduleFireForSessionRow{
			ScheduleID:         pgtype.UUID{Bytes: schedule, Valid: true},
			StartedAt:          pgtype.Timestamptz{Time: started, Valid: true},
			NotifyPolicy:       NotifyImportant,
			ContactViaChannels: true,
		}
		if kind != "" {
			r.AttentionKind = pgtype.Text{String: kind, Valid: true}
			r.AttentionAt = pgtype.Timestamptz{Time: at, Valid: true}
		}
		return r
	}
	cases := []struct {
		name     string
		row      dbsqlc.GetActiveScheduleFireForSessionRow
		notified bool
	}{
		{name: "never attended", row: row("", time.Time{})},
		{name: "update from an earlier fire", row: row(string(KindUpdate), started.Add(-time.Hour))},
		{name: "reply during this fire", row: row(string(KindReply), started.Add(time.Minute))},
		{name: "update during this fire", row: row(string(KindUpdate), started.Add(time.Minute)), notified: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestService(&fakeQueries{fire: tc.row}, &recordingPublisher{})
			fire, ok, err := svc.ActiveScheduleFire(context.Background(), testSession.String())
			if err != nil || !ok {
				t.Fatalf("ActiveScheduleFire = %v, %v", ok, err)
			}
			if fire.Notified != tc.notified {
				t.Fatalf("Notified = %v, want %v", fire.Notified, tc.notified)
			}
			if fire.ScheduleID != schedule.String() || !fire.ContactViaChannels || fire.NotifyPolicy != NotifyImportant {
				t.Fatalf("fire = %+v", fire)
			}
		})
	}

	t.Run("no fire running", func(t *testing.T) {
		svc := newTestService(&fakeQueries{fireErr: pgx.ErrNoRows}, &recordingPublisher{})
		if _, ok, err := svc.ActiveScheduleFire(context.Background(), testSession.String()); ok || err != nil {
			t.Fatalf("ActiveScheduleFire = %v, %v; want not found", ok, err)
		}
	})
}
