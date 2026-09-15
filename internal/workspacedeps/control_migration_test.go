package workspacedeps

import "context"

func (f *fakeStore) EnrollLegacyOperationEpoch(_ context.Context, key InstallationKey, operationID, epoch string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.records[key]
	if !ok || !rec.Status.InProgress() || rec.OperationID != operationID {
		return "", ErrBusy
	}
	intent := OperationReceipt{}
	if rec.OperationIntent != nil {
		intent = *rec.OperationIntent
	}
	if intent.ControlMigrationEpoch == "" {
		intent.ControlMigrationEpoch = epoch
		rec.OperationIntent = &intent
		f.records[key] = rec
	}
	return intent.ControlMigrationEpoch, nil
}
