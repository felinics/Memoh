package containerfs

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

type legacyTestClientProvider struct{ client *bridge.Client }

func (p legacyTestClientProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	return p.client, nil
}

func newLegacyTestProvider(t *testing.T, root string) *Provider {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir: root,
		WorkspaceRoot:  root,
		DataMount:      "/data",
	}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(lis)
	}()
	t.Cleanup(func() {
		server.Stop()
		<-done
	})

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return New(legacyTestClientProvider{client: bridge.NewClientFromConn(conn)})
}

func writeLegacyTestFile(t *testing.T, filePath, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(filePath), err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", filePath, err)
	}
}

func TestProvider_LegacyMediaTransition(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	provider := newLegacyTestProvider(t, root)
	ctx := context.Background()
	const key = "bot-1/aa/asset.png"
	legacyPath := filepath.Join(root, "media", "aa", "asset.png")
	currentPath := filepath.Join(root, ".memoh", "media", "aa", "asset.png")
	writeLegacyTestFile(t, legacyPath, "legacy")

	reader, err := provider.Open(ctx, key)
	if err != nil {
		t.Fatalf("Open legacy media: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read legacy media: %v", err)
	}
	_ = reader.Close()
	if got := string(data); got != "legacy" {
		t.Fatalf("legacy media content = %q, want legacy", got)
	}
	if got := provider.AccessPath(ctx, key); got != "/data/media/aa/asset.png" {
		t.Fatalf("legacy AccessPath = %q, want /data/media/aa/asset.png", got)
	}
	keys, err := provider.ListPrefix(ctx, "bot-1/aa/asset")
	if err != nil || len(keys) != 1 || keys[0] != key {
		t.Fatalf("legacy ListPrefix = %v, %v; want [%s]", keys, err, key)
	}

	writeLegacyTestFile(t, currentPath, "current")
	reader, err = provider.Open(ctx, key)
	if err != nil {
		t.Fatalf("Open current media: %v", err)
	}
	data, err = io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read current media: %v", err)
	}
	_ = reader.Close()
	if got := string(data); got != "current" {
		t.Fatalf("current media content = %q, want current", got)
	}
	if got := provider.AccessPath(ctx, key); got != "/data/.memoh/media/aa/asset.png" {
		t.Fatalf("current AccessPath = %q, want /data/.memoh/media/aa/asset.png", got)
	}
	keys, err = provider.ListPrefix(ctx, "bot-1/aa/asset")
	if err != nil || len(keys) != 1 || keys[0] != key {
		t.Fatalf("deduplicated ListPrefix = %v, %v; want [%s]", keys, err, key)
	}

	if err := provider.Delete(ctx, key); err != nil {
		t.Fatalf("Delete current and legacy media: %v", err)
	}
	for _, deletedPath := range []string{currentPath, legacyPath} {
		if _, err := os.Stat(deletedPath); !os.IsNotExist(err) {
			t.Fatalf("deleted media %s still exists or stat failed: %v", deletedPath, err)
		}
	}

	const newKey = "bot-1/bb/new.png"
	if err := provider.Put(ctx, newKey, bytes.NewBufferString("new")); err != nil {
		t.Fatalf("Put new media: %v", err)
	}
	currentNewPath := filepath.Join(root, ".memoh", "media", "bb", "new.png")
	if data, err := os.ReadFile(currentNewPath); err != nil || string(data) != "new" { //nolint:gosec // G304: path is under t.TempDir.
		t.Fatalf("current media write = %q, %v; want new", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "media", "bb", "new.png")); !os.IsNotExist(err) {
		t.Fatalf("new media unexpectedly written to legacy root: %v", err)
	}
}
