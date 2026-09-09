// Database checkpointing for Claude Code sessions. The CLI keeps its durable
// session transcript under CLAUDE_CONFIG_DIR/projects on the bot volume;
// staging a copy into the generalized runtime session store lets a session
// survive the volume being reset or the bot moving hosts. Capture streams the
// transcript twice (shape pass, record pass) so no whole-file allocation is
// ever held, and the store's validators arbitrate divergence.
package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/agentstate"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const (
	projectsDirName = "projects"
	// checkpointMaxProjectEntries bounds the transcript search; the projects
	// tree holds one directory per working directory with one JSONL per
	// session, so thousands of entries already indicate an unexpected layout.
	checkpointMaxProjectEntries = 8192
	checkpointMaxLineBytes      = 8 * 1024 * 1024
)

// checkpointFS is the minimal workspace file surface checkpointing needs;
// *bridge.Client satisfies it.
type checkpointFS interface {
	ListDirBounded(ctx context.Context, path string, recursive bool, maxEntries int32) ([]*pb.FileEntry, error)
	Stat(ctx context.Context, path string) (*pb.FileEntry, error)
	ReadRaw(ctx context.Context, path string) (io.ReadCloser, error)
	WriteRaw(ctx context.Context, path string, r io.Reader) (int64, error)
	Mkdir(ctx context.Context, path string) error
}

// errCheckpointNotApplicable aborts a restore whose stored snapshot does not
// belong to this runtime (or names no session); the caller treats it as
// "nothing to restore", not as a failure.
var errCheckpointNotApplicable = errors.New("stored checkpoint is not an applicable claude session snapshot")

// locateSessionTranscript finds the session's transcript under the config
// dir's projects tree by its session-id file name, returning the transcript
// path relative to the config dir. The project directory name encodes the
// working directory in a CLI-private way, so discovery goes by file name
// instead of reimplementing that encoding.
func locateSessionTranscript(ctx context.Context, fs checkpointFS, sessionID string) (string, bool, error) {
	if err := validateClaudeSessionID(sessionID); err != nil {
		return "", false, err
	}
	projectsRoot := path.Join(configDir, projectsDirName)
	if _, err := fs.Stat(ctx, projectsRoot); err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("stat claude projects dir: %w", err)
	}
	entries, err := fs.ListDirBounded(ctx, projectsRoot, true, checkpointMaxProjectEntries)
	if err != nil {
		return "", false, fmt.Errorf("list claude projects dir: %w", err)
	}
	wanted := sessionID + ".jsonl"
	for _, entry := range entries {
		if entry.GetIsDir() || strings.HasPrefix(entry.GetMode(), "L") {
			continue
		}
		rel := cleanTranscriptRelPath(entry.GetPath())
		if rel == "" || path.Base(rel) != wanted {
			continue
		}
		return path.Join(projectsDirName, rel), true, nil
	}
	return "", false, nil
}

// cleanTranscriptRelPath normalizes a listed entry path and rejects anything
// that could escape the projects tree.
func cleanTranscriptRelPath(value string) string {
	value = strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"), "/")
	if value == "" || path.Clean(value) != value {
		return ""
	}
	for _, component := range strings.Split(value, "/") {
		if component == "." || component == ".." || component == "" {
			return ""
		}
	}
	return value
}

func validateClaudeSessionID(value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 256 ||
		strings.ContainsAny(value, "\x00\r\n/\\") || value == "." || value == ".." {
		return errors.New("claude session id is not a safe file name")
	}
	return nil
}

// checkpointRequest identifies the turn a checkpoint capture belongs to.
type checkpointRequest struct {
	BotID    string
	ThreadID string
	RunID    string
	// RuntimeMetadata is the session's merged runtime metadata including any
	// delta the finished turn produced (e.g. a newly created session id).
	RuntimeMetadata map[string]any
}

// stageSessionCheckpoint captures the session transcript backing the turn
// that just completed and stages it under the turn's run id. It returns
// false without error when there is nothing to snapshot (no store, no
// session, no transcript) or when the capture no longer extends the
// canonical checkpoint — the turn then reports a declined checkpoint and the
// round publishes a reset head.
func (d *Driver) stageWithFS(ctx context.Context, fs checkpointFS, input checkpointRequest) (bool, error) {
	sessionID := strings.TrimSpace(metadataString(input.RuntimeMetadata, metadataSessionIDKey))
	if sessionID == "" {
		return false, nil
	}
	fence, ok := runtimefence.FromContext(ctx)
	if !ok {
		return false, errors.New("claude checkpoint staging requires a runtime persistence fence")
	}
	transcriptRel, found, err := locateSessionTranscript(ctx, fs, sessionID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	transcriptFull := path.Join(configDir, transcriptRel)

	canonical, _, err := d.stateStore.CanonicalShape(ctx, input.BotID, input.ThreadID)
	if err != nil {
		return false, err
	}
	shape, err := scanTranscriptShape(ctx, fs, transcriptFull, canonical[transcriptRel])
	if err != nil {
		return false, err
	}
	if shape.Records == 0 {
		return false, nil
	}
	cwd := firstNonEmpty(metadataString(input.RuntimeMetadata, "project_path"), defaultProjectPath)
	state := agentstate.PersistedSessionState{
		AgentID:             RuntimeType,
		AgentSessionID:      sessionID,
		ThroughRunID:        input.RunID,
		Cwd:                 cwd,
		TranscriptPath:      transcriptRel,
		RuntimeFencingToken: fence.Token,
		FileCount:           1,
		RecordCount:         shape.Records,
		Files: []agentstate.PersistedSessionStateFile{{
			SessionStateFileShape: agentstate.SessionStateFileShape{
				Path: transcriptRel, Records: shape.Records, Digest: shape.Digest,
			},
			PrefixRecords: shape.PrefixRecords,
			PrefixDigest:  shape.PrefixDigest,
		}},
	}
	reader := newTranscriptRecordReader(fs, transcriptFull, transcriptRel)
	defer reader.close()
	if err := d.stateStore.Replace(ctx, input.BotID, input.ThreadID, state, reader.next); err != nil {
		if errors.Is(err, agentstate.ErrSessionStateDivergent) {
			d.logger.Warn("claude transcript diverged from the canonical checkpoint; publishing a reset instead",
				slog.String("bot_id", input.BotID), slog.String("session_id", input.ThreadID))
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// transcriptShape is one pass's view of the transcript file.
type transcriptShape struct {
	Records       int64
	Digest        string
	PrefixRecords int64
	PrefixDigest  string
}

// scanTranscriptShape streams the transcript once, producing the shape the
// store header declares: logical record count, the digest over compacted
// records, and — when the file still extends the canonical version — the
// running digest at the canonical boundary as the append-only proof.
func scanTranscriptShape(ctx context.Context, fs checkpointFS, fullPath string, canonical agentstate.SessionStateFileShape) (transcriptShape, error) {
	reader, err := fs.ReadRaw(ctx, fullPath)
	if err != nil {
		return transcriptShape{}, fmt.Errorf("read claude transcript: %w", err)
	}
	defer func() { _ = reader.Close() }()

	digest := sha256.New()
	var shape transcriptShape
	scan := newTranscriptScanner(reader)
	for {
		compact, scanErr := scan.next()
		if errors.Is(scanErr, io.EOF) {
			break
		}
		if scanErr != nil {
			return transcriptShape{}, scanErr
		}
		digest.Write(compact)
		digest.Write([]byte{'\n'})
		shape.Records++
		if canonical.Records > 0 && shape.Records == canonical.Records {
			shape.PrefixRecords = canonical.Records
			shape.PrefixDigest = hex.EncodeToString(digest.Sum(nil))
		}
	}
	shape.Digest = hex.EncodeToString(digest.Sum(nil))
	return shape, nil
}

// transcriptScanner yields the compacted JSON of each non-blank transcript
// line; blank physical lines are not part of the persistence contract.
type transcriptScanner struct {
	scanner *bufio.Scanner
	line    int64
}

func newTranscriptScanner(r io.Reader) *transcriptScanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), checkpointMaxLineBytes)
	return &transcriptScanner{scanner: scanner}
}

func (s *transcriptScanner) next() ([]byte, error) {
	for s.scanner.Scan() {
		s.line++
		raw := bytes.TrimSpace(s.scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("claude transcript line %d is not valid JSON", s.line)
		}
		var compact bytes.Buffer
		compact.Grow(len(raw))
		if err := json.Compact(&compact, raw); err != nil {
			return nil, fmt.Errorf("compact claude transcript line %d: %w", s.line, err)
		}
		return compact.Bytes(), nil
	}
	if err := s.scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan claude transcript: %w", err)
	}
	return nil, io.EOF
}

// transcriptRecordReader is the second streaming pass, exposed in the store's
// record-reader shape. A file mutated between the passes fails the store's
// shape validation and the staging declines cleanly.
type transcriptRecordReader struct {
	fs       checkpointFS
	fullPath string
	relPath  string
	reader   io.ReadCloser
	scan     *transcriptScanner
	record   int64
}

func newTranscriptRecordReader(fs checkpointFS, fullPath, relPath string) *transcriptRecordReader {
	return &transcriptRecordReader{fs: fs, fullPath: fullPath, relPath: relPath}
}

func (r *transcriptRecordReader) next(ctx context.Context) (agentstate.SessionStateRecord, error) {
	if r.reader == nil {
		reader, err := r.fs.ReadRaw(ctx, r.fullPath)
		if err != nil {
			return agentstate.SessionStateRecord{}, fmt.Errorf("read claude transcript: %w", err)
		}
		r.reader = reader
		r.scan = newTranscriptScanner(reader)
	}
	compact, err := r.scan.next()
	if err != nil {
		return agentstate.SessionStateRecord{}, err
	}
	r.record++
	content := make([]byte, len(compact))
	copy(content, compact)
	return agentstate.SessionStateRecord{
		FilePath: r.relPath, LineNumber: r.record, Content: json.RawMessage(content),
	}, nil
}

func (r *transcriptRecordReader) close() {
	if r.reader != nil {
		_ = r.reader.Close()
		r.reader = nil
	}
}

// restoreSessionCheckpoint materializes the thread's published snapshot back
// into the workspace and returns the claude session id it describes. The
// checkpoint is the authority on which native session the thread's history
// corresponds to — the session id in runtime metadata lives in a separate
// store that can lag behind (a lost or fenced-out write), so a mismatch
// means the metadata is stale, never that the checkpoint is wrong. An empty
// id means no applicable checkpoint exists.
func (d *Driver) restoreSessionCheckpoint(ctx context.Context, fs checkpointFS, botID, threadID string) (string, error) {
	restoredID := ""
	found, err := d.stateStore.Load(ctx, botID, threadID, func(ctx context.Context, state agentstate.PersistedSessionState, next agentstate.SessionStateRecordReader) error {
		if state.AgentID != RuntimeType || strings.TrimSpace(state.AgentSessionID) == "" {
			return errCheckpointNotApplicable
		}
		if err := writeSnapshotFiles(ctx, fs, next); err != nil {
			return err
		}
		restoredID = strings.TrimSpace(state.AgentSessionID)
		return nil
	})
	if err != nil {
		if errors.Is(err, errCheckpointNotApplicable) {
			return "", nil
		}
		return "", err
	}
	if !found {
		return "", nil
	}
	return restoredID, nil
}

// writeSnapshotFiles streams the snapshot's records back into the config dir,
// one file at a time in the reader's path order.
func writeSnapshotFiles(ctx context.Context, fs checkpointFS, next agentstate.SessionStateRecordReader) error {
	var (
		currentPath string
		pipeWriter  *io.PipeWriter
		writeDone   chan error
	)
	closeCurrent := func() error {
		if pipeWriter == nil {
			return nil
		}
		_ = pipeWriter.Close()
		err := <-writeDone
		pipeWriter = nil
		return err
	}
	openFile := func(rel string) error {
		if cleaned := cleanTranscriptRelPath(rel); cleaned != rel || !strings.HasPrefix(rel, projectsDirName+"/") {
			return fmt.Errorf("checkpoint file path %q is outside the claude projects tree", rel)
		}
		full := path.Join(configDir, rel)
		if err := fs.Mkdir(ctx, path.Dir(full)); err != nil {
			return fmt.Errorf("create claude project dir: %w", err)
		}
		pr, pw := io.Pipe()
		pipeWriter = pw
		writeDone = make(chan error, 1)
		go func() {
			_, err := fs.WriteRaw(ctx, full, pr)
			// Unblock the producer if the write fails part-way.
			_ = pr.CloseWithError(err)
			writeDone <- err
		}()
		return nil
	}
	for {
		record, err := next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = closeCurrent()
			return err
		}
		if record.FilePath != currentPath {
			if err := closeCurrent(); err != nil {
				return err
			}
			if err := openFile(record.FilePath); err != nil {
				return err
			}
			currentPath = record.FilePath
		}
		if _, err := pipeWriter.Write(append(record.Content, '\n')); err != nil {
			_ = closeCurrent()
			return fmt.Errorf("write restored claude transcript: %w", err)
		}
	}
	return closeCurrent()
}
