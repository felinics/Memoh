package modelcatalog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const validBody = `{"p":{"id":"p","models":{"m":{"id":"m","name":"<Model>","tool_call":true}}}}`

func TestCacheRevalidationFailureAndDiskRestart(t *testing.T) {
	var calls, fail atomic.Int32
	var elapsed atomic.Int64
	start := time.Now().UTC().Truncate(time.Second)
	clock := func() time.Time { return start.Add(time.Duration(elapsed.Load())) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("credentials reached public catalog")
		}
		if fail.Load() != 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("If-None-Match") == `"one"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"one"`)
		_, _ = w.Write([]byte(validBody))
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "cache", "models.json")
	options := Options{URL: upstream.URL, CachePath: path, Now: clock}
	service := New(nil, options)
	first, err := service.Models(context.Background(), "p", "openai-completions")
	if err != nil {
		t.Fatal(err)
	}
	if first.Stale || calls.Load() != 1 {
		t.Fatal(first, calls.Load())
	}
	first.Models[0].Config.Modalities.Input[0] = "audio"
	second, err := service.Models(context.Background(), "p", "openai-completions")
	if err != nil {
		t.Fatal(err)
	}
	if second.Models[0].Config.Modalities.Input[0] != "text" || calls.Load() != 1 {
		t.Fatal("caller mutated shared cache")
	}
	restarted, err := New(nil, options).Models(context.Background(), "p", "openai-completions")
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Revision != first.Revision || calls.Load() != 1 {
		t.Fatal("restart did not reuse exact valid snapshot")
	}
	elapsed.Store(int64(cacheTTL + time.Minute))
	revalidated, err := service.Models(context.Background(), "p", "openai-completions")
	if err != nil {
		t.Fatal(err)
	}
	if revalidated.Stale || revalidated.Revision != first.Revision || !revalidated.CheckedAt.Equal(clock()) || calls.Load() != 2 {
		t.Fatal(revalidated, calls.Load())
	}
	fail.Store(1)
	elapsed.Store(int64(2*cacheTTL + 2*time.Minute))
	stale, err := service.Models(context.Background(), "p", "openai-completions")
	if err != nil || !stale.Stale {
		t.Fatal(stale, err)
	}
	_, _ = service.Models(context.Background(), "p", "openai-completions")
	if calls.Load() != 3 {
		t.Fatal("failed source was retried without backoff", calls.Load())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	fail.Store(0)
	elapsed.Store(int64(2*cacheTTL + 4*time.Minute))
	recovered, err := service.Models(context.Background(), "p", "openai-completions")
	if err != nil || recovered.Stale {
		t.Fatal(recovered, err)
	}
}

func TestCoalescedFetchSurvivesOneCanceledCaller(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			_, _ = w.Write([]byte(validBody))
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	service := New(nil, Options{URL: upstream.URL})
	ctx, cancel := context.WithCancel(context.Background())
	canceled := make(chan error, 1)
	go func() { _, err := service.Models(ctx, "p", "openai-completions"); canceled <- err }()
	<-entered
	var group sync.WaitGroup
	errorsCh := make(chan error, 16)
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := service.Models(context.Background(), "p", "openai-completions")
			errorsCh <- err
		}()
	}
	cancel()
	if err := <-canceled; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent requests fetched more than once", calls.Load())
	}
}

func TestInvalidEnvelopeDoesNotReplaceSnapshot(t *testing.T) {
	var broken atomic.Bool
	var elapsed atomic.Int64
	start := time.Now()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if broken.Load() {
			_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable"}}`))
			return
		}
		_, _ = w.Write([]byte(validBody))
	}))
	defer upstream.Close()
	service := New(nil, Options{URL: upstream.URL, Now: func() time.Time { return start.Add(time.Duration(elapsed.Load())) }})
	first, err := service.Models(context.Background(), "p", "openai-completions")
	if err != nil {
		t.Fatal(err)
	}
	broken.Store(true)
	elapsed.Store(int64(cacheTTL + time.Minute))
	stale, err := service.Models(context.Background(), "p", "openai-completions")
	if err != nil || !stale.Stale || stale.Revision != first.Revision || !stale.Models[0].Config.ToolCall {
		t.Fatal(stale, err)
	}
}

func TestCorruptDiskAndColdFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusBadGateway) }))
	defer upstream.Close()
	service := New(nil, Options{URL: upstream.URL, CachePath: path})
	if _, err := service.Models(context.Background(), "p", "openai-completions"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := service.Models(context.Background(), "p", "openai-completions"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("cold failure ignored retry backoff")
	}
}
