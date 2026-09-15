package codex

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
	"path"
	"sort"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/agentstate"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const checkpointTimeout = 30 * time.Second

type checkpointFS interface {
	Stat(context.Context, string) (*pb.FileEntry, error)
	ReadRawNoFollow(context.Context, string, string) (io.ReadCloser, error)
	WriteRaw(context.Context, string, io.Reader) (int64, error)
	Mkdir(context.Context, string) error
}

// A warm handle is valid only after its staged run becomes the publication
// head. Starting another turn invalidates it until that turn is committed.
type checkpointHandle struct {
	RunID, NativeID, Path string
}

func (s *appServer) checkpointHandle(sessionID string) checkpointHandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkpoints[sessionID]
}

func (s *appServer) rememberCheckpoint(sessionID string, h checkpointHandle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkpoints == nil {
		s.checkpoints = make(map[string]checkpointHandle)
	}
	s.checkpoints[sessionID] = h
}

// unsubscribe is asynchronous: the acknowledgement alone does not fence a
// writer. thread/closed is sent only after native shutdown and rollout flush.
func (s *appServer) unloadThread(ctx context.Context, id string) error {
	s.mu.Lock()
	if s.threadClosed == nil {
		s.threadClosed = make(map[string]chan struct{})
	}
	done := s.threadClosed[id]
	if done == nil {
		done = make(chan struct{})
		s.threadClosed[id] = done
	}
	s.mu.Unlock()
	var response protocol.ThreadUnsubscribeResponse
	if err := s.conn.Call(ctx, protocol.MethodThreadUnsubscribe, protocol.ThreadUnsubscribeParams{ThreadID: id}, &response); err != nil {
		return err
	}
	if response.Status == protocol.ThreadUnsubscribeStatusNotLoaded {
		s.forgetThread(id)
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *appServer) forgetThread(id string) {
	s.mu.Lock()
	delete(s.loadedThreads, id)
	if done := s.threadClosed[id]; done != nil {
		close(done)
		delete(s.threadClosed, id)
	}
	s.mu.Unlock()
}

// prepareCheckpoint resolves the authoritative native id before ensureThread
// considers its in-memory handle. Existing installations without a publication
// retain their native session and acquire their first checkpoint on the next
// completed turn.
func (d *Driver) prepareCheckpoint(ctx context.Context, srv *appServer, fs checkpointFS, input external.PromptInput) (checkpointHandle, error) {
	return d.prepareCheckpointAt(ctx, srv, fs, input, codexHome(input.BotAgentID))
}

// A nil server restores an independent read-only source for thread/fork. It
// never unloads or replaces a source thread that may concurrently be running.
func (d *Driver) prepareCheckpointAt(ctx context.Context, srv *appServer, fs checkpointFS, input external.PromptInput, root string) (checkpointHandle, error) {
	hint := checkpointHandle{NativeID: metadataString(input.RuntimeMetadata, metadataThreadIDKey)}
	if d.stateStore == nil || input.ForceFreshRuntime {
		return hint, nil
	}
	head, found, err := d.stateStore.Head(ctx, input.BotID, input.ThreadID)
	if err != nil {
		return checkpointHandle{}, err
	}
	if !found {
		if input.RuntimeMetadata[metadataCheckpointRequiredKey] == true {
			return checkpointHandle{}, nil
		}
		return hint, nil
	}
	if head.Kind == agentstate.SessionPublicationReset {
		return checkpointHandle{}, nil
	}
	if head.Kind != agentstate.SessionPublicationCheckpoint {
		return checkpointHandle{}, agentstate.ErrSessionStateOutOfSync
	}
	if srv != nil {
		warm := srv.checkpointHandle(input.ThreadID)
		if warm.RunID == head.RunID && srv.threadLoaded(warm.NativeID) {
			if _, err := fs.Stat(ctx, warm.Path); err == nil {
				return warm, nil
			}
		}
	}
	var restored checkpointHandle
	found, err = d.stateStore.Load(ctx, input.BotID, input.ThreadID, func(ctx context.Context, state agentstate.PersistedSessionState, next agentstate.SessionStateRecordReader) error {
		if state.AgentID != RuntimeType || state.AgentSessionID == "" || state.ThroughRunID != head.RunID || state.FileCount != 1 || len(state.Files) != 1 || state.Files[0].Path != state.TranscriptPath {
			return agentstate.ErrSessionStateOutOfSync
		}
		if err := validateRolloutPath(state.TranscriptPath); err != nil {
			return err
		}
		// Even an unchanged file cannot repair a dirty in-memory history. Wait
		// for that handle to close before comparing or replacing its files.
		if srv != nil && srv.threadLoaded(state.AgentSessionID) {
			if err := srv.unloadThread(ctx, state.AgentSessionID); err != nil {
				return fmt.Errorf("unload stale codex thread: %w", err)
			}
		}
		if err := restoreRollouts(ctx, fs, root, state, next); err != nil {
			return err
		}
		meta, err := readRolloutMeta(ctx, fs, root, state.TranscriptPath)
		if err != nil {
			return err
		}
		if meta.ID != state.AgentSessionID {
			return agentstate.ErrSessionStateOutOfSync
		}
		restored = checkpointHandle{RunID: state.ThroughRunID, NativeID: state.AgentSessionID, Path: path.Join(root, state.TranscriptPath)}
		return nil
	})
	if err != nil {
		return checkpointHandle{}, err
	}
	if !found {
		return checkpointHandle{}, agentstate.ErrSessionStateOutOfSync
	}
	return restored, nil
}

// stageCheckpoint waits for the terminal rollout record, not just its earlier
// app-server notification. The second pass is bounded to the captured shape:
// later appends cannot accidentally publish a different native history.
func (d *Driver) stageCheckpoint(ctx context.Context, srv *appServer, input external.PromptInput, nativeID, turnID string) error {
	if d.stateStore == nil || input.ForceFreshRuntime {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkpointTimeout)
	defer cancel()
	var response protocol.ThreadReadResponse
	if err := srv.conn.Call(ctx, protocol.MethodThreadRead, protocol.ThreadReadParams{ThreadID: nativeID}, &response); err != nil {
		return fmt.Errorf("locate codex rollout: %w", err)
	}
	if response.Thread.Path == nil {
		return errors.New("codex thread has no persisted rollout path")
	}
	if err := d.stageRollout(ctx, srv.client, input, nativeID, *response.Thread.Path, turnID); err != nil {
		return err
	}
	srv.rememberCheckpoint(input.ThreadID, checkpointHandle{RunID: input.RunID, NativeID: nativeID, Path: *response.Thread.Path})
	return nil
}

func (d *Driver) stageRollout(ctx context.Context, fs checkpointFS, input external.PromptInput, nativeID, fullPath, turnID string) error {
	if turnID == "" {
		return errors.New("codex checkpoint requires a terminal turn id")
	}
	fence, ok := runtimefence.FromContext(ctx)
	if !ok {
		return errors.New("codex checkpoint requires a runtime persistence fence")
	}
	root := codexHome(input.BotAgentID)
	rel := strings.TrimPrefix(fullPath, root+"/")
	if err := validateRolloutPath(rel); err != nil {
		return err
	}
	canonical, _, err := d.stateStore.CanonicalShape(ctx, input.BotID, input.ThreadID)
	if err != nil {
		return err
	}
	var shape agentstate.PersistedSessionStateFile
	for {
		shape, err = scanRollout(ctx, fs, root, rel, canonical[rel], turnID)
		if err == nil {
			break
		}
		if !errors.Is(err, errRolloutPending) && !errors.Is(err, bridge.ErrNotFound) {
			return err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for codex rollout flush: %w", ctx.Err())
		case <-timer.C:
		}
	}
	meta, err := readRolloutMeta(ctx, fs, root, rel)
	if err != nil {
		return err
	}
	if meta.ID != nativeID {
		return errors.New("codex rollout belongs to a different native thread")
	}
	if meta.HistoryMode == "paginated" || (len(meta.HistoryBase) > 0 && string(meta.HistoryBase) != "null") {
		return errors.New("codex checkpoint requires a self-contained legacy rollout")
	}
	state := agentstate.PersistedSessionState{
		AgentID: RuntimeType, AgentSessionID: nativeID, ThroughRunID: input.RunID,
		Cwd:            firstNonEmpty(metadataString(input.RuntimeMetadata, "project_path"), defaultProjectPath),
		TranscriptPath: rel, RuntimeFencingToken: fence.Token,
		FileCount: 1, RecordCount: shape.Records, Files: []agentstate.PersistedSessionStateFile{shape},
	}
	reader := &rolloutReader{fs: fs, root: root, files: state.Files}
	defer reader.close()
	if err := d.stateStore.Replace(ctx, input.BotID, input.ThreadID, state, reader.next); err != nil {
		return err
	}
	return nil
}

type rolloutMeta struct {
	ID          string          `json:"id"`
	HistoryMode string          `json:"history_mode"`
	HistoryBase json.RawMessage `json:"history_base"`
}

func readRolloutMeta(ctx context.Context, fs checkpointFS, root, rel string) (rolloutMeta, error) {
	r, err := fs.ReadRawNoFollow(ctx, root, rel)
	if err != nil {
		return rolloutMeta{}, err
	}
	defer func() { _ = r.Close() }()
	raw, err := nextRolloutRecord(rolloutScanner(r))
	if err != nil {
		return rolloutMeta{}, err
	}
	var envelope struct {
		Type    string      `json:"type"`
		Payload rolloutMeta `json:"payload"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return rolloutMeta{}, err
	}
	if envelope.Type != "session_meta" {
		return rolloutMeta{}, errors.New("codex rollout has no session metadata")
	}
	return envelope.Payload, nil
}

func checkpointError(err error) error {
	return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
}

func validateRolloutPath(rel string) error {
	// state/sessions is the root used by the original ACP checkpoints.
	allowedRoot := strings.HasPrefix(rel, "sessions/") || strings.HasPrefix(rel, "archived_sessions/") || strings.HasPrefix(rel, "state/sessions/")
	if path.Clean(rel) != rel || strings.ContainsAny(rel, "\\\x00\r\n") ||
		!allowedRoot ||
		!strings.HasSuffix(rel, ".jsonl") {
		return fmt.Errorf("invalid codex checkpoint path %q", rel)
	}
	return nil
}

var errRolloutPending = errors.New("codex terminal rollout record is not flushed")

func rolloutScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 8*1024*1024)
	return s
}

func nextRolloutRecord(s *bufio.Scanner) ([]byte, error) {
	for s.Scan() {
		raw := bytes.TrimSpace(s.Bytes())
		if len(raw) == 0 {
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return nil, fmt.Errorf("invalid codex rollout record: %w", err)
		}
		return compact.Bytes(), nil
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func scanRollout(ctx context.Context, fs checkpointFS, root, rel string, canonical agentstate.SessionStateFileShape, terminalID string) (agentstate.PersistedSessionStateFile, error) {
	shape := agentstate.PersistedSessionStateFile{SessionStateFileShape: agentstate.SessionStateFileShape{Path: rel}}
	if err := validateRolloutPath(rel); err != nil {
		return shape, err
	}
	r, err := fs.ReadRawNoFollow(ctx, root, rel)
	if err != nil {
		return shape, err
	}
	defer func() { _ = r.Close() }()
	scanner := rolloutScanner(io.LimitReader(r, 512*1024*1024+1))
	digest := sha256.New()
	complete := terminalID == ""
	for {
		if err := ctx.Err(); err != nil {
			return shape, err
		}
		raw, err := nextRolloutRecord(scanner)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if !complete {
				return shape, errRolloutPending
			}
			return shape, err
		}
		digest.Write(raw)
		digest.Write([]byte{'\n'})
		shape.Records++
		if shape.Records > 2_000_000 {
			return shape, errors.New("codex rollout exceeds checkpoint record limit")
		}
		if shape.Records == canonical.Records {
			shape.PrefixRecords = canonical.Records
			shape.PrefixDigest = hex.EncodeToString(digest.Sum(nil))
		}
		if terminalID != "" {
			var record struct {
				Type    string `json:"type"`
				Payload struct {
					Type   string `json:"type"`
					TurnID string `json:"turn_id"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(raw, &record); err != nil {
				return shape, err
			}
			if record.Type == "event_msg" && record.Payload.TurnID == terminalID &&
				(record.Payload.Type == "task_complete" || record.Payload.Type == "turn_complete") {
				complete = true
				break
			}
		}
	}
	if !complete {
		return shape, errRolloutPending
	}
	shape.Digest = hex.EncodeToString(digest.Sum(nil))
	return shape, nil
}

type rolloutReader struct {
	fs      checkpointFS
	root    string
	files   []agentstate.PersistedSessionStateFile
	index   int
	line    int64
	r       io.ReadCloser
	scanner *bufio.Scanner
}

func (r *rolloutReader) close() {
	if r.r != nil {
		_ = r.r.Close()
		r.r = nil
	}
}

func (r *rolloutReader) next(ctx context.Context) (agentstate.SessionStateRecord, error) {
	for r.index < len(r.files) {
		file := r.files[r.index]
		if r.line == file.Records {
			r.close()
			r.index++
			r.line = 0
			continue
		}
		if r.r == nil {
			var err error
			r.r, err = r.fs.ReadRawNoFollow(ctx, r.root, file.Path)
			if err != nil {
				return agentstate.SessionStateRecord{}, err
			}
			r.scanner = rolloutScanner(r.r)
		}
		raw, err := nextRolloutRecord(r.scanner)
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			return agentstate.SessionStateRecord{}, err
		}
		r.line++
		return agentstate.SessionStateRecord{FilePath: file.Path, LineNumber: r.line, Content: raw}, nil
	}
	return agentstate.SessionStateRecord{}, io.EOF
}

// Compare local files before writing, but consume every database record even
// for cache hits so Load can validate the committed shapes and digests.
func restoreRollouts(ctx context.Context, fs checkpointFS, root string, state agentstate.PersistedSessionState, next agentstate.SessionStateRecordReader) error {
	files := append([]agentstate.PersistedSessionStateFile(nil), state.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, file := range files {
		if err := validateRolloutPath(file.Path); err != nil {
			return err
		}
		local, err := scanRollout(ctx, fs, root, file.Path, agentstate.SessionStateFileShape{}, "")
		cached := err == nil && local.Records == file.Records && local.Digest == file.Digest
		var writer *io.PipeWriter
		var done chan error
		if !cached {
			full := path.Join(root, file.Path)
			if err := fs.Mkdir(ctx, path.Dir(full)); err != nil {
				return err
			}
			pr, pw := io.Pipe()
			writer = pw
			done = make(chan error, 1)
			go func() {
				_, err := fs.WriteRaw(ctx, full, pr)
				_ = pr.CloseWithError(err)
				done <- err
			}()
		}
		consumeErr := func() error {
			for line := int64(1); line <= file.Records; line++ {
				record, err := next(ctx)
				if err != nil {
					return err
				}
				if record.FilePath != file.Path || record.LineNumber != line {
					return agentstate.ErrSessionStateOutOfSync
				}
				if writer != nil {
					if _, err := writer.Write(append(record.Content, '\n')); err != nil {
						return err
					}
				}
			}
			return nil
		}()
		if writer != nil {
			_ = writer.CloseWithError(consumeErr)
			consumeErr = errors.Join(consumeErr, <-done)
		}
		if consumeErr != nil {
			return consumeErr
		}
	}
	_, err := next(ctx)
	if !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return agentstate.ErrSessionStateOutOfSync
	}
	return nil
}
