package client

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/agentprocess"
)

func TestForceCloseDoesNotTreatTransportClosureAsProcessExit(t *testing.T) {
	done, finalized := make(chan struct{}), make(chan struct{})
	close(done)
	close(finalized)
	session := &Session{proc: &bridgeProcess{done: done, finalizeDone: finalized}}
	if err := session.ForceClose(); !errors.Is(err, agentprocess.ErrTerminationUnconfirmed) {
		t.Fatalf("missing EXIT receipt was reported as stopped: %v", err)
	}
}

func TestForceCloseConfirmsRunningBridgeProcessBeforeCancelingTransport(t *testing.T) {
	client := newTestBridgeClient(t, t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	proc, err := startBridgeProcess(ctx, client, "sh", []string{"-c", "printf ready; sleep 30"}, "", time.Minute, processOptions{
		AgentID: "acp", SetupMode: SetupModeAPIKey, NoTimeout: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Close() }()
	ready := make([]byte, 5)
	if _, err := io.ReadFull(proc, ready); err != nil || string(ready) != "ready" {
		t.Fatalf("process did not become ready: %q %v", ready, err)
	}
	go func() { _, _ = io.Copy(io.Discard, proc) }()
	session := &Session{proc: proc, cancel: cancel, lifecycleCtx: ctx}
	if err := session.ForceClose(); err != nil {
		t.Fatalf("force close did not confirm remote exit: %v", err)
	}
}
