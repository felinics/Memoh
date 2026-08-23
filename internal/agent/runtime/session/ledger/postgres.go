package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	dbpkg "github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	"github.com/memohai/memoh/internal/runtimefence"
)

// PostgresStore implements Store over the session_runs table.
type PostgresStore struct {
	q    *dbsqlc.Queries
	pool *pgxpool.Pool
}

// NewPostgres returns a Store backed by the PostgreSQL sqlc queries.
func NewPostgres(q *dbsqlc.Queries, pools ...*pgxpool.Pool) Store {
	var pool *pgxpool.Pool
	if len(pools) > 0 {
		pool = pools[0]
	}
	return &PostgresStore{q: q, pool: pool}
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
	if s.pool == nil {
		return Run{}, false, errors.New("ledger(postgres): admission requires a transaction pool")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): begin admission: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	txq := s.q.WithTx(tx)
	if _, err := txq.LockBotForSessionRunClaim(ctx, botID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, false, ErrSessionNotFound
		}
		return Run{}, false, fmt.Errorf("ledger(postgres): lock admission bot: %w", err)
	}
	row, err := txq.AdmitLockedSessionRun(ctx, dbsqlc.AdmitLockedSessionRunParams{
		RunID:            runID,
		BotID:            botID,
		SessionID:        sessionID,
		InvocationID:     params.InvocationID,
		TurnID:           turnID,
		InputJson:        params.Input,
		InputFingerprint: params.InputFingerprint,
	})
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return Run{}, false, fmt.Errorf("ledger(postgres): commit admission: %w", err)
		}
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
	existingRow, err := txq.GetSessionRunByInvocation(ctx, dbsqlc.GetSessionRunByInvocationParams{
		SessionID: sessionID, InvocationID: params.InvocationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, resetErr := txq.GetEffectiveSessionRuntimeReset(ctx, dbsqlc.GetEffectiveSessionRuntimeResetParams{
			BotID: botID, SessionID: sessionID,
		}); resetErr == nil {
			return Run{}, false, ErrHistoryResetInProgress
		} else if !errors.Is(resetErr, pgx.ErrNoRows) {
			return Run{}, false, fmt.Errorf("ledger(postgres): check admission reset: %w", resetErr)
		}
		return Run{}, false, ErrSessionNotFound
	}
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): get admitted invocation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): commit replay admission: %w", err)
	}
	return runFromRow(existingRow), false, nil
}

func (s *PostgresStore) AcquireReset(ctx context.Context, lease ResetLease, ttl time.Duration) (ResetLease, bool, error) {
	if err := s.ready(); err != nil {
		return ResetLease{}, false, err
	}
	lease.Scope = strings.TrimSpace(lease.Scope)
	lease.BotID = strings.TrimSpace(lease.BotID)
	lease.SessionID = strings.TrimSpace(lease.SessionID)
	lease.Token = strings.TrimSpace(lease.Token)
	if ttl <= 0 || lease.BotID == "" || lease.Token == "" {
		return ResetLease{}, false, errors.New("ledger(postgres): reset scope, token, and positive ttl are required")
	}
	botID, err := dbpkg.ParseUUID(lease.BotID)
	if err != nil {
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): invalid reset bot id: %w", err)
	}
	token, err := dbpkg.ParseUUID(lease.Token)
	if err != nil {
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): invalid reset token: %w", err)
	}
	if s.pool == nil {
		return ResetLease{}, false, errors.New("ledger(postgres): reset acquisition requires a transaction pool")
	}
	var expires pgtype.Timestamptz
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): begin reset acquisition: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	txq := s.q.WithTx(tx)
	if _, err = txq.LockBotForRuntimeReset(ctx, botID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The bot is gone; the lease can never become acquirable. Retrying
			// would spin forever, so surface a terminal error instead.
			return ResetLease{}, false, ErrResetScopeNotFound
		}
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): lock reset bot: %w", err)
	}
	switch lease.Scope {
	case ResetScopeBot:
		expires, err = txq.AcquireLockedBotRuntimeReset(ctx, dbsqlc.AcquireLockedBotRuntimeResetParams{
			BotID: botID, ResetToken: token, LeaseMilliseconds: ttl.Milliseconds(),
		})
	case ResetScopeSession:
		sessionID, parseErr := dbpkg.ParseUUID(lease.SessionID)
		if parseErr != nil {
			return ResetLease{}, false, fmt.Errorf("ledger(postgres): invalid reset session id: %w", parseErr)
		}
		expires, err = txq.AcquireLockedBotSessionRuntimeReset(ctx, dbsqlc.AcquireLockedBotSessionRuntimeResetParams{
			BotID: botID, SessionID: sessionID, ResetToken: token, LeaseMilliseconds: ttl.Milliseconds(),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// No row means either "blocked by a live lease" (retryable) or
			// "session deleted / soft-deleted" (terminal). Distinguish them so
			// an acquire racing a session delete fails fast instead of spinning.
			live, liveErr := txq.SessionLiveForRuntimeReset(ctx, dbsqlc.SessionLiveForRuntimeResetParams{
				SessionID: sessionID, BotID: botID,
			})
			if liveErr != nil {
				return ResetLease{}, false, fmt.Errorf("ledger(postgres): check reset session: %w", liveErr)
			}
			if !live {
				return ResetLease{}, false, ErrResetScopeNotFound
			}
		}
	default:
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): unsupported reset scope %q", lease.Scope)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ResetLease{}, false, nil
	}
	if err != nil {
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): acquire reset: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): commit reset acquisition: %w", err)
	}
	lease.ExpiresAt = dbpkg.TimeFromPg(expires)
	return lease, true, nil
}

func (s *PostgresStore) RenewReset(ctx context.Context, lease ResetLease, ttl time.Duration) (ResetLease, bool, error) {
	if err := s.ready(); err != nil {
		return ResetLease{}, false, err
	}
	botID, token, sessionID, err := parseResetLeaseIDs(lease)
	if err != nil || ttl <= 0 {
		if err == nil {
			err = errors.New("positive reset ttl is required")
		}
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): renew reset: %w", err)
	}
	if s.pool == nil {
		return ResetLease{}, false, errors.New("ledger(postgres): reset renewal requires a transaction pool")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): begin reset renewal: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	txq := s.q.WithTx(tx)
	if _, err = txq.LockBotForRuntimeReset(ctx, botID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ResetLease{}, false, nil
		}
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): lock reset renewal bot: %w", err)
	}
	var expires pgtype.Timestamptz
	switch strings.TrimSpace(lease.Scope) {
	case ResetScopeBot:
		expires, err = txq.RenewBotRuntimeReset(ctx, dbsqlc.RenewBotRuntimeResetParams{
			BotID: botID, ResetToken: token, LeaseMilliseconds: ttl.Milliseconds(),
		})
	case ResetScopeSession:
		expires, err = txq.RenewBotSessionRuntimeReset(ctx, dbsqlc.RenewBotSessionRuntimeResetParams{
			BotID: botID, SessionID: sessionID, ResetToken: token, LeaseMilliseconds: ttl.Milliseconds(),
		})
	default:
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): unsupported reset scope %q", lease.Scope)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ResetLease{}, false, nil
	}
	if err != nil {
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): renew reset: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): commit reset renewal: %w", err)
	}
	lease.ExpiresAt = dbpkg.TimeFromPg(expires)
	return lease, true, nil
}

func (s *PostgresStore) ReleaseReset(ctx context.Context, lease ResetLease) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	botID, token, sessionID, err := parseResetLeaseIDs(lease)
	if err != nil {
		return false, fmt.Errorf("ledger(postgres): release reset: %w", err)
	}
	switch strings.TrimSpace(lease.Scope) {
	case ResetScopeBot:
		_, err = s.q.ReleaseBotRuntimeReset(ctx, dbsqlc.ReleaseBotRuntimeResetParams{BotID: botID, ResetToken: token})
	case ResetScopeSession:
		_, err = s.q.ReleaseBotSessionRuntimeReset(ctx, dbsqlc.ReleaseBotSessionRuntimeResetParams{
			BotID: botID, SessionID: sessionID, ResetToken: token,
		})
	default:
		return false, fmt.Errorf("ledger(postgres): unsupported reset scope %q", lease.Scope)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("ledger(postgres): release reset: %w", err)
	}
	return true, nil
}

func (s *PostgresStore) EffectiveReset(ctx context.Context, botID, sessionID string) (ResetLease, bool, error) {
	if err := s.ready(); err != nil {
		return ResetLease{}, false, err
	}
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): invalid reset bot id: %w", err)
	}
	if strings.TrimSpace(sessionID) == "" {
		// Bot-scope readers follow the shared precedence rule: any active
		// session lease of this bot blocks bot-wide activity too.
		row, loadErr := s.q.GetEffectiveBotScopeRuntimeReset(ctx, pgBotID)
		if errors.Is(loadErr, pgx.ErrNoRows) {
			return ResetLease{}, false, nil
		}
		if loadErr != nil {
			return ResetLease{}, false, fmt.Errorf("ledger(postgres): load bot reset: %w", loadErr)
		}
		lease := ResetLease{Scope: row.Scope, BotID: botID, Token: row.ResetToken.String(), ExpiresAt: dbpkg.TimeFromPg(row.RuntimeResetExpiresAt)}
		if row.Scope == ResetScopeSession && row.SessionID.Valid {
			lease.SessionID = row.SessionID.String()
		}
		return lease, true, nil
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): invalid reset session id: %w", err)
	}
	row, err := s.q.GetEffectiveSessionRuntimeReset(ctx, dbsqlc.GetEffectiveSessionRuntimeResetParams{BotID: pgBotID, SessionID: pgSessionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ResetLease{}, false, nil
	}
	if err != nil {
		return ResetLease{}, false, fmt.Errorf("ledger(postgres): load session reset: %w", err)
	}
	return ResetLease{Scope: row.Scope, BotID: botID, SessionID: sessionID, Token: row.ResetToken.String(), ExpiresAt: dbpkg.TimeFromPg(row.RuntimeResetExpiresAt)}, true, nil
}

func (s *PostgresStore) ActiveRunsByBot(ctx context.Context, botID string) ([]Run, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, fmt.Errorf("ledger(postgres): invalid bot id: %w", err)
	}
	rows, err := s.q.ListActiveSessionRunsByBot(ctx, pgBotID)
	if err != nil {
		return nil, fmt.Errorf("ledger(postgres): list active bot runs: %w", err)
	}
	return runsFromRows(rows), nil
}

func (s *PostgresStore) FenceAndFinalizeOrphan(ctx context.Context, reset ResetLease, run Run) (Run, bool, error) {
	if err := s.ready(); err != nil {
		return Run{}, false, err
	}
	if s.pool == nil {
		return Run{}, false, errors.New("ledger(postgres): orphan reset finalization requires a transaction pool")
	}
	if strings.TrimSpace(reset.BotID) != strings.TrimSpace(run.BotID) ||
		(reset.Scope == ResetScopeSession && strings.TrimSpace(reset.SessionID) != strings.TrimSpace(run.SessionID)) {
		return Run{}, false, runtimefence.ErrResetLeaseLost
	}
	botID, resetToken, resetSessionID, err := parseResetLeaseIDs(reset)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): orphan reset lease: %w", err)
	}
	runID, err := dbpkg.ParseUUID(run.RunID)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): orphan run id: %w", err)
	}
	sessionID, err := dbpkg.ParseUUID(run.SessionID)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): orphan session id: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): begin orphan reset finalization: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	txq := s.q.WithTx(tx)
	if _, err := txq.LockBotForRuntimeReset(ctx, botID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, false, runtimefence.ErrResetLeaseLost
		}
		return Run{}, false, runtimefence.NormalizeResetError(ctx, fmt.Errorf("ledger(postgres): lock orphan reset bot: %w", err))
	}
	switch reset.Scope {
	case ResetScopeBot:
		_, err = txq.ValidateLockedBotRuntimeReset(ctx, dbsqlc.ValidateLockedBotRuntimeResetParams{BotID: botID, ResetToken: resetToken})
	case ResetScopeSession:
		_, err = txq.ValidateLockedBotSessionRuntimeReset(ctx, dbsqlc.ValidateLockedBotSessionRuntimeResetParams{
			BotID: botID, SessionID: resetSessionID, ResetToken: resetToken,
		})
	default:
		return Run{}, false, runtimefence.ErrResetLeaseLost
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, false, runtimefence.ErrResetLeaseLost
	}
	if err != nil {
		return Run{}, false, runtimefence.NormalizeResetError(ctx, fmt.Errorf("ledger(postgres): validate orphan reset lease: %w", err))
	}
	if _, err := txq.LockActiveSessionRunForHistoryReset(ctx, dbsqlc.LockActiveSessionRunForHistoryResetParams{
		RunID: runID, BotID: botID, SessionID: sessionID, FencingToken: run.FencingToken,
	}); errors.Is(err, pgx.ErrNoRows) {
		return Run{}, false, nil
	} else if err != nil {
		return Run{}, false, runtimefence.NormalizeResetError(ctx, fmt.Errorf("ledger(postgres): lock orphan run: %w", err))
	}
	if strings.TrimSpace(run.OwnerID) != "" {
		currentFence, err := txq.LockSessionRuntimeFenceForActivation(ctx, dbsqlc.LockSessionRuntimeFenceForActivationParams{
			BotID: botID, SessionID: sessionID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, false, nil
		}
		if err != nil {
			return Run{}, false, runtimefence.NormalizeResetError(ctx, fmt.Errorf("ledger(postgres): lock orphan session fence: %w", err))
		}
		newFence, err := txq.NextSessionRuntimeFenceToken(ctx)
		if err != nil {
			return Run{}, false, runtimefence.NormalizeResetError(ctx, fmt.Errorf("ledger(postgres): draw orphan fencing token: %w", err))
		}
		if newFence <= currentFence || newFence <= run.FencingToken {
			return Run{}, false, errors.New("ledger(postgres): orphan fencing token did not advance")
		}
		if _, err := txq.ActivateSessionRuntimeFence(ctx, dbsqlc.ActivateSessionRuntimeFenceParams{
			BotID: botID, SessionID: sessionID, RuntimeFencingToken: newFence,
		}); err != nil {
			return Run{}, false, runtimefence.NormalizeResetError(ctx, fmt.Errorf("ledger(postgres): activate orphan fencing token: %w", err))
		}
		if _, err := txq.SupersedePendingToolApprovalsBySession(ctx, dbsqlc.SupersedePendingToolApprovalsBySessionParams{
			Reason: "tool approval cancelled: superseded by history reset", BotID: botID, SessionID: sessionID,
		}); err != nil {
			return Run{}, false, runtimefence.NormalizeResetError(ctx, fmt.Errorf("ledger(postgres): cancel orphan approvals: %w", err))
		}
		if _, err := txq.SupersedePendingUserInputsBySession(ctx, dbsqlc.SupersedePendingUserInputsBySessionParams{
			ResultJson: []byte(`{"status":"canceled","reason":"history_reset"}`), BotID: botID, SessionID: sessionID,
		}); err != nil {
			return Run{}, false, runtimefence.NormalizeResetError(ctx, fmt.Errorf("ledger(postgres): cancel orphan inputs: %w", err))
		}
	}
	row, err := txq.FinalizeSessionRun(ctx, dbsqlc.FinalizeSessionRunParams{
		RunID: runID, FencingToken: run.FencingToken, State: string(StateAborted),
		ErrorCode: textOrNull("history_reset"), ErrorMessage: textOrNull("run canceled by history reset"),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, runtimefence.NormalizeResetError(ctx, fmt.Errorf("ledger(postgres): finalize orphan run: %w", err))
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, false, runtimefence.NormalizeResetError(ctx, fmt.Errorf("ledger(postgres): commit orphan reset finalization: %w", err))
	}
	return runFromRow(row), true, nil
}

func parseResetLeaseIDs(lease ResetLease) (pgtype.UUID, pgtype.UUID, pgtype.UUID, error) {
	botID, err := dbpkg.ParseUUID(strings.TrimSpace(lease.BotID))
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("invalid reset bot id: %w", err)
	}
	token, err := dbpkg.ParseUUID(strings.TrimSpace(lease.Token))
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("invalid reset token: %w", err)
	}
	var sessionID pgtype.UUID
	if strings.TrimSpace(lease.Scope) == ResetScopeSession {
		sessionID, err = dbpkg.ParseUUID(strings.TrimSpace(lease.SessionID))
		if err != nil {
			return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("invalid reset session id: %w", err)
		}
	}
	return botID, token, sessionID, nil
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
	if _, active, err := s.EffectiveReset(ctx, params.BotID, params.SessionID); err != nil {
		return Run{}, true, err
	} else if active {
		return Run{}, true, ErrHistoryResetInProgress
	}
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
	if s.pool == nil {
		return Run{}, false, errors.New("ledger(postgres): claim requires a transaction pool")
	}
	existing, err := s.Get(ctx, params.RunID)
	if err != nil {
		return Run{}, false, err
	}
	botID, err := dbpkg.ParseUUID(existing.BotID)
	if err != nil {
		return Run{}, false, err
	}
	sessionID, err := dbpkg.ParseUUID(existing.SessionID)
	if err != nil {
		return Run{}, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	txq := s.q.WithTx(tx)
	if _, err := txq.LockBotForSessionRunClaim(ctx, botID); err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): lock claim bot: %w", err)
	}
	row, err := txq.ClaimLockedSessionRun(ctx, dbsqlc.ClaimLockedSessionRunParams{
		RunID: runID, BotID: botID, SessionID: sessionID,
		OwnerID: textOrNull(params.OwnerID), FencingToken: params.FencingToken,
		LiveGeneration: textOrNull(params.LiveGeneration),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, resetErr := txq.GetEffectiveSessionRuntimeReset(ctx, dbsqlc.GetEffectiveSessionRuntimeResetParams{
			BotID: botID, SessionID: sessionID,
		}); resetErr == nil {
			return Run{}, false, ErrHistoryResetInProgress
		} else if !errors.Is(resetErr, pgx.ErrNoRows) {
			return Run{}, false, fmt.Errorf("ledger(postgres): check claim reset: %w", resetErr)
		}
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): claim run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, false, fmt.Errorf("ledger(postgres): commit claim: %w", err)
	}
	return runFromRow(row), true, nil
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
