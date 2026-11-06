package attention

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/db"
)

// Notify policies of a schedule, as the schedule package stores them.
const (
	NotifyImportant = "important"
	NotifyEveryRun  = "every_run"
)

// ScheduleFire is the fire of a schedule running in a session right now.
type ScheduleFire struct {
	ScheduleID         string
	StartedAt          time.Time
	NotifyPolicy       string
	ContactViaChannels bool
	// Notified is true once this fire raised an update.
	Notified bool
}

// ActiveScheduleFire returns the fire running in sessionID; ok is false when
// no schedule is running there.
func (s *Service) ActiveScheduleFire(ctx context.Context, sessionID string) (ScheduleFire, bool, error) {
	id, err := db.ParseUUID(sessionID)
	if err != nil {
		return ScheduleFire{}, false, fmt.Errorf("parse session id: %w", err)
	}
	row, err := s.queries.GetActiveScheduleFireForSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScheduleFire{}, false, nil
	}
	if err != nil {
		return ScheduleFire{}, false, fmt.Errorf("get active schedule fire: %w", err)
	}
	return ScheduleFire{
		ScheduleID:         row.ScheduleID.String(),
		StartedAt:          row.StartedAt.Time,
		NotifyPolicy:       row.NotifyPolicy,
		ContactViaChannels: row.ContactViaChannels,
		Notified: row.AttentionKind.String == string(KindUpdate) &&
			row.AttentionAt.Valid && !row.AttentionAt.Time.Before(row.StartedAt.Time),
	}, true, nil
}

// QuietCompletion reports whether a run that completed should raise no reply:
// a schedule fired it and that schedule tells its owner only when the run asks
// to (notify_user), fails or waits. A run the user started never is quiet.
func (s *Service) QuietCompletion(ctx context.Context, runID string) (bool, error) {
	id, err := db.ParseUUID(runID)
	if err != nil {
		return false, fmt.Errorf("parse run id: %w", err)
	}
	row, err := s.queries.GetScheduleNotifyPolicyForRun(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get schedule notify policy: %w", err)
	}
	return row.Scheduled && row.NotifyPolicy != NotifyEveryRun, nil
}
