package engine

import (
	"sync"
	"testing"
)

func TestToolLoopGuardNilReceiver(t *testing.T) {
	var guard *ToolLoopGuard

	result := guard.Inspect(ToolLoopInput{
		ToolName: "search",
		Input: map[string]any{
			"query": "memoh",
		},
	})

	if result.Hash == "" {
		t.Fatal("expected hash for nil receiver")
	}
	if result.Warn {
		t.Fatal("did not expect warning for nil receiver")
	}
	if result.Abort {
		t.Fatal("did not expect abort for nil receiver")
	}
}

func TestToolLoopGuardConcurrentInspectAndReset(t *testing.T) {
	guard := NewToolLoopGuard(2, 1)
	input := ToolLoopInput{
		ToolName: "web_search",
		Input: map[string]any{
			"query":     "memoh logs",
			"requestId": "volatile",
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				result := guard.Inspect(input)
				if result.Hash == "" {
					t.Error("expected non-empty hash")
					return
				}
				if (i+j)%25 == 0 {
					guard.Reset()
				}
			}
		}(i)
	}
	wg.Wait()
}
