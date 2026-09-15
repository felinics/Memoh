//go:build linux

package workspacedeps

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyOperationWaitsForWholeWorkspaceRestart(t *testing.T) {
	f := isolatedFixture(t)
	if _, err := f.svc.Install(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	before := f.readState(t, "foo")
	rec, _ := f.store.get(f.key("foo"))
	rec.Status, rec.OperationID, rec.OperationIntent = StatusUpdating, strings.Repeat("a", 32), nil
	f.store.seed(rec)
	legacyLock := lockPath(f.home("foo"), "foo")
	if err := os.MkdirAll(filepath.Dir(legacyLock), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "flock", legacyLock, "sh", "-c", "printf 'ready\\n'; cat >/dev/null") //nolint:gosec // The synthetic older runner holds only this fixture's lock.
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = command.Process.Kill() })
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.markInterrupted(f.ctx(), f.key("foo"), rec); !errors.Is(err, ErrBusy) {
		t.Fatalf("new control lock reclaimed a live legacy runner: %v", err)
	}
	peer := NewService(Options{Workspace: f.ws, Store: f.store, Catalog: f.cat})
	if _, err := peer.markInterrupted(f.ctx(), f.key("foo"), rec); !errors.Is(err, ErrBusy) {
		t.Fatalf("Server restart forgot enrolled workspace lifetime: %v", err)
	}
	_ = stdin.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.markInterrupted(f.ctx(), f.key("foo"), rec); !errors.Is(err, ErrBusy) {
		t.Fatalf("legacy lock inactivity substituted for full restart: %v", err)
	}
	// Simulate the database fence recorded before a controlled full restart.
	f.store.mu.Lock()
	current := f.store.records[f.key("foo")]
	if current.OperationIntent == nil || current.OperationIntent.ControlMigrationEpoch == "" {
		f.store.mu.Unlock()
		t.Fatal("legacy operation did not enroll a proven workspace lifetime")
	}
	current.OperationIntent.ControlMigrationEpoch = "prior-workspace-lifetime"
	f.store.records[f.key("foo")] = current
	f.store.mu.Unlock()
	terminal, err := peer.markInterrupted(f.ctx(), f.key("foo"), rec)
	if err != nil || terminal.Status != StatusFailed || terminal.OperationID != "" {
		t.Fatalf("dead legacy claim not released: %+v %v", terminal, err)
	}
	if after := f.readState(t, "foo"); after.InstallationID != before.InstallationID {
		t.Fatal("legacy retirement modified the installed payload")
	}
	if _, err := os.Stat(before.Entrypoints["foo"]); err != nil {
		t.Fatal("legacy retirement removed the usable command", err)
	}
}
