package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/labstack/echo/v4"
	"google.golang.org/grpc"

	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/userruntime"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const runtimeConnectTestID = "11111111-1111-4111-8111-111111111111"

var runtimeConnectTestKey = "mrk_" + strings.Repeat("2", 64)

type runtimeConnectTestStore struct {
	dbstore.UserRuntimeStore

	mu      sync.Mutex
	runtime dbstore.UserRuntimeRecord
}

func newRuntimeConnectTestStore() *runtimeConnectTestStore {
	return &runtimeConnectTestStore{
		runtime: dbstore.UserRuntimeRecord{
			ID:               runtimeConnectTestID,
			UserID:           "user-1",
			Name:             "Workstation",
			APIToken:         runtimeConnectTestKey,
			PendingExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		},
	}
}

func (s *runtimeConnectTestStore) GetUserRuntimeByAPIToken(context.Context, string) (dbstore.UserRuntimeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtime, nil
}

func (s *runtimeConnectTestStore) ActivateUserRuntime(_ context.Context, _, _ string) (dbstore.UserRuntimeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtime.ActivatedAt = time.Now().UTC()
	s.runtime.PendingExpiresAt = time.Time{}
	return s.runtime, nil
}

func (s *runtimeConnectTestStore) BackfillUserRuntimeName(_ context.Context, _, _, name, defaultName string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtime.Name != "" && s.runtime.Name != defaultName {
		return false, nil
	}
	s.runtime.Name = name
	return true, nil
}

type runtimeConnectTestService struct {
	pb.UnimplementedContainerServiceServer
}

func (*runtimeConnectTestService) Stat(_ context.Context, req *pb.StatRequest) (*pb.StatResponse, error) {
	return &pb.StatResponse{Entry: &pb.FileEntry{Path: req.GetPath(), IsDir: true}}, nil
}

func TestRuntimeConnectActivatesReadyConnectionAndUnregistersDisconnect(t *testing.T) {
	store := newRuntimeConnectTestStore()
	hub := userruntime.NewHub(nil)
	service := userruntime.NewService(store, hub)
	handler := NewRuntimeConnectHandler(nil, service, userruntime.NewDirectPipe())
	e := echo.New()
	handler.Register(e)
	httpServer := httptest.NewServer(e)
	defer httpServer.Close()

	metadata, err := json.Marshal(userruntime.HandshakeInfo{
		Version:       1,
		Hostname:      "workstation.local",
		OS:            "linux",
		Arch:          "amd64",
		ClientVersion: "test",
		WorkspaceBase: "/workspace",
		Capabilities:  []string{userruntime.CapabilityFS, userruntime.CapabilityExec},
	})
	if err != nil {
		t.Fatalf("marshal Runtime metadata: %v", err)
	}
	header := http.Header{}
	header.Set(echo.HeaderAuthorization, "Bearer "+runtimeConnectTestKey)
	header.Set(userruntime.RuntimeMetadataHeader, base64.RawURLEncoding.EncodeToString(metadata))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/runtimes/connect", &websocket.DialOptions{
		HTTPHeader:   header,
		Subprotocols: []string{runtimeProtocolGRPC},
	})
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil {
		t.Fatalf("dial Runtime WebSocket: %v", err)
	}
	stream := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	ws.SetReadLimit(userruntime.RuntimeGRPCMessageLimit)
	listener := newRuntimeConnectTestListener(stream)
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(userruntime.RuntimeGRPCMessageLimit),
		grpc.MaxSendMsgSize(userruntime.RuntimeGRPCMessageLimit),
	)
	pb.RegisterContainerServiceServer(grpcServer, &runtimeConnectTestService{})
	serveDone := make(chan error, 1)
	go func() { serveDone <- grpcServer.Serve(listener) }()
	defer func() {
		_ = ws.CloseNow()
		grpcServer.Stop()
		_ = listener.Close()
		select {
		case <-serveDone:
		case <-time.After(time.Second):
			t.Error("Runtime gRPC server did not stop")
		}
	}()

	var connection *userruntime.Connection
	for connection == nil {
		connection, _ = hub.Get(runtimeConnectTestID)
		if connection == nil {
			select {
			case <-time.After(time.Millisecond):
			case <-ctx.Done():
				t.Fatalf("ready Runtime was not published: %v", ctx.Err())
			}
		}
	}
	entry, err := connection.Client.Stat(ctx, "/probe")
	if err != nil || entry.GetPath() != "/probe" {
		t.Fatalf("activated Runtime Stat = %#v, %v", entry, err)
	}
	if connection.Info.WorkspaceBase != "/workspace" || connection.Info.Hostname != "workstation.local" {
		t.Fatalf("connection info = %#v", connection.Info)
	}

	if err := ws.CloseNow(); err != nil {
		t.Fatalf("disconnect Runtime WebSocket: %v", err)
	}
	for {
		if _, online := hub.Get(runtimeConnectTestID); !online {
			break
		}
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("disconnected Runtime stayed registered: %v", ctx.Err())
		}
	}
}

type runtimeConnectTestListener struct {
	mu     sync.Mutex
	conn   net.Conn
	closed chan struct{}
	once   sync.Once
}

func newRuntimeConnectTestListener(conn net.Conn) *runtimeConnectTestListener {
	return &runtimeConnectTestListener{conn: conn, closed: make(chan struct{})}
}

func (l *runtimeConnectTestListener) Accept() (net.Conn, error) {
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

func (l *runtimeConnectTestListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (*runtimeConnectTestListener) Addr() net.Addr { return runtimeConnectTestAddr{} }

type runtimeConnectTestAddr struct{}

func (runtimeConnectTestAddr) Network() string { return "websocket" }
func (runtimeConnectTestAddr) String() string  { return "runtime-connect-test" }
