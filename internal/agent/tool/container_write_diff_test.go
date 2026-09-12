package tools

import (
	"context"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// writeDiffTestContainerService is a minimal in-memory file store behind the
// bridge RPC surface execWrite consults for its UI-only diff: Stat decides
// whether old content exists, ReadRaw streams it, WriteFile records writes.
type writeDiffTestContainerService struct {
	pb.UnimplementedContainerServiceServer
	files   map[string][]byte
	statErr error
}

func (s *writeDiffTestContainerService) Stat(_ context.Context, req *pb.StatRequest) (*pb.StatResponse, error) {
	if s.statErr != nil {
		return nil, s.statErr
	}
	content, ok := s.files[req.GetPath()]
	if !ok {
		return nil, status.Error(codes.NotFound, "not found")
	}
	return &pb.StatResponse{Entry: &pb.FileEntry{
		Path: req.GetPath(),
		Size: int64(len(content)),
	}}, nil
}

func (s *writeDiffTestContainerService) ReadRaw(req *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	content, ok := s.files[req.GetPath()]
	if !ok {
		return status.Error(codes.NotFound, "not found")
	}
	return stream.Send(&pb.DataChunk{Data: content})
}

func (s *writeDiffTestContainerService) WriteFile(_ context.Context, req *pb.WriteFileRequest) (*pb.WriteFileResponse, error) {
	s.files[req.GetPath()] = req.GetContent()
	return &pb.WriteFileResponse{}, nil
}

func newWriteDiffTestClient(t *testing.T, svc *writeDiffTestContainerService) *bridge.Client {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterContainerServiceServer(srv, svc)

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
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return bridge.NewClientFromConn(conn)
}

func execWriteForTest(t *testing.T, svc *writeDiffTestContainerService, path, content string) map[string]any {
	t.Helper()
	provider := NewContainerProvider(nil, containerTestBridgeProvider{client: newWriteDiffTestClient(t, svc)}, nil, "")
	out, err := provider.execWrite(context.Background(), SessionContext{BotID: "bot-1"}, map[string]any{
		"path":    path,
		"content": content,
	})
	if err != nil {
		t.Fatalf("execWrite error = %v", err)
	}
	result, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("execWrite result = %T, want map[string]any", out)
	}
	return result
}

func uiDiffForTest(t *testing.T, result map[string]any) string {
	t.Helper()
	ui, ok := result[UIOutputMetadataKey].(map[string]any)
	if !ok {
		return ""
	}
	diff, _ := ui["diff"].(string)
	return diff
}

func TestExecWriteAttachesAllAddDiffForNewFile(t *testing.T) {
	t.Parallel()
	svc := &writeDiffTestContainerService{files: map[string][]byte{}}
	content := "# Title\n\n- first\n- second\n"
	result := execWriteForTest(t, svc, "demo/new.md", content)

	diff := uiDiffForTest(t, result)
	if diff == "" {
		t.Fatal("execWrite attached no UI diff for a brand-new file")
	}
	adds, removes := 0, 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			adds++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removes++
		}
	}
	if removes != 0 {
		t.Fatalf("new-file diff contains %d removal lines:\n%s", removes, diff)
	}
	if want := strings.Count(strings.TrimSuffix(content, "\n"), "\n") + 1; adds != want {
		t.Fatalf("new-file diff has %d additions, want %d (every written line):\n%s", adds, want, diff)
	}
}

func TestExecWriteAttachesRealDiffForOverwrite(t *testing.T) {
	t.Parallel()
	svc := &writeDiffTestContainerService{files: map[string][]byte{
		"demo/existing.md": []byte("keep\nold\ntail\n"),
	}}
	result := execWriteForTest(t, svc, "demo/existing.md", "keep\nnew\ntail\n")

	diff := uiDiffForTest(t, result)
	if diff == "" {
		t.Fatal("execWrite attached no UI diff for an overwrite")
	}
	if !strings.Contains(diff, "\n-old\n") || !strings.Contains(diff, "\n+new\n") {
		t.Fatalf("overwrite diff does not show the replaced line:\n%s", diff)
	}
}

func TestExecWriteSkipsDiffWhenOldContentUnreadable(t *testing.T) {
	t.Parallel()
	svc := &writeDiffTestContainerService{
		files:   map[string][]byte{},
		statErr: status.Error(codes.Internal, "stat backend broken"),
	}
	result := execWriteForTest(t, svc, "demo/unknown.md", "content\n")

	// A stat failure that is not ErrNotFound must not be read as "new file":
	// painting an all-add diff for what may be an overwrite would lie.
	if diff := uiDiffForTest(t, result); diff != "" {
		t.Fatalf("execWrite attached a diff despite stat failure:\n%s", diff)
	}
}

func TestExecWriteSkipsDiffForIdenticalRewrite(t *testing.T) {
	t.Parallel()
	svc := &writeDiffTestContainerService{files: map[string][]byte{
		"demo/same.md": []byte("same\n"),
	}}
	result := execWriteForTest(t, svc, "demo/same.md", "same\n")

	if diff := uiDiffForTest(t, result); diff != "" {
		t.Fatalf("execWrite attached a diff for a no-op rewrite:\n%s", diff)
	}
}
