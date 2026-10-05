package storageruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/storage/providers/containerfs"
	"github.com/felinics/memoh/internal/storage/providers/fallback"
	"github.com/felinics/memoh/internal/storage/providers/localfs"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

// Exercise the production bridge, Server storage, RPC proxy, media service,
// and attachment preparer with no shared filesystem in the Channel process.
func TestChannelPreparesWorkspaceAttachmentsThroughServerStorage(t *testing.T) {
	root := t.TempDir()
	workspace := newWorkspaceBridge(t, root)
	backend := fallback.New(containerfs.New(workspaceBridgeProvider{client: workspace}), localfs.New(t.TempDir()))
	provider, _, cleanup := newTestProvider(t, backend)
	defer cleanup()
	store := media.NewService(nil, provider)
	ctx := context.Background()
	payload := []byte("workspace attachment payload")
	if err := os.MkdirAll(filepath.Join(root, "generated"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "generated/report.txt"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	prepared, err := channel.PrepareOutboundMessage(ctx, store, channel.ChannelConfig{BotID: "bot-1", ChannelType: channel.ChannelTypeQQ}, channel.OutboundMessage{Message: channel.Message{Attachments: []channel.Attachment{{Type: channel.AttachmentFile, Path: "/data/generated/report.txt"}}}})
	if err != nil {
		t.Fatalf("prepare workspace attachment: %v", err)
	}
	att := prepared.Message.Attachments[0]
	reader, err := att.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("uploaded bytes = %q, %v", got, err)
	}
	if att.Logical.ContentHash == "" || att.Logical.Path != "/data/.memoh/media/"+att.Logical.Metadata["storage_key"].(string) {
		t.Fatalf("attachment lost its Server-owned asset/path: %+v", att.Logical)
	}

	// A stored asset may exist only in the legacy media directory. Querying the
	// current path must use storage-key resolution before attempting raw ingest.
	legacy := []byte("legacy attachment")
	sha := sha256.Sum256(legacy)
	hash := hex.EncodeToString(sha[:])
	key := hash[:2] + "/" + hash + ".txt"
	legacyPath := filepath.Join(root, "media", filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err = channel.PrepareOutboundMessage(ctx, store, channel.ChannelConfig{BotID: "bot-1", ChannelType: channel.ChannelTypeQQ}, channel.OutboundMessage{Message: channel.Message{Attachments: []channel.Attachment{{Type: channel.AttachmentFile, Path: "/data/.memoh/media/" + key}}}})
	if err != nil {
		t.Fatalf("prepare legacy media alias: %v", err)
	}
	reader, err = prepared.Message.Attachments[0].Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || !bytes.Equal(got, legacy) {
		t.Fatalf("legacy uploaded bytes = %q, %v", got, err)
	}
	if _, err := provider.OpenContainerFile(ctx, "bot-1", "/data/generated/missing.txt"); err == nil || strings.Contains(err.Error(), "missing.txt") {
		t.Fatalf("missing workspace file error = %v, want a sanitized failure", err)
	}
}

type workspaceBridgeProvider struct{ client *bridge.Client }

func (p workspaceBridgeProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	return p.client, nil
}

func newWorkspaceBridge(t *testing.T, root string) *bridge.Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{DefaultWorkDir: root, WorkspaceRoot: root, DataMount: "/data"}))
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///workspace", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); server.Stop(); _ = listener.Close() })
	return bridge.NewClientFromConn(conn)
}
