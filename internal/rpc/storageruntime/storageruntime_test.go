package storageruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	intrpc "github.com/felinics/memoh/internal/rpc"
	"github.com/felinics/memoh/internal/rpc/storagepb"
	"github.com/felinics/memoh/internal/storage"
	"github.com/felinics/memoh/internal/storage/providers/fallback"
	"github.com/felinics/memoh/internal/storage/providers/localfs"
)

func TestProviderStreamsObjectsLargerThanUnaryLimit(t *testing.T) {
	root := t.TempDir()
	provider, pbClient, cleanup := newTestProvider(t, localfs.New(root))
	defer cleanup()

	// This exceeds the internal RPC's 16 MiB per-message cap. The transfer must
	// still succeed because only bounded chunks cross the wire.
	payload := bytes.Repeat([]byte("streamed-storage-payload"), (17<<20)/len("streamed-storage-payload")+1)
	payload = payload[:17<<20]
	key := "bot-1/aa/asset.bin"
	if err := provider.Put(context.Background(), key, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	reader, err := provider.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("streamed payload differs: got %d bytes, want %d", len(got), len(payload))
	}

	if gotPath := provider.AccessPath(context.Background(), key); gotPath != filepath.Join(root, filepath.FromSlash(key)) {
		t.Fatalf("AccessPath() = %q", gotPath)
	}
	keys, err := provider.ListPrefix(context.Background(), "bot-1/aa/asset")
	if err != nil {
		t.Fatalf("ListPrefix() error = %v", err)
	}
	if len(keys) != 1 || keys[0] != key {
		t.Fatalf("ListPrefix() = %#v", keys)
	}

	if _, err := provider.EnsureAccessPath(context.Background(), key); !errors.Is(err, storage.ErrAccessPathUnavailable) {
		t.Fatalf("EnsureAccessPath() error = %v, want ErrAccessPathUnavailable", err)
	}
	if err := provider.Delete(context.Background(), key); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := provider.Open(context.Background(), key); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open() after delete error = %v, want os.ErrNotExist", err)
	}

	stream, err := pbClient.Put(context.Background())
	if err != nil {
		t.Fatalf("raw Put() error = %v", err)
	}
	if err := stream.Send(&storagepb.PutRequest{Body: &storagepb.PutRequest_Chunk{Chunk: []byte("missing metadata")}}); err != nil {
		t.Fatalf("raw Put().Send() error = %v", err)
	}
	_, err = stream.CloseAndRecv()
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("raw Put() status = %v, want InvalidArgument", status.Code(err))
	}
}

func TestServerSpoolsPutForFallbackRetry(t *testing.T) {
	secondary := localfs.New(t.TempDir())
	backend := fallback.New(failingProvider{}, secondary)
	provider, _, cleanup := newTestProvider(t, backend)
	defer cleanup()

	payload := bytes.Repeat([]byte("fallback"), 128<<10)
	key := "bot-2/bb/fallback.bin"
	if err := provider.Put(context.Background(), key, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	reader, err := secondary.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("secondary.Open() error = %v", err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatalf("secondary ReadAll() error = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("fallback payload differs")
	}
}

func newTestProvider(t *testing.T, backend storage.Provider, options ...grpc.ServerOption) (*Provider, storagepb.StorageServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := intrpc.NewServer(nil, "test-secret", options...)
	storagepb.RegisterStorageServiceServer(server, NewServer(nil, backend))
	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithUnaryInterceptor(intrpc.UnaryClientAuth("test-secret")),
		grpc.WithStreamInterceptor(intrpc.StreamClientAuth("test-secret")),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(intrpc.MaxMessageBytes),
			grpc.MaxCallSendMsgSize(intrpc.MaxMessageBytes),
		),
	)
	if err != nil {
		server.Stop()
		_ = listener.Close()
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		server.Stop()
		_ = listener.Close()
	}
	return NewProvider(conn), storagepb.NewStorageServiceClient(conn), cleanup
}

type failingProvider struct{}

func (failingProvider) Put(_ context.Context, _ string, reader io.Reader) error {
	buf := make([]byte, 1024)
	_, _ = reader.Read(buf)
	return errors.New("primary unavailable")
}

func (failingProvider) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, os.ErrNotExist
}

func (failingProvider) Delete(context.Context, string) error { return nil }
func (failingProvider) AccessPath(context.Context, string) string {
	return ""
}

// A failed source must release the Server's receive stream and spool without
// waiting for an unrelated parent-context deadline.
func TestProviderSourceFailureCancelsPutStream(t *testing.T) {
	started, finished := make(chan struct{}), make(chan struct{})
	option := grpc.ChainStreamInterceptor(func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if info.FullMethod == storagepb.StorageService_Put_FullMethodName {
			close(started)
			defer close(finished)
		}
		return handler(srv, stream)
	})
	provider, _, cleanup := newTestProvider(t, localfs.New(t.TempDir()), option)
	defer cleanup()
	if err := provider.Put(t.Context(), "bot-1/aa/aborted.bin", failingSource{started: started}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("source error = %v, want io.ErrUnexpectedEOF", err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("source failure left the Server's Put stream open")
	}
}

type failingSource struct{ started <-chan struct{} }

func (r failingSource) Read([]byte) (int, error) {
	select {
	case <-r.started:
		return 0, io.ErrUnexpectedEOF
	case <-time.After(5 * time.Second):
		return 0, errors.New("Put stream did not start")
	}
}
