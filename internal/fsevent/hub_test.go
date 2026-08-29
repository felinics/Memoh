package fsevent

import (
	"sort"
	"testing"
	"time"
)

func collect(t *testing.T, ch <-chan []string) []string {
	t.Helper()
	select {
	case paths := <-ch:
		return paths
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delivery")
		return nil
	}
}

func expectNoDelivery(t *testing.T, ch <-chan []string, wait time.Duration) {
	t.Helper()
	select {
	case paths := <-ch:
		t.Fatalf("unexpected delivery: %v", paths)
	case <-time.After(wait):
	}
}

func TestHubDeliversCoalescedBatch(t *testing.T) {
	hub := NewHub(10 * time.Millisecond)
	ch := make(chan []string, 4)
	cancel := hub.Subscribe("bot-1", func(paths []string) { ch <- paths })
	defer cancel()

	hub.Publish("bot-1", []string{"/data/a.txt"})
	hub.Publish("bot-1", []string{"/data/b.txt", "/data/a.txt"})

	got := collect(t, ch)
	sort.Strings(got)
	want := []string{"/data/a.txt", "/data/b.txt"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
	expectNoDelivery(t, ch, 50*time.Millisecond)
}

func TestHubWildcardSwallowsPaths(t *testing.T) {
	hub := NewHub(10 * time.Millisecond)
	ch := make(chan []string, 4)
	cancel := hub.Subscribe("bot-1", func(paths []string) { ch <- paths })
	defer cancel()

	hub.Publish("bot-1", []string{"/data/a.txt"})
	hub.Publish("bot-1", nil)
	hub.Publish("bot-1", []string{"/data/b.txt"})

	if got := collect(t, ch); got != nil {
		t.Fatalf("got %v, want wildcard nil", got)
	}
}

func TestHubOverflowCollapsesToWildcard(t *testing.T) {
	hub := NewHub(10 * time.Millisecond)
	ch := make(chan []string, 4)
	cancel := hub.Subscribe("bot-1", func(paths []string) { ch <- paths })
	defer cancel()

	paths := make([]string, maxBatchPaths+1)
	for i := range paths {
		paths[i] = "/data/file-" + string(rune('a'+i))
	}
	hub.Publish("bot-1", paths)

	if got := collect(t, ch); got != nil {
		t.Fatalf("got %v, want wildcard nil", got)
	}
}

func TestHubScopesByBot(t *testing.T) {
	hub := NewHub(10 * time.Millisecond)
	ch1 := make(chan []string, 4)
	ch2 := make(chan []string, 4)
	cancel1 := hub.Subscribe("bot-1", func(paths []string) { ch1 <- paths })
	defer cancel1()
	cancel2 := hub.Subscribe("bot-2", func(paths []string) { ch2 <- paths })
	defer cancel2()

	hub.Publish("bot-1", []string{"/data/a.txt"})

	if got := collect(t, ch1); len(got) != 1 || got[0] != "/data/a.txt" {
		t.Fatalf("bot-1 got %v", got)
	}
	expectNoDelivery(t, ch2, 50*time.Millisecond)
}

func TestHubUnsubscribeStopsDelivery(t *testing.T) {
	hub := NewHub(10 * time.Millisecond)
	ch := make(chan []string, 4)
	cancel := hub.Subscribe("bot-1", func(paths []string) { ch <- paths })
	cancel()
	cancel()

	hub.Publish("bot-1", []string{"/data/a.txt"})
	expectNoDelivery(t, ch, 50*time.Millisecond)
}

func TestHubSeparateWindowsDeliverSeparately(t *testing.T) {
	hub := NewHub(10 * time.Millisecond)
	ch := make(chan []string, 4)
	cancel := hub.Subscribe("bot-1", func(paths []string) { ch <- paths })
	defer cancel()

	hub.Publish("bot-1", []string{"/data/a.txt"})
	first := collect(t, ch)
	hub.Publish("bot-1", []string{"/data/b.txt"})
	second := collect(t, ch)

	if len(first) != 1 || first[0] != "/data/a.txt" {
		t.Fatalf("first delivery got %v", first)
	}
	if len(second) != 1 || second[0] != "/data/b.txt" {
		t.Fatalf("second delivery got %v", second)
	}
}

func TestHubPublishWithoutSubscribersIsNoop(_ *testing.T) {
	hub := NewHub(10 * time.Millisecond)
	hub.Publish("bot-1", []string{"/data/a.txt"})
	time.Sleep(30 * time.Millisecond)
}

func TestHubBlockedSubscriberDoesNotDelayOthers(t *testing.T) {
	hub := NewHub(10 * time.Millisecond)
	release := make(chan struct{})
	blockedGot := make(chan []string, 16)
	cancelBlocked := hub.Subscribe("bot-1", func(paths []string) {
		<-release
		blockedGot <- paths
	})
	defer cancelBlocked()
	healthy := make(chan []string, 16)
	cancelHealthy := hub.Subscribe("bot-1", func(paths []string) { healthy <- paths })
	defer cancelHealthy()

	hub.Publish("bot-1", []string{"/data/a.txt"})
	first := collect(t, healthy)
	if len(first) != 1 || first[0] != "/data/a.txt" {
		t.Fatalf("healthy first = %v", first)
	}
	hub.Publish("bot-1", []string{"/data/b.txt"})
	second := collect(t, healthy)
	if len(second) != 1 || second[0] != "/data/b.txt" {
		t.Fatalf("healthy second = %v", second)
	}

	close(release)
	seen := map[string]bool{}
	for len(seen) < 2 {
		for _, p := range collect(t, blockedGot) {
			seen[p] = true
		}
	}
	if !seen["/data/a.txt"] || !seen["/data/b.txt"] {
		t.Fatalf("blocked subscriber saw %v", seen)
	}
}

func TestHubBacklogCoalescesForSlowSubscriber(t *testing.T) {
	hub := NewHub(5 * time.Millisecond)
	release := make(chan struct{})
	deliveries := make(chan []string, 16)
	first := true
	cancel := hub.Subscribe("bot-1", func(paths []string) {
		if first {
			first = false
			<-release
		}
		deliveries <- paths
	})
	defer cancel()

	hub.Publish("bot-1", []string{"/data/a.txt"})
	time.Sleep(20 * time.Millisecond)
	// These land while the subscriber is blocked on its first delivery and
	// must merge into ONE pending batch instead of queueing per flush.
	hub.Publish("bot-1", []string{"/data/b.txt"})
	time.Sleep(20 * time.Millisecond)
	hub.Publish("bot-1", []string{"/data/c.txt"})
	time.Sleep(20 * time.Millisecond)
	close(release)

	got := collect(t, deliveries)
	if len(got) != 1 || got[0] != "/data/a.txt" {
		t.Fatalf("first delivery = %v", got)
	}
	merged := collect(t, deliveries)
	sort.Strings(merged)
	if len(merged) != 2 || merged[0] != "/data/b.txt" || merged[1] != "/data/c.txt" {
		t.Fatalf("merged backlog = %v, want [/data/b.txt /data/c.txt]", merged)
	}
	expectNoDelivery(t, deliveries, 50*time.Millisecond)
}

func TestHubWildcardSwallowsBacklogForSlowSubscriber(t *testing.T) {
	hub := NewHub(5 * time.Millisecond)
	release := make(chan struct{})
	deliveries := make(chan []string, 16)
	first := true
	cancel := hub.Subscribe("bot-1", func(paths []string) {
		if first {
			first = false
			<-release
		}
		deliveries <- paths
	})
	defer cancel()

	hub.Publish("bot-1", []string{"/data/a.txt"})
	time.Sleep(20 * time.Millisecond)
	hub.Publish("bot-1", []string{"/data/b.txt"})
	time.Sleep(20 * time.Millisecond)
	hub.Publish("bot-1", nil)
	time.Sleep(20 * time.Millisecond)
	close(release)

	if got := collect(t, deliveries); len(got) != 1 || got[0] != "/data/a.txt" {
		t.Fatalf("first delivery = %v", got)
	}
	if got := collect(t, deliveries); got != nil {
		t.Fatalf("backlog delivery = %v, want wildcard nil", got)
	}
}
