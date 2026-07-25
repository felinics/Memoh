package httpfixture

import (
	"context"
	"net"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/memohai/memoh/domains/runtime/bridge/bridgepb"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	"github.com/memohai/memoh/domains/runtime/workspace"
)

type ACPConfigWorkspace struct {
	Backend        string
	DefaultWorkDir string
	Client         *bridge.Client
	MCPErr         error
}

func (w *ACPConfigWorkspace) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	defaultWorkDir := w.DefaultWorkDir
	if defaultWorkDir == "" {
		defaultWorkDir = "/data"
	}
	return bridge.WorkspaceInfo{Backend: w.Backend, DefaultWorkDir: defaultWorkDir}, nil
}

func (w *ACPConfigWorkspace) MCPClient(context.Context, string) (*bridge.Client, error) {
	if w.MCPErr != nil {
		return nil, w.MCPErr
	}
	return w.Client, nil
}

func (*ACPConfigWorkspace) SetupBotContainerWithProgress(context.Context, string, workspace.ContainerSetupProgress) error {
	return nil
}

type ACPConfigWrite struct {
	Path    string
	Content []byte
}

type ACPConfigBridgeServer struct {
	bridgepb.UnimplementedContainerServiceServer

	mu           sync.Mutex
	files        []ACPConfigWrite
	WriteErr     error
	WriteStarted chan string
	UnblockWrite <-chan struct{}
}

func (s *ACPConfigBridgeServer) WriteFile(ctx context.Context, req *bridgepb.WriteFileRequest) (*bridgepb.WriteFileResponse, error) {
	if s.WriteStarted != nil {
		select {
		case s.WriteStarted <- req.GetPath():
		default:
		}
	}
	if s.UnblockWrite != nil {
		select {
		case <-s.UnblockWrite:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.WriteErr != nil {
		return nil, s.WriteErr
	}
	s.files = append(s.files, ACPConfigWrite{
		Path:    req.GetPath(),
		Content: append([]byte(nil), req.GetContent()...),
	})
	return &bridgepb.WriteFileResponse{}, nil
}

func (s *ACPConfigBridgeServer) Writes() []ACPConfigWrite {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ACPConfigWrite, len(s.files))
	copy(out, s.files)
	return out
}

func FindACPConfigWrite(writes []ACPConfigWrite, path string) (ACPConfigWrite, bool) {
	for _, write := range writes {
		if write.Path == path {
			return write, true
		}
	}
	return ACPConfigWrite{}, false
}

func NewACPConfigBridgeClient(t *testing.T) (*bridge.Client, *ACPConfigBridgeServer) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	recorder := &ACPConfigBridgeServer{}
	bridgepb.RegisterContainerServiceServer(server, recorder)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///users-acp-config-test",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn), recorder
}
