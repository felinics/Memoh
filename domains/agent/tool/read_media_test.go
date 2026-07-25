package tool

import (
	"context"
	"encoding/base64"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/memohai/memoh/domains/runtime/bridge/bridgepb"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

const readMediaTestBufSize = 1 << 20

type readMediaTestContainerService struct {
	bridgepb.UnimplementedContainerServiceServer
	files map[string][]byte
}

func (s *readMediaTestContainerService) ReadRaw(req *bridgepb.ReadRawRequest, stream bridgepb.ContainerService_ReadRawServer) error {
	data, ok := s.files[req.GetPath()]
	if !ok {
		return status.Error(codes.NotFound, "not found")
	}
	if len(data) == 0 {
		return nil
	}
	return stream.Send(&bridgepb.DataChunk{Data: data})
}

func newReadMediaTestClient(t *testing.T, files map[string][]byte) *bridge.Client {
	t.Helper()

	lis := bufconn.Listen(readMediaTestBufSize)
	srv := grpc.NewServer()
	bridgepb.RegisterContainerServiceServer(srv, &readMediaTestContainerService{files: files})

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

func TestReadImageFromContainerSuccess(t *testing.T) {
	t.Parallel()

	pngBytes := []byte("\x89PNG\r\n\x1a\npayload")
	client := newReadMediaTestClient(t, map[string][]byte{
		"/data/images/demo.png": pngBytes,
	})

	result := ReadImageFromContainer(context.Background(), client, "/data/images/demo.png", 0)

	if !result.Public.OK {
		t.Fatalf("expected success result, got %+v", result.Public)
	}
	if result.Public.Path != "/data/images/demo.png" {
		t.Fatalf("unexpected path: %q", result.Public.Path)
	}
	if result.Public.Mime != "image/png" {
		t.Fatalf("unexpected mime: %q", result.Public.Mime)
	}
	if result.Public.Size != len(pngBytes) {
		t.Fatalf("unexpected size: %d", result.Public.Size)
	}

	expectedBase64 := base64.StdEncoding.EncodeToString(pngBytes)
	if result.ImageBase64 != expectedBase64 {
		t.Fatalf("unexpected image payload: %q", result.ImageBase64)
	}
	if result.ImageMediaType != "image/png" {
		t.Fatalf("unexpected image media type: %q", result.ImageMediaType)
	}
}

func TestReadImageFromContainerRejectsUnsupportedMime(t *testing.T) {
	t.Parallel()

	svgBytes := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	client := newReadMediaTestClient(t, map[string][]byte{
		"/data/images/demo.svg": svgBytes,
	})

	result := ReadImageFromContainer(context.Background(), client, "/data/images/demo.svg", 0)

	if result.Public.OK {
		t.Fatalf("expected error result, got %+v", result.Public)
	}
	if !strings.Contains(result.Public.Error, "PNG, JPEG, GIF, or WebP") {
		t.Fatalf("unexpected error: %q", result.Public.Error)
	}
	if result.ImageBase64 != "" {
		t.Fatalf("expected no injected image for error result, got %q", result.ImageBase64)
	}
}

func TestReadImageFromContainerRejectsCorruptedBytes(t *testing.T) {
	t.Parallel()

	client := newReadMediaTestClient(t, map[string][]byte{
		"/data/images/demo.png": []byte("definitely not a png"),
	})

	result := ReadImageFromContainer(context.Background(), client, "/data/images/demo.png", 0)

	if result.Public.OK {
		t.Fatalf("expected error result, got %+v", result.Public)
	}
	if !strings.Contains(result.Public.Error, "PNG, JPEG, GIF, or WebP") {
		t.Fatalf("unexpected error: %q", result.Public.Error)
	}
	if result.ImageBase64 != "" {
		t.Fatalf("expected no injected image for error result, got %q", result.ImageBase64)
	}
}

func TestReadImageFromContainerNotFound(t *testing.T) {
	t.Parallel()

	client := newReadMediaTestClient(t, map[string][]byte{})

	result := ReadImageFromContainer(context.Background(), client, "/data/images/missing.png", 0)

	if result.Public.OK {
		t.Fatalf("expected error result, got %+v", result.Public)
	}
	if result.ImageBase64 != "" {
		t.Fatalf("expected no injected image for error result, got %q", result.ImageBase64)
	}
}
