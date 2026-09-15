//go:build linux

package workspacedeps

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/payloadlease"
)

func installPendingCleanup(t *testing.T, f *serviceFixture) *State {
	t.Helper()
	if _, err := f.svc.Install(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	first := f.readState(t, "foo")
	if _, err := f.svc.Reinstall(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	jobs, err := filepath.Glob(filepath.Join(operationRoot(f.home("foo"), "foo"), ".cleanup", "*.json"))
	if err != nil || len(jobs) != 1 {
		t.Fatalf("cleanup queue: %v %v", jobs, err)
	}
	data, err := os.ReadFile(jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	var job PayloadCleanup
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	job.RetirementEpoch = "previous-workspace-lifetime"
	data, err = json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jobs[0], data, 0o600); err != nil {
		t.Fatal(err)
	}
	return &first
}

func startupCleanupClient(t *testing.T, root string) *bridge.Client {
	t.Helper()
	directory := filepath.Join(root, ".memoh", "deps")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	// This fixture models durable metadata from the previous whole workspace.
	// Production derives the new identity from boot ID, PID namespace and PID 1.
	if err := os.WriteFile(filepath.Join(directory, ".execution-window.json"), []byte(`{"epoch":"previous-workspace-lifetime","owner":"previous-bridge","used":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return newExecTestClientAtRoot(t, root)
}

func assertStartupCleanupResult(t *testing.T, payload string) {
	t.Helper()
	// A non-root CI process cannot inspect root's PID 1 maps. That is a real
	// missing ownership proof, so this environment must exercise retention;
	// the same tests exercise successful collection in a readable namespace.
	initMaps, err := os.Open("/proc/1/maps")
	if errors.Is(err, os.ErrPermission) {
		if _, err := os.Stat(payload); err != nil {
			t.Fatal("payload removed despite unreadable bootstrap process", err)
		}
		t.Log("unreadable PID 1 ownership correctly retains the retired payload")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = initMaps.Close()
	if _, err := os.Stat(payload); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("safe startup did not reclaim old payload: %v", err)
	}
}

func TestStartupPayloadGCDefersForReferenceAndClosesAfterExec(t *testing.T) {
	f := isolatedFixture(t)
	first := installPendingCleanup(t, f)
	client := startupCleanupClient(t, f.dataRoot)
	command := exec.CommandContext(t.Context(), "sh", "-c", "cd \"$1\"; printf 'ready\\n'; read line", "sh", first.PayloadPath) //nolint:gosec // Opens only the test-owned candidate directory.
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
	if err := f.svc.ReapPayloadsAtStartup(f.ctx(), testBot, client); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.PayloadPath); err != nil {
		t.Fatal("bootstrap process lost payload", err)
	}
	_, _ = io.WriteString(stdin, "exit\n")
	_ = stdin.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReapPayloadsAtStartup(f.ctx(), testBot, client); err != nil {
		t.Fatal(err)
	}
	assertStartupCleanupResult(t, first.PayloadPath)
	if _, err := os.Stat(f.readState(t, "foo").PayloadPath); err != nil {
		t.Fatal("current removed", err)
	}
	if _, err := client.Exec(f.ctx(), "true", "", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ExecQuiescent(f.ctx(), "echo unexpected\n"); !errors.Is(err, bridge.ErrPayloadWindowClosed) {
		t.Fatalf("ordinary exec did not close window: %v", err)
	}
}

func TestStartupPayloadGCHonorsResolvedLauncherLease(t *testing.T) {
	f := isolatedFixture(t)
	first := installPendingCleanup(t, f)
	lease, err := payloadlease.Acquire(f.ctx(), f.client, payloadlease.LockPath(f.dataRoot, "foo"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	client := startupCleanupClient(t, f.dataRoot)
	if err := f.svc.ReapPayloadsAtStartup(f.ctx(), testBot, client); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.PayloadPath); err != nil {
		t.Fatal("lease between resolve and spawn lost payload", err)
	}
	// The spawned CLI and descendants inherit their installation lock. This
	// remains held after the Server releases the interval-before-spawn lease.
	processLock := payloadlease.EntrypointLockPath(f.dataRoot, "foo", first.Entrypoints["foo"])
	command := exec.CommandContext(t.Context(), "sh", "-c", payloadlease.Command("sh -c 'echo ready; read line'", processLock, lease.Epoch)) //nolint:gosec // Executes the test-owned kernel lease guard until stdin releases it.
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
	lease.Release()
	if err := f.svc.ReapPayloadsAtStartup(f.ctx(), testBot, client); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.PayloadPath); err != nil {
		t.Fatal("running CLI lost inherited payload ownership", err)
	}
	_, _ = io.WriteString(stdin, "exit\n")
	_ = stdin.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	// A blocking kernel lock acquisition confirms release without sleep timing.
	if _, err := f.client.Exec(f.ctx(), "flock -x "+shellQuote(lease.Path)+" true", "", 5); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReapPayloadsAtStartup(f.ctx(), testBot, client); err != nil {
		t.Fatal(err)
	}
	assertStartupCleanupResult(t, first.PayloadPath)
	marker := filepath.Join(t.TempDir(), "must-not-spawn")
	stale := payloadlease.Command("touch "+shellQuote(marker), lease.Path, "previous-workspace-lifetime")
	result, err := f.client.Exec(f.ctx(), stale, "", 5)
	if err != nil || result.ExitCode != 76 {
		t.Fatalf("stale resolved launcher was accepted: %+v %v", result, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale command spawned")
	}
}

func TestUnreadableProcessMetadataDefersPayloadCleanup(t *testing.T) {
	// Inject a proc tree with an unreadable/missing maps record. This must not
	// be mistaken for a negative reference scan (common under hidepid/cross UID).
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "100", "fd"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "unsafe-delete")
	script := "set -eu\npayload=/payload/old\n" + strings.ReplaceAll(payloadReferenceGuard, "/proc/[0-9]*", shellQuote(root)+"/[0-9]*") + "touch " + shellQuote(marker) + "\n"
	command := exec.CommandContext(t.Context(), "sh", "-s")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", output, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unreadable process state treated as unused")
	}
}
