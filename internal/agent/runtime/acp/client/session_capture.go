// Capture: read the agent's live JSONL tree from the workspace into a
// stable, freshness-proven host spool snapshot.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"time"

	acpprofile "github.com/memohai/memoh/internal/agent/runtime/acp/profile"
)

type sessionFileCandidate struct {
	path string
	size int64
}

func (p *bridgeProcess) SnapshotSessionState(
	ctx context.Context,
	sessionID string,
	previous SessionStateCursor,
	receipt *SessionStateReceipt,
	boundaries map[string]int64,
) (*SessionStateSnapshot, error) {
	if p == nil || p.lease == nil {
		return nil, errors.New("ACP runtime process is unavailable")
	}
	return p.lease.snapshotSessionState(ctx, sessionID, previous, receipt, boundaries)
}

func (l *runtimeLease) snapshotSessionState(
	ctx context.Context,
	sessionID string,
	previous SessionStateCursor,
	receipt *SessionStateReceipt,
	boundaries map[string]int64,
) (*SessionStateSnapshot, error) {
	locator, err := l.sessionStateLocator()
	if err != nil {
		return nil, err
	}
	sessionID, err = validateSessionID(sessionID)
	if err != nil {
		return nil, err
	}
	var previousManifest *SessionStateSnapshot
	var previousErr error
	defer func() {
		if previousManifest != nil {
			_ = previousManifest.Close()
		}
	}()
	for read := 0; read < runtimeSessionSnapshotReads; read++ {
		current, captureErr := l.captureSessionState(ctx, locator, sessionID, receipt, boundaries)
		if ctxErr := ctx.Err(); ctxErr != nil {
			if current != nil {
				_ = current.Close()
			}
			return nil, ctxErr
		}
		if previousManifest != nil && captureErr == nil && equalSessionStateSnapshot(previousManifest, current) {
			if err := validateSessionStateAdvanced(locator, previous, current.Cursor()); err != nil {
				_ = current.Close()
				return nil, err
			}
			return current, nil
		}
		if previousErr != nil && sameSessionCaptureError(previousErr, captureErr) {
			if current != nil {
				_ = current.Close()
			}
			return nil, captureErr
		}
		if previousManifest != nil {
			_ = previousManifest.Close()
			previousManifest = nil
		}
		if captureErr == nil {
			// Retain only its small manifest in memory, then delete the first
			// physical spool before creating the next candidate snapshot.
			previousManifest = cloneSnapshotManifest(current)
			_ = current.Close()
		}
		previousErr = captureErr
		if read+1 < runtimeSessionSnapshotReads {
			timer := time.NewTimer(runtimeSessionSnapshotSettleDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, fmt.Errorf("%w after %d reads", ErrSessionStateUnstable, runtimeSessionSnapshotReads)
}

func (l *runtimeLease) captureSessionState(
	ctx context.Context,
	locator acpprofile.RuntimeSessionLocator,
	sessionID string,
	receipt *SessionStateReceipt,
	boundaries map[string]int64,
) (*SessionStateSnapshot, error) {
	candidates, err := l.listSessionFileCandidates(ctx)
	if err != nil {
		return nil, err
	}
	primaryCandidates := make([]sessionFileCandidate, 0, 1)
	for _, candidate := range candidates {
		if sessionPrimaryPathMatches(locator, candidate.path, sessionID) {
			primaryCandidates = append(primaryCandidates, candidate)
		}
	}
	if len(primaryCandidates) == 0 {
		return nil, fmt.Errorf("%w for session %q", ErrSessionStateNotFound, sessionID)
	}
	if len(primaryCandidates) > runtimeSessionMaxPrimaryCandidates {
		return nil, fmt.Errorf("ACP session %q has too many primary transcript candidates", sessionID)
	}
	matchedPrimary := ""
	for _, candidate := range primaryCandidates {
		matches, err := l.inspectPrimaryCandidate(ctx, locator, sessionID, candidate)
		if err != nil {
			return nil, err
		}
		if !matches {
			continue
		}
		if matchedPrimary != "" {
			return nil, fmt.Errorf("ACP session %q has multiple content-validated primary transcripts", sessionID)
		}
		matchedPrimary = candidate.path
	}
	if matchedPrimary == "" {
		return nil, fmt.Errorf("%w for session %q: primary transcript content has no matching session ID", ErrSessionStateNotFound, sessionID)
	}
	selected := make([]sessionFileCandidate, 0, 1)
	var listedTotal int64
	for _, candidate := range candidates {
		if sessionFileBelongsToPrimary(locator, candidate.path, matchedPrimary, sessionID) {
			selected = append(selected, candidate)
			listedTotal += candidate.size
		}
	}
	if len(selected) > runtimeSessionMaxFiles || listedTotal > runtimeSessionMaxTotal {
		return nil, errors.New("ACP session state exceeds file or byte limits")
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].path < selected[j].path })
	state := SessionState{SessionID: sessionID, TranscriptPath: matchedPrimary}
	builder, err := newSessionSpoolBuilder(ctx, locator, state, receipt, boundaries)
	if err != nil {
		return nil, err
	}
	defer func() { builder.abort() }()
	for _, candidate := range selected {
		reader, err := l.openSessionCandidate(ctx, candidate)
		if err != nil {
			return nil, err
		}
		addErr := builder.addFileFromReader(ctx, candidate.path, reader)
		closeErr := reader.Close()
		if addErr != nil {
			return nil, fmt.Errorf("capture ACP session file %q: %w", candidate.path, addErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	snapshot, err := builder.finish()
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (l *runtimeLease) inspectPrimaryCandidate(
	ctx context.Context,
	locator acpprofile.RuntimeSessionLocator,
	sessionID string,
	candidate sessionFileCandidate,
) (bool, error) {
	reader, err := l.openSessionCandidate(ctx, candidate)
	if err != nil {
		return false, err
	}
	defer func() { _ = reader.Close() }()
	validator := primaryStateValidator{locator: locator, sessionID: sessionID}
	err = scanSessionJSONL(ctx, reader, func(content json.RawMessage) error {
		validator.observe(content)
		if locator == acpprofile.RuntimeSessionLocatorCodexRollout || validator.claudeFoundID {
			return errPrimaryCandidateInspected
		}
		return nil
	})
	if err != nil && !errors.Is(err, errPrimaryCandidateInspected) {
		return false, fmt.Errorf("inspect ACP primary transcript %q: %w", candidate.path, err)
	}
	return validator.valid(), nil
}

func (l *runtimeLease) openSessionCandidate(ctx context.Context, candidate sessionFileCandidate) (io.ReadCloser, error) {
	if candidate.size < 0 || candidate.size > runtimeSessionMaxFileSize {
		return nil, fmt.Errorf("ACP session file %q exceeds %d bytes", candidate.path, runtimeSessionMaxFileSize)
	}
	rel, err := l.validateSessionFilePath(candidate.path)
	if err != nil {
		return nil, err
	}
	reader, err := l.client.ReadRawNoFollow(ctx, l.root, rel)
	if err != nil {
		return nil, fmt.Errorf("read ACP session file %q: %w", candidate.path, err)
	}
	return reader, nil
}

func (l *runtimeLease) listSessionFileCandidates(ctx context.Context) ([]sessionFileCandidate, error) {
	candidates := make([]sessionFileCandidate, 0, 1)
	seen := make(map[string]struct{})
	var totalEntries int
	for _, root := range l.sessionRoots {
		root = path.Clean(root)
		runtimeRoot := path.Join(l.root, root)
		entry, exists, err := l.safeEntry(ctx, l.root, runtimeRoot)
		if err != nil {
			return nil, fmt.Errorf("inspect ACP session root %q: %w", root, err)
		}
		if !exists {
			continue
		}
		if !entry.GetIsDir() {
			return nil, fmt.Errorf("ACP session root %q is not a directory", root)
		}
		// Bounded listing: the bridge stops traversing past the remaining
		// budget instead of collecting an attacker-sized tree into memory
		// first. The +1 lets this side distinguish "exactly at the cap" from
		// "over the cap".
		remaining := runtimeSessionMaxEntries - totalEntries
		entries, err := l.client.ListDirBounded(ctx, runtimeRoot, true, int32(remaining)+1) //nolint:gosec // bounded by runtimeSessionMaxEntries.
		if err != nil {
			return nil, fmt.Errorf("list ACP session root %q: %w", root, err)
		}
		totalEntries += len(entries)
		if totalEntries > runtimeSessionMaxEntries {
			return nil, fmt.Errorf("ACP session roots contain more than %d entries", runtimeSessionMaxEntries)
		}
		for _, child := range entries {
			rel := cleanListedRelativePath(child.GetPath())
			if rel == "" || isSymlinkMode(child.GetMode()) {
				return nil, fmt.Errorf("ACP session root %q contains unsafe path %q", root, child.GetPath())
			}
			if child.GetIsDir() || path.Ext(rel) != ".jsonl" {
				continue
			}
			filePath := path.Join(root, rel)
			if _, err := validateSessionFilePathForRoots(filePath, l.sessionRoots); err != nil {
				return nil, err
			}
			if _, duplicate := seen[filePath]; duplicate {
				return nil, fmt.Errorf("ACP session roots repeat file %q", filePath)
			}
			seen[filePath] = struct{}{}
			candidates = append(candidates, sessionFileCandidate{path: filePath, size: child.GetSize()})
		}
	}
	return candidates, nil
}

func (l *runtimeLease) sessionStateLocator() (acpprofile.RuntimeSessionLocator, error) {
	if len(l.sessionRoots) == 0 {
		return acpprofile.RuntimeSessionLocatorNone, ErrSessionStateUnsupported
	}
	profile, ok := acpprofile.Lookup(l.agentID)
	if !ok || profile.RuntimeStorage.SessionLocator == acpprofile.RuntimeSessionLocatorNone {
		return acpprofile.RuntimeSessionLocatorNone, ErrSessionStateUnsupported
	}
	return profile.RuntimeStorage.SessionLocator, nil
}

func (l *runtimeLease) validateSessionFilePath(value string) (string, error) {
	return validateSessionFilePathForRoots(value, l.sessionRoots)
}

func sameSessionCaptureError(first, second error) bool {
	return first != nil && second != nil && first.Error() == second.Error()
}
