package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/memohai/memoh/domains/runtime/bridge/bridgepb"
)

const testBufSize = 1 << 20

type rawReadTestServer struct {
	bridgepb.UnimplementedContainerServiceServer
	files map[string][]byte
}

type tunnelEchoTestServer struct {
	bridgepb.UnimplementedContainerServiceServer
}

type execCancelTestServer struct {
	bridgepb.UnimplementedContainerServiceServer
	started   chan struct{}
	cancelled chan struct{}
}

type failAfterDataReader struct {
	sent bool
}

func (r *failAfterDataReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errors.New("injected reader failure")
	}
	r.sent = true
	return copy(p, "replacement"), nil
}

type writeRawAbortProbeServer struct {
	bridgepb.UnimplementedContainerServiceServer
	handlerDone chan struct{}
	sawAbort    bool
}

func (s *writeRawAbortProbeServer) WriteRaw(stream bridgepb.ContainerService_WriteRawServer) error {
	defer close(s.handlerDone)
	for {
		chunk, err := stream.Recv()
		if err != nil {
			return err
		}
		if chunk.GetAbort() {
			s.sawAbort = true
			return status.Error(codes.Aborted, "write aborted")
		}
	}
}

type writeRawSendFailServer struct {
	bridgepb.UnimplementedContainerServiceServer
	handlerDone chan struct{}
}

func (s *writeRawSendFailServer) WriteRaw(stream bridgepb.ContainerService_WriteRawServer) error {
	defer close(s.handlerDone)
	if _, err := stream.Recv(); err != nil {
		return err
	}
	return status.Error(codes.ResourceExhausted, "injected server failure")
}

type metadataCaptureTestServer struct {
	bridgepb.UnimplementedContainerServiceServer
	unary  chan metadata.MD
	stream chan metadata.MD
}

func (s *metadataCaptureTestServer) Stat(ctx context.Context, _ *bridgepb.StatRequest) (*bridgepb.StatResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.unary <- md
	return &bridgepb.StatResponse{Entry: &bridgepb.FileEntry{IsDir: true}}, nil
}

func (s *metadataCaptureTestServer) Exec(stream bridgepb.ContainerService_ExecServer) error {
	md, _ := metadata.FromIncomingContext(stream.Context())
	s.stream <- md
	if _, err := stream.Recv(); err != nil {
		return err
	}
	return stream.Send(&bridgepb.ExecOutput{Stream: bridgepb.ExecOutput_EXIT})
}

func newExecCancelTestServer() *execCancelTestServer {
	return &execCancelTestServer{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
}

func (s *execCancelTestServer) Exec(stream bridgepb.ContainerService_ExecServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	close(s.started)
	<-stream.Context().Done()
	close(s.cancelled)
	return stream.Context().Err()
}

func (s *rawReadTestServer) ReadRaw(req *bridgepb.ReadRawRequest, stream bridgepb.ContainerService_ReadRawServer) error {
	data, ok := s.files[req.GetPath()]
	if !ok {
		return status.Errorf(codes.NotFound, "open: open %s: no such file or directory", req.GetPath())
	}
	if len(data) == 0 {
		return nil
	}
	if err := stream.Send(&bridgepb.DataChunk{Data: data[:1]}); err != nil {
		return err
	}
	if len(data) > 1 {
		if err := stream.Send(&bridgepb.DataChunk{Data: data[1:]}); err != nil {
			return err
		}
	}
	return nil
}

func (*tunnelEchoTestServer) Tunnel(stream bridgepb.ContainerService_TunnelServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.GetOpen() == nil {
		return status.Error(codes.InvalidArgument, "expected tunnel open")
	}
	if err := stream.Send(&bridgepb.TunnelFrame{Frame: &bridgepb.TunnelFrame_Data{Data: &bridgepb.TunnelData{}}}); err != nil {
		return err
	}
	for {
		frame, err := stream.Recv()
		if err != nil {
			return nil
		}
		switch payload := frame.GetFrame().(type) {
		case *bridgepb.TunnelFrame_Data:
			if data := payload.Data.GetData(); len(data) > 0 {
				if err := stream.Send(&bridgepb.TunnelFrame{Frame: &bridgepb.TunnelFrame_Data{Data: &bridgepb.TunnelData{Data: data}}}); err != nil {
					return err
				}
			}
		case *bridgepb.TunnelFrame_Close:
			return nil
		}
	}
}

func newTestClient(t *testing.T, server bridgepb.ContainerServiceServer) *Client {
	t.Helper()

	lis := bufconn.Listen(testBufSize)
	srv := grpc.NewServer()
	bridgepb.RegisterContainerServiceServer(srv, server)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.Stop()
		<-done
	})

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return NewClientFromConn(conn)
}

func newTestReadRawClient(t *testing.T, files map[string][]byte) *Client {
	t.Helper()
	return newTestClient(t, &rawReadTestServer{files: files})
}

func TestClientReadRawMissingFileReturnsNotFoundImmediately(t *testing.T) {
	t.Parallel()

	client := newTestReadRawClient(t, map[string][]byte{})
	_, err := client.ReadRaw(context.Background(), "/data/media/missing.jpg")
	if err == nil {
		t.Fatal("expected read raw to fail for missing file")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestClientReadRawPreservesFirstChunk(t *testing.T) {
	t.Parallel()

	client := newTestReadRawClient(t, map[string][]byte{
		"/data/media/existing.jpg": []byte("hello"),
	})
	reader, err := client.ReadRaw(context.Background(), "/data/media/existing.jpg")
	if err != nil {
		t.Fatalf("ReadRaw returned error: %v", err)
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read raw reader failed: %v", err)
	}
	if got := string(data); got != "hello" {
		t.Fatalf("expected full payload, got %q", got)
	}
}

func TestClientReadRawSupportsEmptyFile(t *testing.T) {
	t.Parallel()

	client := newTestReadRawClient(t, map[string][]byte{
		"/data/media/empty.txt": {},
	})
	reader, err := client.ReadRaw(context.Background(), "/data/media/empty.txt")
	if err != nil {
		t.Fatalf("ReadRaw returned error: %v", err)
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read raw empty reader failed: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("expected empty payload, got %q", string(data))
	}
}

func TestClientTunnelSurvivesDialContextCancellation(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, &tunnelEchoTestServer{})
	dialCtx, cancel := context.WithCancel(context.Background())
	conn, err := client.DialContext(dialCtx, "tcp", "127.0.0.1:9222")
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer func() { _ = conn.Close() }()
	cancel()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write after dial context cancellation failed: %v", err)
	}
	buf := make([]byte, 4)
	n, err := io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("read after dial context cancellation failed: %v", err)
	}
	if got := string(buf[:n]); got != "ping" {
		t.Fatalf("expected echoed payload, got %q", got)
	}
}

func TestClientWriteRawReaderFailureSendsAbortWaitsAndPreservesError(t *testing.T) {
	t.Parallel()

	server := &writeRawAbortProbeServer{handlerDone: make(chan struct{})}
	client := newTestClient(t, server)

	_, err := client.WriteRaw(context.Background(), "/data/media/asset.txt", &failAfterDataReader{})
	if err == nil {
		t.Fatal("WriteRaw() error = nil, want reader failure")
	}
	if got := err.Error(); got != "injected reader failure" {
		t.Fatalf("WriteRaw() error = %v, want injected reader failure", err)
	}
	if !server.sawAbort {
		t.Fatal("server did not receive abort termination frame")
	}

	select {
	case <-server.handlerDone:
	default:
		t.Fatal("WriteRaw returned before server handler finished")
	}
}

func TestClientWriteRawSendFailureWaitsForServerAndPreservesError(t *testing.T) {
	t.Parallel()

	server := &writeRawSendFailServer{handlerDone: make(chan struct{})}
	client := newTestClient(t, server)

	payload := bytes.Repeat([]byte("x"), 128*1024)
	_, err := client.WriteRaw(context.Background(), "/data/media/asset.txt", bytes.NewReader(payload))
	if err == nil {
		t.Fatal("WriteRaw() error = nil, want send/stream failure")
	}
	if status.Code(err) != codes.ResourceExhausted && !errors.Is(err, io.EOF) {
		t.Fatalf("WriteRaw() error = %v, want ResourceExhausted or io.EOF", err)
	}

	select {
	case <-server.handlerDone:
	default:
		t.Fatal("WriteRaw returned before server handler finished")
	}
}

func TestExecStreamCloseCancelsServerContext(t *testing.T) {
	server := newExecCancelTestServer()
	client := newTestClient(t, server)
	stream, err := client.ExecStreamWithEnv(context.Background(), "sleep 60", "/tmp", -1, nil)
	if err != nil {
		t.Fatalf("ExecStreamWithEnv returned error: %v", err)
	}
	select {
	case <-server.started:
	case <-time.After(10 * time.Second):
		t.Fatal("server stream did not receive exec config")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	select {
	case <-server.cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("server stream context was not cancelled after ExecStream.Close")
	}
}

func TestClientWithOutgoingMetadataScopesUnaryAndStreamingWithoutOwningConnection(t *testing.T) {
	server := &metadataCaptureTestServer{
		unary: make(chan metadata.MD, 2), stream: make(chan metadata.MD, 1),
	}
	root := newTestClient(t, server)
	scoped := root.WithOutgoingMetadata(map[string]string{
		"x-memoh-workspace-id":   "11111111-1111-4111-8111-111111111111",
		"x-memoh-workspace-path": "bots/one",
	})
	if scoped == nil {
		t.Fatal("WithOutgoingMetadata returned nil")
	}
	if _, err := scoped.Stat(context.Background(), "/"); err != nil {
		t.Fatalf("scoped Stat: %v", err)
	}
	assertMetadataValue(t, <-server.unary, "x-memoh-workspace-path", "bots/one")

	result, err := scoped.Exec(context.Background(), "true", "/data", 5)
	if err != nil {
		t.Fatalf("scoped Exec: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("scoped Exec exit code = %d", result.ExitCode)
	}
	assertMetadataValue(t, <-server.stream, "x-memoh-workspace-id", "11111111-1111-4111-8111-111111111111")

	if err := scoped.Close(); err != nil {
		t.Fatalf("scoped Close: %v", err)
	}
	if _, err := root.Stat(context.Background(), "/"); err != nil {
		t.Fatalf("root client was closed by scoped view: %v", err)
	}
	assertMetadataValue(t, <-server.unary, "x-memoh-workspace-id", "")
}

func assertMetadataValue(t *testing.T, md metadata.MD, key, want string) {
	t.Helper()
	values := md.Get(key)
	if want == "" {
		if len(values) != 0 {
			t.Fatalf("metadata %s = %v, want absent", key, values)
		}
		return
	}
	if len(values) != 1 || values[0] != want {
		t.Fatalf("metadata %s = %v, want %q", key, values, want)
	}
}
