package schedule

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/robfig/cron/v3"
)

func newNotifyTestService(t *testing.T) (*Service, *createQueries) {
	t.Helper()
	queries := &createQueries{id: mustUUID(t, "77777777-7777-7777-7777-777777777777")}
	svc := newExecutionService(t, &queries.executionQueries, nil)
	svc.queries = queries
	svc.parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	return svc, queries
}

func TestCreateDefaultsToImportantAndChannelContact(t *testing.T) {
	svc, queries := newNotifyTestService(t)
	disabled := false
	if _, err := svc.Create(context.Background(), execTestBotID, CreateRequest{
		Name: "News", Pattern: "0 9 * * *", Command: "Summarize AI news", Enabled: &disabled,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if queries.created.NotifyPolicy != NotifyImportant || !queries.created.ContactViaChannels {
		t.Fatalf("created notify = %q, contact = %v; want important, true", queries.created.NotifyPolicy, queries.created.ContactViaChannels)
	}
}

func TestCreateKeepsChosenNotifySettings(t *testing.T) {
	svc, queries := newNotifyTestService(t)
	disabled, off := false, false
	if _, err := svc.Create(context.Background(), execTestBotID, CreateRequest{
		Name: "Backup", Pattern: "0 23 * * *", Command: "Check the backup", Enabled: &disabled,
		NotifyPolicy: NotifyEveryRun, ContactViaChannels: &off,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if queries.created.NotifyPolicy != NotifyEveryRun || queries.created.ContactViaChannels {
		t.Fatalf("created notify = %q, contact = %v; want every_run, false", queries.created.NotifyPolicy, queries.created.ContactViaChannels)
	}
}

func TestCreateRejectsUnknownNotifyPolicy(t *testing.T) {
	svc, queries := newNotifyTestService(t)
	_, err := svc.Create(context.Background(), execTestBotID, CreateRequest{
		Name: "News", Pattern: "0 9 * * *", Command: "Summarize AI news", NotifyPolicy: "sometimes",
	})
	var fieldErr InvalidRequestError
	if !errors.As(err, &fieldErr) || fieldErr.Field() != "notify_policy" {
		t.Fatalf("err = %v, want a notify_policy field error", err)
	}
	if queries.created != nil {
		t.Fatal("an invalid policy still created the schedule")
	}
}

type payloadRecorder struct{ payload *TriggerPayload }

func (r payloadRecorder) TriggerSchedule(_ context.Context, _ string, payload TriggerPayload, _ string) (TriggerResult, error) {
	*r.payload = payload
	return TriggerResult{Status: "ok"}, nil
}

func TestRunSchedulePassesNotifySettingsToTheRun(t *testing.T) {
	cases := []struct {
		name        string
		policy      string
		contact     bool
		wantEvery   bool
		wantNoReach bool
	}{
		{name: "defaults", policy: NotifyImportant, contact: true},
		{name: "every run", policy: NotifyEveryRun, contact: true, wantEvery: true},
		{name: "no channel contact", policy: NotifyImportant, contact: false, wantNoReach: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := runTestSchedule()
			row.NotifyPolicy = tc.policy
			row.ContactViaChannels = tc.contact
			var got TriggerPayload
			svc := &Service{
				queries:        &runQueries{schedule: row},
				triggerer:      payloadRecorder{payload: &got},
				sessionCreator: fakeSessionCreator{},
				events:         &recordingPublisher{},
				jwtSecret:      "secret",
				logger:         slog.Default(),
			}
			if err := svc.runSchedule(t.Context(), toSchedule(row)); err != nil {
				t.Fatalf("runSchedule: %v", err)
			}
			if got.NotifiesEveryRun != tc.wantEvery || got.NoChannelContact != tc.wantNoReach {
				t.Fatalf("payload every_run=%v no_channel=%v, want %v %v", got.NotifiesEveryRun, got.NoChannelContact, tc.wantEvery, tc.wantNoReach)
			}
		})
	}
}
