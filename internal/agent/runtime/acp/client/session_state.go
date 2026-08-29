package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
)

const (
	runtimeSessionMaxFileSize          = 512 * 1024 * 1024
	runtimeSessionMaxTotal             = 512 * 1024 * 1024
	runtimeSessionMaxLineSize          = 8 * 1024 * 1024
	runtimeSessionMaxPathSize          = 1024
	runtimeSessionMaxLinesPerFile      = 2_000_000
	runtimeSessionMaxRecords           = 2_000_000
	runtimeSessionMaxFiles             = 1024
	runtimeSessionMaxEntries           = 16_384
	runtimeSessionMaxPrimaryCandidates = 4
	runtimeSessionSnapshotReads        = 4
	runtimeSessionSnapshotSettleDelay  = 25 * time.Millisecond
	runtimeSessionSpoolPrefix          = "memoh-acp-session-"
	runtimeSessionMaxActiveSpools      = 2
	runtimeSessionSpoolStaleAge        = 24 * time.Hour
	// runtimeSessionSpoolBudgetBytes bounds the total disk held by finished
	// spools whose capture slot was handed back (see finish). Spools past this
	// budget keep their capture slot instead, degrading to the serialized
	// pre-handover behavior rather than adding a new blocking point.
	runtimeSessionSpoolBudgetBytes = int64(4) << 30
)

var (
	// ErrSessionStateUnsupported means the ACP profile has no audited JSONL roots.
	ErrSessionStateUnsupported = errors.New("ACP profile does not declare session state storage")
	// ErrSessionStateNotFound means no non-empty primary transcript matched the ACP session ID.
	ErrSessionStateNotFound = errors.New("ACP session JSONL state was not found")
	// ErrSessionStateUnstable means the selected group of JSONL files did not
	// have the same path/content manifest in two consecutive reads.
	ErrSessionStateUnstable = errors.New("ACP session JSONL state changed while snapshotting")
	// ErrSessionStateRestoreInvalid means a database checkpoint cannot be
	// materialized under the current audited runtime layout.
	ErrSessionStateRestoreInvalid = errors.New("ACP session JSONL state is incompatible with the runtime")

	runtimeSessionSpoolAdmission = make(chan struct{}, runtimeSessionMaxActiveSpools)
	runtimeSessionSpoolCleanup   sync.Once
	// runtimeSessionSpoolBudgetUsed tracks bytes reserved by finished spools
	// that released their capture slot early.
	runtimeSessionSpoolBudgetUsed atomic.Int64
)

// SessionState is the small, durable identity of an adapter-native session.
// The potentially large JSONL body is exposed separately through a record
// stream so neither PostgreSQL nor the workspace bridge requires a whole-file
// Go allocation.
type SessionState struct {
	SessionID      string
	TranscriptPath string
}

// SessionStateRecord is one non-empty JSONL value. LineNumber is the logical
// record number within FilePath (blank physical lines are intentionally not
// part of the JSONB persistence contract).
type SessionStateRecord struct {
	FilePath   string          `json:"file_path"`
	LineNumber int64           `json:"line_number"`
	Content    json.RawMessage `json:"content"`
}

// SessionStateRecordReader returns io.EOF after the last ordered record.
// Returned Content is owned by the caller until the next call.
type SessionStateRecordReader func(context.Context) (SessionStateRecord, error)

// SessionStateCursor is an agent-format-aware freshness watermark. It contains
// no transcript text and remains bounded regardless of transcript size.
type SessionStateCursor struct {
	locator       acpprofile.RuntimeSessionLocator
	sessionID     string
	primaryLines  int64
	terminalTurns int64
}

// SessionStateFileShape mirrors the store-level per-file shape: record count,
// full-content digest, and the optional append-boundary proof.
type SessionStateFileShape struct {
	Path          string
	Records       int64
	Digest        string
	PrefixRecords int64
	PrefixDigest  string
}

func validateSessionStateAdvanced(locator acpprofile.RuntimeSessionLocator, previous, current SessionStateCursor) error {
	if previous.locator != "" && (previous.locator != locator || previous.sessionID != current.sessionID) {
		return errors.New("ACP session state cursor belongs to a different native session")
	}
	if locator == acpprofile.RuntimeSessionLocatorCodexRollout && current.terminalTurns <= previous.terminalTurns {
		return fmt.Errorf("%w: Codex terminal turn record did not advance", ErrSessionStateUnstable)
	}
	return nil
}

var errPrimaryCandidateInspected = errors.New("ACP primary candidate inspected")

func validateSessionFilePathForRoots(value string, roots []string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if len(value) > runtimeSessionMaxPathSize || !safeLeaseRelativePath(value) || path.Ext(value) != ".jsonl" {
		return "", errors.New("path must be a safe relative .jsonl file")
	}
	for _, root := range roots {
		root = path.Clean(root)
		if strings.HasPrefix(value, root+"/") {
			return value, nil
		}
	}
	return "", errors.New("path is outside the profile-declared session roots")
}

func validateSessionStateHeader(
	locator acpprofile.RuntimeSessionLocator,
	roots []string,
	state SessionState,
) (SessionState, error) {
	sessionID, err := validateSessionID(state.SessionID)
	if err != nil {
		return SessionState{}, err
	}
	transcript, err := validateSessionFilePathForRoots(state.TranscriptPath, roots)
	if err != nil {
		return SessionState{}, err
	}
	if !sessionPrimaryPathMatches(locator, transcript, sessionID) {
		return SessionState{}, fmt.Errorf("primary ACP transcript path %q does not identify session %q", transcript, sessionID)
	}
	return SessionState{SessionID: sessionID, TranscriptPath: transcript}, nil
}

func validateSessionID(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed != value || len(trimmed) > runtimeSessionMaxPathSize || strings.ContainsAny(trimmed, "\x00\r\n/\\") {
		return "", errors.New("ACP session ID is invalid")
	}
	return trimmed, nil
}
