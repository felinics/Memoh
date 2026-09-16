//go:build linux

package bridgesvc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/felinics/memoh/internal/workspace/controlpath"
)

func TestProcessEpochRejectsAncestorProcNamespace(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"sys/kernel/random/boot_id", "1/stat"} {
		data, err := os.ReadFile(filepath.Join("/proc", name)) //nolint:gosec // name is one of the two fixed procfs records listed above.
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "self/ns"), 0o700); err != nil {
		t.Fatal(err)
	}
	// PID 1's namespace link is deliberately unavailable, as with a different
	// UID on CI. Self plus one NSpid supplies the same namespace proof.
	if err := os.Symlink("pid:[1234]", filepath.Join(root, "self/ns/pid")); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"NSpid:\t42\n", "NSpid:\t123 42\n", "Name:\tbridge\n", "NSpid:\t0\n"} {
		if err := os.WriteFile(filepath.Join(root, "self/status"), []byte(status), 0o600); err != nil {
			t.Fatal(err)
		}
		epoch, err := workspaceProcessEpochAt(root)
		if status == "NSpid:\t42\n" {
			if err != nil || !strings.HasSuffix(epoch, ":pid:[1234]") {
				t.Fatalf("self namespace proof was rejected: %q %v", epoch, err)
			}
		} else if err == nil || epoch != "" {
			t.Fatalf("unproven namespace status %q granted lifetime %q", status, epoch)
		}
	}
}

func seedExecutionWindow(t *testing.T, root, epoch, owner string) {
	t.Helper()
	directory := filepath.Join(root, ".memoh", "deps")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(executionWindowState{Epoch: epoch, Owner: owner, Used: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".execution-window.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionWindowRequiresFullWorkspaceBoundary(t *testing.T) {
	root := t.TempDir()
	s := New(Options{DefaultWorkDir: root, AllowHostAbsolute: true})
	if _, err := s.beginExecution(t.Context(), true); !errors.Is(err, errPayloadWindowClosed) {
		t.Fatalf("first enrollment admitted cleanup: %v", err)
	}
	restart := New(Options{DefaultWorkDir: root, AllowHostAbsolute: true})
	if _, err := restart.beginExecution(t.Context(), true); !errors.Is(err, errPayloadWindowClosed) {
		t.Fatalf("bridge-only restart admitted cleanup: %v", err)
	}
	seedExecutionWindow(t, root, "previous-workspace-lifetime", "old-bridge")
	fresh := New(Options{DefaultWorkDir: root, AllowHostAbsolute: true})
	release, err := fresh.beginExecution(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	// A second bridge cannot claim this same workspace while maintenance runs.
	if _, err := New(Options{DefaultWorkDir: root, AllowHostAbsolute: true}).beginExecution(t.Context(), true); !errors.Is(err, errPayloadWindowClosed) {
		t.Fatalf("second maintenance admitted: %v", err)
	}
	// Verify the maintenance guard is a kernel lock, not a local process mutex.
	fd, err := syscall.Open(filepath.Join(controlpath.Directory(filepath.Join(root, ".memoh", "deps")), ".execution-window.lock"), syscall.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("maintenance did not own kernel lock: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fresh.beginExecution(canceled, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("ordinary command bypassed maintenance: %v", err)
	}
	release()
	ordinary, err := fresh.beginExecution(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	ordinary()
	if _, err := fresh.beginExecution(t.Context(), true); !errors.Is(err, errPayloadWindowClosed) {
		t.Fatalf("cleanup admitted after command: %v", err)
	}
}

func TestExecutionWindowRejectsMetadataLinks(t *testing.T) {
	for _, name := range []string{".execution-window.lock", ".execution-window.json"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			seedExecutionWindow(t, root, "previous-workspace", "old-owner")
			target := filepath.Join(t.TempDir(), "protected")
			if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, ".memoh", "deps", name)
			if name == ".execution-window.lock" {
				link = filepath.Join(controlpath.Directory(filepath.Join(root, ".memoh", "deps")), name)
				if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			_ = os.Remove(link)
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			s := New(Options{DefaultWorkDir: root, AllowHostAbsolute: true})
			if release, err := s.beginExecution(t.Context(), true); err == nil {
				release()
				t.Fatal("symlinked metadata admitted cleanup")
			}
			data, err := os.ReadFile(target) //nolint:gosec // Reads the protected test fixture to prove it was not followed.
			if err != nil || string(data) != "unchanged" {
				t.Fatalf("metadata symlink target changed: %q %v", data, err)
			}
		})
	}
}
