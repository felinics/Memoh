package discuss

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/logger"
)

func runDiscussTurn(t *testing.T, ctx context.Context, service turn.Service) (discussRunOutcome, map[string]any) {
	t.Helper()
	var logs bytes.Buffer
	runner := discussTurnRunner{projector: newDiscussEventProjector(nil)}
	outcome, _ := runner.Run(ctx, service, turn.StartTurnCommand{BotID: "bot-1"}, logger.New(&logs, "debug", "json"))

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		records = append(records, record)
	}
	if len(records) != 1 || records[0]["msg"] != "discuss turn" {
		t.Fatalf("records = %v, want one discuss turn line", records)
	}
	return outcome, records[0]
}

// A discuss turn gets exactly one result line, whichever way it ends.
func TestDiscussTurnWritesOneResultLine(t *testing.T) {
	tests := []struct {
		name      string
		service   *fakeTurnService
		wantLevel string
		wantError string
	}{
		{name: "clean end", service: &fakeTurnService{}, wantLevel: "INFO"},
		{name: "recovered stream error", service: &fakeTurnService{midStreamError: true}, wantLevel: "INFO"},
		{name: "start failure", service: &fakeTurnService{startErr: errors.New("agent unreachable")}, wantLevel: "ERROR", wantError: "start turn: agent unreachable"},
		{name: "turn failure", service: &fakeTurnService{streamErr: errors.New("model call failed")}, wantLevel: "ERROR", wantError: "model call failed"},
		{name: "stream error without end", service: &fakeTurnService{endWithError: true}, wantLevel: "ERROR", wantError: "provider rejected the request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, record := runDiscussTurn(t, context.Background(), tt.service)
			if record["level"] != tt.wantLevel {
				t.Fatalf("level = %v, want %s: %v", record["level"], tt.wantLevel, record)
			}
			if tt.wantError == "" {
				if _, ok := record["error"]; ok {
					t.Fatalf("success record carries an error: %v", record)
				}
				return
			}
			if record["error"] != tt.wantError || record["fault"] != "server" {
				t.Fatalf("fault/error = %v/%v, want server/%s", record["fault"], record["error"], tt.wantError)
			}
		})
	}
}

func TestDiscussTurnResultLineRecordsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome, record := runDiscussTurn(t, ctx, pendingTurnService{&fakeTurnService{}})
	if !outcome.cancelled {
		t.Fatal("outcome not cancelled")
	}
	if record["level"] != "INFO" || record["fault"] != "canceled" || record["error"] != "context canceled" {
		t.Fatalf("level/fault/error = %v/%v/%v: %v", record["level"], record["fault"], record["error"], record)
	}
}

// pendingTurnService starts a turn that never produces an event.
type pendingTurnService struct{ *fakeTurnService }

func (pendingTurnService) StartTurn(context.Context, turn.StartTurnCommand) (turn.RunHandle, error) {
	return &fakeRunHandle{events: make(chan turn.Event), errs: make(chan error)}, nil
}

// Each discuss trigger reports a request id of its own on everything it logs,
// its result line included; the session worker has none to inherit.
func TestDiscussReplyHasItsOwnRequestID(t *testing.T) {
	var logs bytes.Buffer
	driver := NewDiscussDriver(DiscussDriverDeps{Turn: &fakeTurnService{}, Logger: logger.New(&logs, "debug", "json")})
	turnIDs := map[string]bool{}
	for range 2 {
		logs.Reset()
		sess := &discussSession{config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"}}
		driver.handleReply(context.Background(), sess, recomposeTestRC(), driver.logger)

		var turnID string
		ids := map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatalf("decode log line %q: %v", line, err)
			}
			id, _ := record["request_id"].(string)
			ids[id] = true
			if record["msg"] == "discuss turn" {
				turnID = id
			}
		}
		if turnID == "" || len(ids) != 1 || turnIDs[turnID] {
			t.Fatalf("request ids = %v, turn line %q; want one fresh id on every line", ids, turnID)
		}
		turnIDs[turnID] = true
	}
}
