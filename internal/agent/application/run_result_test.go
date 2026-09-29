package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/testutil/sessionledger"
)

// capturedLogs records every record written through a logger built on it.
type capturedLogs struct {
	mu      sync.Mutex
	records []slog.Record
}

type capturingHandler struct {
	logs  *capturedLogs
	attrs []slog.Attr
}

func captureLogs() (*slog.Logger, *capturedLogs) {
	logs := &capturedLogs{}
	return slog.New(capturingHandler{logs: logs}), logs
}

func (capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h capturingHandler) Handle(_ context.Context, record slog.Record) error {
	record = record.Clone()
	record.AddAttrs(h.attrs...)
	h.logs.mu.Lock()
	defer h.logs.mu.Unlock()
	h.logs.records = append(h.logs.records, record)
	return nil
}

func (h capturingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return capturingHandler{logs: h.logs, attrs: append(append([]slog.Attr(nil), h.attrs...), attrs...)}
}

func (h capturingHandler) WithGroup(string) slog.Handler { return h }

// runResults are the result records of agent runs.
func (l *capturedLogs) runResults() []slog.Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []slog.Record
	for _, record := range l.records {
		if record.Message == "agent run" {
			out = append(out, record)
		}
	}
	return out
}

func (l *capturedLogs) atLevel(level slog.Level) []slog.Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []slog.Record
	for _, record := range l.records {
		if record.Level == level {
			out = append(out, record)
		}
	}
	return out
}

func recordAttrs(record slog.Record) map[string]string {
	out := map[string]string{}
	record.Attrs(func(attr slog.Attr) bool {
		out[attr.Key] = attr.Value.String()
		return true
	})
	return out
}

func TestRunResultRecordsHowTheRunEnded(t *testing.T) {
	t.Parallel()
	providerFailure := apperror.Wrap(apperror.CodeAgentProviderOverloaded, errors.New("upstream 503"), nil)
	tests := []struct {
		name      string
		terminal  sessionruntime.TerminalRun
		cause     error
		wantLevel slog.Level
		wantFault string
	}{
		{
			name:      "owner completed",
			terminal:  sessionruntime.TerminalRun{State: string(ledger.StateCompleted)},
			wantLevel: slog.LevelInfo,
		},
		{
			name: "owner failed with its cause",
			terminal: sessionruntime.TerminalRun{
				State: string(ledger.StateFailed), ErrorCode: string(apperror.CodeAgentProviderOverloaded),
			},
			cause:     providerFailure,
			wantLevel: slog.LevelError,
			wantFault: "dependency",
		},
		{
			name:      "stopped",
			terminal:  sessionruntime.TerminalRun{State: string(ledger.StateAborted)},
			cause:     context.Canceled,
			wantLevel: slog.LevelInfo,
		},
		{
			name: "lost and found by the reaper",
			terminal: sessionruntime.TerminalRun{
				State: string(ledger.StateLost), ErrorCode: string(apperror.CodeRuntimeOwnerLeaseExpired),
			},
			wantLevel: slog.LevelError,
			wantFault: "server",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger, logs := captureLogs()
			service := &Service{logger: logger}
			terminal := tt.terminal
			terminal.RunID, terminal.BotID, terminal.SessionID, terminal.Applied = "run-1", "bot-1", "session-1", true
			ctx := context.Background()
			if tt.cause != nil {
				ctx = WithRunOutcome(ctx, "run-1", RunOutcome{Cause: tt.cause})
			}

			service.logRunResult(ctx, terminal)

			records := logs.runResults()
			if len(records) != 1 {
				t.Fatalf("result records = %d, want 1", len(records))
			}
			if records[0].Level != tt.wantLevel {
				t.Errorf("level = %v, want %v", records[0].Level, tt.wantLevel)
			}
			attrs := recordAttrs(records[0])
			want := map[string]string{
				"operation": "agent.run", "run_id": "run-1", "bot_id": "bot-1", "session_id": "session-1",
				"state": terminal.State, "error_code": terminal.ErrorCode,
			}
			for key, value := range want {
				if attrs[key] != value {
					t.Errorf("%s = %q, want %q", key, attrs[key], value)
				}
			}
			if attrs["fault"] != tt.wantFault {
				t.Errorf("fault = %q, want %q", attrs["fault"], tt.wantFault)
			}
			if tt.name == "owner failed with its cause" && attrs["error"] == "" {
				t.Error("a failed run's record does not carry its owner's cause")
			}
		})
	}
}

// Only the observation whose own write ended the run is recorded. A replay,
// a durable retry that finds the run already terminal and a stale owner all
// observe the run again without applying anything.
func TestRunResultSkipsObservationsThatDidNotEndTheRun(t *testing.T) {
	t.Parallel()
	logger, logs := captureLogs()
	service := &Service{logger: logger}

	service.logRunResult(context.Background(), sessionruntime.TerminalRun{
		RunID: "run-1", State: string(ledger.StateFailed), ErrorCode: string(apperror.CodeAgentProviderOverloaded),
	})

	if records := logs.runResults(); len(records) != 0 {
		t.Fatalf("result records = %d, want none for an observation that applied nothing", len(records))
	}
}

// A run the session runtime ended itself, such as one whose admission failed
// or one released at shutdown, reports the cause the runtime held. The run's
// record in session_runs carries only the code, so this record is where that
// cause is found.
func TestRunResultReportsTheSessionRuntimeCause(t *testing.T) {
	t.Parallel()
	logger, logs := captureLogs()
	service := &Service{logger: logger}

	service.logRunResult(context.Background(), sessionruntime.TerminalRun{
		RunID: "run-1", BotID: "bot-1", SessionID: "session-1",
		State: string(ledger.StateFailed), ErrorCode: string(apperror.CodeRuntimeRunFailed),
		Cause: errors.New("persist user turn failed"), Applied: true,
	})

	records := logs.runResults()
	if len(records) != 1 {
		t.Fatalf("result records = %d, want 1", len(records))
	}
	if got := recordAttrs(records[0])["error"]; !strings.Contains(got, "persist user turn failed") {
		t.Fatalf("result record error = %q, want the session runtime's cause", got)
	}
}

// A follow-up run starts from the finished run's context; it must not report
// the finished run's cause as its own.
func TestRunResultIgnoresAnotherRunsCause(t *testing.T) {
	t.Parallel()
	logger, logs := captureLogs()
	service := &Service{logger: logger}
	ctx := WithRunOutcome(context.Background(), "run-previous", RunOutcome{
		Cause: apperror.Wrap(apperror.CodeAgentProviderOverloaded, errors.New("previous run's failure"), nil),
	})

	service.logRunResult(ctx, sessionruntime.TerminalRun{
		RunID: "run-next", State: string(ledger.StateLost), ErrorCode: string(apperror.CodeRuntimeOwnerLeaseExpired),
		Applied: true,
	})

	records := logs.runResults()
	if len(records) != 1 {
		t.Fatalf("result records = %d, want 1", len(records))
	}
	if got := recordAttrs(records[0])["reason"]; got != string(apperror.CodeRuntimeOwnerLeaseExpired) {
		t.Fatalf("reason = %q, want the run's own code", got)
	}
}

// A run through a real session runtime manager writes one result record when
// it succeeds.
func TestRunResultOneRecordForASuccessfulRun(t *testing.T) {
	fixture, _ := newScriptedFailureFixture(t)
	logger, logs := captureLogs()
	fixture.service.logger = logger

	got := runNativeTurn(t, fixture)

	if got.ledger[0] != string(ledger.StateCompleted) {
		t.Fatalf("session_runs state = %q, want completed", got.ledger[0])
	}
	records := waitRunResults(t, logs, 1)
	if records[0].Level != slog.LevelInfo || recordAttrs(records[0])["state"] != "completed" {
		t.Fatalf("result record = %v %v, want INFO completed", records[0].Level, recordAttrs(records[0]))
	}
}

// A run whose stream reports several failures (a failed attempt, its retry,
// the final failure) writes one ERROR, its result record. The failure events
// themselves are not logged at ERROR.
func TestRunResultOneErrorForARunWithSeveralFailureEvents(t *testing.T) {
	fixture, _ := newScriptedFailureFixture(t, http.StatusServiceUnavailable, http.StatusBadRequest)
	logger, logs := captureLogs()
	fixture.service.logger = logger

	got := runNativeTurn(t, fixture)

	assertOrdered(t, got.types, "error", "retry", "error", "agent_abort")
	records := waitRunResults(t, logs, 1)
	attrs := recordAttrs(records[0])
	if attrs["state"] != "failed" || attrs["error_code"] != got.ledger[1] || attrs["fault"] == "" {
		t.Fatalf("result record = %v, want the failed run with its code and fault", attrs)
	}
	if strings.Contains(attrs["error"], "without its owner's cause") {
		t.Fatalf("result record error = %q, want the owner's cause", attrs["error"])
	}
	if errorsLogged := logs.atLevel(slog.LevelError); len(errorsLogged) != 1 || errorsLogged[0].Message != "agent run" {
		messages := make([]string, 0, len(errorsLogged))
		for _, record := range errorsLogged {
			messages = append(messages, record.Message)
		}
		t.Fatalf("ERROR records = %q, want only the run's result record", messages)
	}
}

// When the terminal event's proposal write fails, the run ends through the
// owner's finish instead; the run still has one result record.
func TestRunResultOneRecordWhenTheProposalIsDeferred(t *testing.T) {
	fixture, _ := newScriptedFailureFixture(t, http.StatusServiceUnavailable, http.StatusBadRequest)
	logger, logs := captureLogs()
	fixture.service.logger = logger
	runs := &proposalRecordingLedger{Store: sessionledger.New(), failFirst: true}

	got := runNativeTurnWith(t, fixture, runs)

	if got.ledger[0] != string(ledger.StateFailed) {
		t.Fatalf("session_runs state = %q, want failed", got.ledger[0])
	}
	waitRunResults(t, logs, 1)
}

// waitRunResults waits for the terminal observer, which runs after the turn's
// handle closes, and then requires exactly want result records.
func waitRunResults(t *testing.T, logs *capturedLogs, want int) []slog.Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(logs.runResults()) < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// Give a duplicate observation the same chance to arrive.
	time.Sleep(50 * time.Millisecond)
	records := logs.runResults()
	if len(records) != want {
		attrs := make([]map[string]string, 0, len(records))
		for _, record := range records {
			attrs = append(attrs, recordAttrs(record))
		}
		encoded, _ := json.Marshal(attrs)
		t.Fatalf("result records = %s, want %d", encoded, want)
	}
	return records
}

// A WebSocket stream that delivers its failure returns no error; the turn span
// still reports the failure the run's result record reports.
func TestTurnSpanReportsAFailureTheWebSocketStreamDelivered(t *testing.T) {
	recorder := recordTurnSpans(t)
	fixture, _ := newScriptedFailureFixture(t, http.StatusServiceUnavailable, http.StatusBadRequest)
	eventCh := make(chan WSStreamEvent)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range eventCh {
		}
	}()

	_, outcome, err := fixture.service.streamChatWSResultWithHooks(context.Background(), ChatRequest{
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                directLifecyclePrompt,
		UserMessagePersisted: true,
	}, eventCh, make(chan struct{}), nil, nil)
	close(eventCh)
	<-drained

	if err != nil || outcome.Status != sessionruntime.RunStatusErrored {
		t.Fatalf("WS turn = %q, %v; want a delivered failure", outcome.Status, err)
	}
	span := turnSpan(t, recorder)
	if got := spanAttr(span, "agent.turn.outcome").AsString(); got != "errored" {
		t.Errorf("outcome = %q, want errored", got)
	}
	if span.Status().Code != codes.Error {
		t.Error("the turn span of a failed run is not an error")
	}
}
