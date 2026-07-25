package asset

import (
	"context"
	"errors"
	"io"
	"testing"

	domainmedia "github.com/memohai/memoh/domains/media"
	"github.com/memohai/memoh/domains/media/storage"
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

	provider := &accessPathEnsuringProvider{path: " /data/media/aa/asset.pdf "}
	service := NewService(nil, provider)
	got, err := service.EnsureAccessPath(context.Background(), domainmedia.Asset{
		BotID:      "bot-1",
		StorageKey: "aa/asset.pdf",
	})
	if err != nil {
		t.Fatalf("EnsureAccessPath() error = %v", err)
	}
	if got != "/data/media/aa/asset.pdf" {
		t.Fatalf("EnsureAccessPath() = %q", got)
	}
	if provider.ensuredKey != "bot-1/aa/asset.pdf" {
		t.Fatalf("materialized key = %q", provider.ensuredKey)
	}
}

func TestServiceEnsureAccessPathRejectsEmptyMaterializedPath(t *testing.T) {
	t.Parallel()

	service := NewService(nil, &accessPathEnsuringProvider{})
	_, err := service.EnsureAccessPath(context.Background(), domainmedia.Asset{
		BotID:      "bot-1",
		StorageKey: "aa/asset.pdf",
	})
	if !errors.Is(err, storage.ErrAccessPathUnavailable) {
		t.Fatalf("EnsureAccessPath() error = %v, want ErrAccessPathUnavailable", err)
	}
}
