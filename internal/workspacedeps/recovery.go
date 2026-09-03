package workspacedeps

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"

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
	if s.provider != nil {
		definition, err := s.provider.StoredDefinition(ctx, DefinitionKey{SourceURL: receipt.SourceURL, DependencyID: receipt.DependencyID, Revision: receipt.DefinitionRevision})
		if err != nil {
			return nil, err
		}
		dep = definition.Dependency()
	}
	if dep.ID != receipt.DependencyID || dep.ManifestDigest != receipt.ManifestDigest || dep.SourceURL != receipt.SourceURL || dep.RegistryID != receipt.RegistryID || dep.Revision != receipt.DefinitionRevision {
		return nil, fmt.Errorf("%w: receipt publication does not match the verified definition", ErrDefinitionInvalid)
	}
	client, root, err := s.target(ctx, key.BotID, key.WorkspaceTargetID)
	if err != nil {
		return nil, err
	}
	op := &operation{key: key, dep: dep, client: client, dataRoot: root, home: Home(root, dep.ID), shimDir: ShimDir(root), platform: platform, version: receipt.RequestedVersion, receipt: receipt, previous: receipt.Previous, operationID: receipt.ID}
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
		s.cleanupReceipt(ctx, op)
		return nil, nil
	case ActionRollback:
		current := receipt.Previous
		if current == nil || strings.TrimSpace(current.PreviousVersion) == "" {
			return nil, ErrRollbackUnavailable
		}
		state := State{DependencyID: dep.ID, Version: current.PreviousVersion, InstalledAt: s.now().UTC(), ManifestDigest: current.ManifestDigest, Entrypoints: current.Entrypoints, PreviousVersion: current.Version, Previous: previousInstallation(*current)}
		if previous := current.Previous; previous != nil && previous.Version == state.Version {
			state.SourceURL, state.RegistryID, state.DefinitionRevision = previous.SourceURL, previous.RegistryID, previous.DefinitionRevision
			state.ManifestDigest, state.Entrypoints = previous.ManifestDigest, cloneStringMap(previous.Entrypoints)
		}
		if err := s.finalizeFilesystem(ctx, op, &state, current); err != nil {
			return nil, err
		}
		result, err := s.record(ctx, op, ActionRollback, state)
		if err != nil {
			return nil, err
		}
		return &result.Installation, nil
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
	client, root, err := s.target(ctx, key.BotID, key.WorkspaceTargetID)
	if err != nil {
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
