package media

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/felinics/memoh/internal/storage"
)

type accessPathEnsuringProvider struct {
	path       string
	err        error
	ensuredKey string
}

func (*accessPathEnsuringProvider) Put(context.Context, string, io.Reader) error { return nil }

func (*accessPathEnsuringProvider) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}

func (*accessPathEnsuringProvider) Delete(context.Context, string) error { return nil }

func (*accessPathEnsuringProvider) AccessPath(context.Context, string) string { return "legacy" }

func (p *accessPathEnsuringProvider) EnsureAccessPath(_ context.Context, key string) (string, error) {
	p.ensuredKey = key
	return p.path, p.err
}

func TestServiceEnsureAccessPathUsesMaterializingProvider(t *testing.T) {
	t.Parallel()

	provider := &accessPathEnsuringProvider{path: " /data/.memoh/media/aa/asset.pdf "}
	service := NewService(nil, provider)
	got, err := service.EnsureAccessPath(context.Background(), Asset{
		BotID:      "bot-1",
		StorageKey: "aa/asset.pdf",
	})
	if err != nil {
		t.Fatalf("EnsureAccessPath() error = %v", err)
	}
	if got != "/data/.memoh/media/aa/asset.pdf" {
		t.Fatalf("EnsureAccessPath() = %q", got)
	}
	if provider.ensuredKey != "bot-1/aa/asset.pdf" {
		t.Fatalf("materialized key = %q", provider.ensuredKey)
	}
}

func TestServiceEnsureAccessPathRejectsEmptyMaterializedPath(t *testing.T) {
	t.Parallel()

	service := NewService(nil, &accessPathEnsuringProvider{})
	_, err := service.EnsureAccessPath(context.Background(), Asset{
		BotID:      "bot-1",
		StorageKey: "aa/asset.pdf",
	})
	if !errors.Is(err, storage.ErrAccessPathUnavailable) {
		t.Fatalf("EnsureAccessPath() error = %v, want ErrAccessPathUnavailable", err)
	}
}

func TestMimeFromPathUsesMetadataOnly(t *testing.T) {
	t.Parallel()

	if got := MimeFromPath(" aa/asset.webp "); got != "image/webp" {
		t.Fatalf("MimeFromPath() = %q, want image/webp", got)
	}
	if got := MimeFromPath("aa/asset.unknown"); got != "" {
		t.Fatalf("MimeFromPath() = %q, want empty for unknown extension", got)
	}
}
