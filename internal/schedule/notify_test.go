package schedule

import (
	"context"
	"errors"
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
