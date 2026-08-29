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

func TestTarGzDirSkipsHermesSecrets(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceArchiveFixture(t, root)
	writeTestSymlink(t, root, "../.memoh-hermes/.env", "notes/env-link")

	var buf bytes.Buffer
	if err := tarGzDir(&buf, root, false); err != nil {
		t.Fatalf("tarGzDir error = %v", err)
	}

	names := tarGzNames(t, buf.Bytes())
	assertNoWorkspaceSecretsInArchive(t, names)
	assertNoWorkspaceSecretAliasesInArchive(t, names)
	assertWorkspaceUserDataInArchive(t, names)
}

func TestExportDataViaGRPCSkipsHermesSecrets(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceArchiveFixture(t, root)
	writeTestSymlink(t, root, "../.memoh-hermes/.env", "notes/env-link")

	client := newDataIOTestBridgeClient(t, root)
	manager := &Manager{service: &dataIOBridgeProvider{client: client}}
	reader, err := manager.exportDataViaGRPC(context.Background(), "bot-1", false)
	if err != nil {
		t.Fatalf("exportDataViaGRPC error = %v", err)
	}
	defer func() { _ = reader.Close() }()
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}

	names := tarGzNames(t, raw)
	assertNoWorkspaceSecretsInArchive(t, names)
	assertNoWorkspaceSecretAliasesInArchive(t, names)
	assertWorkspaceUserDataInArchive(t, names)
}

func TestImportDataViaGRPCSkipsHermesSecrets(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceSecretFixture(t, root)
	writeTestFile(t, root, ".memoh-hermes/mcp-tokens/stale-target.json", `{"refresh_token":"old"}`)
	writeTestFile(t, root, ".hermes/auth/stale-target.json", `{"refresh_token":"old"}`)
	client := newDataIOTestBridgeClient(t, root)
	manager := &Manager{service: &dataIOBridgeProvider{client: client}}
	raw := buildWorkspaceArchive(t, workspaceArchiveFixture())

	if err := manager.importDataViaGRPC(context.Background(), "bot-1", bytes.NewReader(raw), false); err != nil {
		t.Fatalf("importDataViaGRPC error = %v", err)
	}

	assertWorkspaceSecretsMissingOnDisk(t, root)
	assertPathMissing(t, root, ".memoh-hermes/mcp-tokens/stale-target.json")
	assertPathMissing(t, root, ".hermes/auth/stale-target.json")
	assertWorkspaceUserDataOnDisk(t, root)
}

func TestUntarGzDirSkipsHermesSecrets(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceSecretFixture(t, root)
	writeTestFile(t, root, ".memoh-hermes/mcp-tokens/stale-target.json", `{"refresh_token":"old"}`)
	writeTestFile(t, root, ".hermes/auth/stale-target.json", `{"refresh_token":"old"}`)
	raw := buildWorkspaceArchive(t, workspaceArchiveFixture())

	if err := untarGzDir(bytes.NewReader(raw), root, false); err != nil {
		t.Fatalf("untarGzDir error = %v", err)
	}

	assertWorkspaceSecretsMissingOnDisk(t, root)
	assertPathMissing(t, root, ".memoh-hermes/mcp-tokens/stale-target.json")
	assertPathMissing(t, root, ".hermes/auth/stale-target.json")
	assertWorkspaceUserDataOnDisk(t, root)
}

func TestPreservedDataKeepsCredentialsAndSkipsRuntimeFiles(t *testing.T) {
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
	if err := tarGzDir(&archive, root, true); err != nil {
		t.Fatal(err)
	}
	names := tarGzNames(t, archive.Bytes())
	if !hasArchiveName(names, ".codex/auth.json") {
		t.Fatalf("archive did not preserve Codex credentials: %v", names)
	}
	for _, path := range []string{".memoh-hermes/sessions/latest.json", ".memoh-hermes/state.db", "runtime.sock"} {
		if hasArchiveName(names, path) {
			t.Fatalf("archive included runtime file %s: %v", path, names)
		}
	}

	restored := t.TempDir()
	if err := untarGzDir(bytes.NewReader(archive.Bytes()), restored, true); err != nil {
		t.Fatal(err)
	}
	assertPathContent(t, restored, ".codex/auth.json", `{"OPENAI_API_KEY":"secret"}`)
}

func workspaceArchiveFixture() map[string]string {
	files := workspaceSecretFiles()
	files[".memoh-hermes/config.yaml"] = "model:\n  provider: openrouter\n"
	files[".hermes/config.yaml"] = "model:\n  provider: openrouter\n"
	files[".codex/config.toml"] = "model = \"gpt-5.4-codex\"\n"
	files["notes/readme.txt"] = "hello\n"
	return files
}

func workspaceSecretFiles() map[string]string {
	return map[string]string{
		".memoh-hermes/.env":                       "OPENROUTER_API_KEY=secret\n",
		".memoh-hermes/auth.json":                  `{"token":"secret"}`,
		".memoh-hermes/auth/google_oauth.json":     `{"refresh_token":"secret"}`,
		".memoh-hermes/mcp-tokens/github.json":     `{"refresh_token":"secret"}`,
		".memoh-hermes/sessions/latest.json":       `{"token":"secret"}`,
		".memoh-hermes/state.db":                   "secret",
		".memoh-hermes/state.db-wal":               "secret",
		".memoh-hermes/accounts/default/.env":      "OPENAI_API_KEY=secret\n",
		".memoh-hermes/accounts/default/auth.json": `{"token":"secret"}`,
		".hermes/.env":                             "OPENAI_API_KEY=secret\n",
		".hermes/auth.json":                        `{"token":"secret"}`,
		".hermes/auth/google_oauth.json":           `{"refresh_token":"secret"}`,
		".hermes/mcp-tokens/github.json":           `{"refresh_token":"secret"}`,
		".hermes/sessions/latest.json":             `{"token":"secret"}`,
		".hermes/state.db-shm":                     "secret",
		".hermes/accounts/default/.env":            "OPENAI_API_KEY=secret\n",
		".hermes/accounts/default/auth.json":       `{"token":"secret"}`,
		".codex/auth.json":                         `{"OPENAI_API_KEY":"secret"}`,
		".codex/auth/token.json":                   `{"token":"secret"}`,
	}
}

func writeWorkspaceArchiveFixture(t *testing.T, root string) {
	t.Helper()
	for path, content := range workspaceArchiveFixture() {
		writeTestFile(t, root, path, content)
	}
}

func writeWorkspaceSecretFixture(t *testing.T, root string) {
	t.Helper()
	for path, content := range workspaceSecretFiles() {
		writeTestFile(t, root, path, content)
	}
}

func assertNoWorkspaceSecretsInArchive(t *testing.T, names []string) {
	t.Helper()
	for path := range workspaceSecretFiles() {
		if hasArchiveName(names, path) {
			t.Fatalf("archive leaked ACP secret %s: %v", path, names)
		}
	}
}

func assertNoWorkspaceSecretAliasesInArchive(t *testing.T, names []string) {
	t.Helper()
	if hasArchiveName(names, "notes/env-link") {
		t.Fatalf("archive included symlink alias to ACP secret: %v", names)
	}
}

func assertWorkspaceUserDataInArchive(t *testing.T, names []string) {
	t.Helper()
	for _, path := range []string{".memoh-hermes/config.yaml", ".hermes/config.yaml", ".codex/config.toml", "notes/readme.txt"} {
		if !hasArchiveName(names, path) {
			t.Fatalf("archive names = %v, want %s", names, path)
		}
	}
}

func assertWorkspaceSecretsMissingOnDisk(t *testing.T, root string) {
	t.Helper()
	for path := range workspaceSecretFiles() {
		assertPathMissing(t, root, path)
	}
}

func assertWorkspaceUserDataOnDisk(t *testing.T, root string) {
	t.Helper()
	assertPathContent(t, root, ".memoh-hermes/config.yaml", "model:\n  provider: openrouter\n")
	assertPathContent(t, root, ".hermes/config.yaml", "model:\n  provider: openrouter\n")
	assertPathContent(t, root, ".codex/config.toml", "model = \"gpt-5.4-codex\"\n")
	assertPathContent(t, root, "notes/readme.txt", "hello\n")
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

func writeTestSymlink(t *testing.T, root, target, rel string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
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

func assertPathMissing(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists or stat failed with non-not-exist error: %v", rel, err)
	}
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
