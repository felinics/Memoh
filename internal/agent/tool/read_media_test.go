package tools

import (
	"bytes"
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

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const readMediaTestBufSize = 1 << 20

type readMediaTestContainerService struct {
	pb.UnimplementedContainerServiceServer
	files map[string][]byte
}

func (s *readMediaTestContainerService) ReadRaw(req *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	data, ok := s.files[req.GetPath()]
	if !ok {
		return status.Error(codes.NotFound, "not found")
	}
	if len(data) == 0 {
		return nil
	}
	return stream.Send(&pb.DataChunk{Data: data})
}

func newReadMediaTestClient(t *testing.T, files map[string][]byte) *bridge.Client {
	t.Helper()

	lis := bufconn.Listen(readMediaTestBufSize)
	srv := grpc.NewServer()
	pb.RegisterContainerServiceServer(srv, &readMediaTestContainerService{files: files})

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
	if !strings.Contains(result.Public.Error, "PNG, JPEG, GIF, WebP") {
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
	if !strings.Contains(result.Public.Error, "PNG, JPEG, GIF, WebP") {
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

func TestReadImageFromContainerPDFBecomesFilePayload(t *testing.T) {
	t.Parallel()

	pdfBytes := []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n%%EOF\n")
	client := newReadMediaTestClient(t, map[string][]byte{
		"/workspace/uploads/report.pdf": pdfBytes,
	})

	result := ReadImageFromContainer(context.Background(), client, "/workspace/uploads/report.pdf", 0)

	if !result.Public.OK {
		t.Fatalf("expected success, got %+v", result.Public)
	}
	if result.Public.Mime != "application/pdf" {
		t.Fatalf("expected application/pdf, got %q", result.Public.Mime)
	}
	if result.FileBase64 == "" {
		t.Fatal("expected FileBase64 to be populated for PDF")
	}
	if result.ImageBase64 != "" {
		t.Fatalf("PDF must not ride the image lane, got ImageBase64 %q", result.ImageBase64)
	}
	if result.Filename != "report.pdf" {
		t.Fatalf("expected filename report.pdf, got %q", result.Filename)
	}
	decoded, err := base64.StdEncoding.DecodeString(result.FileBase64)
	if err != nil {
		t.Fatalf("decode FileBase64: %v", err)
	}
	if !bytes.Equal(decoded, pdfBytes) {
		t.Fatal("FileBase64 does not round-trip the PDF bytes")
	}
}
