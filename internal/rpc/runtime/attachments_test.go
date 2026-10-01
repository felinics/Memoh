package runtime

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // compatibility digest required by the Weixin upload protocol
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/rpc/runtimepb"
)

type attachmentFixture struct {
	botID       string
	container   string
	contentHash string
	data        []byte
	asset       media.Asset
	ingestErr   error
	openCalls   int
}

func (f *attachmentFixture) Resolve(_ context.Context, botID, contentHash string) (media.Asset, error) {
	if botID != f.botID || contentHash != f.contentHash {
		return media.Asset{}, media.ErrAssetNotFound
	}
	return f.asset, nil
}

func (f *attachmentFixture) IngestContainerFile(_ context.Context, botID, containerPath string) (media.Asset, error) {
	if botID != f.botID || containerPath != f.container {
		return media.Asset{}, media.ErrAssetNotFound
	}
	if f.ingestErr != nil {
		return media.Asset{}, f.ingestErr
	}
	return f.asset, nil
}

func (f *attachmentFixture) Open(_ context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error) {
	if botID != f.botID || contentHash != f.contentHash {
		return nil, media.Asset{}, media.ErrAssetNotFound
	}
	f.openCalls++
	return io.NopCloser(bytes.NewReader(f.data)), f.asset, nil
}

type attachmentChunkStream struct {
	ctx    context.Context
	chunks [][]byte
	err    error
}

func (s *attachmentChunkStream) Send(chunk *runtimepb.AttachmentChunk) error {
	if s.err != nil {
		return s.err
	}
	s.chunks = append(s.chunks, append([]byte(nil), chunk.GetData()...))
	return nil
}
func (*attachmentChunkStream) SetHeader(metadata.MD) error  { return nil }
func (*attachmentChunkStream) SendHeader(metadata.MD) error { return nil }
func (*attachmentChunkStream) SetTrailer(metadata.MD)       {}
func (s *attachmentChunkStream) Context() context.Context   { return s.ctx }
func (*attachmentChunkStream) SendMsg(any) error            { return nil }
func (*attachmentChunkStream) RecvMsg(any) error            { return nil }

func newAttachmentFixture(data []byte) *attachmentFixture {
	contentHash := strings.Repeat("a", 64)
	return &attachmentFixture{
		botID: "bot-1", container: "/data/report.txt", contentHash: contentHash, data: data,
		asset: media.Asset{BotID: "bot-1", ContentHash: contentHash, StorageKey: "aa/" + contentHash + ".txt", Mime: "text/plain", SizeBytes: int64(len(data))},
	}
}

func TestResolveAttachmentCalculatesRawMD5ForStoredAndWorkspaceSources(t *testing.T) {
	fixture := newAttachmentFixture([]byte("payload"))
	srv := NewServer(nil, nil, fixture)
	wantMD5 := md5.Sum(fixture.data) //nolint:gosec
	for _, req := range []*runtimepb.ResolveAttachmentRequest{
		{BotId: fixture.botID, ContentHash: fixture.contentHash},
		{BotId: fixture.botID, ContainerPath: fixture.container},
	} {
		got, err := srv.ResolveAttachment(context.Background(), req)
		if err != nil {
			t.Fatalf("ResolveAttachment(%+v): %v", req, err)
		}
		if got.GetRawMd5() != hex.EncodeToString(wantMD5[:]) || got.GetSizeBytes() != int64(len(fixture.data)) {
			t.Fatalf("metadata = md5 %q size %d", got.GetRawMd5(), got.GetSizeBytes())
		}
	}
}

func TestResolveAttachmentReusesExistingMetadata(t *testing.T) {
	fixture := newAttachmentFixture([]byte("payload"))
	md5sum := md5.Sum(fixture.data) //nolint:gosec
	fixture.asset.RawMD5 = hex.EncodeToString(md5sum[:])
	srv := NewServer(nil, nil, fixture)

	got, err := srv.ResolveAttachment(context.Background(), &runtimepb.ResolveAttachmentRequest{
		BotId: fixture.botID, ContainerPath: fixture.container,
	})
	if err != nil {
		t.Fatalf("ResolveAttachment: %v", err)
	}
	if got.GetRawMd5() != fixture.asset.RawMD5 || got.GetSizeBytes() != fixture.asset.SizeBytes {
		t.Fatalf("metadata = md5 %q size %d, want md5 %q size %d", got.GetRawMd5(), got.GetSizeBytes(), fixture.asset.RawMD5, fixture.asset.SizeBytes)
	}
	if fixture.openCalls != 0 {
		t.Fatalf("opened stored asset %d times, want no extra read when metadata is present", fixture.openCalls)
	}
}

func TestResolveAttachmentRejectsInvalidSourcesAndPaths(t *testing.T) {
	srv := NewServer(nil, nil, newAttachmentFixture([]byte("payload")))
	tests := []struct {
		name string
		req  *runtimepb.ResolveAttachmentRequest
	}{
		{name: "missing source", req: &runtimepb.ResolveAttachmentRequest{BotId: "bot-1"}},
		{name: "both sources", req: &runtimepb.ResolveAttachmentRequest{BotId: "bot-1", ContentHash: strings.Repeat("a", 64), ContainerPath: "/data/report.txt"}},
		{name: "invalid hash", req: &runtimepb.ResolveAttachmentRequest{BotId: "bot-1", ContentHash: "../../etc/passwd"}},
		{name: "path outside data", req: &runtimepb.ResolveAttachmentRequest{BotId: "bot-1", ContainerPath: "/etc/passwd"}},
		{name: "path traversal", req: &runtimepb.ResolveAttachmentRequest{BotId: "bot-1", ContainerPath: "/data/../etc/passwd"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := srv.ResolveAttachment(context.Background(), tt.req)
			if status.Code(err).String() != "InvalidArgument" {
				t.Fatalf("status = %v, want InvalidArgument", status.Code(err))
			}
		})
	}
}

func TestResolveAttachmentLogsWorkspaceIngestFailureWithoutPath(t *testing.T) {
	fixture := newAttachmentFixture([]byte("payload"))
	fixture.ingestErr = errors.New("open /data/report.txt: permission denied")
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	srv := NewServer(logger, nil, fixture)

	_, err := srv.ResolveAttachment(context.Background(), &runtimepb.ResolveAttachmentRequest{
		BotId: fixture.botID, ContainerPath: fixture.container,
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("status = %v, want Internal", status.Code(err))
	}
	logText := logs.String()
	for _, want := range []string{"stage=ingest_workspace_file", "source_kind=workspace_path", "permission denied"} {
		if !strings.Contains(logText, want) {
			t.Errorf("log %q does not contain %q", logText, want)
		}
	}
	if strings.Contains(logText, fixture.container) {
		t.Fatalf("log contains workspace path %q: %s", fixture.container, logText)
	}
}

func TestReadAttachmentStreamsBoundedChunks(t *testing.T) {
	data := bytes.Repeat([]byte("m"), attachmentChunkSize*2+17)
	fixture := newAttachmentFixture(data)
	stream := &attachmentChunkStream{ctx: context.Background()}
	srv := NewServer(nil, nil, fixture)
	if err := srv.ReadAttachment(&runtimepb.ReadAttachmentRequest{BotId: fixture.botID, ContentHash: fixture.contentHash}, stream); err != nil {
		t.Fatalf("ReadAttachment: %v", err)
	}
	var got []byte
	for _, chunk := range stream.chunks {
		if len(chunk) == 0 || len(chunk) > attachmentChunkSize {
			t.Fatalf("chunk size = %d, want 1..%d", len(chunk), attachmentChunkSize)
		}
		got = append(got, chunk...)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("streamed %d bytes, want %d", len(got), len(data))
	}
}

func TestResolveAttachmentRejectsOversizedAsset(t *testing.T) {
	fixture := newAttachmentFixture([]byte("payload"))
	fixture.asset.SizeBytes = media.MaxAssetBytes + 1
	srv := NewServer(nil, nil, fixture)
	_, err := srv.ResolveAttachment(context.Background(), &runtimepb.ResolveAttachmentRequest{BotId: fixture.botID, ContentHash: fixture.contentHash})
	if status.Code(err).String() != "ResourceExhausted" {
		t.Fatalf("status = %v, want ResourceExhausted", status.Code(err))
	}
}

func TestReadAttachmentReturnsSendError(t *testing.T) {
	fixture := newAttachmentFixture([]byte("payload"))
	sendErr := context.Canceled
	stream := &attachmentChunkStream{ctx: context.Background(), err: sendErr}
	srv := NewServer(nil, nil, fixture)
	if err := srv.ReadAttachment(&runtimepb.ReadAttachmentRequest{BotId: fixture.botID, ContentHash: fixture.contentHash}, stream); !errors.Is(err, sendErr) {
		t.Fatalf("ReadAttachment error = %v, want %v", err, sendErr)
	}
}

func TestClientResolvesAndReadsAttachmentOverGRPC(t *testing.T) {
	data := bytes.Repeat([]byte("stream"), attachmentChunkSize/6+13)
	fixture := newAttachmentFixture(data)
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	runtimepb.RegisterRuntimeServiceServer(server, NewServer(nil, nil, fixture))
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
	client := NewClient(conn)
	asset, err := client.ResolveAttachment(context.Background(), fixture.botID, fixture.contentHash, "")
	if err != nil {
		t.Fatalf("ResolveAttachment: %v", err)
	}
	wantMD5 := md5.Sum(data) //nolint:gosec
	if asset.RawMD5 != hex.EncodeToString(wantMD5[:]) || asset.SizeBytes != int64(len(data)) {
		t.Fatalf("resolved metadata = %+v", asset)
	}
	reader, err := client.OpenAttachment(context.Background(), fixture.botID, fixture.contentHash)
	if err != nil {
		t.Fatalf("OpenAttachment: %v", err)
	}
	defer func() { _ = reader.Close() }()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read attachment stream: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("streamed %d bytes, want %d", len(got), len(data))
	}
}
