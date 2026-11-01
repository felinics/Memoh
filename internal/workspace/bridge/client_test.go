package bridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

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

const testBufSize = 1 << 20

type rawReadTestServer struct {
	pb.UnimplementedContainerServiceServer
	files map[string][]byte
}

type tunnelEchoTestServer struct {
	pb.UnimplementedContainerServiceServer
}

type execCancelTestServer struct {
	pb.UnimplementedContainerServiceServer
	started   chan struct{}
	cancelled chan struct{}
}

type metadataCaptureTestServer struct {
	pb.UnimplementedContainerServiceServer
	unary  chan metadata.MD
	stream chan metadata.MD
}

func (s *metadataCaptureTestServer) Stat(ctx context.Context, _ *pb.StatRequest) (*pb.StatResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.unary <- md
	return &pb.StatResponse{Entry: &pb.FileEntry{IsDir: true}}, nil
}

func (s *metadataCaptureTestServer) Exec(stream pb.ContainerService_ExecServer) error {
	md, _ := metadata.FromIncomingContext(stream.Context())
	s.stream <- md
	if _, err := stream.Recv(); err != nil {
		return err
	}
	return stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_EXIT})
}

func newExecCancelTestServer() *execCancelTestServer {
	return &execCancelTestServer{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
}

func (s *execCancelTestServer) Exec(stream pb.ContainerService_ExecServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	close(s.started)
	<-stream.Context().Done()
	close(s.cancelled)
	return stream.Context().Err()
}

func (s *rawReadTestServer) ReadRaw(req *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	data, ok := s.files[req.GetPath()]
	if !ok {
		return status.Errorf(codes.NotFound, "open: open %s: no such file or directory", req.GetPath())
	}
	if len(data) == 0 {
		return nil
	}
	if err := stream.Send(&pb.DataChunk{Data: data[:1]}); err != nil {
		return err
	}
	if len(data) > 1 {
		if err := stream.Send(&pb.DataChunk{Data: data[1:]}); err != nil {
			return err
		}
	}
	return nil
}

func (*tunnelEchoTestServer) Tunnel(stream pb.ContainerService_TunnelServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.GetOpen() == nil {
		return status.Error(codes.InvalidArgument, "expected tunnel open")
	}
	if err := stream.Send(&pb.TunnelFrame{Frame: &pb.TunnelFrame_Data{Data: &pb.TunnelData{}}}); err != nil {
		return err
	}
	for {
		frame, err := stream.Recv()
		if err != nil {
			return nil
		}
		switch payload := frame.GetFrame().(type) {
		case *pb.TunnelFrame_Data:
			if data := payload.Data.GetData(); len(data) > 0 {
				if err := stream.Send(&pb.TunnelFrame{Frame: &pb.TunnelFrame_Data{Data: &pb.TunnelData{Data: data}}}); err != nil {
					return err
				}
			}
		case *pb.TunnelFrame_Close:
			return nil
		}
	}
}

func newTestClient(t *testing.T, server pb.ContainerServiceServer) *Client {
	t.Helper()

	lis := bufconn.Listen(testBufSize)
	srv := grpc.NewServer()
	pb.RegisterContainerServiceServer(srv, server)

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

func TestClientRenameMissingSourceReturnsNotFound(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newTestClient(t, bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir: root,
		WorkspaceRoot:  root,
		DataMount:      "/data",
	}))
	err := client.Rename(context.Background(), "/data/skills/openai/docs", "/data/skills/.staging/openai/docs/backup")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Rename() error = %v, want ErrNotFound", err)
	}
}

func TestClientReadRawMissingFileReturnsNotFoundImmediately(t *testing.T) {
	t.Parallel()

	client := newTestReadRawClient(t, map[string][]byte{})
	_, err := client.ReadRaw(context.Background(), "/data/.memoh/media/missing.jpg")
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
		"/data/.memoh/media/existing.jpg": []byte("hello"),
	})
	reader, err := client.ReadRaw(context.Background(), "/data/.memoh/media/existing.jpg")
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
		"/data/.memoh/media/empty.txt": {},
	})
	reader, err := client.ReadRaw(context.Background(), "/data/.memoh/media/empty.txt")
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

func TestClientWriteRawSupportsEmptyFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newTestClient(t, bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir: root,
		WorkspaceRoot:  root,
		DataMount:      "/data",
	}))
	if _, err := client.WriteRaw(context.Background(), "/data/.memoh/media/empty.txt", bytes.NewReader(nil)); err != nil {
		t.Fatalf("WriteRaw() error = %v", err)
	}
	info, err := os.Stat(filepath.Join(root, ".memoh", "media", "empty.txt"))
	if err != nil {
		t.Fatalf("stat empty file: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("empty file size = %d, want 0", info.Size())
	}
}

func TestClientWriteRawDoesNotReplaceTargetOnReaderFailure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, ".memoh", "media", "asset.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	client := newTestClient(t, bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir: root,
		WorkspaceRoot:  root,
		DataMount:      "/data",
	}))
	if _, err := client.WriteRaw(context.Background(), "/data/.memoh/media/asset.txt", &failAfterDataReader{}); err == nil {
		t.Fatal("WriteRaw() error = nil, want reader failure")
	}

	// The abort frame makes cleanup synchronous: once WriteRaw returned, the
	// bridge has already discarded its temporary file and left the target
	// untouched, so no polling window is needed.
	data, readErr := os.ReadFile(target) //nolint:gosec // G304: target is constructed under t.TempDir.
	if readErr != nil || string(data) != "original" {
		t.Fatalf("target after aborted write = (%q, %v), want the original content", data, readErr)
	}
	temps, globErr := filepath.Glob(filepath.Join(filepath.Dir(target), ".asset.txt.tmp-*"))
	if globErr != nil || len(temps) != 0 {
		t.Fatalf("temporary files after aborted write = (%v, %v), want none", temps, globErr)
	}
}

// writeRawUnavailableTestServer ends every WriteRaw stream with Unavailable,
// after the path chunk or after the whole payload.
type writeRawUnavailableTestServer struct {
	pb.UnimplementedContainerServiceServer
	drain bool
}

func (s *writeRawUnavailableTestServer) WriteRaw(stream pb.ContainerService_WriteRawServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	for s.drain {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}
	return status.Error(codes.Unavailable, "bridge restarting")
}

func TestClientWriteRawMapsStreamStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		drain   bool
		payload int
	}{
		// The stream ends while chunks are still being sent, so Send fails.
		{name: "during send", payload: 8 << 20},
		// The stream ends after the payload, so CloseAndRecv fails.
		{name: "at close", drain: true, payload: 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := newTestClient(t, &writeRawUnavailableTestServer{drain: tc.drain})
			_, err := client.WriteRaw(context.Background(), "/data/file", bytes.NewReader(make([]byte, tc.payload)))
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("WriteRaw() error = %v, want ErrUnavailable", err)
			}
		})
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

// blockedTunnelStream models a peer that no longer drains its receive window.
// Both I/O directions remain blocked until the stream context is canceled.
type blockedTunnelStream struct {
	grpc.ClientStream
	ctx         context.Context
	sendStarted chan struct{}
	recvStarted chan struct{}
	sendOnce    sync.Once
}

func (s *blockedTunnelStream) Context() context.Context { return s.ctx }
func (*blockedTunnelStream) CloseSend() error           { return nil }

func (s *blockedTunnelStream) Send(*pb.TunnelFrame) error {
	s.sendOnce.Do(func() { close(s.sendStarted) })
	<-s.ctx.Done()
	return s.ctx.Err()
}

func (s *blockedTunnelStream) Recv() (*pb.TunnelFrame, error) {
	close(s.recvStarted)
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

func TestTunnelCloseUnblocksConcurrentReadAndWrite(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &blockedTunnelStream{ctx: ctx, sendStarted: make(chan struct{}), recvStarted: make(chan struct{})}
	conn := &tunnelConn{stream: stream, cancel: cancel}
	writeDone := make(chan error, 1)
	readDone := make(chan error, 1)
	go func() {
		_, err := conn.Write([]byte("blocked input"))
		writeDone <- err
	}()
	go func() {
		_, err := conn.Read(make([]byte, 1))
		readDone <- err
	}()
	<-stream.sendStarted
	<-stream.recvStarted
	closeDone := make(chan error, 1)
	go func() { closeDone <- conn.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close waited for blocked stream I/O instead of canceling it")
	}
	for name, done := range map[string]<-chan error{"read": readDone, "write": writeDone} {
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("%s succeeded after Close", name)
			}
		case <-time.After(time.Second):
			t.Fatalf("Close left %s blocked", name)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("repeated Close: %v", err)
	}
}

type tunnelCancellationTestServer struct {
	pb.UnimplementedContainerServiceServer
	canceled chan struct{}
}

func (s *tunnelCancellationTestServer) Tunnel(stream pb.ContainerService_TunnelServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.Send(&pb.TunnelFrame{Frame: &pb.TunnelFrame_Data{Data: &pb.TunnelData{}}}); err != nil {
		return err
	}
	<-stream.Context().Done()
	close(s.canceled)
	return stream.Context().Err()
}

func TestClientTunnelCloseCancelsServerStream(t *testing.T) {
	t.Parallel()
	server := &tunnelCancellationTestServer{canceled: make(chan struct{})}
	client := newTestClient(t, server)
	conn, err := client.DialContext(context.Background(), "tcp", "127.0.0.1:9222")
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.canceled:
	case <-time.After(time.Second):
		t.Fatal("Close left the server tunnel alive")
	}
}

func TestServeReverseHTTPForwardsRequests(t *testing.T) {
	broker := bridgesvc.NewReverseHTTPBroker()
	client := newTestClient(t, bridgesvc.New(bridgesvc.Options{ReverseHTTP: broker}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := client.ServeReverseHTTP(ctx, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Host != "127.0.0.1" {
			t.Errorf("request host = %q", req.Host)
		}
		if req.URL.Path != "/mcp" || req.URL.RawQuery != "q=1" {
			t.Errorf("request URL = %q", req.URL.RequestURI())
		}
		body, _ := io.ReadAll(req.Body)
		if string(body) != "ping" {
			t.Errorf("request body = %q", body)
		}
		if req.Header.Get("X-Test") != "ok" {
			t.Errorf("request header X-Test = %q", req.Header.Get("X-Test"))
		}
		w.Header().Set("X-Reply", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("pong"))
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	deadline := time.Now().Add(time.Second)
	for {
		req := httptest.NewRequest(http.MethodPost, "/mcp?q=1", strings.NewReader("ping"))
		req.Header.Set("X-Test", "ok")
		rec := httptest.NewRecorder()
		broker.ServeHTTP(rec, req)
		if rec.Code == http.StatusCreated {
			if rec.Body.String() != "pong" || rec.Header().Get("X-Reply") != "yes" {
				t.Fatalf("response body/header = %q/%q", rec.Body.String(), rec.Header().Get("X-Reply"))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reverse HTTP response code = %d body=%q", rec.Code, rec.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An agent may connect to a route the moment it is handed one, before the
// broker has registered the route's stream; the request waits for it.
func TestReverseHTTPRequestWaitsForItsRoute(t *testing.T) {
	broker := bridgesvc.NewReverseHTTPBroker()
	client := newTestClient(t, bridgesvc.New(bridgesvc.Options{ReverseHTTP: broker}))

	got := make(chan string, 1)
	go func() { got <- reverseHTTPTestRequest(t, broker, "/mcp/late") }()
	time.Sleep(50 * time.Millisecond)
	stop, err := client.ServeReverseHTTPRoute(t.Context(), "/mcp/late", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("late"))
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	select {
	case body := <-got:
		if body != "late" {
			t.Fatalf("response = %q, want the route's handler", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach the route registered after it")
	}
}

func TestServeReverseHTTPRoutesConcurrentStreams(t *testing.T) {
	broker := bridgesvc.NewReverseHTTPBroker()
	client := newTestClient(t, bridgesvc.New(bridgesvc.Options{ReverseHTTP: broker}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopA, err := client.ServeReverseHTTPRoute(ctx, "/mcp/session-a", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("a"))
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer stopA()
	stopB, err := client.ServeReverseHTTPRoute(ctx, "/mcp/session-b", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("b"))
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer stopB()

	deadline := time.Now().Add(time.Second)
	for {
		gotA := reverseHTTPTestRequest(t, broker, "/mcp/session-a")
		gotB := reverseHTTPTestRequest(t, broker, "/mcp/session-b")
		if gotA == "a" && gotB == "b" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reverse HTTP routed responses = %q/%q", gotA, gotB)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServeReverseHTTPStopWaitsForInFlightRequests(t *testing.T) {
	broker := bridgesvc.NewReverseHTTPBroker()
	client := newTestClient(t, bridgesvc.New(bridgesvc.Options{ReverseHTTP: broker}))

	started := make(chan struct{})
	finished := make(chan struct{})
	var startedOnce sync.Once
	var finishedOnce sync.Once
	stop, err := client.ServeReverseHTTP(context.Background(), http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-req.Context().Done()
		finishedOnce.Do(func() { close(finished) })
	}))
	if err != nil {
		t.Fatal(err)
	}

	var cancelReq context.CancelFunc
	var brokerDone chan struct{}
	deadline := time.Now().Add(time.Second)
	for {
		reqCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("ping")).WithContext(reqCtx)
			rec := httptest.NewRecorder()
			broker.ServeHTTP(rec, req)
		}()
		select {
		case <-started:
			cancelReq = cancel
			brokerDone = done
		case <-done:
			cancel()
			if time.Now().After(deadline) {
				t.Fatal("reverse HTTP handler did not start")
			}
			time.Sleep(10 * time.Millisecond)
			continue
		case <-time.After(time.Until(deadline)):
			cancel()
			t.Fatal("reverse HTTP handler did not start")
		}
		break
	}
	defer cancelReq()

	stopReturned := make(chan struct{})
	go func() {
		defer close(stopReturned)
		stop()
	}()

	select {
	case <-stopReturned:
	case <-time.After(time.Second):
		t.Fatal("stop did not return after cancelling in-flight request")
	}
	select {
	case <-finished:
	default:
		t.Fatal("stop returned before in-flight request observed cancellation")
	}

	cancelReq()
	select {
	case <-brokerDone:
	case <-time.After(time.Second):
		t.Fatal("broker request did not finish")
	}
}

func reverseHTTPTestRequest(t *testing.T, broker http.Handler, target string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader("ping"))
	rec := httptest.NewRecorder()
	broker.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return ""
	}
	return rec.Body.String()
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

// The interactive terminal must not inherit the bridge's default PTY
// deadline; only the stream's lifetime bounds the shell.
func TestExecStreamPTYRequestsNoDeadline(t *testing.T) {
	server := &execInputCaptureServer{first: make(chan *pb.ExecInput, 1)}
	client := newTestClient(t, server)
	stream, err := client.ExecStreamPTY(context.Background(), "/bin/sh", "/data", 80, 24)
	if err != nil {
		t.Fatalf("ExecStreamPTY returned error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	select {
	case msg := <-server.first:
		if !msg.GetPty() {
			t.Fatal("pty = false, want true")
		}
		if msg.GetTimeoutSeconds() != -1 {
			t.Fatalf("timeout_seconds = %d, want -1", msg.GetTimeoutSeconds())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server stream did not receive exec config")
	}
}

type execInputCaptureServer struct {
	pb.UnimplementedContainerServiceServer
	first chan *pb.ExecInput
}

func (s *execInputCaptureServer) Exec(stream pb.ContainerService_ExecServer) error {
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	s.first <- msg
	<-stream.Context().Done()
	return stream.Context().Err()
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

type execEarlyCloseTestServer struct {
	pb.UnimplementedContainerServiceServer
}

func (*execEarlyCloseTestServer) Exec(stream pb.ContainerService_ExecServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_STDOUT, Data: []byte("done")}); err != nil {
		return err
	}
	return stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_EXIT, ExitCode: 0})
}

func TestClientExecReturnsResultWhenServerClosesBeforeStdinLands(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, &execEarlyCloseTestServer{})
	stdin := bytes.Repeat([]byte("x"), 8<<20)
	result, err := client.ExecWithStdin(context.Background(), "hook", "", 5, stdin)
	if err != nil {
		t.Fatalf("ExecWithStdin returned error: %v", err)
	}
	if result.Stdout != "done" || result.ExitCode != 0 {
		t.Fatalf("exec result = %q/%d, want %q/0", result.Stdout, result.ExitCode, "done")
	}
}

// newExecTestClient wires the client to a real bridgesvc server in-process, so
// Exec runs actual /bin/sh processes with the production stdin/stdout plumbing
// (bridgesvc.Server.execPipe) instead of a scripted fake.
func newExecTestClient(t *testing.T) *Client {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bridgesvc exec runs commands via /bin/sh")
	}
	return newTestClient(t, bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir:    t.TempDir(),
		AllowHostAbsolute: true,
	}))
}

// drainExecStream receives until EXIT, returning the concatenated stdout and
// the exit code. It fails the test if the stream ends without EXIT.
func drainExecStream(t *testing.T, stream *ExecStream) (string, int32) {
	t.Helper()
	var stdout bytes.Buffer
	for {
		out, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv returned error before EXIT: %v (stdout so far %q)", err, stdout.String())
		}
		switch out.GetStream() {
		case pb.ExecOutput_STDOUT:
			stdout.Write(out.GetData())
		case pb.ExecOutput_EXIT:
			return stdout.String(), out.GetExitCode()
		}
	}
}

// TestExecStreamCloseSendDeliversStdinEOF proves the half-close contract end
// to end against the real bridge server: `sh -s` reads its script from stdin,
// `cat` then blocks on the same stdin, and only the client's CloseSend can
// unblock it. Seeing `done` and EXIT 0 means bridgesvc closed stdinPipe when
// its Recv loop hit EOF while the output side kept streaming.
func TestExecStreamCloseSendDeliversStdinEOF(t *testing.T) {
	client := newExecTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := client.ExecStream(ctx, "exec sh -s", "", 20)
	if err != nil {
		t.Fatalf("ExecStream returned error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if err := stream.SendStdin([]byte("cat; echo done\n")); err != nil {
		t.Fatalf("SendStdin returned error: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend returned error: %v", err)
	}

	stdout, exitCode := drainExecStream(t, stream)
	if !strings.Contains(stdout, "done") {
		t.Fatalf("stdout = %q, want it to contain %q", stdout, "done")
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
}

// TestExecStreamCloseAfterCloseSendIsSafe pins the documented call order
// `defer stream.Close()` → SendStdin → CloseSend: the deferred Close must not
// panic or surface an error because gRPC's CloseSend is idempotent.
func TestExecStreamCloseAfterCloseSendIsSafe(t *testing.T) {
	client := newExecTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := client.ExecStream(ctx, "exec sh -s", "", 20)
	if err != nil {
		t.Fatalf("ExecStream returned error: %v", err)
	}
	if err := stream.SendStdin([]byte("echo ok\n")); err != nil {
		t.Fatalf("SendStdin returned error: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend returned error: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("second CloseSend returned error: %v", err)
	}
	if _, exitCode := drainExecStream(t, stream); exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close after CloseSend returned error: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}
}

// TestExecStreamCloseWithoutCloseSendCancelsProcess fixes the semantic gap
// between the two methods: Close cancels the whole stream, so the shell that
// is still blocked on stdin never gets to run `echo done` for us and Recv
// reports cancellation instead of output + EXIT. Compare with
// TestExecStreamCloseSendDeliversStdinEOF, which sends the same script.
func TestExecStreamCloseWithoutCloseSendCancelsProcess(t *testing.T) {
	client := newExecTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := client.ExecStream(ctx, "exec sh -s", "", 20)
	if err != nil {
		t.Fatalf("ExecStream returned error: %v", err)
	}
	if err := stream.SendStdin([]byte("cat; echo done\n")); err != nil {
		t.Fatalf("SendStdin returned error: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	for {
		out, err := stream.Recv()
		if err == nil {
			if out.GetStream() == pb.ExecOutput_EXIT || strings.Contains(string(out.GetData()), "done") {
				t.Fatalf("Close() let the process finish normally: %v", out)
			}
			continue
		}
		if status.Code(err) != codes.Canceled && !errors.Is(err, context.Canceled) {
			t.Fatalf("Recv error = %v, want cancellation", err)
		}
		return
	}
}

func TestClientTunnelCloseUnblocksServerTargetWrite(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	handlerDone := make(chan struct{})
	lis := bufconn.Listen(testBufSize)
	srv := grpc.NewServer(grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		defer close(handlerDone)
		return handler(srv, ss)
	}))
	pb.RegisterContainerServiceServer(srv, bridgesvc.New(bridgesvc.Options{}))
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	rpc, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rpc.Close() }()
	client := NewClientFromConn(rpc)
	tunnel, err := client.DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	target := <-accepted
	defer func() { _ = target.Close() }()
	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		for i := 0; i < 2000; i++ {
			if _, err := tunnel.Write(make([]byte, 48*1024)); err != nil {
				return
			}
		}
	}()
	time.Sleep(300 * time.Millisecond)
	select {
	case <-wrote:
		t.Fatal("writes finished without target reading")
	default:
	}
	if err := tunnel.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Error("canceled tunnel handler remains blocked writing the target socket")
	}
	_ = target.Close()
	<-wrote
}
