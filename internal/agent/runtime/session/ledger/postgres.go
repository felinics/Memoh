package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
)

// PostgresStore implements Store over the session_runs table.
type PostgresStore struct {
	q *dbsqlc.Queries
}

// NewPostgres returns a Store backed by the PostgreSQL sqlc queries.
func NewPostgres(q *dbsqlc.Queries) Store {
	return &PostgresStore{q: q}
}

func (s *PostgresStore) ready() error {
	if s == nil || s.q == nil {
		return errors.New("ledger(postgres): queries not configured")
	}
	return nil
}

func (s *PostgresStore) Admit(ctx context.Context, params AdmitParams) (Run, bool, error) {
	if err := s.ready(); err != nil {
		return Run{}, false, err
	}
	runID, err := dbpkg.ParseUUID(params.RunID)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): invalid run id: %w", err)
	}
	botID, err := dbpkg.ParseUUID(params.BotID)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): invalid bot id: %w", err)
	}
	sessionID, err := dbpkg.ParseUUID(params.SessionID)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): invalid session id: %w", err)
	}
	turnID, err := dbpkg.ParseUUID(params.TurnID)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): invalid turn id: %w", err)
	}
	if run, handled, err := s.admitFastPath(ctx, params); handled {
		return run, false, err
	}
	row, err := s.q.AdmitSessionRun(ctx, dbsqlc.AdmitSessionRunParams{
		RunID:            runID,
		BotID:            botID,
		SessionID:        sessionID,
		InvocationID:     params.InvocationID,
		TurnID:           turnID,
		InputJson:        params.Input,
		InputFingerprint: params.InputFingerprint,
	})
	if err == nil {
		return runFromRow(row), true, nil
	}
	if isSingleActiveViolation(err) {
		// An admission that raced past the fast path. The statement rolled back
		// whole, position included, so the caller may submit this same
		// invocation again later.
		return Run{}, false, ErrSessionBusy
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Run{}, false, fmt.Errorf("ledger(postgres): admit run: %w", err)
	}
	// No row inserted. Either this invocation was already admitted — in which
	// case ON CONFLICT DO NOTHING has already waited for the concurrent inserter
	// to commit, so the re-select is guaranteed to see it — or the session does
	// not exist.
	existing, err := s.GetByInvocation(ctx, params.SessionID, params.InvocationID)
	if errors.Is(err, ErrRunNotFound) {
		return Run{}, false, ErrSessionNotFound
	}
	if err != nil {
		return Run{}, false, err
	}
	return existing, false, nil
}

// admitFastPath answers the two cases that need no write at all, using one
// indexed read of session_runs_single_active. handled=false means "go write".
//
// The point is that busy stops being an exceptional path. Without this, every
// contended submission reaches the INSERT, takes the bot_sessions row lock to
// allocate a position, throws that work away on the unique violation, and logs
// a PostgreSQL ERROR on the way out — per retry, per caller. A retrying channel
// adapter would turn ordinary backpressure into error-log volume.
//
// This read takes no lock, so it is a filter and not a decision: between it and
// the INSERT another admission can still win. That race is exactly what
// session_runs_single_active is for, and the write path still handles it. The
// fast path only has to be right about the common case to be worth having.
func (s *PostgresStore) admitFastPath(ctx context.Context, params AdmitParams) (Run, bool, error) {
	active, err := s.ActiveRun(ctx, params.SessionID)
	switch {
	case errors.Is(err, ErrRunNotFound):
		// Idle session, or one that does not exist; the write path tells those
		// apart because only it knows whether the session row is there.
		return Run{}, false, nil
	case err != nil:
		return Run{}, true, err
	case active.InvocationID == params.InvocationID:
		// Our own run, still going. This is a replay, not a conflict, and the
		// active row is the same answer GetByInvocation would return.
		return active, true, nil
	default:
		return Run{}, true, ErrSessionBusy
	}
}

func (s *PostgresStore) Get(ctx context.Context, runID string) (Run, error) {
	if err := s.ready(); err != nil {
		return Run{}, err
	}
	pgRunID, err := dbpkg.ParseUUID(runID)
	if err != nil {
		return Run{}, fmt.Errorf("ledger(postgres): invalid run id: %w", err)
	}
	row, err := s.q.GetSessionRun(ctx, pgRunID)
	if err != nil {
		return Run{}, wrapLookup("get run", err)
	}
	return runFromRow(row), nil
}

func (s *PostgresStore) GetByInvocation(ctx context.Context, sessionID, invocationID string) (Run, error) {
	if err := s.ready(); err != nil {
		return Run{}, err
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return Run{}, fmt.Errorf("ledger(postgres): invalid session id: %w", err)
	}
	row, err := s.q.GetSessionRunByInvocation(ctx, dbsqlc.GetSessionRunByInvocationParams{
		SessionID:    pgSessionID,
		InvocationID: invocationID,
	})
	if err != nil {
		return Run{}, wrapLookup("get run by invocation", err)
	}
	return runFromRow(row), nil
}

func (s *PostgresStore) ActiveRun(ctx context.Context, sessionID string) (Run, error) {
	if err := s.ready(); err != nil {
		return Run{}, err
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return Run{}, fmt.Errorf("ledger(postgres): invalid session id: %w", err)
	}
	row, err := s.q.GetActiveSessionRun(ctx, pgSessionID)
	if err != nil {
		return Run{}, wrapLookup("get active run", err)
	}
	return runFromRow(row), nil
}

func (s *PostgresStore) LatestRun(ctx context.Context, sessionID string) (Run, error) {
	if err := s.ready(); err != nil {
		return Run{}, err
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return Run{}, fmt.Errorf("ledger(postgres): invalid session id: %w", err)
	}
	row, err := s.q.GetLatestSessionRun(ctx, pgSessionID)
	if err != nil {
		return Run{}, wrapLookup("get latest run", err)
	}
	return runFromRow(row), nil
}

func (s *PostgresStore) NextFencingToken(ctx context.Context) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	token, err := s.q.NextSessionRunFencingToken(ctx)
	if err != nil {
		return 0, fmt.Errorf("ledger(postgres): next fencing token: %w", err)
	}
	return token, nil
}

func (s *PostgresStore) Claim(ctx context.Context, params ClaimParams) (Run, bool, error) {
	if err := s.ready(); err != nil {
		return Run{}, false, err
	}
	runID, err := dbpkg.ParseUUID(params.RunID)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): invalid run id: %w", err)
	}
	row, err := s.q.ClaimSessionRun(ctx, dbsqlc.ClaimSessionRunParams{
		RunID:          runID,
		OwnerID:        textOrNull(params.OwnerID),
		FencingToken:   params.FencingToken,
		LiveGeneration: textOrNull(params.LiveGeneration),
	})
	return applyResult("claim run", row, err)
}

func (s *PostgresStore) SetWaitingDecision(ctx context.Context, runID string, fencingToken int64) (Run, bool, error) {
	if err := s.ready(); err != nil {
		return Run{}, false, err
	}
	pgRunID, err := dbpkg.ParseUUID(runID)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): invalid run id: %w", err)
	}
	row, err := s.q.SetSessionRunWaitingDecision(ctx, dbsqlc.SetSessionRunWaitingDecisionParams{
		RunID:        pgRunID,
		FencingToken: fencingToken,
	})
	return applyResult("set run waiting decision", row, err)
}

func (s *PostgresStore) Resume(ctx context.Context, runID string, fencingToken int64) (Run, bool, error) {
	if err := s.ready(); err != nil {
		return Run{}, false, err
	}
	pgRunID, err := dbpkg.ParseUUID(runID)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): invalid run id: %w", err)
	}
	row, err := s.q.ResumeSessionRun(ctx, dbsqlc.ResumeSessionRunParams{
		RunID:        pgRunID,
		FencingToken: fencingToken,
	})
	return applyResult("resume run", row, err)
}

func (s *PostgresStore) Finalize(ctx context.Context, params FinalizeParams) (Run, bool, error) {
	if err := s.ready(); err != nil {
		return Run{}, false, err
	}
	if !params.State.Terminal() {
		return Run{}, false, fmt.Errorf("ledger(postgres): %q is not a terminal state", params.State)
	}
	runID, err := dbpkg.ParseUUID(params.RunID)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): invalid run id: %w", err)
	}
	row, err := s.q.FinalizeSessionRun(ctx, dbsqlc.FinalizeSessionRunParams{
		RunID:        runID,
		FencingToken: params.FencingToken,
		State:        string(params.State),
		ErrorCode:    textOrNull(params.ErrorCode),
		ErrorMessage: textOrNull(params.ErrorMessage),
	})
	return applyResult("finalize run", row, err)
}

func (s *PostgresStore) RequestAbort(ctx context.Context, runID string) (Run, bool, error) {
	if err := s.ready(); err != nil {
		return Run{}, false, err
	}
	pgRunID, err := dbpkg.ParseUUID(runID)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): invalid run id: %w", err)
	}
	row, err := s.q.RequestSessionRunAbort(ctx, pgRunID)
	return applyResult("request run abort", row, err)
}

func (s *PostgresStore) StaleGenerationRuns(ctx context.Context, query StaleGenerationQuery) ([]Run, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.q.ListStaleGenerationSessionRuns(ctx, dbsqlc.ListStaleGenerationSessionRunsParams{
		CurrentGeneration: textOrNull(query.CurrentGeneration),
		CursorGeneration:  textOrNull(query.After.LiveGeneration),
		CursorRunID:       dbpkg.ParseUUIDOrEmpty(query.After.RunID),
		BatchSize:         query.Limit,
	})
	if err != nil {
		return nil, fmt.Errorf("ledger(postgres): list stale generation runs: %w", err)
	}
	return runsFromRows(rows), nil
}

func (s *PostgresStore) OrphanedRuns(ctx context.Context, query OrphanQuery) ([]Run, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.q.ListOrphanedSessionRuns(ctx, dbsqlc.ListOrphanedSessionRunsParams{
		GraceSeconds: query.MinAge.Seconds(),
		BatchSize:    query.Limit,
	})
	if err != nil {
		return nil, fmt.Errorf("ledger(postgres): list orphaned runs: %w", err)
	}
	return runsFromRows(rows), nil
}

// applyResult folds the fenced-CAS contract into one place: no rows means the
// transition was already applied or a newer owner superseded this token, which
// is an ordinary outcome rather than an error.
func applyResult(operation string, row dbsqlc.SessionRun, err error) (Run, bool, error) {
	switch {
	case err == nil:
		return runFromRow(row), true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return Run{}, false, nil
	default:
		return Run{}, false, fmt.Errorf("ledger(postgres): %s: %w", operation, err)
	}
}

// singleActiveRunConstraint is the partial unique index that enforces
// SR-OWN-001. Matching the constraint name rather than SQLSTATE alone matters:
// the same statement can also violate the invocation index, and the two mean
// opposite things — one is "you are too early", the other is "you already have
// an answer".
const singleActiveRunConstraint = "session_runs_single_active"

func isSingleActiveViolation(err error) bool {
	if !dbpkg.IsUniqueViolation(err) {
		return false
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == singleActiveRunConstraint
}

func wrapLookup(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRunNotFound
	}
	return fmt.Errorf("ledger(postgres): %s: %w", operation, err)
}

func runsFromRows(rows []dbsqlc.SessionRun) []Run {
	runs := make([]Run, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, runFromRow(row))
	}
	return runs
}

func runFromRow(row dbsqlc.SessionRun) Run {
	return Run{
		RunID:            uuidString(row.RunID),
		BotID:            uuidString(row.BotID),
		SessionID:        uuidString(row.SessionID),
		InvocationID:     row.InvocationID,
		TurnID:           uuidString(row.TurnID),
		TurnPosition:     row.TurnPosition,
		State:            State(row.State),
		Input:            row.InputJson,
		InputFingerprint: row.InputFingerprint,
		OwnerID:          dbpkg.TextToString(row.OwnerID),
		FencingToken:     row.FencingToken,
		OwnerSince:       dbpkg.TimeFromPg(row.OwnerSince),
		LiveGeneration:   dbpkg.TextToString(row.LiveGeneration),
		AbortRequestedAt: dbpkg.TimeFromPg(row.AbortRequestedAt),
		ErrorCode:        dbpkg.TextToString(row.ErrorCode),
		ErrorMessage:     dbpkg.TextToString(row.ErrorMessage),
		CreatedAt:        dbpkg.TimeFromPg(row.CreatedAt),
		UpdatedAt:        dbpkg.TimeFromPg(row.UpdatedAt),
	}
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return value.String()
}

// textOrNull keeps an unset string out of the column entirely, so owner_id and
// live_generation stay NULL until a claim writes them. The orphan and recovery
// indexes both depend on that distinction.
func textOrNull(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}
