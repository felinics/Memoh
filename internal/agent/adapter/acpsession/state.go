package acpsession

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	acp "github.com/memohai/memoh/internal/agent/runtime/acp"
	dbpkg "github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
	"github.com/memohai/memoh/internal/runtimefence"
)

// These are persistence limits, not ACP protocol limits. Checkpoints are
// streamed, so the 512 MiB logical budget never becomes one Go or PostgreSQL
// value. Page and insert-batch limits bound resident payload independently.
const (
	maxStateAgentIDBytes   = 256
	maxStateSessionIDBytes = 1024
	maxStateCwdBytes       = 16 * 1024
	maxStatePathBytes      = 4096
	maxStateFiles          = 1024
	maxStateRecords        = 2_000_000
	maxStateLineBytes      = 8 * 1024 * 1024
	maxStateJSONLBytes     = 512 * 1024 * 1024
	targetStateBatchBytes  = 4 * 1024 * 1024
	stateLineBatchRecords  = 1024
	stateLinePageBytes     = 4 * 1024 * 1024
	stateLinePageResults   = 4096
)

// StateStore persists ACP-owned JSONL snapshots without making their opaque
// records part of Memoh's canonical chat timeline.
type StateStore struct {
	queries dbstore.Queries
}

type stateTransactionRunner interface {
	InTx(context.Context, func(dbstore.Queries) error) error
}

type stateTransactionCapability interface {
	SupportsTransactions() bool
}

func NewStateStore(queries dbstore.Queries) *StateStore {
	return &StateStore{queries: queries}
}

func (s *StateStore) RuntimeConfigEpoch(
	ctx context.Context,
	botID string,
	sessionID string,
) (acp.RuntimeConfigEpoch, error) {
	if s == nil || s.queries == nil {
		return acp.RuntimeConfigEpoch{}, errors.New("ACP session state store is unavailable")
	}
	pgBotID, err := dbpkg.ParseUUID(strings.TrimSpace(botID))
	if err != nil {
		return acp.RuntimeConfigEpoch{}, fmt.Errorf("invalid ACP runtime bot id: %w", err)
	}
	var pgSessionID pgtype.UUID
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		pgSessionID, err = dbpkg.ParseUUID(sessionID)
		if err != nil {
			return acp.RuntimeConfigEpoch{}, fmt.Errorf("invalid ACP runtime session id: %w", err)
		}
	}
	row, err := s.queries.GetACPRuntimeConfigEpoch(ctx, sqlc.GetACPRuntimeConfigEpochParams{
		BotID: pgBotID, SessionID: pgSessionID,
	})
	if err != nil {
		return acp.RuntimeConfigEpoch{}, fmt.Errorf("load ACP runtime config epoch: %w", err)
	}
	return acp.RuntimeConfigEpoch{
		Bot: row.BotRuntimeConfigEpoch, Session: row.SessionRuntimeConfigEpoch,
	}, nil
}

// GuardRuntimeSync serializes an old ACP process's workspace reads/writes
// against bot-scoped reset publication. The database guard deliberately wraps
// the external callback: once this transaction owns the bot row lock, a reset
// successor cannot acquire or publish until the callback has finished.
func (s *StateStore) GuardRuntimeSync(
	ctx context.Context,
	botID string,
	expectedBotEpoch int64,
	fn func(context.Context) error,
) error {
	if s == nil || s.queries == nil {
		return errors.New("ACP session state store is unavailable")
	}
	if fn == nil {
		return errors.New("ACP runtime sync callback is required")
	}
	pgBotID, err := dbpkg.ParseUUID(strings.TrimSpace(botID))
	if err != nil {
		return fmt.Errorf("invalid ACP runtime bot id: %w", err)
	}
	txer, ok := s.queries.(stateTransactionRunner)
	capability, supported := s.queries.(stateTransactionCapability)
	if !ok || !supported || !capability.SupportsTransactions() {
		return errors.New("ACP runtime sync guard requires transaction support")
	}
	return txer.InTx(ctx, func(queries dbstore.Queries) error {
		if _, err := queries.LockBotForRuntimeReset(ctx, pgBotID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return acp.ErrRuntimeConfigStale
			}
			return fmt.Errorf("lock ACP runtime configuration: %w", err)
		}
		if _, err := queries.GetBotRuntimeReset(ctx, pgBotID); err == nil {
			return acp.ErrRuntimeConfigResetInProgress
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check ACP bot runtime reset: %w", err)
		}
		epoch, err := queries.GetACPRuntimeConfigEpoch(ctx, sqlc.GetACPRuntimeConfigEpochParams{
			BotID: pgBotID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return acp.ErrRuntimeConfigStale
		}
		if err != nil {
			return fmt.Errorf("load guarded ACP runtime config epoch: %w", err)
		}
		if epoch.BotRuntimeConfigEpoch != expectedBotEpoch {
			return fmt.Errorf(
				"%w: expected bot epoch %d, found %d",
				acp.ErrRuntimeConfigStale,
				expectedBotEpoch,
				epoch.BotRuntimeConfigEpoch,
			)
		}
		return fn(ctx)
	})
}

// Head reads only the newest successful canonical ACP publication. It is kept
// separate from Load because reset publications intentionally have no snapshot,
// and warm-runtime fencing must also detect a transition from a non-empty head
// to no head after history is cleared.
func (s *StateStore) Head(
	ctx context.Context,
	botID string,
	sessionID string,
) (acp.SessionPublicationHead, bool, error) {
	if s == nil || s.queries == nil {
		return acp.SessionPublicationHead{}, false, errors.New("ACP session state store is unavailable")
	}
	pgBotID, pgSessionID, err := parseScope(botID, sessionID)
	if err != nil {
		return acp.SessionPublicationHead{}, false, err
	}
	row, err := s.queries.GetACPSessionPublicationHead(ctx, sqlc.GetACPSessionPublicationHeadParams{
		BotID: pgBotID, SessionID: pgSessionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return acp.SessionPublicationHead{}, false, nil
	}
	if err != nil {
		return acp.SessionPublicationHead{}, false, fmt.Errorf("load ACP session publication head: %w", err)
	}
	runID := row.RunID.String()
	if _, err := dbpkg.ParseUUID(runID); err != nil {
		return acp.SessionPublicationHead{}, false, sessionStateOutOfSync(
			fmt.Errorf("canonical ACP publication has invalid run id: %w", err),
		)
	}
	kind := acp.SessionPublicationCheckpoint
	if row.CheckpointReset {
		kind = acp.SessionPublicationReset
	}
	return acp.SessionPublicationHead{RunID: runID, Kind: kind}, true, nil
}

func (s *StateStore) Load(
	ctx context.Context,
	botID string,
	sessionID string,
	consume acp.SessionStateRecordConsumer,
) (bool, error) {
	if s == nil || s.queries == nil {
		return false, errors.New("ACP session state store is unavailable")
	}
	if consume == nil {
		return false, errors.New("ACP session state consumer is required")
	}
	pgBotID, pgSessionID, err := parseScope(botID, sessionID)
	if err != nil {
		return false, err
	}
	txer, ok := s.queries.(stateTransactionRunner)
	capability, supported := s.queries.(stateTransactionCapability)
	if !ok || !supported || !capability.SupportsTransactions() {
		return false, errors.New("ACP session state load requires transaction support")
	}
	var found bool
	err = txer.InTx(ctx, func(queries dbstore.Queries) error {
		state, stateFound, loadErr := loadStateHeader(ctx, queries, pgBotID, pgSessionID)
		if loadErr != nil || !stateFound {
			return loadErr
		}
		found = true
		reader := newStateLineReader(queries, pgBotID, pgSessionID, state)
		consumeErr := consume(ctx, state, reader.next)
		reader.close()
		if consumeErr != nil {
			return consumeErr
		}
		if reader.terminalErr != nil && !errors.Is(reader.terminalErr, io.EOF) {
			return reader.terminalErr
		}
		if !reader.exhausted {
			return errors.New("ACP session state consumer returned before reading through EOF")
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

func loadStateHeader(
	ctx context.Context,
	queries dbstore.Queries,
	pgBotID pgtype.UUID,
	pgSessionID pgtype.UUID,
) (acp.PersistedSessionState, bool, error) {
	header, err := queries.GetACPSessionState(ctx, sqlc.GetACPSessionStateParams{
		BotID: pgBotID, SessionID: pgSessionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return acp.PersistedSessionState{}, false, nil
	}
	if err != nil {
		return acp.PersistedSessionState{}, false, fmt.Errorf("load ACP session state header: %w", err)
	}
	throughRunID := header.ThroughRunID.String()
	if !header.StagedThroughRunID.Valid {
		return acp.PersistedSessionState{}, false, fmt.Errorf(
			"%w: latest successful ACP run %s has no staged snapshot",
			acp.ErrSessionStateOutOfSync,
			throughRunID,
		)
	}
	shapes, err := decodeStateFileShapes(header.FileShapes)
	if err != nil {
		return acp.PersistedSessionState{}, false, sessionStateOutOfSync(err)
	}
	files := make([]acp.PersistedSessionStateFile, 0, len(shapes))
	for _, shape := range shapes {
		files = append(files, acp.PersistedSessionStateFile{SessionStateFileShape: shape})
	}
	state := acp.PersistedSessionState{
		AgentID:             header.AgentID,
		ACPSessionID:        header.AcpSessionID,
		ThroughRunID:        throughRunID,
		Cwd:                 header.Cwd,
		TranscriptPath:      header.TranscriptPath,
		RuntimeFencingToken: header.RuntimeFencingToken,
		FileCount:           header.FileCount,
		RecordCount:         header.RecordCount,
		Files:               files,
	}
	if err := validateStateHeader(state); err != nil {
		return acp.PersistedSessionState{}, false, sessionStateOutOfSync(
			fmt.Errorf("validate stored ACP session state header: %w", err),
		)
	}
	if err := validateStateFileShapes(state); err != nil {
		return acp.PersistedSessionState{}, false, sessionStateOutOfSync(
			fmt.Errorf("validate stored ACP session state shapes: %w", err),
		)
	}
	return state, true, nil
}

type stateLineReader struct {
	queries       dbstore.Queries
	botID         pgtype.UUID
	sessionID     pgtype.UUID
	fileBounds    []byte
	boundsErr     error
	validator     stateStreamValidator
	page          []sqlc.ListACPSessionStateLinePageRow
	pageIndex     int
	afterFilePath string
	afterLine     int64
	terminalErr   error
	exhausted     bool
	closed        bool
}

func newStateLineReader(
	queries dbstore.Queries,
	botID pgtype.UUID,
	sessionID pgtype.UUID,
	state acp.PersistedSessionState,
) *stateLineReader {
	// The requested version's shape is the read bound: rows past a file's
	// declared record count (a crashed candidate's dangling tail) and files
	// outside the shape set are invisible to this reader by construction.
	bounds := make(map[string]int64, len(state.Files))
	for _, file := range state.Files {
		bounds[file.Path] = file.Records
	}
	encodedBounds, boundsErr := json.Marshal(bounds)
	return &stateLineReader{
		queries: queries, botID: botID, sessionID: sessionID,
		fileBounds: encodedBounds, boundsErr: boundsErr,
		validator: newStateStreamValidator(state),
	}
}

func (r *stateLineReader) next(ctx context.Context) (acp.SessionStateRecord, error) {
	if r == nil || r.closed {
		return acp.SessionStateRecord{}, errors.New("ACP session state reader is no longer valid")
	}
	if r.terminalErr != nil {
		return acp.SessionStateRecord{}, r.terminalErr
	}
	if r.pageIndex >= len(r.page) {
		if r.boundsErr != nil {
			r.terminalErr = fmt.Errorf("encode ACP session state read bounds: %w", r.boundsErr)
			return acp.SessionStateRecord{}, r.terminalErr
		}
		rows, err := r.queries.ListACPSessionStateLinePage(ctx, sqlc.ListACPSessionStateLinePageParams{
			FileBounds: r.fileBounds, SessionID: r.sessionID, BotID: r.botID,
			AfterFilePath: r.afterFilePath, AfterLineNumber: r.afterLine,
			MaxResults: stateLinePageResults, MaxPageBytes: stateLinePageBytes,
		})
		if err != nil {
			r.terminalErr = fmt.Errorf("load ACP session state line page: %w", err)
			return acp.SessionStateRecord{}, r.terminalErr
		}
		r.page = rows
		r.pageIndex = 0
		if len(rows) == 0 {
			if err := r.validator.finish(); err != nil {
				r.terminalErr = sessionStateOutOfSync(fmt.Errorf("validate stored ACP session state: %w", err))
				return acp.SessionStateRecord{}, r.terminalErr
			}
			r.exhausted = true
			r.terminalErr = io.EOF
			return acp.SessionStateRecord{}, io.EOF
		}
	}
	row := r.page[r.pageIndex]
	r.pageIndex++
	record, err := r.validator.accept(acp.SessionStateRecord{
		FilePath: row.FilePath, LineNumber: row.LineNumber, Content: json.RawMessage(row.Content),
	})
	if err != nil {
		r.terminalErr = sessionStateOutOfSync(fmt.Errorf("validate stored ACP session state: %w", err))
		return acp.SessionStateRecord{}, r.terminalErr
	}
	r.afterFilePath = record.FilePath
	r.afterLine = record.LineNumber
	return record, nil
}

func (r *stateLineReader) close() {
	if r == nil {
		return
	}
	r.closed = true
	r.page = nil
}

func sessionStateOutOfSync(err error) error {
	if errors.Is(err, acp.ErrSessionStateOutOfSync) {
		return err
	}
	return fmt.Errorf("%w: %w", acp.ErrSessionStateOutOfSync, err)
}

func (s *StateStore) Replace(
	ctx context.Context,
	botID string,
	sessionID string,
	state acp.PersistedSessionState,
	records acp.SessionStateRecordReader,
) error {
	if s == nil || s.queries == nil {
		return errors.New("ACP session state store is unavailable")
	}
	if records == nil {
		return errors.New("ACP session state record reader is required")
	}
	pgBotID, pgSessionID, err := parseScope(botID, sessionID)
	if err != nil {
		return err
	}
	pgThroughRunID, err := dbpkg.ParseUUID(strings.TrimSpace(state.ThroughRunID))
	if err != nil {
		return fmt.Errorf("invalid ACP session state through run id: %w", err)
	}
	if err := validateStateHeader(state); err != nil {
		return fmt.Errorf("validate ACP session state header: %w", err)
	}
	if err := validateStateFileShapes(state); err != nil {
		return fmt.Errorf("validate ACP session state shapes: %w", err)
	}
	fence, ok := runtimefence.FromContext(ctx)
	if !ok {
		return errors.New("ACP session state replacement requires a runtime persistence fence")
	}
	if state.RuntimeFencingToken != fence.Token {
		return fmt.Errorf(
			"ACP session state runtime fencing token %d does not match active token %d",
			state.RuntimeFencingToken,
			fence.Token,
		)
	}
	encodedShapes, err := encodeStateFileShapes(state.Files)
	if err != nil {
		return err
	}
	keptPaths := make([]string, 0, len(state.Files))
	for _, file := range state.Files {
		keptPaths = append(keptPaths, file.Path)
	}
	encodedKeptPaths, err := json.Marshal(keptPaths)
	if err != nil {
		return fmt.Errorf("encode ACP session state paths: %w", err)
	}
	return runtimefence.InTransaction(ctx, s.queries, botID, sessionID, func(queries dbstore.Queries) error {
		// The canonical head's shapes are the append-only baseline, read before
		// any write. Staging and publication commit in different transactions,
		// so no statement below may touch a row the canonical version
		// references: while a canonical checkpoint exists, every canonical file
		// must survive with a proven byte-identical prefix, or staging declines
		// entirely (the caller publishes an explicit reset; once that reset is
		// canonical the rows are unreferenced and a full rewrite becomes safe).
		canonical := map[string]acp.SessionStateFileShape{}
		if row, canonicalErr := queries.GetACPCanonicalStateShape(ctx, sqlc.GetACPCanonicalStateShapeParams{
			SessionID: pgSessionID, BotID: pgBotID,
		}); canonicalErr == nil {
			shapes, decodeErr := decodeStateFileShapes(row.FileShapes)
			if decodeErr != nil {
				return decodeErr
			}
			for _, shape := range shapes {
				canonical[shape.Path] = shape
			}
		} else if !errors.Is(canonicalErr, pgx.ErrNoRows) {
			return fmt.Errorf("load canonical ACP session state shape: %w", canonicalErr)
		}
		candidate := make(map[string]acp.PersistedSessionStateFile, len(state.Files))
		for _, file := range state.Files {
			candidate[file.Path] = file
		}
		appendFrom := make(map[string]int64, len(state.Files))
		var expectedInserted int64
		for path, base := range canonical {
			file, present := candidate[path]
			if !present {
				return fmt.Errorf("%w: canonical file %q is absent from the capture", acp.ErrSessionStateDivergent, path)
			}
			if file.PrefixRecords != base.Records || file.PrefixDigest != base.Digest || file.Records < base.Records {
				return fmt.Errorf(
					"%w: canonical file %q prefix is no longer provable (canonical %d records, capture proves %d)",
					acp.ErrSessionStateDivergent, path, base.Records, file.PrefixRecords,
				)
			}
		}
		for _, file := range state.Files {
			keep := int64(0)
			if base, hasBase := canonical[file.Path]; hasBase {
				keep = base.Records
			}
			appendFrom[file.Path] = keep + 1
			expectedInserted += file.Records - keep
		}

		stored, err := queries.UpsertACPSessionState(ctx, sqlc.UpsertACPSessionStateParams{
			SessionID:      pgSessionID,
			BotID:          pgBotID,
			ThroughRunID:   pgThroughRunID,
			AgentID:        strings.TrimSpace(state.AgentID),
			AcpSessionID:   strings.TrimSpace(state.ACPSessionID),
			Cwd:            strings.TrimSpace(state.Cwd),
			TranscriptPath: state.TranscriptPath,
			FileCount:      state.FileCount,
			RecordCount:    state.RecordCount,
			FileShapes:     encodedShapes,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return runtimefence.ErrStale
		}
		if err != nil {
			return fmt.Errorf("stage ACP session state: %w", err)
		}
		if stored.RuntimeFencingToken != fence.Token || stored.ThroughRunID != pgThroughRunID {
			return runtimefence.ErrStale
		}

		// The divergence gate above guarantees candidate files are a superset
		// of canonical files, so both destructive statements below can only
		// ever touch rows no canonical version references: files a crashed
		// candidate introduced, and dangling tails past canonical counts.
		if _, err := queries.DeleteACPSessionStateLineFilesNotIn(ctx, sqlc.DeleteACPSessionStateLineFilesNotInParams{
			SessionID: pgSessionID, KeptPaths: encodedKeptPaths,
		}); err != nil {
			return fmt.Errorf("drop removed ACP session state files: %w", err)
		}
		for _, file := range state.Files {
			if _, err := queries.TrimACPSessionStateLines(ctx, sqlc.TrimACPSessionStateLinesParams{
				SessionID: pgSessionID, FilePath: file.Path, KeepRecords: appendFrom[file.Path] - 1,
			}); err != nil {
				return fmt.Errorf("trim ACP session state file %q: %w", file.Path, err)
			}
		}

		validator := newStateStreamValidator(state)
		writer := stateLineBatchWriter{
			ctx: ctx, queries: queries, sessionID: pgSessionID, throughRunID: pgThroughRunID,
			payload: []byte{'['},
		}
		for {
			record, readErr := records(ctx)
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return fmt.Errorf("read ACP session state record: %w", readErr)
			}
			record, err = validator.accept(record)
			if err != nil {
				return fmt.Errorf("validate ACP session state record: %w", err)
			}
			from, known := appendFrom[record.FilePath]
			if !known {
				return fmt.Errorf("ACP session state record for undeclared file %q", record.FilePath)
			}
			if record.LineNumber < from {
				// Proven canonical prefix: already stored, skip the write while
				// the validator still checks ordering and totals.
				continue
			}
			if err := writer.add(record); err != nil {
				return err
			}
		}
		if err := validator.finish(); err != nil {
			return fmt.Errorf("validate ACP session state records: %w", err)
		}
		if err := writer.flush(); err != nil {
			return err
		}
		if writer.inserted != expectedInserted {
			return fmt.Errorf(
				"insert staged ACP session state lines: wrote %d of %d appended rows",
				writer.inserted,
				expectedInserted,
			)
		}
		if _, err := queries.PruneACPSessionStateVersions(ctx, sqlc.PruneACPSessionStateVersionsParams{
			SessionID: pgSessionID, ThroughRunID: pgThroughRunID,
		}); err != nil {
			return fmt.Errorf("prune ACP session state versions: %w", err)
		}
		return nil
	})
}

// CanonicalShape returns the committed head version's per-file shapes as an
// advisory pre-capture read; Replace re-validates any prefix proof against its
// own in-transaction read of the same shape.
func (s *StateStore) CanonicalShape(
	ctx context.Context,
	botID string,
	sessionID string,
) (map[string]acp.SessionStateFileShape, bool, error) {
	if s == nil || s.queries == nil {
		return nil, false, errors.New("ACP session state store is unavailable")
	}
	pgBotID, pgSessionID, err := parseScope(botID, sessionID)
	if err != nil {
		return nil, false, err
	}
	row, err := s.queries.GetACPCanonicalStateShape(ctx, sqlc.GetACPCanonicalStateShapeParams{
		SessionID: pgSessionID, BotID: pgBotID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load canonical ACP session state shape: %w", err)
	}
	shapes, err := decodeStateFileShapes(row.FileShapes)
	if err != nil {
		return nil, false, err
	}
	byPath := make(map[string]acp.SessionStateFileShape, len(shapes))
	for _, shape := range shapes {
		byPath[shape.Path] = shape
	}
	return byPath, true, nil
}

func parseScope(botID, sessionID string) (pgBotID, pgSessionID pgtype.UUID, err error) {
	pgBotID, err = dbpkg.ParseUUID(strings.TrimSpace(botID))
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("invalid ACP session state bot id: %w", err)
	}
	pgSessionID, err = dbpkg.ParseUUID(strings.TrimSpace(sessionID))
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("invalid ACP session state session id: %w", err)
	}
	return pgBotID, pgSessionID, nil
}

// stateFileShape is the JSONB encoding of one file's shape in the header's
// file_shapes column. It deliberately excludes the transient prefix proof.
type stateFileShape struct {
	Path    string `json:"path"`
	Records int64  `json:"records"`
	Digest  string `json:"digest"`
}

func encodeStateFileShapes(files []acp.PersistedSessionStateFile) ([]byte, error) {
	shapes := make([]stateFileShape, 0, len(files))
	for _, file := range files {
		shapes = append(shapes, stateFileShape{Path: file.Path, Records: file.Records, Digest: file.Digest})
	}
	encoded, err := json.Marshal(shapes)
	if err != nil {
		return nil, fmt.Errorf("encode ACP session state file shapes: %w", err)
	}
	return encoded, nil
}

func decodeStateFileShapes(raw []byte) ([]acp.SessionStateFileShape, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var shapes []stateFileShape
	if err := json.Unmarshal(raw, &shapes); err != nil {
		return nil, fmt.Errorf("decode ACP session state file shapes: %w", err)
	}
	decoded := make([]acp.SessionStateFileShape, 0, len(shapes))
	for _, shape := range shapes {
		decoded = append(decoded, acp.SessionStateFileShape{
			Path: shape.Path, Records: shape.Records, Digest: shape.Digest,
		})
	}
	return decoded, nil
}

func validateStateFileShapes(state acp.PersistedSessionState) error {
	if len(state.Files) > maxStateFiles || len(state.Files) != int(state.FileCount) {
		return fmt.Errorf("ACP session state declares %d files but %d shapes", state.FileCount, len(state.Files))
	}
	var totalRecords int64
	previousPath := ""
	transcriptFound := false
	for _, file := range state.Files {
		if err := validateStatePath(file.Path); err != nil {
			return fmt.Errorf("invalid shape path %q: %w", file.Path, err)
		}
		if previousPath != "" && file.Path <= previousPath {
			return fmt.Errorf("shape path %q is not strictly ordered after %q", file.Path, previousPath)
		}
		previousPath = file.Path
		if file.Records <= 0 || file.Records > runtimeStateMaxLinesPerFile {
			return fmt.Errorf("shape %q record count %d is outside 1..%d", file.Path, file.Records, runtimeStateMaxLinesPerFile)
		}
		if len(file.Digest) != 64 {
			return fmt.Errorf("shape %q digest must be a hex sha256", file.Path)
		}
		if file.PrefixRecords < 0 || file.PrefixRecords > file.Records {
			return fmt.Errorf("shape %q prefix %d is outside 0..%d", file.Path, file.PrefixRecords, file.Records)
		}
		totalRecords += file.Records
		if file.Path == state.TranscriptPath {
			transcriptFound = true
		}
	}
	if totalRecords != state.RecordCount {
		return fmt.Errorf("ACP session state declares %d records but shapes sum to %d", state.RecordCount, totalRecords)
	}
	if !transcriptFound {
		return fmt.Errorf("shapes do not contain transcript path %q", state.TranscriptPath)
	}
	return nil
}

// runtimeStateMaxLinesPerFile mirrors the per-file record ceiling enforced by
// the client-side spool; the adapter re-checks it on the shape metadata.
const runtimeStateMaxLinesPerFile = 2_000_000

func validateStateHeader(state acp.PersistedSessionState) error {
	if err := validateBoundedText("agent id", state.AgentID, maxStateAgentIDBytes); err != nil {
		return err
	}
	if err := validateBoundedText("ACP session id", state.ACPSessionID, maxStateSessionIDBytes); err != nil {
		return err
	}
	if _, err := dbpkg.ParseUUID(strings.TrimSpace(state.ThroughRunID)); err != nil {
		return fmt.Errorf("through run id is invalid: %w", err)
	}
	if err := validateBoundedText("working directory", state.Cwd, maxStateCwdBytes); err != nil {
		return err
	}
	if err := validateStatePath(state.TranscriptPath); err != nil {
		return fmt.Errorf("invalid transcript path: %w", err)
	}
	if state.RuntimeFencingToken <= 0 {
		return errors.New("runtime fencing token must be positive")
	}
	if state.FileCount <= 0 || state.FileCount > maxStateFiles {
		return fmt.Errorf("ACP session state file count %d is outside 1..%d", state.FileCount, maxStateFiles)
	}
	if state.RecordCount <= 0 || state.RecordCount > maxStateRecords {
		return fmt.Errorf("ACP session state record count %d is outside 1..%d", state.RecordCount, maxStateRecords)
	}
	if int64(state.FileCount) > state.RecordCount {
		return fmt.Errorf("ACP session state has %d files but only %d records", state.FileCount, state.RecordCount)
	}
	return nil
}

func validateBoundedText(label, value string, limit int) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s is required", label)
	}
	if len(trimmed) > limit {
		return fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	return nil
}

func validateStatePath(value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return errors.New("path is empty or has surrounding whitespace")
	}
	if len(value) > maxStatePathBytes {
		return fmt.Errorf("path exceeds %d bytes", maxStatePathBytes)
	}
	if strings.ContainsAny(value, "\x00\r\n\\") || strings.HasPrefix(value, "/") {
		return errors.New("path must be a relative slash path")
	}
	if path.Clean(value) != value || value == "." {
		return errors.New("path is not clean")
	}
	for _, component := range strings.Split(value, "/") {
		if component == "." || component == ".." {
			return errors.New("path contains a traversal component")
		}
	}
	if !strings.HasSuffix(value, ".jsonl") {
		return errors.New("path must name a .jsonl file")
	}
	return nil
}

type stateStreamValidator struct {
	expectedFiles       int32
	expectedRecords     int64
	expectedPerFile     map[string]acp.SessionStateFileShape
	transcriptPath      string
	currentFilePath     string
	currentFileHash     hash.Hash
	nextLineNumber      int64
	fileCount           int32
	recordCount         int64
	jsonlBytes          int64
	transcriptPathFound bool
	finished            bool
}

func newStateStreamValidator(state acp.PersistedSessionState) stateStreamValidator {
	validator := stateStreamValidator{
		expectedFiles: state.FileCount, expectedRecords: state.RecordCount,
		transcriptPath: state.TranscriptPath,
	}
	if len(state.Files) > 0 {
		validator.expectedPerFile = make(map[string]acp.SessionStateFileShape, len(state.Files))
		for _, file := range state.Files {
			validator.expectedPerFile[file.Path] = file.SessionStateFileShape
		}
	}
	return validator
}

// closeCurrentFile verifies the file that just ended matches its declared
// shape, including the content digest over its compacted records. Totals
// alone would let one file run short while another runs long, and the digest
// is what makes a corrupted or partially rewritten stored file fail closed
// instead of restoring silently wrong context.
func (v *stateStreamValidator) closeCurrentFile() error {
	if v.currentFilePath == "" || v.expectedPerFile == nil {
		return nil
	}
	declared, ok := v.expectedPerFile[v.currentFilePath]
	if !ok {
		return fmt.Errorf("JSONL file %q is not declared by the header shapes", v.currentFilePath)
	}
	if got := v.nextLineNumber - 1; got != declared.Records {
		return fmt.Errorf("JSONL file %q has %d records; shape declares %d", v.currentFilePath, got, declared.Records)
	}
	if declared.Digest != "" && v.currentFileHash != nil {
		if got := hex.EncodeToString(v.currentFileHash.Sum(nil)); got != declared.Digest {
			return fmt.Errorf("JSONL file %q content digest %s does not match declared %s", v.currentFilePath, got, declared.Digest)
		}
	}
	return nil
}

func (v *stateStreamValidator) accept(record acp.SessionStateRecord) (acp.SessionStateRecord, error) {
	if v == nil || v.finished {
		return acp.SessionStateRecord{}, errors.New("ACP session state record stream is already complete")
	}
	if err := validateStatePath(record.FilePath); err != nil {
		return acp.SessionStateRecord{}, fmt.Errorf("invalid JSONL file path %q: %w", record.FilePath, err)
	}
	if record.LineNumber <= 0 {
		return acp.SessionStateRecord{}, fmt.Errorf("invalid JSONL record %q line %d", record.FilePath, record.LineNumber)
	}
	raw := bytes.TrimSpace(record.Content)
	if len(raw) == 0 || len(raw) > maxStateLineBytes || !json.Valid(raw) {
		return acp.SessionStateRecord{}, fmt.Errorf(
			"JSONL file %q line %d must be valid JSON no larger than %d bytes",
			record.FilePath,
			record.LineNumber,
			maxStateLineBytes,
		)
	}
	if !utf8.Valid(raw) {
		// json.Valid accepts invalid UTF-8 inside string literals, but such
		// bytes cannot round-trip: json.Marshal would silently rewrite them to
		// U+FFFD in the transport envelope and PostgreSQL would reject them,
		// so the stored bytes could never match the capture digest. Declining
		// as divergence keeps the session usable (the turn publishes a reset
		// head) instead of failing every checkpoint forever.
		return acp.SessionStateRecord{}, fmt.Errorf(
			"%w: JSONL file %q line %d contains invalid UTF-8",
			acp.ErrSessionStateDivergent,
			record.FilePath,
			record.LineNumber,
		)
	}
	var compact bytes.Buffer
	compact.Grow(len(raw))
	if err := json.Compact(&compact, raw); err != nil {
		return acp.SessionStateRecord{}, fmt.Errorf("compact JSONL file %q line %d: %w", record.FilePath, record.LineNumber, err)
	}
	record.Content = json.RawMessage(compact.Bytes())

	if v.currentFilePath == "" || record.FilePath != v.currentFilePath {
		if v.currentFilePath != "" && record.FilePath <= v.currentFilePath {
			return acp.SessionStateRecord{}, fmt.Errorf(
				"JSONL file path %q is not strictly ordered after %q",
				record.FilePath,
				v.currentFilePath,
			)
		}
		if err := v.closeCurrentFile(); err != nil {
			return acp.SessionStateRecord{}, err
		}
		if record.LineNumber != 1 {
			return acp.SessionStateRecord{}, fmt.Errorf(
				"non-contiguous JSONL record %q line %d (want 1)",
				record.FilePath,
				record.LineNumber,
			)
		}
		v.fileCount++
		if v.fileCount > v.expectedFiles || v.fileCount > maxStateFiles {
			return acp.SessionStateRecord{}, errors.New("ACP session state contains too many JSONL files")
		}
		v.currentFilePath = record.FilePath
		v.currentFileHash = sha256.New()
		v.nextLineNumber = 2
		if record.FilePath == v.transcriptPath {
			v.transcriptPathFound = true
		}
	} else {
		if record.LineNumber != v.nextLineNumber {
			return acp.SessionStateRecord{}, fmt.Errorf(
				"non-contiguous JSONL record %q line %d (want %d)",
				record.FilePath,
				record.LineNumber,
				v.nextLineNumber,
			)
		}
		v.nextLineNumber++
	}

	if v.currentFileHash != nil {
		v.currentFileHash.Write(record.Content)
		v.currentFileHash.Write([]byte{'\n'})
	}
	v.recordCount++
	if v.recordCount > v.expectedRecords || v.recordCount > maxStateRecords {
		return acp.SessionStateRecord{}, errors.New("ACP session state contains too many JSONL records")
	}
	// The total budget describes bytes restored to JSONL files, not repeated
	// database wrapper/path overhead: each compact JSON value is followed by LF.
	v.jsonlBytes += int64(len(record.Content)) + 1
	if v.jsonlBytes > maxStateJSONLBytes {
		return acp.SessionStateRecord{}, fmt.Errorf("ACP session state exceeds %d restored JSONL bytes", maxStateJSONLBytes)
	}
	return record, nil
}

func (v *stateStreamValidator) finish() error {
	if v == nil || v.finished {
		return errors.New("ACP session state record stream is already complete")
	}
	v.finished = true
	if err := v.closeCurrentFile(); err != nil {
		return err
	}
	if v.fileCount != v.expectedFiles {
		return fmt.Errorf("ACP session state contains %d files; header declares %d", v.fileCount, v.expectedFiles)
	}
	if v.recordCount != v.expectedRecords {
		return fmt.Errorf("ACP session state contains %d records; header declares %d", v.recordCount, v.expectedRecords)
	}
	if !v.transcriptPathFound {
		return fmt.Errorf("transcript path %q is not present in snapshot files", v.transcriptPath)
	}
	return nil
}

type stateLineBatchWriter struct {
	ctx          context.Context
	queries      dbstore.Queries
	sessionID    pgtype.UUID
	throughRunID pgtype.UUID
	payload      []byte
	batchCount   int64
	batchJSONL   int64
	inserted     int64
	jsonlBytes   int64
}

func (w *stateLineBatchWriter) add(record acp.SessionStateRecord) error {
	encoded, err := encodeStateLine(record)
	if err != nil {
		return fmt.Errorf("encode ACP session state record: %w", err)
	}
	separatorBytes := 0
	if w.batchCount > 0 {
		separatorBytes = 1
	}
	recordJSONLBytes := int64(len(record.Content)) + 1
	if w.batchCount > 0 && (w.batchCount >= stateLineBatchRecords ||
		len(w.payload)+separatorBytes+len(encoded)+1 > targetStateBatchBytes ||
		recordJSONLBytes > int64(targetStateBatchBytes)-w.batchJSONL) {
		if err := w.flush(); err != nil {
			return err
		}
	}
	if w.batchCount > 0 {
		w.payload = append(w.payload, ',')
	}
	w.payload = append(w.payload, encoded...)
	w.batchCount++
	w.batchJSONL += recordJSONLBytes
	return nil
}

func encodeStateLine(record acp.SessionStateRecord) ([]byte, error) {
	encodedPath, err := json.Marshal(record.FilePath)
	if err != nil {
		return nil, fmt.Errorf("encode ACP session state file path: %w", err)
	}
	// Content is embedded as a JSON *string*, not as a nested JSON value: the
	// envelope is parsed as JSONB on the server, and only a string member
	// survives that parse byte-for-byte. A nested value would be normalized
	// (key order, number rendering), breaking every digest the append-only
	// proof and load verification depend on.
	encodedContent, err := json.Marshal(string(record.Content))
	if err != nil {
		return nil, fmt.Errorf("encode ACP session state content: %w", err)
	}
	encoded := make([]byte, 0, len(encodedPath)+len(encodedContent)+64)
	encoded = append(encoded, `{"file_path":`...)
	encoded = append(encoded, encodedPath...)
	encoded = append(encoded, `,"line_number":`...)
	encoded = strconv.AppendInt(encoded, record.LineNumber, 10)
	encoded = append(encoded, `,"content":`...)
	encoded = append(encoded, encodedContent...)
	encoded = append(encoded, '}')
	return encoded, nil
}

func (w *stateLineBatchWriter) flush() error {
	if w.batchCount == 0 {
		return nil
	}
	w.payload = append(w.payload, ']')
	result, err := w.queries.InsertACPSessionStateLines(w.ctx, sqlc.InsertACPSessionStateLinesParams{
		SessionID: w.sessionID, ThroughRunID: w.throughRunID, StateLines: w.payload,
	})
	if err != nil {
		return fmt.Errorf("insert staged ACP session state lines: %w", err)
	}
	if err := w.account(result); err != nil {
		return err
	}
	w.payload = []byte{'['}
	w.batchCount = 0
	w.batchJSONL = 0
	return nil
}

func (w *stateLineBatchWriter) account(result sqlc.InsertACPSessionStateLinesRow) error {
	if result.RowsWritten != w.batchCount {
		return fmt.Errorf(
			"insert staged ACP session state lines: wrote %d of %d batch rows",
			result.RowsWritten,
			w.batchCount,
		)
	}
	if result.JsonlBytes < result.RowsWritten {
		return fmt.Errorf("insert staged ACP session state lines: invalid stored byte count %d", result.JsonlBytes)
	}
	if result.JsonlBytes > maxStateJSONLBytes-w.jsonlBytes {
		return fmt.Errorf("ACP session state exceeds %d restored JSONL bytes", maxStateJSONLBytes)
	}
	w.inserted += result.RowsWritten
	w.jsonlBytes += result.JsonlBytes
	return nil
}
