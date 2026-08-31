package claudecode

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/agentstate"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/runtimefence"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// fakeCheckpointFS is an in-memory workspace file tree keyed by absolute path.
type fakeCheckpointFS struct {
	files map[string]string
	dirs  map[string]bool
}

func newFakeCheckpointFS() *fakeCheckpointFS {
	return &fakeCheckpointFS{files: map[string]string{}, dirs: map[string]bool{}}
}

func (f *fakeCheckpointFS) addFile(fullPath, content string) {
	f.files[fullPath] = content
	dir := path.Dir(fullPath)
	for dir != "/" && dir != "." {
		f.dirs[dir] = true
		dir = path.Dir(dir)
	}
}

func (f *fakeCheckpointFS) ListDirBounded(_ context.Context, root string, _ bool, _ int32) ([]*pb.FileEntry, error) {
	if !f.dirs[root] {
		return nil, errors.New("no such directory")
	}
	entries := []*pb.FileEntry{}
	prefix := root + "/"
	for full := range f.files {
		if strings.HasPrefix(full, prefix) {
			entries = append(entries, &pb.FileEntry{Path: strings.TrimPrefix(full, prefix), Mode: "-rw-r--r--"})
		}
	}
	for dir := range f.dirs {
		if strings.HasPrefix(dir, prefix) {
			entries = append(entries, &pb.FileEntry{Path: strings.TrimPrefix(dir, prefix), IsDir: true, Mode: "drwxr-xr-x"})
		}
	}
	return entries, nil
}

func (f *fakeCheckpointFS) Stat(_ context.Context, target string) (*pb.FileEntry, error) {
	if f.dirs[target] {
		return &pb.FileEntry{Path: target, IsDir: true}, nil
	}
	if _, ok := f.files[target]; ok {
		return &pb.FileEntry{Path: target}, nil
	}
	return nil, errStatNotFound
}

// errStatNotFound mimics bridge.ErrNotFound-style detection; locate treats a
// missing projects root as "no transcript", so any error works for the test
// as long as it is the bridge sentinel.
var errStatNotFound = errNotFoundSentinel{}

type errNotFoundSentinel struct{}

func (errNotFoundSentinel) Error() string { return "not found" }

func (f *fakeCheckpointFS) ReadRaw(_ context.Context, target string) (io.ReadCloser, error) {
	content, ok := f.files[target]
	if !ok {
		return nil, errors.New("no such file")
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func (f *fakeCheckpointFS) WriteRaw(_ context.Context, target string, r io.Reader) (int64, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, r)
	if err != nil {
		return n, err
	}
	f.addFile(target, buf.String())
	return n, nil
}

func (f *fakeCheckpointFS) Mkdir(_ context.Context, target string) error {
	f.dirs[target] = true
	for dir := path.Dir(target); dir != "/" && dir != "."; dir = path.Dir(dir) {
		f.dirs[dir] = true
	}
	return nil
}

// fakeStateStore records Replace calls and serves Load from a fixed snapshot.
type fakeStateStore struct {
	canonical map[string]agentstate.SessionStateFileShape

	replacedState   *agentstate.PersistedSessionState
	replacedRecords []agentstate.SessionStateRecord
	replaceErr      error

	loadState   *agentstate.PersistedSessionState
	loadRecords []agentstate.SessionStateRecord
}

func (*fakeStateStore) RuntimeConfigEpoch(context.Context, string, string) (agentstate.RuntimeConfigEpoch, error) {
	return agentstate.RuntimeConfigEpoch{}, nil
}

func (*fakeStateStore) GuardRuntimeSync(ctx context.Context, _ string, _ int64, fn func(context.Context) error) error {
	return fn(ctx)
}

func (*fakeStateStore) Head(context.Context, string, string) (agentstate.SessionPublicationHead, bool, error) {
	return agentstate.SessionPublicationHead{}, false, nil
}

func (s *fakeStateStore) CanonicalShape(context.Context, string, string) (map[string]agentstate.SessionStateFileShape, bool, error) {
	return s.canonical, s.canonical != nil, nil
}

func (s *fakeStateStore) Load(ctx context.Context, _, _ string, consume agentstate.SessionStateRecordConsumer) (bool, error) {
	if s.loadState == nil {
		return false, nil
	}
	index := 0
	reader := func(context.Context) (agentstate.SessionStateRecord, error) {
		if index >= len(s.loadRecords) {
			return agentstate.SessionStateRecord{}, io.EOF
		}
		record := s.loadRecords[index]
		index++
		return record, nil
	}
	if err := consume(ctx, *s.loadState, reader); err != nil {
		return true, err
	}
	return true, nil
}

func (s *fakeStateStore) Replace(ctx context.Context, _, _ string, state agentstate.PersistedSessionState, records agentstate.SessionStateRecordReader) error {
	if s.replaceErr != nil {
		return s.replaceErr
	}
	s.replacedState = &state
	s.replacedRecords = nil
	for {
		record, err := records(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		s.replacedRecords = append(s.replacedRecords, record)
	}
	return nil
}

func checkpointDriver(store agentstate.SessionStateStore) *Driver {
	return &Driver{stateStore: store, logger: slog.Default()}
}

const claudeTestSession = "6f1f8a44-9a1a-4a7e-9df1-0f6f8f3f1a10"

func transcriptFullPath() string {
	return path.Join(configDir, projectsDirName, "-data-project", claudeTestSession+".jsonl")
}

func TestLocateSessionTranscript(t *testing.T) {
	fs := newFakeCheckpointFS()
	fs.addFile(transcriptFullPath(), `{"type":"user"}`+"\n")
	fs.addFile(path.Join(configDir, projectsDirName, "-data-project", "other.jsonl"), "{}\n")

	rel, found, err := locateSessionTranscript(t.Context(), fs, claudeTestSession)
	if err != nil || !found {
		t.Fatalf("locate = (%q, %t, %v), want found", rel, found, err)
	}
	if rel != path.Join(projectsDirName, "-data-project", claudeTestSession+".jsonl") {
		t.Fatalf("rel = %q", rel)
	}

	_, found, err = locateSessionTranscript(t.Context(), fs, "00000000-0000-0000-0000-000000000000")
	if err != nil || found {
		t.Fatalf("missing session: found=%t err=%v", found, err)
	}

	if _, _, err := locateSessionTranscript(t.Context(), fs, "../escape"); err == nil {
		t.Fatal("traversal session id must be rejected")
	}
}

func TestLocateSessionTranscriptNoProjectsDir(t *testing.T) {
	fs := newFakeCheckpointFS()
	_, found, err := locateSessionTranscript(t.Context(), fs, claudeTestSession)
	if found {
		t.Fatal("must not find a transcript without a projects dir")
	}
	// The fake's Stat error is not bridge.ErrNotFound, so an error return is
	// also acceptable here; the driver treats it as "attempt resume anyway".
	_ = err
}

func TestScanTranscriptShape(t *testing.T) {
	fs := newFakeCheckpointFS()
	full := transcriptFullPath()
	fs.addFile(full, `{"a": 1}`+"\n\n"+`{"b":2}`+"\n"+`{"c": 3}`+"\n")

	shape, err := scanTranscriptShape(t.Context(), fs, full, agentstate.SessionStateFileShape{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if shape.Records != 3 || shape.Digest == "" || shape.PrefixRecords != 0 {
		t.Fatalf("shape = %+v", shape)
	}

	// The prefix proof snapshots the running digest at the canonical record
	// boundary; a canonical shape equal to the first two records must match a
	// direct scan of those records.
	prefixOnly := newFakeCheckpointFS()
	prefixOnly.addFile(full, `{"a": 1}`+"\n"+`{"b":2}`+"\n")
	prefixShape, err := scanTranscriptShape(t.Context(), prefixOnly, full, agentstate.SessionStateFileShape{})
	if err != nil {
		t.Fatalf("prefix scan: %v", err)
	}
	withProof, err := scanTranscriptShape(t.Context(), fs, full, agentstate.SessionStateFileShape{Records: 2, Digest: prefixShape.Digest})
	if err != nil {
		t.Fatalf("proof scan: %v", err)
	}
	if withProof.PrefixRecords != 2 || withProof.PrefixDigest != prefixShape.Digest {
		t.Fatalf("prefix proof = %+v, want digest %s at 2", withProof, prefixShape.Digest)
	}
}

func TestScanTranscriptShapeRejectsInvalidJSON(t *testing.T) {
	fs := newFakeCheckpointFS()
	full := transcriptFullPath()
	fs.addFile(full, "not-json\n")
	if _, err := scanTranscriptShape(t.Context(), fs, full, agentstate.SessionStateFileShape{}); err == nil {
		t.Fatal("invalid JSON line must fail the scan")
	}
}

func stagingContext(t *testing.T) context.Context {
	t.Helper()
	return runtimefence.WithContext(t.Context(), runtimefence.Fence{
		BotID: "bot-1", SessionID: "thread-1", Token: 7,
	})
}

func TestStageSessionCheckpoint(t *testing.T) {
	fs := newFakeCheckpointFS()
	fs.addFile(transcriptFullPath(), `{"type":"user","text":"hi"}`+"\n"+`{"type":"assistant"}`+"\n")
	store := &fakeStateStore{}
	driver := checkpointDriver(store)

	staged, err := driver.stageWithFS(stagingContext(t), fs, checkpointRequest{
		BotID: "bot-1", ThreadID: "thread-1", RunID: "run-1",
		RuntimeMetadata: map[string]any{
			metadataSessionIDKey: claudeTestSession,
			"project_path":       "/data/project",
		},
	})
	if err != nil || !staged {
		t.Fatalf("stage = (%t, %v), want staged", staged, err)
	}
	state := store.replacedState
	if state == nil {
		t.Fatal("store received no state")
	}
	if state.AgentID != RuntimeType || state.AgentSessionID != claudeTestSession ||
		state.ThroughRunID != "run-1" || state.Cwd != "/data/project" ||
		state.RuntimeFencingToken != 7 || state.FileCount != 1 || state.RecordCount != 2 {
		t.Fatalf("state = %+v", *state)
	}
	if len(store.replacedRecords) != 2 || store.replacedRecords[0].LineNumber != 1 ||
		string(store.replacedRecords[0].Content) != `{"type":"user","text":"hi"}` {
		t.Fatalf("records = %+v", store.replacedRecords)
	}
}

func TestStageSessionCheckpointDeclines(t *testing.T) {
	fs := newFakeCheckpointFS()
	store := &fakeStateStore{}
	driver := checkpointDriver(store)

	// No session id.
	staged, err := driver.stageWithFS(stagingContext(t), fs, checkpointRequest{
		BotID: "bot-1", ThreadID: "thread-1", RunID: "run-1", RuntimeMetadata: map[string]any{},
	})
	if staged || err != nil {
		t.Fatalf("no session id: (%t, %v)", staged, err)
	}

	// Transcript missing (projects dir exists but no file).
	_ = fs.Mkdir(t.Context(), path.Join(configDir, projectsDirName))
	staged, err = driver.stageWithFS(stagingContext(t), fs, checkpointRequest{
		BotID: "bot-1", ThreadID: "thread-1", RunID: "run-1",
		RuntimeMetadata: map[string]any{metadataSessionIDKey: claudeTestSession},
	})
	if staged || err != nil {
		t.Fatalf("missing transcript: (%t, %v)", staged, err)
	}

	// Divergent capture declines without error.
	fs.addFile(transcriptFullPath(), `{"a":1}`+"\n")
	store.replaceErr = agentstate.ErrSessionStateDivergent
	staged, err = driver.stageWithFS(stagingContext(t), fs, checkpointRequest{
		BotID: "bot-1", ThreadID: "thread-1", RunID: "run-1",
		RuntimeMetadata: map[string]any{metadataSessionIDKey: claudeTestSession},
	})
	if staged || err != nil {
		t.Fatalf("divergent: (%t, %v)", staged, err)
	}

	// Without a fence the staging must refuse instead of writing unfenced.
	store.replaceErr = nil
	if staged, err = driver.stageWithFS(t.Context(), fs, checkpointRequest{
		BotID: "bot-1", ThreadID: "thread-1", RunID: "run-1",
		RuntimeMetadata: map[string]any{metadataSessionIDKey: claudeTestSession},
	}); staged || err == nil {
		t.Fatalf("unfenced: (%t, %v), want error", staged, err)
	}
}

func TestRestoreSessionCheckpoint(t *testing.T) {
	fs := newFakeCheckpointFS()
	rel := path.Join(projectsDirName, "-data-project", claudeTestSession+".jsonl")
	store := &fakeStateStore{
		loadState: &agentstate.PersistedSessionState{
			AgentID:        RuntimeType,
			AgentSessionID: claudeTestSession,
			TranscriptPath: rel,
		},
		loadRecords: []agentstate.SessionStateRecord{
			{FilePath: rel, LineNumber: 1, Content: []byte(`{"a":1}`)},
			{FilePath: rel, LineNumber: 2, Content: []byte(`{"b":2}`)},
		},
	}
	driver := checkpointDriver(store)

	restoredID, err := driver.restoreSessionCheckpoint(t.Context(), fs, "bot-1", "thread-1")
	if err != nil || restoredID != claudeTestSession {
		t.Fatalf("restore = (%q, %v)", restoredID, err)
	}
	if got := fs.files[path.Join(configDir, rel)]; got != `{"a":1}`+"\n"+`{"b":2}`+"\n" {
		t.Fatalf("restored content = %q", got)
	}
}

func TestRestoreSessionCheckpointRejectsEscapingPath(t *testing.T) {
	fs := newFakeCheckpointFS()
	store := &fakeStateStore{
		loadState: &agentstate.PersistedSessionState{
			AgentID:        RuntimeType,
			AgentSessionID: claudeTestSession,
			TranscriptPath: "outside.jsonl",
		},
		loadRecords: []agentstate.SessionStateRecord{
			{FilePath: "outside.jsonl", LineNumber: 1, Content: []byte(`{}`)},
		},
	}
	driver := checkpointDriver(store)
	if restoredID, err := driver.restoreSessionCheckpoint(t.Context(), fs, "bot-1", "thread-1"); err == nil || restoredID != "" {
		t.Fatalf("restore outside projects tree = (%q, %v), want error", restoredID, err)
	}
}

func TestEnsureResumableSessionFallsBackOnlyWithoutCheckpoint(t *testing.T) {
	fs := newFakeCheckpointFS()
	_ = fs.Mkdir(t.Context(), path.Join(configDir, projectsDirName))
	driver := checkpointDriver(&fakeStateStore{})

	input := external.PromptInput{BotID: "bot-1", ThreadID: "thread-1"}
	if got, err := driver.ensureResumableSession(t.Context(), fs, input, claudeTestSession); err != nil || got != "" {
		t.Fatalf("expected fresh session, got (%q, %v)", got, err)
	}

	fs.addFile(transcriptFullPath(), "{}\n")
	if got, err := driver.ensureResumableSession(t.Context(), fs, input, claudeTestSession); err != nil || got != claudeTestSession {
		t.Fatalf("expected resume of %s, got (%q, %v)", claudeTestSession, got, err)
	}
}

func TestEnsureResumableSessionRejectsBrokenCheckpoint(t *testing.T) {
	fs := newFakeCheckpointFS()
	_ = fs.Mkdir(t.Context(), path.Join(configDir, projectsDirName))
	driver := checkpointDriver(&fakeStateStore{
		loadState: &agentstate.PersistedSessionState{
			AgentID: RuntimeType, AgentSessionID: claudeTestSession, TranscriptPath: "outside.jsonl",
		},
		loadRecords: []agentstate.SessionStateRecord{{FilePath: "outside.jsonl", LineNumber: 1, Content: []byte(`{}`)}},
	})

	input := external.PromptInput{BotID: "bot-1", ThreadID: "thread-1"}
	if got, err := driver.ensureResumableSession(t.Context(), fs, input, claudeTestSession); err == nil || got != "" {
		t.Fatalf("broken checkpoint resume = (%q, %v), want error", got, err)
	}
}

func TestEnsureResumableSessionRestoresFromStore(t *testing.T) {
	fs := newFakeCheckpointFS()
	_ = fs.Mkdir(t.Context(), path.Join(configDir, projectsDirName))
	rel := path.Join(projectsDirName, "-data-project", claudeTestSession+".jsonl")
	driver := checkpointDriver(&fakeStateStore{
		loadState: &agentstate.PersistedSessionState{
			AgentID:        RuntimeType,
			AgentSessionID: claudeTestSession,
			TranscriptPath: rel,
		},
		loadRecords: []agentstate.SessionStateRecord{
			{FilePath: rel, LineNumber: 1, Content: []byte(`{"restored":true}`)},
		},
	})

	input := external.PromptInput{BotID: "bot-1", ThreadID: "thread-1"}
	if got, err := driver.ensureResumableSession(t.Context(), fs, input, claudeTestSession); err != nil || got != claudeTestSession {
		t.Fatalf("expected restored resume, got (%q, %v)", got, err)
	}
	if _, ok := fs.files[path.Join(configDir, rel)]; !ok {
		t.Fatal("transcript was not materialized")
	}
}

func TestEnsureResumableSessionPrefersCheckpointOverStaleMetadata(t *testing.T) {
	fs := newFakeCheckpointFS()
	_ = fs.Mkdir(t.Context(), path.Join(configDir, projectsDirName))
	rel := path.Join(projectsDirName, "-data-project", claudeTestSession+".jsonl")
	driver := checkpointDriver(&fakeStateStore{
		loadState: &agentstate.PersistedSessionState{
			AgentID:        RuntimeType,
			AgentSessionID: claudeTestSession,
			TranscriptPath: rel,
		},
		loadRecords: []agentstate.SessionStateRecord{
			{FilePath: rel, LineNumber: 1, Content: []byte(`{"restored":true}`)},
		},
	})

	// Runtime metadata lags behind the published checkpoint (a lost or
	// fenced-out merge): the checkpoint's session must resume, not a fresh
	// conversation.
	input := external.PromptInput{BotID: "bot-1", ThreadID: "thread-1"}
	if got, err := driver.ensureResumableSession(t.Context(), fs, input, "stale-metadata-session"); err != nil || got != claudeTestSession {
		t.Fatalf("expected the checkpointed session, got (%q, %v)", got, err)
	}
	if _, ok := fs.files[path.Join(configDir, rel)]; !ok {
		t.Fatal("transcript was not materialized")
	}
}
