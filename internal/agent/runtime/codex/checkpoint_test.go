package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/agentstate"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const (
	testRollout  = "sessions/2026/09/14/rollout-2026-09-14-native.jsonl"
	testMetadata = `{"type":"session_meta","payload":{"id":"native"}}` + "\n"
)

func completedRollout(id, text string) string {
	return fmt.Sprintf("{\"type\":\"response_item\",\"payload\":{\"text\":%q}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_complete\",\"turn_id\":%q}}\n", text, id)
}

type checkpointFiles struct {
	files  map[string]string
	writes int
}

func (f *checkpointFiles) Stat(_ context.Context, full string) (*pb.FileEntry, error) {
	if _, ok := f.files[full]; !ok {
		return nil, bridge.ErrNotFound
	}
	return &pb.FileEntry{Path: full}, nil
}

func (f *checkpointFiles) ReadRawNoFollow(_ context.Context, root, rel string) (io.ReadCloser, error) {
	content, ok := f.files[path.Join(root, rel)]
	if !ok {
		return nil, bridge.ErrNotFound
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func (f *checkpointFiles) WriteRaw(_ context.Context, full string, r io.Reader) (int64, error) {
	data, err := io.ReadAll(r)
	if err == nil {
		f.files[full] = string(data)
		f.writes++
	}
	return int64(len(data)), err
}

func (*checkpointFiles) Mkdir(context.Context, string) error { return nil }

// Staging and publishing remain distinct, just as they are in PostgreSQL.
type checkpointStore struct {
	agentstate.SessionStateStore
	head                   agentstate.SessionPublicationHead
	state                  agentstate.PersistedSessionState
	staged                 agentstate.PersistedSessionState
	records, stagedRecords []agentstate.SessionStateRecord
	replaceErr             error
}

func (s *checkpointStore) Head(context.Context, string, string) (agentstate.SessionPublicationHead, bool, error) {
	return s.head, s.head.RunID != "", nil
}

func (s *checkpointStore) CanonicalShape(context.Context, string, string) (map[string]agentstate.SessionStateFileShape, bool, error) {
	shapes := make(map[string]agentstate.SessionStateFileShape)
	for _, f := range s.state.Files {
		shapes[f.Path] = f.SessionStateFileShape
	}
	return shapes, len(shapes) > 0, nil
}

func recordsReader(records []agentstate.SessionStateRecord) agentstate.SessionStateRecordReader {
	index := 0
	return func(context.Context) (agentstate.SessionStateRecord, error) {
		if index == len(records) {
			return agentstate.SessionStateRecord{}, io.EOF
		}
		r := records[index]
		index++
		return r, nil
	}
}

func (s *checkpointStore) Load(ctx context.Context, _, _ string, consume agentstate.SessionStateRecordConsumer) (bool, error) {
	if s.state.ThroughRunID == "" {
		return false, nil
	}
	return true, consume(ctx, s.state, recordsReader(s.records))
}

func (s *checkpointStore) Replace(ctx context.Context, _, _ string, state agentstate.PersistedSessionState, next agentstate.SessionStateRecordReader) error {
	if s.replaceErr != nil {
		return s.replaceErr
	}
	var records []agentstate.SessionStateRecord
	for {
		r, err := next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		records = append(records, r)
	}
	s.staged, s.stagedRecords = state, records
	return nil
}

func (s *checkpointStore) publish() {
	s.state, s.records = s.staged, s.stagedRecords
	s.head = agentstate.SessionPublicationHead{RunID: s.state.ThroughRunID, Kind: agentstate.SessionPublicationCheckpoint}
}

func checkpointFixture(t *testing.T) (*Driver, *checkpointFiles, *checkpointStore, external.PromptInput, context.Context) {
	t.Helper()
	store := &checkpointStore{}
	d := &Driver{stateStore: store, logger: slog.Default()}
	fs := &checkpointFiles{files: map[string]string{}}
	input := external.PromptInput{BotID: "bot", BotAgentID: "agent", ThreadID: "session", RunID: "run-1"}
	ctx := runtimefence.WithContext(t.Context(), runtimefence.Fence{BotID: input.BotID, SessionID: input.ThreadID, Token: 7})
	return d, fs, store, input, ctx
}

func TestCheckpointStagesNativeRecordsWithCanonicalPrefix(t *testing.T) {
	d, fs, store, input, ctx := checkpointFixture(t)
	full := path.Join(codexHome(input.BotAgentID), testRollout)
	fs.files[full] = testMetadata + completedRollout("turn-1", "first")
	if err := d.stageRollout(ctx, fs, input, "native", full, "turn-1"); err != nil {
		t.Fatal(err)
	}
	if store.head.RunID != "" || store.staged.RuntimeFencingToken != 7 || store.staged.RecordCount != 3 {
		t.Fatalf("staged: %+v", store.staged)
	}
	store.publish()
	base := store.state.Files[0]
	input.RunID = "run-2"
	fs.files[full] += completedRollout("turn-2", "second") + `{"type":"event_msg","payload":{"type":"uncommitted"}}` + "\n"
	if err := d.stageRollout(ctx, fs, input, "native", full, "turn-2"); err != nil {
		t.Fatal(err)
	}
	file := store.staged.Files[0]
	if file.Records != 5 || file.PrefixRecords != base.Records || file.PrefixDigest != base.Digest || len(store.stagedRecords) != 5 {
		t.Fatalf("capture did not preserve the canonical prefix and terminal boundary: %+v", file)
	}
	if store.head.RunID != "run-1" {
		t.Fatal("staging advanced publication")
	}
}

func TestCheckpointRestoreUsesPublishedVersionAndRepairsCache(t *testing.T) {
	for _, local := range []string{"missing", "matching", "ahead", "corrupt"} {
		t.Run(local, func(t *testing.T) {
			d, fs, store, input, ctx := checkpointFixture(t)
			full := path.Join(codexHome(input.BotAgentID), testRollout)
			committed := testMetadata + completedRollout("turn-1", "remember orchid")
			fs.files[full] = committed
			if err := d.stageRollout(ctx, fs, input, "native", full, "turn-1"); err != nil {
				t.Fatal(err)
			}
			store.publish()
			fs.files[full] += completedRollout("turn-2", "ghost")
			input.RunID = "unpublished"
			if err := d.stageRollout(ctx, fs, input, "native", full, "turn-2"); err != nil {
				t.Fatal(err)
			}
			switch local {
			case "missing":
				delete(fs.files, full)
			case "matching":
				fs.files[full] = committed
			case "corrupt":
				fs.files[full] = "broken\n"
			}
			input.RuntimeMetadata = map[string]any{metadataThreadIDKey: "stale-metadata"}
			h, err := d.prepareCheckpoint(ctx, &appServer{}, fs, input)
			if err != nil {
				t.Fatal(err)
			}
			if h.NativeID != "native" || h.RunID != "run-1" || h.Path != full || fs.files[full] != committed {
				t.Fatalf("restored %+v: %q", h, fs.files[full])
			}
			if (fs.writes == 0) != (local == "matching") {
				t.Fatalf("cache %s caused %d writes", local, fs.writes)
			}
		})
	}
}

func TestCheckpointRejectsMissingCommittedStateAndUnsafePaths(t *testing.T) {
	d, fs, store, input, ctx := checkpointFixture(t)
	store.head = agentstate.SessionPublicationHead{RunID: "committed", Kind: agentstate.SessionPublicationCheckpoint}
	if _, err := d.prepareCheckpoint(ctx, &appServer{}, fs, input); !errors.Is(err, agentstate.ErrSessionStateOutOfSync) {
		t.Fatalf("missing checkpoint: %v", err)
	}
	for _, rel := range []string{"../auth.jsonl", "sessions/../../auth.jsonl", "/sessions/rollout.jsonl", "sessions/./rollout.jsonl", "sessions/rollout.json", "sessions/rollout\\bad.jsonl"} {
		if validateRolloutPath(rel) == nil {
			t.Errorf("accepted %q", rel)
		}
	}
}

func TestForkCheckpointLeavesActiveSourceUntouched(t *testing.T) {
	d, fs, store, input, ctx := checkpointFixture(t)
	full := path.Join(codexHome(input.BotAgentID), testRollout)
	committed := testMetadata + completedRollout("turn-1", "first")
	fs.files[full] = committed
	if err := d.stageRollout(ctx, fs, input, "native", full, "turn-1"); err != nil {
		t.Fatal(err)
	}
	store.publish()
	active := committed + completedRollout("unpublished", "concurrent source turn")
	fs.files[full] = active
	root := path.Join(codexHome(input.BotAgentID), "tmp", "fork-copy")
	h, err := d.prepareCheckpointAt(ctx, nil, fs, input, root)
	if err != nil {
		t.Fatal(err)
	}
	if fs.files[full] != active || h.Path != path.Join(root, testRollout) || fs.files[h.Path] != committed {
		t.Fatalf("fork modified its source or copied uncommitted history: %+v", h)
	}
}

func TestCheckpointDistinguishesLegacyAndUncommittedNewThreads(t *testing.T) {
	d, fs, store, input, ctx := checkpointFixture(t)
	input.RuntimeMetadata = map[string]any{metadataThreadIDKey: "local-only"}
	h, err := d.prepareCheckpoint(ctx, &appServer{}, fs, input)
	if err != nil || h.NativeID != "local-only" {
		t.Fatalf("pre-upgrade thread must remain resumable: %+v, %v", h, err)
	}
	input.RuntimeMetadata[metadataCheckpointRequiredKey] = true
	h, err = d.prepareCheckpoint(ctx, &appServer{}, fs, input)
	if err != nil || h.NativeID != "" {
		t.Fatalf("uncommitted new thread must not be resumed: %+v, %v", h, err)
	}
	store.head = agentstate.SessionPublicationHead{RunID: "reset", Kind: agentstate.SessionPublicationReset}
	input.RuntimeMetadata[metadataCheckpointRequiredKey] = false
	h, err = d.prepareCheckpoint(ctx, &appServer{}, fs, input)
	if err != nil || h.NativeID != "" {
		t.Fatalf("reset must discard the native hint: %+v, %v", h, err)
	}
	if err := validateRolloutPath("state/sessions/2025/01/01/rollout-native.jsonl"); err != nil {
		t.Fatalf("original ACP checkpoint root: %v", err)
	}
}

func TestCheckpointRequiresThisTurnsTerminalRecord(t *testing.T) {
	d, fs, store, input, ctx := checkpointFixture(t)
	full := path.Join(codexHome(input.BotAgentID), testRollout)
	fs.files[full] = testMetadata + completedRollout("previous", "old reply")
	ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := d.stageRollout(ctx, fs, input, "native", full, "current"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stale rollout: %v", err)
	}
	if store.staged.ThroughRunID != "" {
		t.Fatal("stale rollout staged")
	}
}

func TestCheckpointReaderCannotIncludeLaterAppends(t *testing.T) {
	_, fs, _, input, ctx := checkpointFixture(t)
	root := codexHome(input.BotAgentID)
	full := path.Join(root, testRollout)
	fs.files[full] = testMetadata + completedRollout("turn-1", "reply")
	shape, err := scanRollout(ctx, fs, root, testRollout, agentstate.SessionStateFileShape{}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	fs.files[full] += completedRollout("turn-2", "late")
	r := &rolloutReader{fs: fs, root: root, files: []agentstate.PersistedSessionStateFile{shape}}
	defer r.close()
	var count int64
	for {
		_, err := r.next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("read %d records", count)
	}
}

func TestUnsubscribeAcknowledgementDoesNotAuthorizeRestore(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	srv := &appServer{logger: slog.Default(), loadedThreads: map[string]bool{"native": true}}
	srv.conn = newConn(client, srv, slog.Default())
	defer func() { _ = srv.conn.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.unloadThread(ctx, "native") }()
	scanner := bufio.NewScanner(server)
	if !scanner.Scan() {
		t.Fatal("missing unsubscribe")
	}
	var req struct {
		ID     json.RawMessage
		Method string
	}
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		t.Fatal(err)
	}
	if req.Method != "thread/unsubscribe" {
		t.Fatalf("method %s", req.Method)
	}
	if _, err := fmt.Fprintf(server, "{\"id\":%s,\"result\":{\"status\":\"unsubscribed\"}}\n", req.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("returned before thread/closed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := fmt.Fprintln(server, `{"method":"thread/closed","params":{"threadId":"native"}}`); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if srv.threadLoaded("native") {
		t.Fatal("closed thread remains loaded")
	}
}
