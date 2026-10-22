package contextepoch

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// ErrAdvanceStale reports that a guarded write lost to a concurrent advance:
// the stored state already moved at least as far forward as the attempted
// write. Callers should reload rather than retry the same write.
var ErrAdvanceStale = errors.New("context epoch state already advanced")

// Queries is the narrow slice of the session store the epoch state needs.
type Queries interface {
	GetSessionContextEpoch(ctx context.Context, sessionID pgtype.UUID) (string, error)
	AdvanceSessionContextEpoch(ctx context.Context, arg sqlc.AdvanceSessionContextEpochParams) (string, error)
}

// Store persists and applies Context Epoch state. A nil Store (or one without
// queries) degrades every load to the zero anchor with no persistence, so
// callers never need to nil-check.
type Store struct {
	queries Queries
	logger  *slog.Logger
	now     func() time.Time
}

// NewStore builds a Store over the session queries.
func NewStore(queries Queries, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{queries: queries, logger: logger.With(slog.String("store", "contextepoch")), now: time.Now}
}

// SetClock overrides the wall clock used for state timestamps. Tests use it
// to simulate time passing between turns.
func (s *Store) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Store) available() bool {
	return s != nil && s.queries != nil
}

// Load reads the session's state; the zero State means none was recorded.
func (s *Store) Load(ctx context.Context, sessionID string) (State, error) {
	if !s.available() {
		return State{}, nil
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return State{}, err
	}
	raw, err := s.queries.GetSessionContextEpoch(ctx, pgSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	return decodeState(raw)
}

// ensureInitialized loads the state, writing the initial one when the session
// has none yet. Persistence failures degrade to the in-memory initial state:
// the load path stays anchored at the session's beginning and the next turn
// retries the write.
func (s *Store) ensureInitialized(ctx context.Context, sessionID, path string) State {
	state, err := s.Load(ctx, sessionID)
	if err != nil {
		s.logger.WarnContext(ctx, "context_epoch_load_failed",
			slog.String("path", path),
			slog.String("session_id", sessionID),
			slog.Any("error", err))
		return Initial(s.now())
	}
	if !state.IsZero() {
		return state
	}
	initial := Initial(s.now())
	persisted, err := s.Advance(ctx, sessionID, initial)
	if err != nil {
		if errors.Is(err, ErrAdvanceStale) && !persisted.IsZero() {
			// Lost the init race: Advance resolved to the state the winner
			// committed — use it instead of the never-persisted initial one.
			return persisted
		}
		s.logger.WarnContext(ctx, "context_epoch_init_failed",
			slog.String("path", path),
			slog.String("session_id", sessionID),
			slog.Any("error", err))
		return initial
	}
	return persisted
}

// Advance writes next under the SQL-side monotonic guard. A write that loses
// to a concurrent advance resolves to the stored state with ErrAdvanceStale.
func (s *Store) Advance(ctx context.Context, sessionID string, next State) (State, error) {
	if !s.available() {
		return next, nil
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return State{}, err
	}
	encoded, err := next.encode()
	if err != nil {
		return State{}, err
	}
	stored, err := s.queries.AdvanceSessionContextEpoch(ctx, sqlc.AdvanceSessionContextEpochParams{
		State:             encoded,
		SessionID:         pgSessionID,
		Epoch:             next.Epoch,
		AnchorCreatedAtMs: next.AnchorCreatedAtMs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		current, loadErr := s.Load(ctx, sessionID)
		if loadErr != nil {
			return State{}, errors.Join(ErrAdvanceStale, loadErr)
		}
		return current, ErrAdvanceStale
	}
	if err != nil {
		return State{}, err
	}
	return decodeState(stored)
}

// LoadWindow resolves the session anchor, runs the byte-budgeted load from
// it, and records an explicit budget-driven advance when admission dropped
// older rows. measure counts the rows present since a given anchor; load runs
// the anchored window query. Wall-clock time never enters either callback.
func (s *Store) LoadWindow(
	ctx context.Context,
	sessionID, path string,
	measure func(context.Context, time.Time) (int64, error),
	load func(context.Context, time.Time) ([]messagepkg.Message, error),
) ([]messagepkg.Message, error) {
	if !s.available() {
		return load(ctx, Beginning)
	}
	state := s.ensureInitialized(ctx, sessionID, path)
	since := state.AnchorTime()
	msgs, err := load(ctx, since)
	if err != nil {
		return nil, err
	}
	s.recordBudgetAdvance(ctx, sessionID, path, state, since, measure, msgs)
	return msgs, nil
}

// AdvanceInPlace records an explicit epoch advance that leaves the anchor
// untouched: the stable part changed without a history-window slide (e.g. an
// in-turn budget recovery rewrote the history region), and rule 6 requires a
// record. The session is initialized first when it has no state yet.
func (s *Store) AdvanceInPlace(ctx context.Context, sessionID, path, reason string) (State, error) {
	if !s.available() {
		return State{}, nil
	}
	state := s.ensureInitialized(ctx, sessionID, path)
	next, err := state.AdvancedBy(state.AnchorTime(), state.AnchorMessageID, reason, s.now())
	if err != nil {
		return State{}, err
	}
	persisted, err := s.Advance(ctx, sessionID, next)
	if errors.Is(err, ErrAdvanceStale) {
		// A concurrent writer moved first; the record of a change exists,
		// which is what the bookkeeping requires.
		return persisted, nil
	}
	if err != nil {
		return State{}, err
	}
	s.logger.InfoContext(ctx, "context_epoch_advance",
		slog.String("path", path),
		slog.String("session_id", sessionID),
		slog.String("reason", reason),
		slog.Int64("from_epoch", state.Epoch),
		slog.Int64("to_epoch", persisted.Epoch))
	return persisted, nil
}

// recordBudgetAdvance detects a budget-bound load — the session holds more
// rows since the anchor than the byte budget admitted — and turns the silent
// slide into an explicit, recorded anchor advance (reason "budget"). The new
// anchor is the oldest admitted row, so the following load measures exactly
// what it loads and no further advance fires until the budget binds again.
func (s *Store) recordBudgetAdvance(
	ctx context.Context,
	sessionID, path string,
	state State,
	since time.Time,
	measure func(context.Context, time.Time) (int64, error),
	loaded []messagepkg.Message,
) {
	if len(loaded) == 0 {
		return
	}
	total, err := measure(ctx, since)
	if err != nil {
		s.logger.WarnContext(ctx, "context_epoch_measure_failed",
			slog.String("path", path),
			slog.String("session_id", sessionID),
			slog.Any("error", err))
		return
	}
	if total <= int64(len(loaded)) {
		return
	}
	oldest := loaded[0]
	for _, msg := range loaded[1:] {
		if msg.CreatedAt.Before(oldest.CreatedAt) {
			oldest = msg
		}
	}
	// Compare at the persisted millisecond granularity: batch-persisted rows
	// share one PostgreSQL now(), so a budget cut inside such a batch always
	// leaves rows at the anchor's microsecond timestamp. Advancing to the same
	// millisecond anchor would record a new epoch without moving the window —
	// and repeat every turn.
	if oldest.CreatedAt.IsZero() || oldest.CreatedAt.UnixMilli() <= state.AnchorCreatedAtMs {
		return
	}
	next, err := state.AdvancedBy(oldest.CreatedAt, oldest.ID, ReasonBudget, s.now())
	if err != nil {
		s.logger.WarnContext(ctx, "context_epoch_advance_rejected",
			slog.String("path", path),
			slog.String("session_id", sessionID),
			slog.Any("error", err))
		return
	}
	persisted, err := s.Advance(ctx, sessionID, next)
	if err != nil && !errors.Is(err, ErrAdvanceStale) {
		s.logger.WarnContext(ctx, "context_epoch_advance_failed",
			slog.String("path", path),
			slog.String("session_id", sessionID),
			slog.Any("error", err))
		return
	}
	s.logger.InfoContext(ctx, "context_epoch_advance",
		slog.String("path", path),
		slog.String("session_id", sessionID),
		slog.String("reason", ReasonBudget),
		slog.Int64("from_epoch", state.Epoch),
		slog.Int64("to_epoch", persisted.Epoch),
		slog.Int64("anchor_created_at_ms", persisted.AnchorCreatedAtMs),
		slog.Int64("measured_messages", total),
		slog.Int("loaded_messages", len(loaded)))
}
