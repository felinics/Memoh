package containerfs

import (
	"context"
	"io"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func TestParseRoutingKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key     string
		wantErr bool
	}{
		{key: "bot-1/image/ab12/ab12cd.png", wantErr: false},
		{key: "/absolute/path", wantErr: true},
		{key: "../escape", wantErr: true},
		{key: "nosubpath", wantErr: true},
		{key: "", wantErr: true},
	}
	for _, tt := range tests {
		_, _, err := parseRoutingKey(tt.key)
		if tt.wantErr && err == nil {
			t.Errorf("parseRoutingKey(%q) expected error", tt.key)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("parseRoutingKey(%q) unexpected error: %v", tt.key, err)
		}
	}
}

func TestProvider_AccessPath(t *testing.T) {
	t.Parallel()
	p := &Provider{}

	tests := []struct {
		key  string
		want string
	}{
		{key: "bot-1/image/ab12/ab12cd.png", want: "/data/.memoh/media/image/ab12/ab12cd.png"},
		{key: "bot-1/file/xx/doc.pdf", want: "/data/.memoh/media/file/xx/doc.pdf"},
	}
	for _, tt := range tests {
		got := p.AccessPath(context.Background(), tt.key)
		if got != tt.want {
			t.Errorf("AccessPath(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestParseRoutingKey_PathTraversal(t *testing.T) {
	t.Parallel()

	bad := []string{
		"../etc/passwd",
		"/absolute/key",
		"bot-1/../../escape",
	}
	for _, key := range bad {
		if _, _, err := parseRoutingKey(key); err == nil {
			t.Errorf("parseRoutingKey(%q) should reject traversal", key)
		}
	}
}

func TestSplitRoutingKey(t *testing.T) {
	t.Parallel()

	botID, sub := splitRoutingKey("bot-1/image/test.png")
	if botID != "bot-1" || sub != "image/test.png" {
		t.Errorf("splitRoutingKey: got (%q, %q)", botID, sub)
	}

	botID2, sub2 := splitRoutingKey("nosubpath")
	if botID2 != "" || sub2 != "nosubpath" {
		t.Errorf("splitRoutingKey single: got (%q, %q)", botID2, sub2)
	}
}

// recordingReadServer captures the exact path the client sends over the wire.
type recordingReadServer struct {
	pb.UnimplementedContainerServiceServer
	path string
	data []byte
}

func (s *recordingReadServer) ReadRaw(req *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	s.path = req.GetPath()
	if len(s.data) > 0 {
		return stream.Send(&pb.DataChunk{Data: s.data})
	}
	return nil
}

type staticClientProvider struct{ client *bridge.Client }

func (p staticClientProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	return p.client, nil
}

func newRecordingClient(t *testing.T, server pb.ContainerServiceServer) *bridge.Client {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
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

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn)
}

// OpenContainerFile must send the absolute container path to the workspace
// client. Clients resolve relative paths against different bases (the bridge
// joins its /data workdir; the runtime-worker chain resolves against the
// sandbox home), so stripping /data and sending a relative subPath reads the
// wrong file anywhere off the bridge — this regressed outbound attachment
// ingestion for runtime-worker workspaces.
func TestProvider_OpenContainerFileSendsAbsolutePath(t *testing.T) {
	t.Parallel()

	server := &recordingReadServer{data: []byte("png-bytes")}
	p := New(staticClientProvider{client: newRecordingClient(t, server)})

	rc, err := p.OpenContainerFile(context.Background(), "bot-1", "/data/repro935-big.png")
	if err != nil {
		t.Fatalf("OpenContainerFile: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if string(got) != "png-bytes" {
		t.Errorf("content = %q, want %q", got, "png-bytes")
	}
	if server.path != "/data/repro935-big.png" {
		t.Errorf("client received path %q, want absolute %q", server.path, "/data/repro935-big.png")
	}
}
