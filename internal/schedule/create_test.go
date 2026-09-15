package schedule

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/robfig/cron/v3"

	messageevent "github.com/felinics/memoh/internal/chat/event"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type createQueries struct {
	executionQueries
	created *sqlc.CreateScheduleParams
	id      pgtype.UUID
}

func (q *createQueries) CreateSchedule(_ context.Context, params sqlc.CreateScheduleParams) (sqlc.Schedule, error) {
	q.created = &params
	return sqlc.Schedule{
		ID: q.id, BotID: params.BotID, Name: params.Name, Description: params.Description,
		Command: params.Command, Pattern: params.Pattern, Enabled: params.Enabled,
	}, nil
}

func TestCreatePublishesScheduleChange(t *testing.T) {
	hub := messageevent.NewHub()
	sub, cancel := hub.Subscribe(execTestBotID, 1)
	defer cancel()
	queries := &createQueries{id: mustUUID(t, "66666666-6666-6666-6666-666666666666")}
	svc := newExecutionService(t, &queries.executionQueries, nil)
	svc.queries = queries
	svc.parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	svc.SetEventPublisher(hub)
	disabled := false
	if _, err := svc.Create(context.Background(), execTestBotID, CreateRequest{
		Name: "Daily report", Pattern: "0 9 * * *", Command: "Summarize today's work", Enabled: &disabled,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	select {
	case event := <-sub.Events:
		if event.Type != messageevent.EventTypeScheduleChanged {
			t.Fatalf("event type = %q", event.Type)
		}
		var change messageevent.ScheduleChange
		if err := json.Unmarshal(event.Data, &change); err != nil {
			t.Fatal(err)
		}
		if change.ScheduleID != queries.id.String() {
			t.Fatalf("schedule id = %q", change.ScheduleID)
		}
	default:
		t.Fatal("schedule creation emitted no change event")
	}
}

func TestCreateWithoutDescription(t *testing.T) {
	queries := &createQueries{}
	svc := newExecutionService(t, &queries.executionQueries, nil)
	svc.queries = queries
	svc.parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	disabled := false
	result, err := svc.Create(context.Background(), execTestBotID, CreateRequest{
		Name: "Daily report", Pattern: "0 9 * * *", Command: "Summarize today's work", Enabled: &disabled,
	})
	if err != nil {
		t.Fatalf("create without description: %v", err)
	}
	if queries.created == nil || queries.created.Description != "" || result.Description != "" {
		t.Fatalf("empty description was not preserved: %+v", result)
	}
}
