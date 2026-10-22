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

	newService := func(t *testing.T) (*Service, *fakeContextEpochQueries, *bytes.Buffer) {
		t.Helper()
		queries := &fakeContextEpochQueries{states: map[string]string{}}
		queries.setState("33333333-3333-4333-8333-333333333333", contextepoch.State{Epoch: 1, LastReason: contextepoch.ReasonInit})
		logger, buf := newTestLogCapture()
		svc := &Service{
			contextEpochs: contextepoch.NewStore(queries, nil),
			logger:        logger,
		}
		return svc, queries, buf
	}
	const sessionID = "33333333-3333-4333-8333-333333333333"

	t.Run("first observation pins and stays silent", func(t *testing.T) {
		t.Parallel()
		svc, queries, buf := newService(t)
		svc.reportStablePrefixHash(context.Background(), sessionID, 1, "", "hash-a")
		if got := queries.state(sessionID).PrefixHash; got != "hash-a" {
			t.Fatalf("pinned hash = %q, want hash-a", got)
		}
		if strings.Contains(buf.String(), "context_epoch_prefix_drift") {
			t.Fatalf("first observation must not alarm: %s", buf)
		}
	})

	t.Run("same hash stays silent", func(t *testing.T) {
		t.Parallel()
		svc, _, buf := newService(t)
		svc.reportStablePrefixHash(context.Background(), sessionID, 1, "hash-a", "hash-a")
		if strings.Contains(buf.String(), "context_epoch_prefix_drift") {
			t.Fatalf("matching hash must not alarm: %s", buf)
		}
	})

	t.Run("drift inside one epoch raises the alarm", func(t *testing.T) {
		t.Parallel()
		svc, queries, buf := newService(t)
		svc.reportStablePrefixHash(context.Background(), sessionID, 1, "hash-a", "hash-b")
		if !strings.Contains(buf.String(), "context_epoch_prefix_drift") {
			t.Fatalf("drift must alarm: %s", buf)
		}
		if !strings.Contains(buf.String(), "pinned_prefix_hash=hash-a") || !strings.Contains(buf.String(), "observed_prefix_hash=hash-b") {
			t.Fatalf("alarm must name both hashes: %s", buf)
		}
		// The pin is never overwritten by the drift: it stays hash-a.
		if got := queries.state(sessionID).PrefixHash; got != "" {
			t.Fatalf("report with a loaded pin must not write: pinned = %q", got)
		}
	})

	t.Run("racing pin wins and the loser alarms", func(t *testing.T) {
		t.Parallel()
		svc, queries, buf := newService(t)
		// No pin at load time; a racing turn pins hash-a before this turn
		// reports hash-b.
		queries.setState(sessionID, contextepoch.State{Epoch: 1, LastReason: contextepoch.ReasonInit, PrefixHash: "hash-a"})
		svc.reportStablePrefixHash(context.Background(), sessionID, 1, "", "hash-b")
		if got := queries.state(sessionID).PrefixHash; got != "hash-a" {
			t.Fatalf("racing pin = %q, want hash-a", got)
		}
		if !strings.Contains(buf.String(), "context_epoch_prefix_drift") {
			t.Fatalf("the losing observation must alarm: %s", buf)
		}
	})

	t.Run("empty observations are ignored", func(t *testing.T) {
		t.Parallel()
		svc, queries, buf := newService(t)
		svc.reportStablePrefixHash(context.Background(), sessionID, 1, "", "")
		if got := queries.state(sessionID).PrefixHash; got != "" {
			t.Fatalf("empty observation pinned %q", got)
		}
		if buf.Len() != 0 {
			t.Fatalf("empty observation must stay silent: %s", buf)
		}
	})
}
