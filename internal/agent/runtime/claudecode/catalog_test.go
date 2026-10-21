package claudecode

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestClaudeCommandCatalogNeverWaitsForProbe(t *testing.T) {
	installed := []string{"deploy"}
	probes := 0
	gate := make(chan struct{})
	catalog := newCommandCatalog(func(ctx context.Context, _ external.PromptInput) (observedCommands, error) {
		<-gate
		probes++
		if target := bridge.WorkspaceTargetFromContext(ctx); target != "native" {
			t.Errorf("probed workspace target %q, want the session's", target)
		}
		if installed == nil {
			return observedCommands{}, errors.New("workspace unreachable")
		}
		return observedCommands{skills: installed}, nil
	}, slog.Default())
	ctx := bridge.WithWorkspaceTarget(t.Context(), "native")
	input := external.PromptInput{BotID: "bot", BotAgentID: "agent"}
	key := commandCatalogInputKey(ctx, input)
	// Returns what lookup served, then lets any probe it started land.
	lookup := func() ([]string, bool) {
		observed, ok := catalog.lookup(ctx, input)
		_, _, _ = catalog.flight.Do(key.flightKey(), func() (any, error) { return nil, nil })
		return observed.skills, ok
	}
	age := func() {
		catalog.mu.Lock()
		defer catalog.mu.Unlock()
		entry := catalog.entries[key]
		entry.observedAt = time.Now().Add(-commandProbeRetry)
		catalog.entries[key] = entry
	}

	// The first probe cannot finish until lookup has returned.
	served := make(chan []string, 1)
	go func() {
		observed, _ := catalog.lookup(ctx, input)
		served <- observed.skills
	}()
	select {
	case got := <-served:
		if got != nil {
			t.Fatalf("served an observation before any probe: %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("waited for the first probe")
	}
	close(gate)
	_, _, _ = catalog.flight.Do(key.flightKey(), func() (any, error) { return nil, nil })
	// A skill installed later waits for /reload-skills, however old the observation.
	installed = []string{"deploy", "review"}
	age()
	if got, _ := lookup(); len(got) != 1 || probes != 1 {
		t.Fatalf("probed again before a reload: %v after %d probes", got, probes)
	}
	catalog.forget("bot")
	if got, ok := lookup(); ok {
		t.Fatalf("served %v after a reload", got)
	}
	if got, _ := lookup(); len(got) != 2 {
		t.Fatalf("a skill installed before the reload is missing: %v", got)
	}
	// After a failure the session's own recorded commands apply, and the probe
	// is retried only once the failure has aged.
	installed = nil
	catalog.forget("bot")
	lookup()
	if got, ok := lookup(); ok || probes != 3 {
		t.Fatalf("a failed probe served %v or was retried at once after %d probes", got, probes)
	}
	installed = []string{"deploy"}
	age()
	lookup()
	if got, ok := lookup(); !ok || len(got) != 1 {
		t.Fatalf("a failed probe was not retried: %v", got)
	}
}
