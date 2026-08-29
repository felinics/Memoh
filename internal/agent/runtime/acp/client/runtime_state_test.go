package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestRuntimeLeaseSessionStateRoundTripUsesOnlyDeclaredRoots(t *testing.T) {
	cleanupRoot := t.TempDir()
	now := time.Now()
	staleSpool := filepath.Join(cleanupRoot, runtimeSessionSpoolPrefix+"stale")
	freshSpool := filepath.Join(cleanupRoot, runtimeSessionSpoolPrefix+"fresh")
	unrelatedOldDir := filepath.Join(cleanupRoot, "unrelated-old")
	for _, dir := range []string{staleSpool, freshSpool, unrelatedOldDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-runtimeSessionSpoolStaleAge - time.Hour)
	for _, dir := range []string{staleSpool, unrelatedOldDir} {
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	cleanupStaleSessionSpools(cleanupRoot, now)
	if _, err := os.Stat(staleSpool); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale ACP spool still exists: %v", err)
	}
	for _, dir := range []string{freshSpool, unrelatedOldDir} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("spool cleanup removed protected directory %q: %v", dir, err)
		}
	}

	bridgeClient, server := newRecordingBridgeClient(t)
	lease, err := prepareRuntimeLease(context.Background(), bridgeClient, processOptions{
		BotID:     "bot-1",
		AgentID:   "claude-code",
		SetupMode: SetupModeSelf,
	})
	if err != nil {
		t.Fatalf("prepare RuntimeLease: %v", err)
	}
	t.Cleanup(func() { _ = lease.cleanup(context.Background()) })
	proc := &bridgeProcess{lease: lease}

	const sessionID = "claude-session-123"
	transcript := "state/projects/-data-project/" + sessionID + ".jsonl"
	subagent := "state/projects/-data-project/" + sessionID + "/subagents/agent-research.jsonl"
	empty := "state/projects/-data-project/" + sessionID + "/subagents/empty.jsonl"
	unrelated := "state/projects/-data-other/unrelated-session.jsonl"
	largeText := strings.Repeat("x", 128*1024)
	want := snapshotFromRecordsForTest(t, acpprofile.RuntimeSessionLocatorClaudeProject, []string{"state/projects"}, SessionState{
		SessionID:      sessionID,
		TranscriptPath: transcript,
	}, []SessionStateRecord{
		{FilePath: transcript, LineNumber: 1, Content: json.RawMessage(`{"type":"user","sessionId":"` + sessionID + `"}`)},
		{FilePath: transcript, LineNumber: 2, Content: json.RawMessage(` { "type" : "message", "text" : "hello" } `)},
		{FilePath: subagent, LineNumber: 1, Content: json.RawMessage(`{"type":"tool","text":"` + largeText + `"}`)},
	})
	if err := proc.RestoreSessionState(context.Background(), want); err != nil {
		t.Fatalf("RestoreSessionState: %v", err)
	}
	writeRuntimeTestFile(t, bridgeClient, path.Join(lease.root, empty), nil)
	writeRuntimeTestFile(t, bridgeClient, path.Join(lease.root, unrelated), []byte("not-json\n"))
	if server.exists("/data/.claude/projects/-data-project/" + sessionID + ".jsonl") {
		t.Fatal("database-supplied session state was restored into /data")
	}
	if got := string(readRuntimeTestFile(t, bridgeClient, path.Join(lease.root, transcript))); got !=
		`{"type":"user","sessionId":"`+sessionID+`"}`+"\n"+`{"type":"message","text":"hello"}`+"\n" {
		t.Fatalf("restored primary JSONL = %q", got)
	}

	got, err := proc.SnapshotSessionState(context.Background(), sessionID, SessionStateCursor{}, nil, nil)
	if err != nil {
		t.Fatalf("SnapshotSessionState: %v", err)
	}
	defer func() { _ = got.Close() }()
	if got.State().SessionID != sessionID || got.State().TranscriptPath != transcript {
		t.Fatalf("snapshot identity = %#v", got.State())
	}
	records := drainSnapshotRecordsForTest(t, got)
	if len(records) != 3 || records[0].FilePath != transcript || records[1].LineNumber != 2 || records[2].FilePath != subagent || len(records[2].Content) < len(largeText) {
		t.Fatalf("snapshot did not preserve multi-file record order: %#v", records)
	}

	// Finished snapshots hand their capture-admission slot back (converted
	// into a byte reservation), so both live snapshots above must not block a
	// third spool: the capture semaphore only bounds in-progress captures.
	blockedRecord := SessionStateRecord{
		FilePath: transcript, LineNumber: 1,
		Content: json.RawMessage(`{"type":"user","sessionId":"` + sessionID + `"}`),
	}
	releasedIndex := 0
	released, err := SpoolSessionState(
		context.Background(),
		acpprofile.RuntimeSessionLocatorClaudeProject,
		[]string{"state/projects"},
		SessionState{SessionID: sessionID, TranscriptPath: transcript},
		func(context.Context) (SessionStateRecord, error) {
			if releasedIndex > 0 {
				return SessionStateRecord{}, io.EOF
			}
			releasedIndex++
			return blockedRecord, nil
		},
		1,
		1,
	)
	if err != nil {
		t.Fatalf("spool while two finished snapshots are alive: %v", err)
	}
	if err := released.Close(); err != nil {
		t.Fatalf("close released snapshot: %v", err)
	}
	if err := want.Close(); err != nil {
		t.Fatalf("close restored snapshot: %v", err)
	}
}

func TestRuntimeLeaseCodexSnapshotValidatesHeaderAndExcludesOtherRollouts(t *testing.T) {
	bridgeClient, _ := newRecordingBridgeClient(t)
	lease, err := prepareRuntimeLease(context.Background(), bridgeClient, processOptions{
		BotID:     "bot-1",
		AgentID:   "codex",
		SetupMode: SetupModeAPIKey,
	})
	if err != nil {
		t.Fatalf("prepare RuntimeLease: %v", err)
	}
	t.Cleanup(func() { _ = lease.cleanup(context.Background()) })
	proc := &bridgeProcess{lease: lease}

	const sessionID = "019c1234-abcd-7000-8000-123456789abc"
	transcript := "state/sessions/2026/08/12/rollout-2026-08-12T10-11-12-" + sessionID + ".jsonl"
	writeRuntimeTestFile(t, bridgeClient, path.Join(lease.root, transcript), []byte(
		`{"type":"session_meta","payload":{"id":"`+sessionID+`"}}`+"\n"+
			`{"type":"event_msg","payload":{"type":"task_complete"}}`+"\n",
	))
	// Invalid unrelated JSONL proves snapshotting does not parse every rollout
	// in Codex's date tree.
	writeRuntimeTestFile(t, bridgeClient, path.Join(lease.root, "state/sessions/2026/08/11/rollout-other.jsonl"), []byte("not-json\n"))
	writeRuntimeTestFile(t, bridgeClient, path.Join(lease.root, "state/sessions/2026/08/12/rollout-"+sessionID+"-copy.jsonl"), []byte(
		`{"type":"session_meta","payload":{"id":"`+sessionID+`"}}`+"\n",
	))

	got, err := proc.SnapshotSessionState(context.Background(), sessionID, SessionStateCursor{}, nil, nil)
	if err != nil {
		t.Fatalf("SnapshotSessionState: %v", err)
	}
	defer func() { _ = got.Close() }()
	records := drainSnapshotRecordsForTest(t, got)
	if got.State().TranscriptPath != transcript || len(records) != 2 || records[0].FilePath != transcript {
		t.Fatalf("Codex snapshot selected unrelated rollouts: state=%#v records=%#v", got.State(), records)
	}

	writeRuntimeTestFile(t, bridgeClient, path.Join(lease.root, transcript), []byte(
		`{"type":"session_meta","payload":{"id":"different-session"}}`+"\n",
	))
	if _, err := proc.SnapshotSessionState(context.Background(), sessionID, SessionStateCursor{}, nil, nil); !errors.Is(err, ErrSessionStateNotFound) {
		t.Fatalf("SnapshotSessionState() error = %v, want content-validated ErrSessionStateNotFound", err)
	}
}

func snapshotFromRecordsForTest(
	t *testing.T,
	locator acpprofile.RuntimeSessionLocator,
	roots []string,
	state SessionState,
	records []SessionStateRecord,
) *SessionStateSnapshot {
	t.Helper()
	index := 0
	reader := func(context.Context) (SessionStateRecord, error) {
		if index == len(records) {
			return SessionStateRecord{}, io.EOF
		}
		record := records[index]
		index++
		return record, nil
	}
	files := int32(0)
	for index, record := range records {
		if index == 0 || record.FilePath != records[index-1].FilePath {
			files++
		}
	}
	snapshot, err := SpoolSessionState(context.Background(), locator, roots, state, reader, files, int64(len(records)))
	if err != nil {
		t.Fatalf("SpoolSessionState() error = %v", err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	return snapshot
}

func drainSnapshotRecordsForTest(t *testing.T, snapshot *SessionStateSnapshot) []SessionStateRecord {
	t.Helper()
	reader, err := snapshot.Records()
	if err != nil {
		t.Fatal(err)
	}
	var records []SessionStateRecord
	for {
		record, err := reader(context.Background())
		if errors.Is(err, io.EOF) {
			return records
		}
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
}

func TestRuntimeLeaseUsesUniqueHomesAndStagesOnlyAllowlistedState(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	server.mu.Lock()
	server.writeFileLocked("/data/.codex/config.toml", []byte("model = \"gpt-test\"\n"))
	server.writeFileLocked("/data/.codex/auth.json", codexAuthFixture("2026-08-04T10:00:00Z", "durable"))
	server.writeFileLocked("/data/.codex/state_5.sqlite", []byte("runtime database"))
	server.writeFileLocked("/data/.codex/state_5.sqlite-wal", []byte("runtime wal"))
	server.writeFileLocked("/data/.codex/tmp/arg0/lock", []byte("runtime lock"))
	server.writeFileLocked("/data/.codex/sessions/old.jsonl", []byte("runtime session"))
	server.mu.Unlock()

	opts := processOptions{
		BotID:     "bot-1",
		AgentID:   "codex",
		SetupMode: SetupModeSelf,
	}
	first, err := prepareRuntimeLease(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("prepare first RuntimeLease: %v", err)
	}
	second, err := prepareRuntimeLease(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("prepare second RuntimeLease: %v", err)
	}
	t.Cleanup(func() {
		_ = first.cleanup(context.Background())
		_ = second.cleanup(context.Background())
	})

	if first.root == second.root {
		t.Fatalf("two ACP processes share runtime root %q", first.root)
	}
	for _, lease := range []*runtimeLease{first, second} {
		if !server.exists(path.Join(lease.root, "state/config.toml")) ||
			!server.exists(path.Join(lease.root, "state/auth.json")) {
			t.Fatalf("allowlisted Codex config was not staged under %s", lease.root)
		}
		for _, forbidden := range []string{
			"state/state_5.sqlite",
			"state/state_5.sqlite-wal",
			"state/tmp/arg0/lock",
			"state/sessions/old.jsonl",
		} {
			if server.exists(path.Join(lease.root, forbidden)) {
				t.Fatalf("runtime-only Codex state %q was copied into %s", forbidden, lease.root)
			}
		}
	}

	if err := first.cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup first RuntimeLease: %v", err)
	}
	if server.exists(first.root) {
		t.Fatalf("first RuntimeLease root still exists: %s", first.root)
	}
	if !server.exists(second.root) {
		t.Fatalf("cleaning first RuntimeLease removed the second: %s", second.root)
	}
	if !server.exists("/data/.codex/state_5.sqlite") || !server.exists("/data/.codex/tmp/arg0/lock") {
		t.Fatal("RuntimeLease cleanup changed persistent workspace files")
	}
}

func TestRuntimeLeaseRejectsPreplantedRuntimeParentSymlink(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	server.mu.Lock()
	server.fs[runtimeStateRoot] = recordingFSNode{isSymlink: true, modTime: time.Now()}
	server.mu.Unlock()

	_, err := prepareRuntimeLease(context.Background(), client, processOptions{
		BotID:     "bot-1",
		AgentID:   "codex",
		SetupMode: SetupModeSelf,
	})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("prepare RuntimeLease error = %v, want pre-planted symlink rejection", err)
	}
}

func TestRuntimeLeaseCodexAuthSyncIsMonotonicAcrossConcurrentWriters(t *testing.T) {
	client, _ := newRecordingBridgeClient(t)
	writeRuntimeTestFile(t, client, "/data/.codex/config.toml", []byte("model = \"durable\"\n"))
	writeRuntimeTestFile(t, client, "/data/.codex/auth.json", codexAuthFixture("2026-08-04T10:00:00Z", "initial"))

	opts := processOptions{BotID: "bot-1", AgentID: "codex", SetupMode: SetupModeSelf}
	first, err := prepareRuntimeLease(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("prepare first RuntimeLease: %v", err)
	}
	second, err := prepareRuntimeLease(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("prepare second RuntimeLease: %v", err)
	}
	t.Cleanup(func() {
		_ = first.cleanup(context.Background())
		_ = second.cleanup(context.Background())
	})

	writeRuntimeTestFile(t, client, path.Join(first.root, "state/config.toml"), []byte("model = \"runtime\"\n"))
	writeRuntimeTestFile(t, client, path.Join(first.root, "state/auth.json"), codexAuthFixture("2026-08-04T12:00:00Z", "first-new"))
	if err := first.syncLiveState(context.Background()); err != nil {
		t.Fatalf("periodically sync fresh first auth: %v", err)
	}
	assertCodexAuthToken(t, readRuntimeTestFile(t, client, "/data/.codex/auth.json"), "first-new")
	if got := string(readRuntimeTestFile(t, client, "/data/.codex/config.toml")); got != "model = \"durable\"\n" {
		t.Fatalf("periodic sync wrote compare-and-swap config: %q", got)
	}

	// The second process staged the older 10:00 credential. Even if it exits
	// later, its locally refreshed 11:00 copy must not roll durable auth back.
	writeRuntimeTestFile(t, client, path.Join(second.root, "state/auth.json"), codexAuthFixture("2026-08-04T11:00:00Z", "second-stale"))
	if err := second.Sync(context.Background()); err != nil {
		t.Fatalf("sync stale concurrent auth: %v", err)
	}
	assertCodexAuthToken(t, readRuntimeTestFile(t, client, "/data/.codex/auth.json"), "first-new")

	// Simulate the device OAuth handler writing /data while this ACP process is
	// alive. Its newer credential also wins over the staged process copy.
	writeRuntimeTestFile(t, client, "/data/.codex/auth.json", codexAuthFixture("2026-08-04T13:00:00Z", "device-flow"))
	writeRuntimeTestFile(t, client, path.Join(second.root, "state/auth.json"), codexAuthFixture("2026-08-04T12:30:00Z", "process-older"))
	if err := second.Sync(context.Background()); err != nil {
		t.Fatalf("sync auth older than device flow: %v", err)
	}
	assertCodexAuthToken(t, readRuntimeTestFile(t, client, "/data/.codex/auth.json"), "device-flow")

	// The same process may refresh again later. A genuinely newer token is
	// still accepted even though its earlier write was rejected as stale.
	writeRuntimeTestFile(t, client, path.Join(second.root, "state/auth.json"), codexAuthFixture("2026-08-04T14:00:00Z", "process-newest"))
	if err := second.Sync(context.Background()); err != nil {
		t.Fatalf("sync newest auth: %v", err)
	}
	assertCodexAuthToken(t, readRuntimeTestFile(t, client, "/data/.codex/auth.json"), "process-newest")
}

func TestSessionPromptFlushesRollingCodexAuthBeforeProcessExit(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(root, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, codexAuthFixture("2026-08-04T10:00:00Z", "initial"), 0o600); err != nil {
		t.Fatal(err)
	}

	client := newTestBridgeClient(t, root)
	runner := NewRunner(nil, testWorkspace{
		client: client,
		info: bridge.WorkspaceInfo{
			Backend:        bridge.WorkspaceBackendContainer,
			DefaultWorkDir: root,
		},
	})
	refreshed := codexAuthFixture("2026-08-04T12:00:00Z", "prompt-refresh")
	session, err := runner.StartSession(context.Background(), StartRequest{
		AgentID:     "codex",
		BotID:       "bot-1",
		ProjectPath: "/data/project",
		Command:     writeFakeAgentScript(t, root),
		SetupMode:   SetupModeOAuth,
		Env:         []string{"MEMOH_ACP_FAKE_AGENT_REFRESH_AUTH=" + string(refreshed)},
		Timeout:     10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	if _, err := session.Prompt(context.Background(), "refresh auth"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	persisted, err := os.ReadFile(authPath) //nolint:gosec // test path is under t.TempDir.
	if err != nil {
		t.Fatalf("read durable Codex auth before process exit: %v", err)
	}
	assertCodexAuthToken(t, persisted, "prompt-refresh")
}

func TestRuntimeLeaseStartupFailureRemovesOnlyItsUUIDDirectory(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	server.mu.Lock()
	server.writeFileLocked("/data/keep.txt", []byte("keep"))
	server.ensureDirLocked("/data/.codex")
	server.fs["/data/.codex/config.toml"] = recordingFSNode{isSymlink: true, modTime: time.Now()}
	server.mu.Unlock()

	_, err := prepareRuntimeLease(context.Background(), client, processOptions{
		BotID:     "bot-1",
		AgentID:   "codex",
		SetupMode: SetupModeSelf,
	})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("prepare RuntimeLease error = %v, want unsafe staged path", err)
	}
	if !server.exists("/data/keep.txt") {
		t.Fatal("startup cleanup changed the persistent workspace")
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	prefix := runtimeStateRoot + "/codex/"
	for filePath := range server.fs {
		if strings.HasPrefix(filePath, prefix) {
			t.Fatalf("failed startup left process runtime state behind: %s", filePath)
		}
	}
}

func writeRuntimeTestFile(t *testing.T, client interface {
	WriteRaw(context.Context, string, io.Reader) (int64, error)
}, filePath string, content []byte,
) {
	t.Helper()
	if _, err := client.WriteRaw(context.Background(), filePath, bytes.NewReader(content)); err != nil {
		t.Fatalf("write %s: %v", filePath, err)
	}
}

func readRuntimeTestFile(t *testing.T, client interface {
	ReadRaw(context.Context, string) (io.ReadCloser, error)
}, filePath string,
) []byte {
	t.Helper()
	reader, err := client.ReadRaw(context.Background(), filePath)
	if err != nil {
		t.Fatalf("read %s: %v", filePath, err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read bytes from %s: %v", filePath, err)
	}
	return data
}

func codexAuthFixture(lastRefresh, token string) []byte {
	document := map[string]any{
		"last_refresh": lastRefresh,
		"tokens": map[string]any{
			"refresh_token": token,
		},
	}
	data, _ := json.Marshal(document)
	return data
}

func assertCodexAuthToken(t *testing.T, data []byte, want string) {
	t.Helper()
	var document struct {
		Tokens map[string]string `json:"tokens"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse Codex auth: %v", err)
	}
	if got := document.Tokens["refresh_token"]; got != want {
		t.Fatalf("Codex refresh token = %q, want %q; auth=%s", got, want, data)
	}
}

func TestRuntimeLeaseRejectsPreplantedCacheSymlink(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	server.mu.Lock()
	server.fs[runtimeCacheRoot] = recordingFSNode{isSymlink: true, modTime: time.Now()}
	server.mu.Unlock()

	_, err := prepareRuntimeLease(context.Background(), client, processOptions{
		BotID:     "bot-1",
		AgentID:   "hermes",
		SetupMode: SetupModeSelf,
	})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("prepare RuntimeLease error = %v, want pre-planted cache symlink rejection", err)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	for filePath := range server.fs {
		if strings.HasPrefix(filePath, runtimeCacheRoot+"/") {
			t.Fatalf("directory %q was created through the pre-planted cache symlink", filePath)
		}
		if strings.HasPrefix(filePath, runtimeStateRoot+"/hermes/") {
			t.Fatalf("failed startup left runtime state behind: %s", filePath)
		}
	}
}
