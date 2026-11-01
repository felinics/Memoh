package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

// workspaceFileProvider serves files the way the container storage provider
// does through OpenContainerFile and records what it was asked to read.
type workspaceFileProvider struct {
	files  map[string][]byte
	opened []string
	stored map[string][]byte
}

func (p *workspaceFileProvider) Put(_ context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if p.stored == nil {
		p.stored = map[string][]byte{}
	}
	p.stored[key] = data
	return nil
}

func (*workspaceFileProvider) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not found")
}

func (*workspaceFileProvider) Delete(context.Context, string) error { return nil }

func (*workspaceFileProvider) AccessPath(context.Context, string) string { return "" }

func (p *workspaceFileProvider) OpenContainerFile(_ context.Context, _ string, containerPath string) (io.ReadCloser, error) {
	p.opened = append(p.opened, containerPath)
	data, ok := p.files[containerPath]
	if !ok {
		return nil, errors.New("missing")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func TestIngestWorkspaceFileReadsThroughContainerFileOpener(t *testing.T) {
	t.Parallel()

	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	provider := &workspaceFileProvider{files: map[string][]byte{
		"/data/out/chart.png":  png,
		"/data/out/fake.png":   []byte("<html>not an image</html>"),
		"/data/out/report.txt": []byte("report"),
	}}
	service := NewService(nil, provider)

	tests := []struct {
		path string
		mime string
	}{
		{"/data/out/./chart.png", "image/png"},
		{"/data/out/fake.png", "application/octet-stream"},
		{"/data/out/report.txt", "text/plain"},
	}
	for _, tt := range tests {
		asset, err := service.IngestWorkspaceFile(context.Background(), "bot-1", tt.path)
		if err != nil {
			t.Fatalf("IngestWorkspaceFile(%q) error = %v", tt.path, err)
		}
		if asset.Mime != tt.mime {
			t.Fatalf("IngestWorkspaceFile(%q) mime = %q, want %q", tt.path, asset.Mime, tt.mime)
		}
	}
	if provider.opened[0] != "/data/out/chart.png" {
		t.Fatalf("opened %q, want the cleaned path", provider.opened[0])
	}
}

func TestIngestWorkspaceFileRejectsPathsOutsideData(t *testing.T) {
	t.Parallel()

	provider := &workspaceFileProvider{files: map[string][]byte{"/etc/passwd": []byte("root")}}
	service := NewService(nil, provider)
	for _, p := range []string{"/etc/passwd", "/data/../etc/passwd", "/data", "/database/x"} {
		if _, err := service.IngestWorkspaceFile(context.Background(), "bot-1", p); err == nil {
			t.Fatalf("IngestWorkspaceFile(%q) accepted a path outside /data", p)
		}
	}
	if len(provider.opened) != 0 {
		t.Fatalf("opened %v for rejected paths", provider.opened)
	}
}
