package application

import (
	"context"
	"errors"

	"github.com/google/uuid"

	sessionqueue "github.com/felinics/memoh/internal/agent/runtime/session/queue"
	db "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// SessionQueues is the application surface for user-facing queue operations.
// HTTP and channel adapters call these methods after authorization; the
// service owns transactions and the queue package owns the SQL primitives.
type SessionQueues struct {
	Steer    []sessionqueue.SteerItem
	FollowUp []sessionqueue.FollowUpItem
}

var errQueueTransactionsUnavailable = errors.New("session queue requires transactional queries")

// queueAdmissionLimit bounds concurrent enqueue statements per process. The
// enqueue SQL is one statement, so its cost is one pooled connection for the
// statement's duration; beyond about twice the pool size a request only waits
// in the pgx acquire queue. Rejecting there with ErrAdmissionOverloaded lets
// clients retry with the same invocation_id instead of holding a goroutine
// for hundreds of milliseconds.
const defaultQueueAdmissionLimit = 64

// SetQueueAdmissionLimit overrides the per-process enqueue concurrency bound.
// Zero or negative restores the default.
func (s *Service) SetQueueAdmissionLimit(limit int) {
	if s == nil {
		return
	}
	if limit <= 0 {
		limit = defaultQueueAdmissionLimit
	}
	s.queueAdmissionMu.Lock()
	defer s.queueAdmissionMu.Unlock()
	s.queueAdmissionGate = make(chan struct{}, limit)
}

func (s *Service) acquireQueueAdmission() (release func(), err error) {
	s.queueAdmissionMu.Lock()
	if s.queueAdmissionGate == nil {
		s.queueAdmissionGate = make(chan struct{}, defaultQueueAdmissionLimit)
	}
	gate := s.queueAdmissionGate
	select {
	case gate <- struct{}{}:
		s.queueAdmissionMu.Unlock()
		return func() { <-gate }, nil
	default:
		s.queueAdmissionMu.Unlock()
		return nil, sessionqueue.ErrAdmissionOverloaded
	}
}

func (s *Service) queueTx(ctx context.Context, fn func(dbstore.Queries) error) error {
	if s == nil || s.queries == nil {
		return errQueueTransactionsUnavailable
	}
	runner, ok := s.queries.(interface {
		InTx(context.Context, func(dbstore.Queries) error) error
	})
	if !ok {
		return errQueueTransactionsUnavailable
	}
	return runner.InTx(ctx, fn)
}

// EnqueueSteer admits one steer item for the session's active run. The
// enqueue is a single statement that locks the session row, re-reads the
// active run, and inserts; it runs in autocommit rather than an explicit
// transaction, which removes the BEGIN and COMMIT round trips without
// changing atomicity or the replay lookup semantics.
func (s *Service) EnqueueSteer(ctx context.Context, botID, sessionID, invocationID string, payload []byte) (sessionqueue.SteerItem, error) {
	if s == nil || s.queries == nil {
		return sessionqueue.SteerItem{}, errQueueTransactionsUnavailable
	}
	release, err := s.acquireQueueAdmission()
	if err != nil {
		return sessionqueue.SteerItem{}, err
	}
	defer release()
	return sessionqueue.NewPostgresStore(s.queries).EnqueueSteer(ctx, botID, sessionID, uuid.NewString(), invocationID, payload)
}

// EnqueueFollowUp admits one follow-up item during the session's active run.
func (s *Service) EnqueueFollowUp(ctx context.Context, botID, sessionID, invocationID string, payload []byte) (sessionqueue.FollowUpItem, error) {
	if s == nil || s.queries == nil {
		return sessionqueue.FollowUpItem{}, errQueueTransactionsUnavailable
	}
	release, err := s.acquireQueueAdmission()
	if err != nil {
		return sessionqueue.FollowUpItem{}, err
	}
	defer release()
	return sessionqueue.NewPostgresStore(s.queries).EnqueueFollowUp(ctx, botID, sessionID, uuid.NewString(), invocationID, payload)
}

// ListSessionQueues returns the head of both pending queues from one
// statement snapshot. The list is bounded; a backlog beyond the limit is still
// consumed in FIFO order, it is only not rendered.
func (s *Service) ListSessionQueues(ctx context.Context, sessionID string) (SessionQueues, error) {
	if s == nil || s.queries == nil {
		return SessionQueues{}, errQueueTransactionsUnavailable
	}
	steers, followUps, err := sessionqueue.NewPostgresStore(s.queries).PendingQueues(ctx, sessionID, sessionqueue.DefaultPendingListLimit)
	if err != nil {
		return SessionQueues{}, err
	}
	return SessionQueues{Steer: steers, FollowUp: followUps}, nil
}

// ReorderSteer moves one accepted steer before another. The reorder statement
// locks the session row itself.
func (s *Service) ReorderSteer(ctx context.Context, sessionID string, item, before sessionqueue.SteerPendingRef) ([]sessionqueue.SteerItem, error) {
	var items []sessionqueue.SteerItem
	err := s.queueTx(ctx, func(q dbstore.Queries) error {
		var err error
		items, err = sessionqueue.NewPostgresStore(q).ReorderSteer(ctx, sessionID, item, before)
		return err
	})
	return items, err
}

func (s *Service) ReorderFollowUp(ctx context.Context, sessionID string, item, before sessionqueue.FollowUpPendingRef) ([]sessionqueue.FollowUpItem, error) {
	var items []sessionqueue.FollowUpItem
	err := s.queueTx(ctx, func(q dbstore.Queries) error {
		var err error
		items, err = sessionqueue.NewPostgresStore(q).ReorderFollowUp(ctx, sessionID, item, before)
		return err
	})
	return items, err
}

// UpdateSteer edits an accepted steer. The UPDATE is status-guarded, so a
// concurrent claim makes it return ErrNotPending instead of racing.
func (s *Service) UpdateSteer(ctx context.Context, sessionID, itemID string, payload []byte) (sessionqueue.SteerItem, error) {
	var item sessionqueue.SteerItem
	err := s.queueTx(ctx, func(q dbstore.Queries) error {
		var err error
		item, err = sessionqueue.NewPostgresStore(q).UpdateSteer(ctx, sessionID, itemID, payload)
		return err
	})
	return item, err
}

func (s *Service) UpdateFollowUp(ctx context.Context, sessionID, itemID string, payload []byte) (sessionqueue.FollowUpItem, error) {
	var item sessionqueue.FollowUpItem
	err := s.queueTx(ctx, func(q dbstore.Queries) error {
		var err error
		item, err = sessionqueue.NewPostgresStore(q).UpdateFollowUp(ctx, sessionID, itemID, payload)
		return err
	})
	return item, err
}

func (s *Service) CancelSteer(ctx context.Context, sessionID, itemID string) error {
	return s.queueTx(ctx, func(q dbstore.Queries) error {
		return sessionqueue.NewPostgresStore(q).CancelSteer(ctx, sessionID, itemID)
	})
}

func (s *Service) CancelFollowUp(ctx context.Context, sessionID, itemID string) error {
	return s.queueTx(ctx, func(q dbstore.Queries) error {
		return sessionqueue.NewPostgresStore(q).CancelFollowUp(ctx, sessionID, itemID)
	})
}

// PromoteFollowUpToSteer moves explicit user intent from the follow-up queue
// into the steer queue of the active run. The promotion reads, inserts, and
// cancels across two tables, so it takes the session admission lock first;
// CommitStep takes the same row lock, which serializes assignment against it.
func (s *Service) PromoteFollowUpToSteer(ctx context.Context, botID, sessionID string, followUp sessionqueue.FollowUpPendingRef) (sessionqueue.PromoteFollowUpResult, error) {
	var result sessionqueue.PromoteFollowUpResult
	err := s.queueTx(ctx, func(q dbstore.Queries) error {
		botUUID, err := db.ParseUUID(botID)
		if err != nil {
			return err
		}
		sessionUUID, err := db.ParseUUID(sessionID)
		if err != nil {
			return err
		}
		if _, err = q.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{BotID: botUUID, SessionID: sessionUUID}); err != nil {
			return err
		}
		result, err = sessionqueue.NewPromotionCoordinator(q).PromoteFollowUpToSteer(ctx, sessionqueue.PromoteFollowUpRequest{
			BotID: botID, SessionID: sessionID, FollowUp: followUp,
		})
		return err
	})
	return result, err
}
