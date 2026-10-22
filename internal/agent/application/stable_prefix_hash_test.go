package application

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/chat/contextepoch"
)

// newTestLogCapture collects slog records for drift-alarm assertions.
func newTestLogCapture() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, nil)), buf
}

func TestStablePrefixHashAccounting(t *testing.T) {
	t.Parallel()

	const sessionID = "33333333-3333-4333-8333-333333333333"
	newService := func(t *testing.T, state contextepoch.State) (*Service, *fakeContextEpochQueries, *bytes.Buffer) {
		t.Helper()
		queries := &fakeContextEpochQueries{states: map[string]string{}}
		queries.setState(sessionID, state)
		logger, buf := newTestLogCapture()
		svc := &Service{
			contextEpochs: contextepoch.NewStore(queries, nil),
			logger:        logger,
		}
		return svc, queries, buf
	}
	epochOne := contextepoch.State{Epoch: 1, LastReason: contextepoch.ReasonInit}

	t.Run("first observation pins and stays silent", func(t *testing.T) {
		t.Parallel()
		svc, queries, buf := newService(t, epochOne)
		svc.reportStablePrefixHash(context.Background(), sessionID, "hash-a")
		if got := queries.state(sessionID).PrefixHash; got != "hash-a" {
			t.Fatalf("pinned hash = %q, want hash-a", got)
		}
		if strings.Contains(buf.String(), "context_epoch_prefix_drift") {
			t.Fatalf("first observation must not alarm: %s", buf)
		}
	})

	t.Run("same hash stays silent", func(t *testing.T) {
		t.Parallel()
		svc, _, buf := newService(t, contextepoch.State{Epoch: 1, LastReason: contextepoch.ReasonInit, PrefixHash: "hash-a"})
		svc.reportStablePrefixHash(context.Background(), sessionID, "hash-a")
		if strings.Contains(buf.String(), "context_epoch_prefix_drift") {
			t.Fatalf("matching hash must not alarm: %s", buf)
		}
	})

	t.Run("drift inside one epoch raises the alarm", func(t *testing.T) {
		t.Parallel()
		svc, queries, buf := newService(t, contextepoch.State{Epoch: 1, LastReason: contextepoch.ReasonInit, PrefixHash: "hash-a"})
		svc.reportStablePrefixHash(context.Background(), sessionID, "hash-b")
		if !strings.Contains(buf.String(), "context_epoch_prefix_drift") {
			t.Fatalf("drift must alarm: %s", buf)
		}
		if !strings.Contains(buf.String(), "pinned_prefix_hash=hash-a") || !strings.Contains(buf.String(), "observed_prefix_hash=hash-b") {
			t.Fatalf("alarm must name both hashes: %s", buf)
		}
		// The pin is never overwritten by the drift: it stays hash-a.
		if got := queries.state(sessionID).PrefixHash; got != "hash-a" {
			t.Fatalf("drift must not rewrite the pin: pinned = %q", got)
		}
	})

	t.Run("racing pin wins and the loser alarms", func(t *testing.T) {
		t.Parallel()
		svc, queries, buf := newService(t, epochOne)
		// The report's own load sees no pin; a racing turn pins hash-a before
		// this turn's pin write lands, so the guarded first-pin-wins write
		// returns hash-a and the observed hash-b alarms.
		queries.setState(sessionID, contextepoch.State{Epoch: 1, LastReason: contextepoch.ReasonInit, PrefixHash: "hash-a"})
		svc.reportStablePrefixHash(context.Background(), sessionID, "hash-b")
		if got := queries.state(sessionID).PrefixHash; got != "hash-a" {
			t.Fatalf("racing pin = %q, want hash-a", got)
		}
		if !strings.Contains(buf.String(), "context_epoch_prefix_drift") {
			t.Fatalf("the losing observation must alarm: %s", buf)
		}
	})

	t.Run("empty observations are ignored", func(t *testing.T) {
		t.Parallel()
		svc, queries, buf := newService(t, epochOne)
		svc.reportStablePrefixHash(context.Background(), sessionID, "")
		if got := queries.state(sessionID).PrefixHash; got != "" {
			t.Fatalf("empty observation pinned %q", got)
		}
		if buf.Len() != 0 {
			t.Fatalf("empty observation must stay silent: %s", buf)
		}
	})

	// Regression: an in-turn budget recovery advances the epoch between
	// assembly and the hash report. The report must compare against the
	// post-recovery state — the carried pin still matches (no false drift)
	// and any genuine drift is attributed to the new epoch.
	t.Run("mid-turn recovery advance keeps the pin and attributes the epoch", func(t *testing.T) {
		t.Parallel()
		svc, queries, buf := newService(t, contextepoch.State{Epoch: 1, LastReason: contextepoch.ReasonInit, PrefixHash: "hash-a"})
		if _, err := svc.contextEpochs.AdvanceContextEpoch(context.Background(), sessionID, "test", contextepoch.ReasonBudgetRecovery); err != nil {
			t.Fatalf("advance: %v", err)
		}
		svc.reportStablePrefixHash(context.Background(), sessionID, "hash-a")
		if strings.Contains(buf.String(), "context_epoch_prefix_drift") {
			t.Fatalf("the carried pin must match after a recovery advance: %s", buf)
		}
		svc.reportStablePrefixHash(context.Background(), sessionID, "hash-b")
		out := buf.String()
		if !strings.Contains(out, "context_epoch_prefix_drift") || !strings.Contains(out, "epoch=2") {
			t.Fatalf("drift after the advance must be attributed to epoch 2: %s", out)
		}
		if got := queries.state(sessionID); got.Epoch != 2 || got.PrefixHash != "hash-a" {
			t.Fatalf("state = %+v; want epoch 2 with the carried pin", got)
		}
	})
}
