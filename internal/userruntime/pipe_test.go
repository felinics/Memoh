package userruntime

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

type directPipeTestService struct {
	pb.UnimplementedContainerServiceServer
	blockStarted chan struct{}
}

func (s *directPipeTestService) Stat(ctx context.Context, req *pb.StatRequest) (*pb.StatResponse, error) {
	if req.GetPath() == "/block" {
		close(s.blockStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &pb.StatResponse{Entry: &pb.FileEntry{Path: req.GetPath(), IsDir: true}}, nil
}

func TestDirectPipeWebSocketRoundTripAndDisconnect(t *testing.T) {
	type result struct {
		path string
		err  error
	}
	roundTrip := make(chan result, 1)
	disconnected := make(chan error, 1)

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			roundTrip <- result{err: err}
			return
		}
		defer func() { _ = ws.CloseNow() }()
		stream := websocket.NetConn(r.Context(), ws, websocket.MessageBinary)
		ws.SetReadLimit(RuntimeGRPCMessageLimit)

		readyCtx, cancelReady := context.WithTimeout(r.Context(), 2*time.Second)
		conn, err := NewDirectPipe().ClientConn(readyCtx, stream)
		cancelReady()
		if err != nil {
			roundTrip <- result{err: err}
			return
		}
		defer func() { _ = conn.Close() }()
		client := pb.NewContainerServiceClient(conn)
		response, err := client.Stat(r.Context(), &pb.StatRequest{Path: "/workspace"})
		if err != nil {
			roundTrip <- result{err: err}
			return
		}
		roundTrip <- result{path: response.GetEntry().GetPath()}
		_, err = client.Stat(context.WithoutCancel(r.Context()), &pb.StatRequest{Path: "/block"})
		disconnected <- err
	}))
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil {
		t.Fatalf("dial test WebSocket: %v", err)
	}
	stream := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	ws.SetReadLimit(RuntimeGRPCMessageLimit)
	listener := newSingleConnListener(stream)
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(RuntimeGRPCMessageLimit),
		grpc.MaxSendMsgSize(RuntimeGRPCMessageLimit),
	)
	service := &directPipeTestService{blockStarted: make(chan struct{})}
	pb.RegisterContainerServiceServer(grpcServer, service)
	serveDone := make(chan error, 1)
	go func() { serveDone <- grpcServer.Serve(listener) }()
	defer func() {
		_ = ws.CloseNow()
		grpcServer.Stop()
		_ = listener.Close()
		select {
		case <-serveDone:
		case <-time.After(time.Second):
			t.Error("gRPC server did not stop")
		}
	}()

	select {
	case got := <-roundTrip:
		if got.err != nil || got.path != "/workspace" {
			t.Fatalf("WebSocket gRPC round trip = %q, %v", got.path, got.err)
		}
	case <-ctx.Done():
		t.Fatalf("WebSocket gRPC round trip timed out: %v", ctx.Err())
	}
	select {
	case <-service.blockStarted:
	case <-ctx.Done():
		t.Fatalf("blocking RPC did not start: %v", ctx.Err())
	}
	if err := ws.CloseNow(); err != nil {
		t.Fatalf("close Runtime WebSocket: %v", err)
	}
	select {
	case err := <-disconnected:
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("disconnect error = %v, want Unavailable", err)
		}
	case <-ctx.Done():
		t.Fatalf("disconnect did not end in-flight RPC: %v", ctx.Err())
	}
}

type singleConnListener struct {
	mu     sync.Mutex
	conn   net.Conn
	closed chan struct{}
	once   sync.Once
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	return &singleConnListener{conn: conn, closed: make(chan struct{})}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	conn := l.conn
	l.conn = nil
	l.mu.Unlock()
	if conn != nil {
		return conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (*singleConnListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "websocket" }
func (pipeAddr) String() string  { return "runtime-pipe-test" }
