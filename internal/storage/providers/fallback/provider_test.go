package fallback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/storage"
)

type memoryProvider struct {
	values     map[string][]byte
	putErr     error
	pathPrefix string
}

func newMemoryProvider(pathPrefix string) *memoryProvider {
	return &memoryProvider{values: map[string][]byte{}, pathPrefix: pathPrefix}
}

func (p *memoryProvider) Put(_ context.Context, key string, reader io.Reader) error {
	if p.putErr != nil {
		return p.putErr
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	p.values[key] = data
	return nil
}

func (p *memoryProvider) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, ok := p.values[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (p *memoryProvider) Delete(_ context.Context, key string) error {
	delete(p.values, key)
	return nil
}

func (p *memoryProvider) AccessPath(_ context.Context, key string) string {
	if strings.TrimSpace(p.pathPrefix) == "" {
		return ""
	}
	return p.pathPrefix + "/" + key
}

func TestProviderEnsureAccessPathPromotesSecondaryToPrimary(t *testing.T) {
	t.Parallel()

	const key = "bot-1/aa/asset.pdf"
	primary := newMemoryProvider("/data/.memoh/media")
	primary.putErr = errors.New("workspace unavailable")
	secondary := newMemoryProvider("/host/media")
	provider := New(primary, secondary)

	if err := provider.Put(context.Background(), key, bytes.NewReader([]byte("pdf"))); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	primary.putErr = nil
	got, err := provider.EnsureAccessPath(context.Background(), key)
	if err != nil {
		t.Fatalf("EnsureAccessPath() error = %v", err)
	}
	if want := "/data/.memoh/media/" + key; got != want {
		t.Fatalf("EnsureAccessPath() = %q, want %q", got, want)
	}
	if got := string(primary.values[key]); got != "pdf" {
		t.Fatalf("promoted primary bytes = %q, want pdf", got)
	}
	if _, ok := secondary.values[key]; ok {
		t.Fatal("secondary spill was not removed after promotion")
	}
}

func TestProviderAccessPathPrefersReachablePrimary(t *testing.T) {
	t.Parallel()

	const key = "bot-1/aa/asset.png"
	primary := newMemoryProvider("/data/.memoh/media")
	secondary := newMemoryProvider("/host/media")
	primary.values[key] = []byte("primary")
	secondary.values[key] = []byte("secondary")
	provider := New(primary, secondary)

	if got, want := provider.AccessPath(context.Background(), key), "/data/.memoh/media/"+key; got != want {
		t.Fatalf("AccessPath() = %q, want %q", got, want)
	}
}

func TestProviderAccessPathReturnsEmptyWithoutReachablePath(t *testing.T) {
	t.Parallel()

	const key = "bot-1/aa/missing.txt"
	primary := newMemoryProvider("/data/.memoh/media")
	secondary := newMemoryProvider("/host/media")
	provider := New(primary, secondary)

	if got, err := provider.EnsureAccessPath(context.Background(), key); got != "" || !errors.Is(err, storage.ErrAccessPathUnavailable) {
		t.Fatalf("EnsureAccessPath() = (%q, %v), want empty ErrAccessPathUnavailable", got, err)
	}

	primary.values[key] = []byte("present but not addressable")
	primary.pathPrefix = ""
	secondary.values[key] = []byte("secondary")
	if got, err := provider.EnsureAccessPath(context.Background(), key); got != "" || !errors.Is(err, storage.ErrAccessPathUnavailable) {
		t.Fatalf("EnsureAccessPath() with unaddressable primary = (%q, %v), want empty ErrAccessPathUnavailable", got, err)
	}
	if got := string(primary.values[key]); got != "present but not addressable" {
		t.Fatalf("unaddressable primary was overwritten with %q", got)
	}
}

func TestProviderAccessPathDoesNotExposeSecondaryWhenPromotionFails(t *testing.T) {
	t.Parallel()

	const key = "bot-1/aa/asset.pdf"
	primary := newMemoryProvider("/data/.memoh/media")
	primary.putErr = errors.New("workspace unavailable")
	secondary := newMemoryProvider("/host/media")
	secondary.values[key] = []byte("pdf")
	provider := New(primary, secondary)

	if got := provider.AccessPath(context.Background(), key); got != "" {
		t.Fatalf("AccessPath() = %q, want empty instead of secondary host path", got)
	}
	if _, ok := secondary.values[key]; !ok {
		t.Fatal("failed promotion removed the only secondary copy")
	}
}

func TestProviderDeleteCleansBothStores(t *testing.T) {
	t.Parallel()

	const key = "bot-1/aa/asset.pdf"
	primary := newMemoryProvider("/data/.memoh/media")
	secondary := newMemoryProvider("/host/media")
	primary.values[key] = []byte("primary")
	secondary.values[key] = []byte("secondary")
	provider := New(primary, secondary)

	if err := provider.Delete(context.Background(), key); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, ok := primary.values[key]; ok {
		t.Fatal("primary copy remains after Delete()")
	}
	if _, ok := secondary.values[key]; ok {
		t.Fatal("secondary copy remains after Delete()")
	}
}
