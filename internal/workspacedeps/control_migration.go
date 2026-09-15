package workspacedeps

import (
	"context"
	"fmt"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/payloadlease"
)

func currentControlIntent(rec Installation) bool {
	return rec.OperationIntent != nil && rec.OperationIntent.ControlProtocol == controlProtocol
}

// The absence of a lock under /run says nothing about an older process using
// /data. Enroll its current whole-workspace lifetime in the database and retain
// the claim until that lifetime has ended. The old receipt can never authorize
// publication, even after this fence permits terminating its claim.
func (s *Service) requireLegacyOperationDrained(ctx context.Context, key InstallationKey, rec Installation, client *bridge.Client) error {
	if currentControlIntent(rec) {
		return nil
	}
	if rec.OperationIntent != nil && rec.OperationIntent.ControlProtocol != "" {
		return fmt.Errorf("%w: unknown dependency control protocol", ErrBusy)
	}
	platform, err := ProbePlatform(ctx, client)
	if err != nil {
		return err
	}
	if platform.OS == "darwin" {
		// Darwin still uses its original lockf inode; the caller acquires it.
		return nil
	}
	if platform.OS != "linux" {
		return fmt.Errorf("%w: legacy operation lifetime cannot be established", ErrBusy)
	}
	epoch, err := payloadlease.ReadEpoch(ctx, client)
	if err != nil {
		return err
	}
	store, ok := s.store.(LegacyOperationEpochStore)
	if !ok || epoch == "" {
		return fmt.Errorf("%w: legacy operation requires a proven workspace restart", ErrBusy)
	}
	previous, err := store.EnrollLegacyOperationEpoch(ctx, key, rec.OperationID, epoch)
	if err != nil {
		return err
	}
	if previous == "" || previous == epoch {
		return fmt.Errorf("%w: restart the entire workspace before retiring a legacy dependency operation", ErrBusy)
	}
	return nil
}
