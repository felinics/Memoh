package workspacedeps

import (
	"sync"
	"testing"
	"time"
)

func newTestCache(ttl time.Duration) (*Cache, *time.Time) {
	c := NewCache(ttl)
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	return c, &now
}

func sampleSnapshot() Snapshot {
	return Snapshot{
		Platform: Platform{OS: "linux", Arch: "amd64", Libc: "glibc", TmpDir: "/tmp"},
		Observed: map[string]Observed{
			"codex": {
				DepID:      "codex",
				Present:    true,
				Source:     SourceManaged,
				Version:    "0.150.0",
				Command:    "/data/.memoh/deps/codex/current/bin/codex",
				Candidates: []Candidate{{Source: SourceManaged, Path: "/data/.memoh/deps/codex/current/bin/codex", Version: "0.150.0"}},
			},
		},
	}
}

func TestCacheGetPutAndExpiry(t *testing.T) {
	c, now := newTestCache(time.Minute)
	if _, ok := c.Get("bot", "target"); ok {
		t.Fatal("empty cache reported a hit")
	}
	c.Put("bot", "target", sampleSnapshot())
	snap, ok := c.Get("bot", "target")
	if !ok || snap.Observed["codex"].Version != "0.150.0" || snap.Platform.OS != "linux" {
		t.Fatalf("Get = %+v, %v; want the stored snapshot", snap, ok)
	}
	if !snap.At.Equal(*now) {
		t.Errorf("At = %s, want stamped with now %s", snap.At, *now)
	}
	*now = now.Add(time.Minute)
	if _, ok := c.Get("bot", "target"); ok {
		t.Error("expired snapshot reported a hit")
	}
	if _, ok := c.Get("bot", "other"); ok {
		t.Error("unrelated target reported a hit")
	}
}

func TestCacheZeroTTLNeverExpires(t *testing.T) {
	c, now := newTestCache(0)
	c.Put("bot", "target", sampleSnapshot())
	*now = now.Add(365 * 24 * time.Hour)
	if _, ok := c.Get("bot", "target"); !ok {
		t.Error("snapshot expired with a zero ttl")
	}
}

func TestCacheInvalidateDropsEveryTargetOfBot(t *testing.T) {
	c, _ := newTestCache(time.Hour)
	c.Put("bot", "native", sampleSnapshot())
	c.Put("bot", "remote-1", sampleSnapshot())
	c.Put("other", "native", sampleSnapshot())
	c.Invalidate("bot")
	for _, target := range []string{"native", "remote-1"} {
		if _, ok := c.Get("bot", target); ok {
			t.Errorf("bot/%s survived Invalidate", target)
		}
	}
	if _, ok := c.Get("other", "native"); !ok {
		t.Error("Invalidate removed another bot's snapshot")
	}
}

func TestCacheObserveVersion(t *testing.T) {
	c, _ := newTestCache(time.Hour)
	c.ObserveVersion("bot", "native", "codex", "0.151.0") // nothing cached: no-op
	original := sampleSnapshot()
	c.Put("bot", "native", original)
	c.ObserveVersion("bot", "native", "codex", "0.151.0")
	c.ObserveVersion("bot", "native", "unknown", "1.0.0")

	snap, ok := c.Get("bot", "native")
	if !ok {
		t.Fatal("snapshot missing")
	}
	obs := snap.Observed["codex"]
	if obs.Version != "0.151.0" {
		t.Errorf("Version = %q, want 0.151.0", obs.Version)
	}
	if obs.Candidates[0].Version != "0.151.0" {
		t.Errorf("winning candidate Version = %q, want 0.151.0", obs.Candidates[0].Version)
	}
	if _, present := snap.Observed["unknown"]; present {
		t.Error("ObserveVersion invented an entry for an unknown dependency")
	}
	// The caller's copies must be untouched.
	if original.Observed["codex"].Version != "0.150.0" || original.Observed["codex"].Candidates[0].Version != "0.150.0" {
		t.Errorf("Put/ObserveVersion mutated the caller's snapshot: %+v", original.Observed["codex"])
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	c := NewCache(time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bot := string(rune('a' + i%4))
			c.Put(bot, "native", sampleSnapshot())
			c.Get(bot, "native")
			c.ObserveVersion(bot, "native", "codex", "0.151.0")
			if i%2 == 0 {
				c.Invalidate(bot)
			}
		}(i)
	}
	wg.Wait()
	// Odd-numbered bots (b, d) are never invalidated and must still be there.
	for _, bot := range []string{"b", "d"} {
		if snap, ok := c.Get(bot, "native"); !ok || snap.Observed["codex"].Version != "0.151.0" {
			t.Errorf("bot %s: Get = %+v, %v; want the observed version after concurrent updates", bot, snap.Observed["codex"], ok)
		}
	}
}
