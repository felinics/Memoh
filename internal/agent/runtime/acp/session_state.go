package acp

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrSessionStateOutOfSync means canonical ACP history has advanced to a
// successful run whose staged native snapshot is missing. Falling back to a
// fresh native session would silently fork the two histories, so callers must
// surface or explicitly repair this condition.
var ErrSessionStateOutOfSync = errors.New("ACP session checkpoint is out of sync with canonical history")

// ErrSessionStateDivergent means the captured native state can no longer be
// proven to extend the canonical checkpoint: a canonical file was rewritten,
// shortened, or removed by the agent. Staging must decline without touching
// any row the canonical version references (staging and publication commit in
// different transactions, so a destructive rewrite would corrupt the still-
// canonical version if this round never commits). The caller publishes an
// explicit reset instead; once that reset is canonical the shared rows are
// unreferenced and the next turn may stage a full rewrite safely.
var ErrSessionStateDivergent = errors.New("ACP session state diverged from the canonical checkpoint")

var (
	// ErrRuntimeConfigStale means the ACP process was created from an older
	// bot configuration generation. Retrying the write from that process would
	// let stale credentials or configuration replace the current generation.
	ErrRuntimeConfigStale = errors.New("ACP runtime configuration generation is stale")
	// ErrRuntimeConfigResetInProgress is transient: a bot-scoped reset owns the
	// workspace configuration publication lock. Session-scoped history resets
	// deliberately do not block bot-global configuration synchronization.
	ErrRuntimeConfigResetInProgress = errors.New("ACP bot runtime reset is in progress")
)

// SessionPublicationKind identifies what the newest successful canonical ACP
// turn published. A checkpoint can be restored after a cold start; a reset is
// an explicit, successful turn with no resumable native state.
type SessionPublicationKind string

const (
	SessionPublicationCheckpoint SessionPublicationKind = "checkpoint"
	SessionPublicationReset      SessionPublicationKind = "reset"
)

// SessionPublicationHead is the lightweight canonical-history watermark used
// to fence process-local warm handles across server instances. Absence is
// represented by the bool returned from SessionStateStore.Head, not by a
// sentinel RunID or Kind.
type SessionPublicationHead struct {
	RunID string
	Kind  SessionPublicationKind
}

// RuntimeConfigEpoch is the durable generation of process-affecting bot and
// session configuration. It is deliberately independent of publication head:
// credentials and workspace config may change while canonical chat history
// does not.
type RuntimeConfigEpoch struct {
	Bot     int64
	Session int64
}

// SessionStateFileShape names one JSONL file of a persisted version: its
// record count and the digest over its compacted records. The digest doubles
// as the append-only proof between versions: when a newer capture's running
// digest at PrefixRecords equals the canonical version's full-file Digest, the
// canonical prefix is byte-identical and only the tail needs to be written.
type SessionStateFileShape struct {
	Path    string `json:"path"`
	Records int64  `json:"records"`
	Digest  string `json:"digest"`
}

// PersistedSessionStateFile is one captured file plus its optional prefix
// proof. PrefixRecords/PrefixDigest describe the capture's running digest at
// the previous canonical boundary; a zero PrefixRecords means no proof was
// taken and the file must be fully rewritten.
type PersistedSessionStateFile struct {
	SessionStateFileShape
	PrefixRecords int64
	PrefixDigest  string
}

// PersistedSessionState is the database-backed checkpoint needed to resume an
// adapter-native ACP session inside a new process-owned runtime directory.
// JSONL files stay out of the durable workspace profile; the store reconstructs
// them from ordered JSONB rows only for the lifetime of the resumed runtime.
type PersistedSessionState struct {
	AgentID             string
	ACPSessionID        string
	ThroughRunID        string
	Cwd                 string
	TranscriptPath      string
	RuntimeFencingToken int64
	FileCount           int32
	RecordCount         int64
	// Files describes every captured file in path order. Replace uses the
	// prefix proofs to append only each file's tail; Load uses the committed
	// shapes as its authoritative read bound.
	Files []PersistedSessionStateFile
}

// SessionStateRecord is one ordered JSONL value in a persisted ACP snapshot.
type SessionStateRecord struct {
	FilePath   string          `json:"file_path"`
	LineNumber int64           `json:"line_number"`
	Content    json.RawMessage `json:"content"`
}

// SessionStateRecordReader yields records ordered by FilePath and LineNumber.
// io.EOF ends the snapshot. Implementations and consumers must not retain or
// mutate Content after the next call unless they first copy it.
type SessionStateRecordReader func(context.Context) (SessionStateRecord, error)

// SessionStateRecordConsumer consumes one published snapshot. The reader is
// valid only while the callback is running and must be consumed through
// io.EOF before the callback returns nil. Load keeps its consistency
// transaction open for that lifetime, so consumers should do only the bounded
// restore work needed to durably accept each record.
type SessionStateRecordConsumer func(
	context.Context,
	PersistedSessionState,
	SessionStateRecordReader,
) error

// SessionStateStore is the narrow persistence port used by SessionPool. A
// Replace implementation stages the complete snapshot under ThroughRunID in
// one runtime-fenced transaction. Load may expose that version only after the
// same run is the newest successful canonical ACP watermark; this prevents a
// crash between native completion and chat-history persistence from publishing
// a ghost transcript.
type SessionStateStore interface {
	RuntimeConfigEpoch(ctx context.Context, botID, sessionID string) (RuntimeConfigEpoch, error)
	GuardRuntimeSync(ctx context.Context, botID string, expectedBotEpoch int64, fn func(context.Context) error) error
	Head(ctx context.Context, botID, sessionID string) (SessionPublicationHead, bool, error)
	// CanonicalShape returns the committed head version's per-file shapes. It
	// is an advisory pre-capture read: capture snapshots prefix digests at
	// these boundaries, and Replace re-reads the canonical shape inside its
	// transaction before trusting any proof.
	CanonicalShape(ctx context.Context, botID, sessionID string) (map[string]SessionStateFileShape, bool, error)
	Load(ctx context.Context, botID, sessionID string, consume SessionStateRecordConsumer) (bool, error)
	Replace(
		ctx context.Context,
		botID, sessionID string,
		state PersistedSessionState,
		records SessionStateRecordReader,
	) error
}
