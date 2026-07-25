package server_test

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
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/memohai/memoh/domains/runtime/bridge/bridgepb"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	bridgeserver "github.com/memohai/memoh/domains/runtime/bridge/server"
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

func newTestConn(t *testing.T, server bridgepb.ContainerServiceServer) *grpc.ClientConn {
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
	return conn
}

func newTestClient(t *testing.T, server bridgepb.ContainerServiceServer) *bridge.Client {
	t.Helper()
	return bridge.NewClientFromConn(newTestConn(t, server))
}

func newTestRawClient(t *testing.T, server bridgepb.ContainerServiceServer) bridgepb.ContainerServiceClient {
	t.Helper()
	return bridgepb.NewContainerServiceClient(newTestConn(t, server))
}

func TestClientWriteRawSupportsEmptyFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newTestClient(t, bridgeserver.New(bridgeserver.Options{
		DefaultWorkDir: root,
		WorkspaceRoot:  root,
		DataMount:      "/data",
	}))
	if _, err := client.WriteRaw(context.Background(), "/data/media/empty.txt", bytes.NewReader(nil)); err != nil {
		t.Fatalf("WriteRaw() error = %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "media", "empty.txt"))
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
	target := filepath.Join(root, "media", "asset.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	client := newTestClient(t, bridgeserver.New(bridgeserver.Options{
		DefaultWorkDir: root,
		WorkspaceRoot:  root,
		DataMount:      "/data",
	}))
	_, err := client.WriteRaw(context.Background(), "/data/media/asset.txt", &failAfterDataReader{})
	if err == nil {
		t.Fatal("WriteRaw() error = nil, want reader failure")
	}
	if got := err.Error(); got != "injected reader failure" {
		t.Fatalf("WriteRaw() error = %v, want injected reader failure", err)
	}

	// Abort termination frame makes server cleanup finish before WriteRaw returns.
	data, readErr := os.ReadFile(target) //nolint:gosec // G304: target is constructed under t.TempDir.
	if readErr != nil {
		t.Fatalf("read target: %v", readErr)
	}
	if string(data) != "original" {
		t.Fatalf("target data = %q, want %q", data, "original")
	}
	temps, globErr := filepath.Glob(filepath.Join(filepath.Dir(target), ".asset.txt.tmp-*"))
	if globErr != nil {
		t.Fatalf("glob temps: %v", globErr)
	}
	if len(temps) != 0 {
		t.Fatalf("leftover temp files: %v", temps)
	}
}

type failAfterNReader struct {
	n, i  int
	chunk []byte
}

func (r *failAfterNReader) Read(p []byte) (int, error) {
	if r.i >= r.n {
		return 0, errors.New("injected mid-stream reader failure")
	}
	r.i++
	return copy(p, r.chunk), nil
}

func TestClientWriteRawMidStreamReaderFailureDoesNotTouchTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "media", "asset.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	client := newTestClient(t, bridgeserver.New(bridgeserver.Options{
		DefaultWorkDir: root,
		WorkspaceRoot:  root,
		DataMount:      "/data",
	}))
	chunk := bytes.Repeat([]byte("a"), 64*1024)
	_, err := client.WriteRaw(context.Background(), "/data/media/asset.txt", &failAfterNReader{n: 3, chunk: chunk})
	if err == nil {
		t.Fatal("WriteRaw() error = nil, want mid-stream reader failure")
	}
	if got := err.Error(); got != "injected mid-stream reader failure" {
		t.Fatalf("WriteRaw() error = %v, want injected mid-stream reader failure", err)
	}

	data, readErr := os.ReadFile(target) //nolint:gosec // G304: target is constructed under t.TempDir.
	if readErr != nil {
		t.Fatalf("read target: %v", readErr)
	}
	if string(data) != "original" {
		t.Fatalf("target data = %q, want %q", data, "original")
	}
	temps, globErr := filepath.Glob(filepath.Join(filepath.Dir(target), ".asset.txt.tmp-*"))
	if globErr != nil {
		t.Fatalf("glob temps: %v", globErr)
	}
	if len(temps) != 0 {
		t.Fatalf("leftover temp files: %v", temps)
	}
}

func TestWriteRawBareEOFDoesNotCommit(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "media", "asset.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	raw := newTestRawClient(t, bridgeserver.New(bridgeserver.Options{
		DefaultWorkDir: root,
		WorkspaceRoot:  root,
		DataMount:      "/data",
	}))
	stream, err := raw.WriteRaw(context.Background())
	if err != nil {
		t.Fatalf("WriteRaw() stream error = %v", err)
	}
	if err := stream.Send(&bridgepb.WriteRawChunk{Path: "/data/media/asset.txt"}); err != nil {
		t.Fatalf("Send path: %v", err)
	}
	if err := stream.Send(&bridgepb.WriteRawChunk{Data: []byte("replacement")}); err != nil {
		t.Fatalf("Send data: %v", err)
	}
	_, err = stream.CloseAndRecv()
	if status.Code(err) != codes.Aborted {
		t.Fatalf("CloseAndRecv() error = %v, want codes.Aborted", err)
	}

	data, readErr := os.ReadFile(target) //nolint:gosec // G304: target is constructed under t.TempDir.
	if readErr != nil {
		t.Fatalf("read target: %v", readErr)
	}
	if string(data) != "original" {
		t.Fatalf("target data = %q, want %q", data, "original")
	}
	temps, globErr := filepath.Glob(filepath.Join(filepath.Dir(target), ".asset.txt.tmp-*"))
	if globErr != nil {
		t.Fatalf("glob temps: %v", globErr)
	}
	if len(temps) != 0 {
		t.Fatalf("leftover temp files: %v", temps)
	}
}

func TestWriteRawRejectsIllegalTerminationFrames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		chunk *bridgepb.WriteRawChunk
	}{
		{
			name:  "complete with data",
			chunk: &bridgepb.WriteRawChunk{Complete: true, Data: []byte("x")},
		},
		{
			name:  "abort with path",
			chunk: &bridgepb.WriteRawChunk{Abort: true, Path: "/data/media/asset.txt"},
		},
		{
			name:  "complete and abort",
			chunk: &bridgepb.WriteRawChunk{Complete: true, Abort: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			raw := newTestRawClient(t, bridgeserver.New(bridgeserver.Options{
				DefaultWorkDir: root,
				WorkspaceRoot:  root,
				DataMount:      "/data",
			}))
			stream, err := raw.WriteRaw(context.Background())
			if err != nil {
				t.Fatalf("WriteRaw() stream error = %v", err)
			}
			if err := stream.Send(&bridgepb.WriteRawChunk{Path: "/data/media/asset.txt"}); err != nil {
				t.Fatalf("Send path: %v", err)
			}
			if err := stream.Send(tc.chunk); err != nil {
				t.Fatalf("Send terminal: %v", err)
			}
			_, err = stream.CloseAndRecv()
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("CloseAndRecv() error = %v, want codes.InvalidArgument", err)
			}
		})
	}
}

func TestServeReverseHTTPForwardsRequests(t *testing.T) {
	broker := bridgeserver.NewReverseHTTPBroker()
	client := newTestClient(t, bridgeserver.New(bridgeserver.Options{ReverseHTTP: broker}))

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
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp?q=1", strings.NewReader("ping"))
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

func TestServeReverseHTTPRoutesConcurrentStreams(t *testing.T) {
	broker := bridgeserver.NewReverseHTTPBroker()
	client := newTestClient(t, bridgeserver.New(bridgeserver.Options{ReverseHTTP: broker}))

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
	broker := bridgeserver.NewReverseHTTPBroker()
	client := newTestClient(t, bridgeserver.New(bridgeserver.Options{ReverseHTTP: broker}))

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
			req := httptest.NewRequestWithContext(reqCtx, http.MethodPost, "/mcp", strings.NewReader("ping"))
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
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader("ping"))
	rec := httptest.NewRecorder()
	broker.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return ""
	}
	return rec.Body.String()
}
