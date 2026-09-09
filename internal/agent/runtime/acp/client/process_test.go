package client

import (
	"context"
	"errors"
	"io"
	"net"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func TestBuildShellCommandQuotesCommandAndArgs(t *testing.T) {
	got := buildShellCommand("codex-acp", []string{"--flag", "value with spaces", "it's", "$HOME"})
	want := `codex-acp --flag 'value with spaces' 'it'\''s' '$HOME'`
	if got != want {
		t.Fatalf("buildShellCommand() = %q, want %q", got, want)
	}
}

func TestPrepareRuntimeLeaseUsesProfileStateEnv(t *testing.T) {
	client, _ := newRecordingBridgeClient(t)
	lease, err := prepareRuntimeLease(context.Background(), client, processOptions{
		Backend:   WorkspaceBackendContainer,
		BotID:     "bot-1",
		AgentID:   "acp",
		SetupMode: SetupModeAPIKey,
		Env:       []string{"CUSTOM_FLAG=enabled", "HOME=/host-home"},
	})
	if err != nil {
		t.Fatalf("prepareRuntimeLease() error = %v", err)
	}
	if !validOwnedRuntimeRoot(lease.root, "acp") {
		t.Fatalf("runtime root = %q, want process-owned UUID path", lease.root)
	}
	if got := envValue(lease.agentEnv, "HOME"); got != dataMountPath {
		t.Fatalf("HOME = %q, want profile-owned %q over host value", got, dataMountPath)
	}
	if got := envValue(lease.agentEnv, "TMPDIR"); !strings.HasPrefix(got, lease.root+"/") {
		t.Fatalf("TMPDIR = %q, want a path under the runtime root", got)
	}
	if got := envValue(lease.agentEnv, "CUSTOM_FLAG"); got != "enabled" {
		t.Fatalf("CUSTOM_FLAG = %q, want caller env preserved", got)
	}
}

func TestPrepareRuntimeLeaseFiltersBlockedHostCredentials(t *testing.T) {
	client, _ := newRecordingBridgeClient(t)
	lease, err := prepareRuntimeLease(context.Background(), client, processOptions{
		AgentID:   "acp",
		SetupMode: SetupModeAPIKey,
		Env:       []string{"CUSTOM_AGENT_HOME=/host/custom-agent", "OPENAI_API_KEY=sk-host", "OPENROUTER_API_KEY=sk-router", "CUSTOM_FLAG=1"},
		UnsetEnv:  []string{"CUSTOM_AGENT_*", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENROUTER_API_KEY", "OPENROUTER_BASE_URL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.finalize(context.Background()) }()
	if envHasKey(lease.agentEnv, "OPENAI_API_KEY") || envHasKey(lease.agentEnv, "OPENROUTER_API_KEY") || envHasKey(lease.agentEnv, "CUSTOM_AGENT_HOME") {
		t.Fatalf("blocked host env leaked into agent env: %v", lease.agentEnv)
	}
	assertEnvHas(t, lease.agentEnv, "CUSTOM_FLAG=1")
}

func TestStartBridgeProcessPassesUnsetEnv(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	proc, err := startBridgeProcess(context.Background(), client, "my-agent-acp", nil, "/data", time.Minute, processOptions{
		Backend:   WorkspaceBackendContainer,
		BotID:     "bot-1",
		AgentID:   "acp",
		SetupMode: SetupModeAPIKey,
		UnsetEnv:  []string{"OPENAI_API_KEY", "OPENAI_BASE_URL"},
	})
	if err != nil {
		t.Fatalf("startBridgeProcess() error = %v", err)
	}
	server.waitForRecordWithTimeout(t, int32(time.Minute.Seconds()), 2*time.Second)
	_ = proc.Close()
	processRecord, ok := findRecordWithTimeout(server.records(), int32(time.Minute.Seconds()))
	if !ok {
		t.Fatalf("missing process exec record: %#v", server.records())
	}
	if !hasString(processRecord.UnsetEnv, "OPENAI_API_KEY") || !hasString(processRecord.UnsetEnv, "OPENAI_BASE_URL") {
		t.Fatalf("UnsetEnv = %#v, want requested cleanup keys", processRecord.UnsetEnv)
	}
}

func TestCreateTerminalFiltersBlockedEnv(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	manager := newTerminalManager(
		context.Background(),
		client,
		"/data",
		"/data",
		7,
		[]string{"AGENT_HOME=/data/.agent"},
		[]string{"CUSTOM_AGENT_SECRET", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "GOOGLE_API_KEY", "GOOGLE_BASE_URL", "GEMINI_API_KEY", "GEMINI_BASE_URL"},
		nil,
	)
	term, err := manager.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		Command: "env",
		Env: []acp.EnvVariable{
			{Name: "OPENAI_API_KEY", Value: "sk-agent"},
			{Name: "CUSTOM_AGENT_SECRET", Value: "custom-secret"},
			{Name: "OPENROUTER_API_KEY", Value: "sk-router"},
			{Name: "CUSTOM_FLAG", Value: "1"},
		},
	}, nil, terminalRuntimeScope{})
	if err != nil {
		t.Fatalf("CreateTerminal() error = %v", err)
	}
	if _, err := manager.WaitForTerminalExit(context.Background(), acp.WaitForTerminalExitRequest{TerminalId: term.TerminalId}); err != nil {
		t.Fatalf("WaitForTerminalExit() error = %v", err)
	}
	server.waitForRecordWithTimeout(t, 7, time.Second)
	record, ok := findRecordWithTimeout(server.records(), 7)
	if !ok {
		t.Fatalf("missing terminal exec record: %#v", server.records())
	}
	if !envHasKeyValue(record.Env, "AGENT_HOME", "/data/.agent") {
		t.Fatalf("terminal env missing managed AGENT_HOME: %#v", record.Env)
	}
	if !envHasKeyValue(record.Env, "CUSTOM_FLAG", "1") {
		t.Fatalf("terminal env missing allowed custom flag: %#v", record.Env)
	}
	if envHasKey(record.Env, "CUSTOM_AGENT_SECRET") || envHasKey(record.Env, "OPENAI_API_KEY") || envHasKey(record.Env, "OPENROUTER_API_KEY") {
		t.Fatalf("terminal env leaked provider key: %#v", record.Env)
	}
	for _, key := range []string{"CUSTOM_AGENT_SECRET", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "GOOGLE_API_KEY", "GOOGLE_BASE_URL", "GEMINI_API_KEY", "GEMINI_BASE_URL"} {
		if !hasString(record.UnsetEnv, key) {
			t.Fatalf("terminal UnsetEnv = %#v, missing %q", record.UnsetEnv, key)
		}
	}
}

func TestStartBridgeProcessCanRunWithoutBridgeHardTimeout(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	proc, err := startBridgeProcess(context.Background(), client, "codex-acp", nil, "/data", time.Minute, processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "acp",
		SetupMode: SetupModeAPIKey,
		Env:       []string{"TRACE_ID=trace-1"},
		NoTimeout: true,
	})
	if err != nil {
		t.Fatalf("startBridgeProcess() error = %v", err)
	}

	// The exec stream input is sent asynchronously; wait until the bridge
	// recording server has received it before closing the process and
	// reading records.
	server.waitForRecordWithTimeout(t, -1, 2*time.Second)
	_ = proc.Close()

	records := server.records()
	if len(records) < 2 {
		t.Fatalf("records len = %d, want at least command check + process exec: %#v", len(records), records)
	}
	processRecord, ok := findRecordWithTimeout(records, -1)
	if !ok {
		t.Fatalf("expected a record with NoTimeout (-1); got %#v", records)
	}
	if processRecord.Timeout != -1 {
		t.Fatalf("process timeout = %d, want -1 no bridge hard timeout", processRecord.Timeout)
	}
	if strings.Contains(processRecord.Command, "TRACE_ID=trace-1") {
		t.Fatalf("process command leaked env var: %q", processRecord.Command)
	}
	assertEnvHas(t, processRecord.Env, "TRACE_ID=trace-1")
	assertEnvHas(t, processRecord.Env, "HOME=/data")
	assertEnvHas(t, processRecord.Env, "TMPDIR="+path.Join(proc.lease.root, "tmp"))
}

func TestStartBridgeProcessUsesContainerToolkitFallback(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	server.setExitCode("command -v codex-acp >/dev/null 2>&1", 127)

	proc, err := startBridgeProcess(context.Background(), client, "codex-acp", nil, "/data", time.Minute, processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "acp",
		SetupMode: SetupModeAPIKey,
	})
	if err != nil {
		t.Fatalf("startBridgeProcess() error = %v", err)
	}
	server.waitForRecordWithTimeout(t, int32(time.Minute.Seconds()), 2*time.Second)
	_ = proc.Close()

	processRecord, ok := findRecordWithTimeout(server.records(), int32(time.Minute.Seconds()))
	if !ok {
		t.Fatalf("missing process exec record: %#v", server.records())
	}
	want := containerToolkitBin + "/codex-acp"
	if processRecord.Command != want {
		t.Fatalf("process command = %q, want %q", processRecord.Command, want)
	}
}

func TestStartBridgeProcessRetriesTransientMissingCommand(t *testing.T) {
	oldWindow := commandResolveWindow
	oldDelay := commandResolveDelay
	commandResolveWindow = time.Second
	commandResolveDelay = time.Millisecond
	t.Cleanup(func() {
		commandResolveWindow = oldWindow
		commandResolveDelay = oldDelay
	})

	client, server := newRecordingBridgeClient(t)
	server.setExitCode("command -v codex-acp >/dev/null 2>&1", 127)
	server.setExitCodes("test -x "+containerToolkitBin+"/codex-acp", 1, 0)

	proc, err := startBridgeProcess(context.Background(), client, "codex-acp", nil, "/data", time.Minute, processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "acp",
		SetupMode: SetupModeAPIKey,
	})
	if err != nil {
		t.Fatalf("startBridgeProcess() error = %v", err)
	}
	server.waitForRecordWithTimeout(t, int32(time.Minute.Seconds()), 2*time.Second)
	_ = proc.Close()

	var checks int
	for _, record := range server.records() {
		if record.Command == "test -x "+containerToolkitBin+"/codex-acp" {
			checks++
		}
	}
	if checks < 2 {
		t.Fatalf("command checks = %d, want retry; records=%#v", checks, server.records())
	}
	processRecord, ok := findRecordWithTimeout(server.records(), int32(time.Minute.Seconds()))
	if !ok || processRecord.Command != containerToolkitBin+"/codex-acp" {
		t.Fatalf("process record = %#v, ok=%v", processRecord, ok)
	}
}

func TestStartBridgeProcessReportsToolkitFallbackFailure(t *testing.T) {
	oldWindow := commandResolveWindow
	commandResolveWindow = 0
	t.Cleanup(func() { commandResolveWindow = oldWindow })

	client, server := newRecordingBridgeClient(t)
	server.setExitCode("command -v codex-acp >/dev/null 2>&1", 127)
	server.setExitCode("test -x "+containerToolkitBin+"/codex-acp", 1)

	_, err := startBridgeProcess(context.Background(), client, "codex-acp", nil, "/data", time.Minute, processOptions{
		Backend:   WorkspaceBackendContainer,
		AgentID:   "acp",
		SetupMode: SetupModeAPIKey,
	})
	if err == nil {
		t.Fatalf("startBridgeProcess() error = nil, want missing command error")
	}
	msg := err.Error()
	// With PATH and the toolkit both missing the command, the error names the
	// command and the toolkit location an operator can provision.
	for _, want := range []string{"codex-acp", containerToolkitBin} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

type execRecord struct {
	Command  string
	WorkDir  string
	Env      []string
	CleanEnv bool
	UnsetEnv []string
	Timeout  int32
}

type writeRecord struct {
	Path    string
	Content []byte
}

type recordingFSNode struct {
	isDir     bool
	isSymlink bool
	content   []byte
	modTime   time.Time
}

type recordingBridgeServer struct {
	pb.UnimplementedContainerServiceServer

	mu    sync.Mutex
	execs []execRecord
	files []writeRecord
	reads []string
	exits map[string]int32
	seqs  map[string][]int32
	fs    map[string]recordingFSNode
}

func (s *recordingBridgeServer) Exec(stream grpc.BidiStreamingServer[pb.ExecInput, pb.ExecOutput]) error {
	input, err := stream.Recv()
	if err != nil {
		return err
	}
	s.mu.Lock()
	exitCode := s.exits[input.GetCommand()]
	if len(s.seqs[input.GetCommand()]) > 0 {
		exitCode = s.seqs[input.GetCommand()][0]
		s.seqs[input.GetCommand()] = s.seqs[input.GetCommand()][1:]
	}
	s.execs = append(s.execs, execRecord{
		Command:  input.GetCommand(),
		WorkDir:  input.GetWorkDir(),
		Env:      append([]string(nil), input.GetEnv()...),
		CleanEnv: input.GetCleanEnv(),
		UnsetEnv: append([]string(nil), input.GetUnsetEnv()...),
		Timeout:  input.GetTimeoutSeconds(),
	})
	s.mu.Unlock()
	if err := stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_EXIT, ExitCode: exitCode}); err != nil {
		return err
	}
	return nil
}

func (s *recordingBridgeServer) setExitCodes(command string, codes ...int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seqs == nil {
		s.seqs = make(map[string][]int32)
	}
	s.seqs[command] = append([]int32(nil), codes...)
}

func (s *recordingBridgeServer) setExitCode(command string, code int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exits == nil {
		s.exits = make(map[string]int32)
	}
	s.exits[command] = code
}

func (s *recordingBridgeServer) WriteFile(_ context.Context, req *pb.WriteFileRequest) (*pb.WriteFileResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeFileLocked(req.GetPath(), req.GetContent())
	s.files = append(s.files, writeRecord{
		Path:    req.GetPath(),
		Content: append([]byte(nil), req.GetContent()...),
	})
	return &pb.WriteFileResponse{}, nil
}

func (s *recordingBridgeServer) ReadFile(_ context.Context, req *pb.ReadFileRequest) (*pb.ReadFileResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, req.GetPath())
	if node, ok := s.fs[path.Clean(req.GetPath())]; ok && !node.isDir {
		return &pb.ReadFileResponse{Content: string(node.content), TotalLines: int32(strings.Count(string(node.content), "\n"))}, nil //nolint:gosec // in-memory test files cannot approach int32 limits.
	}
	return &pb.ReadFileResponse{Content: "recorded input\n", TotalLines: 1}, nil
}

func (s *recordingBridgeServer) Mkdir(_ context.Context, req *pb.MkdirRequest) (*pb.MkdirResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureDirLocked(req.GetPath())
	return &pb.MkdirResponse{}, nil
}

func (s *recordingBridgeServer) ListDir(_ context.Context, req *pb.ListDirRequest) (*pb.ListDirResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := path.Clean(req.GetPath())
	node, ok := s.fs[dir]
	if !ok || !node.isDir {
		return nil, status.Error(codes.NotFound, "not found")
	}
	prefix := strings.TrimSuffix(dir, "/") + "/"
	entries := make([]*pb.FileEntry, 0)
	for filePath, child := range s.fs {
		if filePath == dir || !strings.HasPrefix(filePath, prefix) {
			continue
		}
		rel := strings.TrimPrefix(filePath, prefix)
		if !req.GetRecursive() && strings.Contains(rel, "/") {
			continue
		}
		mode := "-rw-------"
		if child.isSymlink {
			mode = "Lrwxrwxrwx"
		} else if child.isDir {
			mode = "drwx------"
		}
		entries = append(entries, &pb.FileEntry{
			Path:    rel,
			IsDir:   child.isDir,
			Size:    int64(len(child.content)),
			Mode:    mode,
			ModTime: child.modTime.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].GetPath() < entries[j].GetPath() })
	// Mirror the bridge server's max_entries contract: more entries than the
	// requested bound is a refusal, never a truncated listing.
	if maxEntries := int(req.GetMaxEntries()); maxEntries > 0 && len(entries) > maxEntries {
		return nil, status.Errorf(codes.ResourceExhausted, "directory listing exceeds %d entries", maxEntries)
	}
	return &pb.ListDirResponse{Entries: entries, TotalCount: int32(len(entries))}, nil //nolint:gosec // test fixture contains only a bounded handful of entries.
}

func (s *recordingBridgeServer) Stat(_ context.Context, req *pb.StatRequest) (*pb.StatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	filePath := path.Clean(req.GetPath())
	node, ok := s.fs[filePath]
	if !ok {
		return nil, status.Error(codes.NotFound, "not found")
	}
	mode := "-rw-------"
	if node.isSymlink {
		mode = "Lrwxrwxrwx"
	} else if node.isDir {
		mode = "drwx------"
	}
	return &pb.StatResponse{Entry: &pb.FileEntry{
		Path:    path.Base(filePath),
		IsDir:   node.isDir,
		Size:    int64(len(node.content)),
		Mode:    mode,
		ModTime: node.modTime.UTC().Format(time.RFC3339),
	}}, nil
}

func (s *recordingBridgeServer) ReadRaw(req *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	s.mu.Lock()
	node, ok := s.fs[path.Clean(req.GetPath())]
	s.mu.Unlock()
	if !ok || node.isDir {
		return status.Error(codes.NotFound, "not found")
	}
	if len(node.content) == 0 {
		return nil
	}
	return stream.Send(&pb.DataChunk{Data: append([]byte(nil), node.content...)})
}

func (s *recordingBridgeServer) ReadRawNoFollow(req *pb.ReadRawNoFollowRequest, stream pb.ContainerService_ReadRawNoFollowServer) error {
	s.mu.Lock()
	target, err := s.noFollowTargetLocked(req.GetRoot(), req.GetRelativePath(), false)
	var content []byte
	if err == nil {
		node, ok := s.fs[target]
		if !ok || node.isDir || node.isSymlink {
			err = status.Error(codes.NotFound, "not found")
		} else {
			content = append([]byte(nil), node.content...)
		}
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	const chunkSize = 64 * 1024
	for len(content) > 0 {
		size := min(len(content), chunkSize)
		if err := stream.Send(&pb.DataChunk{Data: content[:size]}); err != nil {
			return err
		}
		content = content[size:]
	}
	return nil
}

func (s *recordingBridgeServer) WriteRaw(stream grpc.ClientStreamingServer[pb.WriteRawChunk, pb.WriteRawResponse]) error {
	var filePath string
	var content []byte
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if filePath == "" {
			filePath = chunk.GetPath()
		}
		content = append(content, chunk.GetData()...)
	}
	if strings.TrimSpace(filePath) == "" {
		return status.Error(codes.InvalidArgument, "path is required")
	}
	s.mu.Lock()
	s.writeFileLocked(filePath, content)
	s.files = append(s.files, writeRecord{Path: filePath, Content: append([]byte(nil), content...)})
	s.mu.Unlock()
	return stream.SendAndClose(&pb.WriteRawResponse{BytesWritten: int64(len(content))})
}

func (s *recordingBridgeServer) WriteRawNoFollow(stream grpc.ClientStreamingServer[pb.WriteRawNoFollowChunk, pb.WriteRawResponse]) error {
	var root, relativePath string
	var content []byte
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if root == "" {
			root = chunk.GetRoot()
			relativePath = chunk.GetRelativePath()
		}
		content = append(content, chunk.GetData()...)
	}
	s.mu.Lock()
	target, err := s.noFollowTargetLocked(root, relativePath, true)
	if err == nil {
		if _, exists := s.fs[target]; exists {
			err = status.Error(codes.AlreadyExists, "target already exists")
		} else {
			s.writeFileLocked(target, content)
			s.files = append(s.files, writeRecord{Path: target, Content: append([]byte(nil), content...)})
		}
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return stream.SendAndClose(&pb.WriteRawResponse{BytesWritten: int64(len(content))})
}

func (s *recordingBridgeServer) noFollowTargetLocked(root, relativePath string, createParents bool) (string, error) {
	root = path.Clean(root)
	relativePath = path.Clean(relativePath)
	if root == "." || !strings.HasPrefix(root, "/") || relativePath == "." || strings.HasPrefix(relativePath, "../") || strings.HasPrefix(relativePath, "/") {
		return "", status.Error(codes.InvalidArgument, "invalid anchored path")
	}
	current := "/"
	for _, component := range strings.Split(strings.TrimPrefix(root, "/"), "/") {
		current = path.Join(current, component)
		node, ok := s.fs[current]
		if !ok || !node.isDir || node.isSymlink {
			return "", status.Error(codes.FailedPrecondition, "unsafe root")
		}
	}
	components := strings.Split(relativePath, "/")
	current = root
	for _, component := range components[:len(components)-1] {
		current = path.Join(current, component)
		node, ok := s.fs[current]
		if !ok && createParents {
			s.ensureDirLocked(current)
			node, ok = s.fs[current]
		}
		if !ok || !node.isDir || node.isSymlink {
			return "", status.Error(codes.FailedPrecondition, "unsafe parent")
		}
	}
	return path.Join(root, relativePath), nil
}

func (s *recordingBridgeServer) DeleteFile(_ context.Context, req *pb.DeleteFileRequest) (*pb.DeleteFileResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := path.Clean(req.GetPath())
	delete(s.fs, target)
	if req.GetRecursive() {
		prefix := strings.TrimSuffix(target, "/") + "/"
		for filePath := range s.fs {
			if strings.HasPrefix(filePath, prefix) {
				delete(s.fs, filePath)
			}
		}
	}
	return &pb.DeleteFileResponse{}, nil
}

func (s *recordingBridgeServer) ensureDirLocked(dir string) {
	if s.fs == nil {
		s.fs = make(map[string]recordingFSNode)
	}
	dir = path.Clean(dir)
	parts := strings.Split(strings.TrimPrefix(dir, "/"), "/")
	current := "/"
	s.fs[current] = recordingFSNode{isDir: true, modTime: time.Now()}
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		current = path.Join(current, part)
		s.fs[current] = recordingFSNode{isDir: true, modTime: time.Now()}
	}
}

func (s *recordingBridgeServer) writeFileLocked(filePath string, content []byte) {
	filePath = path.Clean(filePath)
	s.ensureDirLocked(path.Dir(filePath))
	s.fs[filePath] = recordingFSNode{content: append([]byte(nil), content...), modTime: time.Now()}
}

func (s *recordingBridgeServer) records() []execRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]execRecord, len(s.execs))
	copy(out, s.execs)
	return out
}

func (s *recordingBridgeServer) writes() []writeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]writeRecord, len(s.files))
	copy(out, s.files)
	return out
}

func (s *recordingBridgeServer) readPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reads...)
}

// waitForRecordWithTimeout polls until a record with the given timeout value
// has been recorded, or the deadline elapses. It is used to bridge the gap
// between the async ExecStreamWithEnv input send and the server-side Recv.
func (s *recordingBridgeServer) waitForRecordWithTimeout(t *testing.T, want int32, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if _, ok := findRecordWithTimeout(s.records(), want); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func findRecordWithTimeout(records []execRecord, want int32) (execRecord, bool) {
	for _, rec := range records {
		if rec.Timeout == want {
			return rec, true
		}
	}
	return execRecord{}, false
}

func newRecordingBridgeClient(t *testing.T) (*bridge.Client, *recordingBridgeServer) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	recorder := &recordingBridgeServer{fs: map[string]recordingFSNode{
		"/":     {isDir: true, modTime: time.Now()},
		"/data": {isDir: true, modTime: time.Now()},
		"/tmp":  {isDir: true, modTime: time.Now()},
	}}
	pb.RegisterContainerServiceServer(server, recorder)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///acpclient-process-test",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn), recorder
}

func assertEnvHas(t *testing.T, env []string, want string) {
	t.Helper()
	for _, item := range env {
		if item == want {
			return
		}
	}
	t.Fatalf("env %v missing %q", env, want)
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func envHasKeyValue(env []string, key, value string) bool {
	want := key + "=" + value
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}

func envHasKey(env []string, key string) bool {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
