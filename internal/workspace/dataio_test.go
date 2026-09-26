package workspace

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

func TestTarGzDirPreservesWorkspaceData(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceArchiveFixture(t, root)

	var buf bytes.Buffer
	if err := tarGzDir(&buf, root); err != nil {
		t.Fatalf("tarGzDir error = %v", err)
	}

	names := tarGzNames(t, buf.Bytes())
	assertWorkspaceDataInArchive(t, names)
}

func TestExportDataViaGRPCPreservesWorkspaceData(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceArchiveFixture(t, root)

	client := newDataIOTestBridgeClient(t, root)
	manager := &Manager{service: &dataIOBridgeProvider{client: client}}
	reader, err := manager.exportDataViaGRPC(context.Background(), "bot-1")
	if err != nil {
		t.Fatalf("exportDataViaGRPC error = %v", err)
	}
	defer func() { _ = reader.Close() }()
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}

	names := tarGzNames(t, raw)
	assertWorkspaceDataInArchive(t, names)
}

func TestImportDataViaGRPCPreservesWorkspaceData(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".codex/auth.json", `{"token":"old"}`)
	writeTestFile(t, root, ".codex/auth/stale-target.json", `{"refresh_token":"old"}`)
	writeTestFile(t, root, ".claude/projects/stale/session.jsonl", `{"message":"old"}`)
	client := newDataIOTestBridgeClient(t, root)
	manager := &Manager{service: &dataIOBridgeProvider{client: client}}
	raw := buildWorkspaceArchive(t, workspaceArchiveFixture())

	if err := manager.importDataViaGRPC(context.Background(), "bot-1", bytes.NewReader(raw)); err != nil {
		t.Fatalf("importDataViaGRPC error = %v", err)
	}

	assertWorkspaceDataOnDisk(t, root)
	assertPathContent(t, root, ".codex/auth/stale-target.json", `{"refresh_token":"old"}`)
	assertPathContent(t, root, ".claude/projects/stale/session.jsonl", `{"message":"old"}`)
}

func TestUntarGzDirPreservesWorkspaceData(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".codex/auth.json", `{"token":"old"}`)
	writeTestFile(t, root, ".codex/auth/stale-target.json", `{"refresh_token":"old"}`)
	writeTestFile(t, root, ".claude/projects/stale/session.jsonl", `{"message":"old"}`)
	raw := buildWorkspaceArchive(t, workspaceArchiveFixture())

	if err := untarGzDir(bytes.NewReader(raw), root); err != nil {
		t.Fatalf("untarGzDir error = %v", err)
	}

	assertWorkspaceDataOnDisk(t, root)
	assertPathContent(t, root, ".codex/auth/stale-target.json", `{"refresh_token":"old"}`)
	assertPathContent(t, root, ".claude/projects/stale/session.jsonl", `{"message":"old"}`)
}

func TestWorkspaceArchiveRoundTripSkipsSockets(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "memoh-archive-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	writeWorkspaceArchiveFixture(t, root)

	socketPath := filepath.Join(root, "runtime.sock")
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	var archive bytes.Buffer
	if err := tarGzDir(&archive, root); err != nil {
		t.Fatal(err)
	}
	names := tarGzNames(t, archive.Bytes())
	assertWorkspaceDataInArchive(t, names)
	if hasArchiveName(names, "runtime.sock") {
		t.Fatalf("archive included socket: %v", names)
	}

	restored := t.TempDir()
	if err := untarGzDir(bytes.NewReader(archive.Bytes()), restored); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceDataOnDisk(t, restored)
}

func workspaceArchiveFixture() map[string]string {
	return map[string]string{ //nolint:gosec // synthetic credentials exercise archive preservation
		".codex/auth.json":                                        `{"OPENAI_API_KEY":"secret"}`,
		".codex/auth/token.json":                                  `{"token":"secret"}`,
		".codex/sessions/latest.json":                             `{"message":"private runtime transcript"}`,
		".codex/state.db":                                         "runtime state",
		".codex/state.db-wal":                                     "runtime write-ahead log",
		".codex/agents/agent-1/auth.json":                         `{"token":"agent-secret"}`,
		".codex/agents/agent-1/state_5.sqlite":                    "agent session index",
		".codex/agents/agent-1/sessions/2026/09/22/rollout.jsonl": `{"message":"agent transcript"}`,
		".claude/.credentials.json":                               `{"claudeAiOauth":"secret"}`,
		".claude/projects/workspace/session.jsonl":                `{"message":"private runtime transcript"}`,
		".claude/mcp-tokens/server.json":                          `{"token":"mcp-secret"}`,
		".memoh-hermes/.env":                                      "OPENAI_API_KEY=legacy-secret\n",
		".hermes/auth.json":                                       `{"token":"legacy-secret"}`,
		".codex/config.toml":                                      "model = \"gpt-5.4-codex\"\n",
		".claude/settings.json":                                   `{"permissions":{"defaultMode":"default"}}`,
		"notes/readme.txt":                                        "hello\n",
	}
}

func writeWorkspaceArchiveFixture(t *testing.T, root string) {
	t.Helper()
	for path, content := range workspaceArchiveFixture() {
		writeTestFile(t, root, path, content)
	}
}

func assertWorkspaceDataInArchive(t *testing.T, names []string) {
	t.Helper()
	for path := range workspaceArchiveFixture() {
		if !hasArchiveName(names, path) {
			t.Fatalf("archive did not preserve %s: %v", path, names)
		}
	}
}

func assertWorkspaceDataOnDisk(t *testing.T, root string) {
	t.Helper()
	for path, content := range workspaceArchiveFixture() {
		assertPathContent(t, root, path, content)
	}
}

type dataIOBridgeProvider struct {
	legacyRouteTestService
	client *bridge.Client
}

func (p *dataIOBridgeProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	return p.client, nil
}

func newDataIOTestBridgeClient(t *testing.T, root string) *bridge.Client {
	t.Helper()
	listener := bufconn.Listen(16 * 1024 * 1024)
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(16*1024*1024),
		grpc.MaxSendMsgSize(16*1024*1024),
	)
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir:    root,
		WorkspaceRoot:     root,
		DataMount:         config.DefaultDataMount,
		AllowHostAbsolute: true,
	}))
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///workspace-dataio-test",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn)
}

func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func buildWorkspaceArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		header := &tar.Header{
			Name: name,
			Mode: 0o600,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func assertPathContent(t *testing.T, root, rel, want string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	got, err := os.ReadFile(path) //nolint:gosec // test path under temp dir
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", rel, string(got), want)
	}
}

func tarGzNames(t *testing.T, raw []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var names []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
}

func hasArchiveName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
