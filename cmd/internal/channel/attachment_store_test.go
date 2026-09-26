package channel

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // compatibility digest required by the Weixin upload protocol
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	domainchannel "github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/rpc/runtime"
	"github.com/felinics/memoh/internal/rpc/runtimepb"
	"github.com/felinics/memoh/internal/storage/providers/localfs"
)

type remoteAttachmentFixture struct {
	asset media.Asset
	data  []byte
	path  string
}

func (f *remoteAttachmentFixture) Resolve(_ context.Context, botID, contentHash string) (media.Asset, error) {
	if botID != f.asset.BotID || contentHash != f.asset.ContentHash {
		return media.Asset{}, media.ErrAssetNotFound
	}
	return f.asset, nil
}

func (f *remoteAttachmentFixture) IngestContainerFile(_ context.Context, botID, containerPath string) (media.Asset, error) {
	if botID != f.asset.BotID || containerPath != f.path {
		return media.Asset{}, media.ErrAssetNotFound
	}
	return f.asset, nil
}

func (f *remoteAttachmentFixture) Open(_ context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error) {
	if botID != f.asset.BotID || contentHash != f.asset.ContentHash {
		return nil, media.Asset{}, media.ErrAssetNotFound
	}
	return io.NopCloser(bytes.NewReader(f.data)), f.asset, nil
}

func TestOutboundAttachmentStoreReadsRemoteWorkspaceWithoutSharedMedia(t *testing.T) {
	data := []byte("remote workspace attachment")
	sha := sha256.Sum256(data)
	md5sum := md5.Sum(data) //nolint:gosec
	contentHash := hex.EncodeToString(sha[:])
	fixture := &remoteAttachmentFixture{
		data: data, path: "/data/generated/report.txt",
		asset: media.Asset{
			BotID: "bot-1", ContentHash: contentHash, Mime: "text/plain", SizeBytes: int64(len(data)),
			StorageKey: contentHash[:2] + "/" + contentHash + ".txt", RawMD5: hex.EncodeToString(md5sum[:]),
		},
	}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	runtimepb.RegisterRuntimeServiceServer(server, runtime.NewServer(nil, nil, fixture))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	local := media.NewService(nil, localfs.New(filepath.Join(t.TempDir(), "empty-local-media")))
	store := &outboundAttachmentStore{local: local, remote: runtime.NewClient(conn)}
	prepared, err := domainchannel.PrepareOutboundMessage(context.Background(), store, domainchannel.ChannelConfig{
		BotID: "bot-1", ChannelType: domainchannel.ChannelTypeQQ,
	}, domainchannel.OutboundMessage{Message: domainchannel.Message{Attachments: []domainchannel.Attachment{{
		Type: domainchannel.AttachmentFile, Path: fixture.path,
	}}}})
	if err != nil {
		t.Fatalf("PrepareOutboundMessage: %v", err)
	}
	att := prepared.Message.Attachments[0]
	if att.RawMD5 != fixture.asset.RawMD5 {
		t.Fatalf("prepared raw MD5 = %q, want %q", att.RawMD5, fixture.asset.RawMD5)
	}
	reader, err := att.Open(context.Background())
	if err != nil {
		t.Fatalf("open prepared attachment: %v", err)
	}
	defer func() { _ = reader.Close() }()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read prepared attachment: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("prepared content = %q, want %q", got, data)
	}
	if _, err := local.Stat(context.Background(), "bot-1", contentHash); err == nil {
		t.Fatal("remote attachment unexpectedly appeared in local media store")
	}
}

func TestLocalAttachmentStoreDoesNotRouteWorkspaceReadsRemotely(t *testing.T) {
	store := provideLocalChannelAttachmentStore(localAttachmentStoreParams{
		Local: media.NewService(nil, localfs.New(t.TempDir())),
	})
	if store.remote != nil {
		t.Fatal("server-local attachment store must not route workspace reads to the channel process")
	}
}
