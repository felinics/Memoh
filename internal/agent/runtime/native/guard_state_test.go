package native

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	tools "github.com/felinics/memoh/internal/agent/tool"
)

func TestToolAbortRegistryConcurrentAccess(t *testing.T) {
	registry := newToolAbortRegistry()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("call-%d", i)
			registry.Add(id)
			if !registry.Any() {
				t.Error("expected registry to report pending aborts")
			}
			if !registry.Take(id) {
				t.Errorf("expected to take %s", id)
			}
		}(i)
	}
	wg.Wait()

	if registry.Any() {
		t.Fatal("expected registry to be empty")
	}
}

func TestToolEventCollectorCloseIgnoresLateAdds(t *testing.T) {
	collector := newToolEventCollector()

	for i := 0; i < 5; i++ {
		if !collector.Add(tools.ToolStreamEvent{Type: tools.StreamEventSpawnProgress}) {
			t.Fatalf("unexpected add failure before close, i=%d", i)
		}
	}
	snapshot := collector.CloseAndSnapshot()
	if len(snapshot) != 5 {
		t.Fatalf("expected snapshot len 5, got %d", len(snapshot))
	}

	var wg sync.WaitGroup
	var postCloseAdds atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if collector.Add(tools.ToolStreamEvent{Type: tools.StreamEventSpawnProgress}) {
				postCloseAdds.Add(1)
			}
		}()
	}
	wg.Wait()
	if postCloseAdds.Load() != 0 {
		t.Fatalf("expected 0 successful adds after close, got %d", postCloseAdds.Load())
	}
}
