//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const postgresURLEnv = "MEMOH_SESSION_RUNTIME_POSTGRES_URL"

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

type sessionRunRecord struct {
	RunID            string
	BotID            string
	SessionID        string
	InvocationID     string
	TurnID           string
	TurnPosition     int64
	State            string
	Input            json.RawMessage
	InputFingerprint string
	OwnerID          string
	FencingToken     int64
	LiveGeneration   string
	AbortRequested   bool
	ErrorCode        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (r sessionRunRecord) terminal() bool {
	switch r.State {
	case "completed", "aborted", "failed", "lost":
		return true
	default:
		return false
	}
}

func (r sessionRunRecord) validateIdentity(botID, sessionID, invocationID, inputMarker string) error {
	switch {
	case r.RunID == "":
		return errors.New("session_runs.run_id is empty")
	case r.TurnID == "":
		return errors.New("session_runs.turn_id is empty")
	case r.BotID != botID:
		return fmt.Errorf("session_runs.bot_id = %q, want %q", r.BotID, botID)
	case r.SessionID != sessionID:
		return fmt.Errorf("session_runs.session_id = %q, want %q", r.SessionID, sessionID)
	case r.InvocationID != invocationID:
		return fmt.Errorf("session_runs.invocation_id = %q, want %q", r.InvocationID, invocationID)
	case r.TurnPosition <= 0:
		return fmt.Errorf("session_runs.turn_position = %d, want positive", r.TurnPosition)
	case !json.Valid(r.Input):
		return errors.New("session_runs.input_json is not valid JSON")
	case !jsonContainsString(r.Input, inputMarker):
		return fmt.Errorf("session_runs.input_json does not contain marker %q: %s", inputMarker, r.Input)
	case !sha256Hex.MatchString(r.InputFingerprint):
		return fmt.Errorf("session_runs.input_fingerprint is not lowercase SHA-256: %q", r.InputFingerprint)
	}
	return nil
}

type historyRunSummary struct {
	Messages          int
	UserMessages      int
	AssistantMessages int
	DistinctTurnIDs   int
	WrongTurnIDs      int
}

type userInputRecord struct {
	ID                  string
	RunID               string
	TurnID              string
	Status              string
	RuntimeFencingToken int64
	UIPayload           json.RawMessage
}

type toolApprovalRecord struct {
	ID                  string
	RunID               string
	TurnID              string
	Status              string
	RuntimeFencingToken int64
}

type decisionResponseIdentityRecord struct {
	ControlID   string
	PayloadHash string
}

type ledgerProbe struct {
	pool *pgxpool.Pool
}

var (
	ledgerOnce      sync.Once
	globalLedger    *ledgerProbe
	globalLedgerErr error
)

func requireLedger(t testingT) *ledgerProbe {
	t.Helper()
	ledgerOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		config, err := pgxpool.ParseConfig(envOr(
			postgresURLEnv,
			"postgres://memoh:memoh123@127.0.0.1:15432/memoh?sslmode=disable",
		))
		if err != nil {
			globalLedgerErr = fmt.Errorf("parse acceptance PostgreSQL URL: %w", err)
			return
		}
		config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
		config.ConnConfig.RuntimeParams["application_name"] = "session-runtime-acceptance-probe"
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			globalLedgerErr = fmt.Errorf("open acceptance PostgreSQL: %w", err)
			return
		}
		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			globalLedgerErr = fmt.Errorf("ping acceptance PostgreSQL: %w", err)
			return
		}
		globalLedger = &ledgerProbe{pool: pool}
	})
	if globalLedgerErr != nil {
		t.Fatalf("prepare acceptance PostgreSQL probe: %v", globalLedgerErr)
	}
	return globalLedger
}

// testingT keeps database helpers usable from tests without coupling them to a
// concrete *testing.T in signatures that are also called by polling helpers.
type testingT interface {
	Helper()
	Fatalf(string, ...any)
}

func closeLedger() {
	if globalLedger != nil {
		globalLedger.pool.Close()
	}
}

func (p *ledgerProbe) runByInvocation(ctx context.Context, sessionID, invocationID string) (sessionRunRecord, error) {
	const query = `
SELECT
  run_id::text,
  bot_id::text,
  session_id::text,
  invocation_id,
  turn_id::text,
  turn_position,
  state,
  input_json,
  input_fingerprint,
  COALESCE(owner_id, ''),
  fencing_token,
  COALESCE(live_generation, ''),
  abort_requested_at IS NOT NULL,
  COALESCE(error_code, ''),
  created_at,
  updated_at
FROM session_runs
WHERE session_id = $1::uuid
  AND invocation_id = $2`
	var run sessionRunRecord
	err := p.pool.QueryRow(ctx, query, sessionID, invocationID).Scan(
		&run.RunID,
		&run.BotID,
		&run.SessionID,
		&run.InvocationID,
		&run.TurnID,
		&run.TurnPosition,
		&run.State,
		&run.Input,
		&run.InputFingerprint,
		&run.OwnerID,
		&run.FencingToken,
		&run.LiveGeneration,
		&run.AbortRequested,
		&run.ErrorCode,
		&run.CreatedAt,
		&run.UpdatedAt,
	)
	return run, err
}

func (p *ledgerProbe) waitRun(
	ctx context.Context,
	sessionID string,
	invocationID string,
	predicate func(sessionRunRecord) bool,
) (sessionRunRecord, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var last sessionRunRecord
	for {
		run, err := p.runByInvocation(ctx, sessionID, invocationID)
		switch {
		case err == nil:
			last = run
			if predicate == nil || predicate(run) {
				return run, nil
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return sessionRunRecord{}, err
		}
		select {
		case <-ctx.Done():
			if last.RunID == "" {
				return sessionRunRecord{}, fmt.Errorf("wait for session run: %w", ctx.Err())
			}
			return last, fmt.Errorf("wait for session run state (last=%s): %w", last.State, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (p *ledgerProbe) runCount(ctx context.Context, sessionID, invocationID string) (int, error) {
	var count int
	err := p.pool.QueryRow(ctx, `
SELECT count(*)
FROM session_runs
WHERE session_id = $1::uuid
  AND invocation_id = $2`, sessionID, invocationID).Scan(&count)
	return count, err
}

func (p *ledgerProbe) sessionRunCount(ctx context.Context, sessionID string) (int, error) {
	var count int
	err := p.pool.QueryRow(ctx, `
SELECT count(*)
FROM session_runs
WHERE session_id = $1::uuid`, sessionID).Scan(&count)
	return count, err
}

func (p *ledgerProbe) activeRunCount(ctx context.Context, sessionID string) (int, error) {
	var count int
	err := p.pool.QueryRow(ctx, `
SELECT count(*)
FROM session_runs
WHERE session_id = $1::uuid
  AND state IN ('accepted', 'running', 'waiting_decision')`, sessionID).Scan(&count)
	return count, err
}

func (p *ledgerProbe) nextTurnPosition(ctx context.Context, sessionID string) (int64, error) {
	var position int64
	err := p.pool.QueryRow(ctx, `
SELECT next_turn_position
FROM bot_sessions
WHERE id = $1::uuid`, sessionID).Scan(&position)
	return position, err
}

func (p *ledgerProbe) historySummary(ctx context.Context, run sessionRunRecord) (historyRunSummary, error) {
	var summary historyRunSummary
	err := p.pool.QueryRow(ctx, `
SELECT
  count(*),
  count(*) FILTER (WHERE role = 'user'),
  count(*) FILTER (WHERE role = 'assistant'),
  count(DISTINCT turn_id),
  count(*) FILTER (WHERE turn_id IS DISTINCT FROM $2::uuid)
FROM bot_history_messages
WHERE session_id = $1::uuid
  AND run_id = $3::uuid`, run.SessionID, run.TurnID, run.RunID).Scan(
		&summary.Messages,
		&summary.UserMessages,
		&summary.AssistantMessages,
		&summary.DistinctTurnIDs,
		&summary.WrongTurnIDs,
	)
	return summary, err
}

func (p *ledgerProbe) waitHistory(ctx context.Context, run sessionRunRecord) (historyRunSummary, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var last historyRunSummary
	for {
		summary, err := p.historySummary(ctx, run)
		if err != nil {
			return historyRunSummary{}, err
		}
		last = summary
		if summary.UserMessages == 1 && summary.AssistantMessages > 0 {
			return summary, nil
		}
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("wait for run-linked history: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (p *ledgerProbe) pendingUserInput(ctx context.Context, runID string) (userInputRecord, error) {
	const query = `
SELECT
  id::text,
  COALESCE(run_id::text, ''),
  COALESCE(turn_id::text, ''),
  status,
  COALESCE(runtime_fencing_token, 0),
  ui_payload_json
FROM user_input_requests
WHERE run_id = $1::uuid
  AND status = 'pending'
ORDER BY created_at DESC
LIMIT 1`
	var decision userInputRecord
	err := p.pool.QueryRow(ctx, query, runID).Scan(
		&decision.ID,
		&decision.RunID,
		&decision.TurnID,
		&decision.Status,
		&decision.RuntimeFencingToken,
		&decision.UIPayload,
	)
	return decision, err
}

func (p *ledgerProbe) waitPendingUserInput(ctx context.Context, runID string) (userInputRecord, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		decision, err := p.pendingUserInput(ctx, runID)
		switch {
		case err == nil:
			return decision, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return userInputRecord{}, err
		}
		select {
		case <-ctx.Done():
			return userInputRecord{}, fmt.Errorf("wait for pending user input: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (p *ledgerProbe) userInputStatus(ctx context.Context, decisionID string) (string, error) {
	var status string
	err := p.pool.QueryRow(ctx, `
SELECT status
FROM user_input_requests
WHERE id = $1::uuid`, decisionID).Scan(&status)
	return status, err
}

func (p *ledgerProbe) pendingToolApproval(ctx context.Context, runID string) (toolApprovalRecord, error) {
	const query = `
SELECT
  id::text,
  COALESCE(run_id::text, ''),
  COALESCE(turn_id::text, ''),
  status,
  COALESCE(runtime_fencing_token, 0)
FROM tool_approval_requests
WHERE run_id = $1::uuid
  AND status = 'pending'
ORDER BY created_at DESC
LIMIT 1`
	var decision toolApprovalRecord
	err := p.pool.QueryRow(ctx, query, runID).Scan(
		&decision.ID,
		&decision.RunID,
		&decision.TurnID,
		&decision.Status,
		&decision.RuntimeFencingToken,
	)
	return decision, err
}

func (p *ledgerProbe) waitPendingToolApproval(ctx context.Context, runID string) (toolApprovalRecord, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		decision, err := p.pendingToolApproval(ctx, runID)
		switch {
		case err == nil:
			return decision, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return toolApprovalRecord{}, err
		}
		select {
		case <-ctx.Done():
			return toolApprovalRecord{}, fmt.Errorf("wait for pending tool approval: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (p *ledgerProbe) toolApprovalStatus(ctx context.Context, decisionID string) (string, error) {
	var status string
	err := p.pool.QueryRow(ctx, `
SELECT status
FROM tool_approval_requests
WHERE id = $1::uuid`, decisionID).Scan(&status)
	return status, err
}

func (p *ledgerProbe) userInputResponseIdentity(ctx context.Context, decisionID string) (decisionResponseIdentityRecord, error) {
	var record decisionResponseIdentityRecord
	err := p.pool.QueryRow(ctx, `
SELECT COALESCE(response_control_id, ''), COALESCE(response_payload_hash, '')
FROM user_input_requests
WHERE id = $1::uuid`, decisionID).Scan(
		&record.ControlID,
		&record.PayloadHash,
	)
	return record, err
}

func jsonContainsString(encoded []byte, needle string) bool {
	if strings.TrimSpace(needle) == "" {
		return true
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return false
	}
	return valueContainsString(value, needle)
}

func valueContainsString(value any, needle string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, needle)
	case []any:
		for _, item := range typed {
			if valueContainsString(item, needle) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if valueContainsString(item, needle) {
				return true
			}
		}
	}
	return false
}
