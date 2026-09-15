package workspacedeps

import (
	"context"
	"fmt"
	"log/slog"
	"path"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func receiptMatches(rec Installation, receipt *OperationReceipt) bool {
	return receipt != nil && receipt.DependencyID == rec.DependencyID && receipt.ID != "" && receipt.ID == rec.OperationID
}

func (s *Service) cleanupReceipt(ctx context.Context, op *operation) {
	if op.receipt == nil {
		return
	}
	cleanupCtx, cancel := finalizeContext(ctx)
	defer cancel()
	if err := CleanupReceipt(cleanupCtx, op.client, op.receipt); err != nil {
		s.logger.Warn("clean completed dependency receipt", slog.String("dependency_id", op.dep.ID), slog.Any("error", err))
	}
}

// recoverReceipt completes only a proven script exit whose receipt matches the
// record's operation ID and a previously verified immutable definition.
// It never downloads a replacement recipe or executes a script on recovery.
func (s *Service) recoverReceipt(ctx context.Context, key InstallationKey, dep catalog.Dependency, platform Platform, receipt *OperationReceipt) (*Installation, error) {
	rec, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if !rec.Status.InProgress() || !receiptMatches(rec, receipt) {
		return nil, ErrBusy
	}
	if !currentControlIntent(rec) {
		return s.rejectUntrustedReceipt(ctx, key, rec)
	}
	if !receiptIntentMatches(rec.OperationIntent, receipt) {
		return s.rejectUntrustedReceipt(ctx, key, rec)
	}
	trusted := *rec.OperationIntent
	trusted.Result, trusted.ExitCode, trusted.Completed = receipt.Result, receipt.ExitCode, receipt.Completed
	receipt = &trusted

	var frozen *catalog.Definition
	if s.provider != nil {
		definition, err := s.provider.StoredDefinition(ctx, DefinitionKey{SourceURL: receipt.SourceURL, DependencyID: receipt.DependencyID, Revision: receipt.DefinitionRevision})
		if err != nil {
			return nil, err
		}
		dep = definition.Dependency()
		frozen = &definition
	}
	if dep.ID != receipt.DependencyID || dep.ManifestDigest != receipt.ManifestDigest || dep.SourceURL != receipt.SourceURL || dep.RegistryID != receipt.RegistryID || dep.Revision != receipt.DefinitionRevision {
		return nil, fmt.Errorf("%w: receipt publication does not match the verified definition", ErrDefinitionInvalid)
	}
	client, root, err := s.target(ctx, key.BotID)
	if err != nil {
		return nil, err
	}
	receipt.Directory = path.Join(operationRoot(Home(root, dep.ID), dep.ID), receipt.ID)
	op := &operation{key: key, dep: dep, client: client, dataRoot: root, home: Home(root, dep.ID), shimDir: ShimDir(root), platform: platform, version: receipt.RequestedVersion, receipt: receipt, previous: receipt.Previous, operationID: receipt.ID, storeRoot: receipt.StoreRoot, desiredRevision: receipt.DesiredRevision, repair: receipt.Repair, authorizedByActor: receipt.AuthorizedByActor, restorePayloadPath: receipt.RestorePayloadPath, restoreInstallationID: receipt.RestoreInstallationID}
	if op.storeRoot == "" {
		op.storeRoot = s.effectiveStoreRoot(root)
	}
	op.catalog = s.catalogFor(ctx)
	op.frozenDefinition = frozen
	finalizeCtx, cancel := finalizeContext(ctx)
	defer cancel()
	ctx = finalizeCtx
	op.finalizing = true
	if receipt.ExitCode != 0 {
		cause := &ExitError{Code: receipt.ExitCode, StderrTail: "recovered script exit"}
		_ = s.fail(ctx, op, cause)
		rec, err := s.store.Get(ctx, key)
		return &rec, err
	}
	switch receipt.Action {
	case catalog.ActionInstall, catalog.ActionUpdate, catalog.ActionReinstall:
		result, err := s.commit(ctx, op, receipt.Action, receipt.Result, receipt.Previous)
		if err != nil {
			return nil, err
		}
		return &result.Installation, nil
	case catalog.ActionRemove:
		if err := s.finalizeFilesystem(ctx, op, nil, receipt.Previous); err != nil {
			return nil, err
		}
		if _, err := s.store.FinishOperation(ctx, key, receipt.ID, nil); err != nil {
			return nil, err
		}
		s.cache.Invalidate(key.BotID)
		s.cleanPreviousPayload(ctx, op)
		s.cleanupReceipt(ctx, op)
		return nil, nil
	default:
		return nil, fmt.Errorf("%w: unsupported recovery action %s", ErrActionUnsupported, receipt.Action)
	}
}

// markInterrupted fences the unique operation under its workspace lock before
// releasing its database claim. A Server paused between Claim and Run can then
// resume safely: its prelude observes the permanent tombstone and does no work.
func (s *Service) markInterrupted(ctx context.Context, key InstallationKey, rec Installation) (Installation, error) {
	if !validReceiptID(rec.OperationID) {
		// Pre-protocol intents have no identity a new runner can fence. Preserve
		// them for explicit operator recovery, including legacy directory locks.
		return Installation{}, ErrBusy
	}
	client, root, err := s.target(ctx, key.BotID)
	if err != nil {
		return Installation{}, err
	}
	if err := s.requireLegacyOperationDrained(ctx, key, rec, client); err != nil {
		return Installation{}, err
	}
	home := Home(root, key.DependencyID)
	operations := operationRoot(home, key.DependencyID)
	completed := path.Join(operations, rec.OperationID, "exit-code")
	tombstone := path.Join(operations, ".cancelled-"+rec.OperationID)
	// The process may have finished after discovery released its probe lock.
	// Preserve its receipt so fresh discovery can recover the actual result.
	script := fmt.Sprintf("set -eu\n[ ! -f %s ] || exit 76\nmkdir -p %s\n: > %s\n", shellQuote(completed), shellQuote(operations), shellQuote(tombstone))
	if err := runFilesystemScript(ctx, client, home, key.DependencyID, script); err != nil {
		return Installation{}, err
	}
	rec.Status, rec.LastError = StatusFailed, interruptedMessage
	return s.store.FinishOperation(ctx, key, rec.OperationID, &rec)
}

// Rejecting a tampered or pre-protocol receipt must not strand its claim or
// publish new authorization. Fence the workspace process under the same kernel
// lock, retain all payload/evidence files, and require a new Manage action.
func (s *Service) rejectUntrustedReceipt(ctx context.Context, key InstallationKey, rec Installation) (*Installation, error) {
	client, root, err := s.target(ctx, key.BotID)
	if err != nil {
		return nil, err
	}
	if err := s.requireLegacyOperationDrained(ctx, key, rec, client); err != nil {
		return nil, err
	}
	home := Home(root, key.DependencyID)
	tombstone := path.Join(operationRoot(home, key.DependencyID), ".cancelled-"+rec.OperationID)
	script := fmt.Sprintf("set -eu\nmkdir -p %s\n: > %s\n", shellQuote(path.Dir(tombstone)), shellQuote(tombstone))
	if err := runFilesystemScript(ctx, client, home, key.DependencyID, script); err != nil {
		return nil, err
	}
	rec.Status, rec.LastError = StatusFailed, "operation receipt does not match its trusted intent; management confirmation is required"
	terminal, err := s.store.FinishOperation(ctx, key, rec.OperationID, &rec)
	if err != nil {
		return nil, err
	}
	s.cache.Invalidate(key.BotID)
	return &terminal, nil
}
