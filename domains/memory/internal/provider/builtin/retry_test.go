package builtin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type fakeUpserter struct {
	mu       sync.Mutex
	failing  bool
	upserted []semanticRetryEntry
}

func (f *fakeUpserter) Upsert(_ context.Context, botID, nodeID, body, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return errors.New("fake upsert failure")
	}
	f.upserted = append(f.upserted, semanticRetryEntry{botID: botID, nodeID: nodeID, body: body, hash: hash})
	return nil
}

func (f *fakeUpserter) setFailing(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing = v
}

func (f *fakeUpserter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.upserted)
}

func TestSemanticRetryQueueFlushClearsSucceeded(t *testing.T) {
	t.Parallel()
	q := newSemanticRetryQueue(nil)
	q.enqueue(semanticRetryEntry{botID: "bot-a", nodeID: "n1", body: "alpha", hash: "h1"})
	q.enqueue(semanticRetryEntry{botID: "bot-a", nodeID: "n2", body: "beta", hash: "h2"})
	if got := q.depth(""); got != 2 {
		t.Fatalf("depth = %d, want 2", got)
	}

	idx := &fakeUpserter{}
	q.flush(context.Background(), idx)
	if got := q.depth(""); got != 0 {
		t.Fatalf("depth after flush = %d, want 0", got)
	}
	if got := idx.count(); got != 2 {
		t.Fatalf("upserted = %d, want 2", got)
	}
}

func TestSemanticRetryQueueKeepsFailures(t *testing.T) {
	t.Parallel()
	q := newSemanticRetryQueue(nil)
	q.enqueue(semanticRetryEntry{botID: "bot-a", nodeID: "n1", body: "alpha", hash: "h1"})

	idx := &fakeUpserter{}
	idx.setFailing(true)
	q.flush(context.Background(), idx)
	if got := q.depth(""); got != 1 {
		t.Fatalf("depth after failing flush = %d, want 1", got)
	}

	idx.setFailing(false)
	q.flush(context.Background(), idx)
	if got := q.depth(""); got != 0 {
		t.Fatalf("depth after recovering flush = %d, want 0", got)
	}
}

func TestSemanticRetryQueueDedupNewestWins(t *testing.T) {
	t.Parallel()
	q := newSemanticRetryQueue(nil)
	q.enqueue(semanticRetryEntry{botID: "bot-a", nodeID: "n1", body: "old", hash: "h1"})
	q.enqueue(semanticRetryEntry{botID: "bot-a", nodeID: "n1", body: "new", hash: "h2"})
	if got := q.depth(""); got != 1 {
		t.Fatalf("depth = %d, want 1 (deduped)", got)
	}

	idx := &fakeUpserter{}
	q.flush(context.Background(), idx)
	if got := idx.count(); got != 1 {
		t.Fatalf("upserted = %d, want 1", got)
	}
	idx.mu.Lock()
	body := idx.upserted[0].body
	idx.mu.Unlock()
	if body != "new" {
		t.Fatalf("flushed body = %q, want %q", body, "new")
	}
}

func TestSemanticRetryQueueDiscard(t *testing.T) {
	t.Parallel()
	q := newSemanticRetryQueue(nil)
	q.enqueue(semanticRetryEntry{botID: "bot-a", nodeID: "n1"})
	q.enqueue(semanticRetryEntry{botID: "bot-a", nodeID: "n2"})
	q.enqueue(semanticRetryEntry{botID: "bot-b", nodeID: "n1"})

	q.discard("bot-a", []string{"n1"})
	if got := q.depth("bot-a"); got != 1 {
		t.Fatalf("bot-a depth = %d, want 1", got)
	}

	q.discardBot("bot-a")
	if got := q.depth("bot-a"); got != 0 {
		t.Fatalf("bot-a depth after discardBot = %d, want 0", got)
	}
	if got := q.depth("bot-b"); got != 1 {
		t.Fatalf("bot-b depth = %d, want 1", got)
	}
}

func TestSemanticRetryQueueCapacityEvictsOldest(t *testing.T) {
	t.Parallel()
	q := newSemanticRetryQueue(nil)
	for i := 0; i < semanticRetryCapacity+10; i++ {
		q.enqueue(semanticRetryEntry{botID: "bot-a", nodeID: fmt.Sprintf("n%03d", i)})
	}
	if got := q.depth(""); got != semanticRetryCapacity {
		t.Fatalf("depth = %d, want %d", got, semanticRetryCapacity)
	}
	// The oldest entries were evicted; the newest survives.
	q.mu.Lock()
	_, oldestPresent := q.pending[semanticRetryKey("bot-a", "n000")]
	_, newestPresent := q.pending[semanticRetryKey("bot-a", fmt.Sprintf("n%03d", semanticRetryCapacity+9))]
	q.mu.Unlock()
	if oldestPresent {
		t.Fatal("oldest entry should have been evicted")
	}
	if !newestPresent {
		t.Fatal("newest entry should be present")
	}
}

func TestSemanticRetryQueueStopIsIdempotent(t *testing.T) {
	t.Parallel()
	q := newSemanticRetryQueue(nil)
	q.start(context.Background(), &fakeUpserter{})

	q.stop()
	q.stop()

	select {
	case <-q.stopCh:
	default:
		t.Fatal("stop channel remains open")
	}
	select {
	case <-q.done:
	default:
		t.Fatal("retry worker remains active after stop")
	}
}
