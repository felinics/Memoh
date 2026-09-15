package workspacedeps

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

const isolatedFooInstall = `# memoh-storage-layout: isolated
version="$MEMOH_DEP_VERSION"
mkdir -p "$MEMOH_DEP_STAGING/bin"
printf '#!/bin/sh\necho "foo %s"\n' "$version" > "$MEMOH_DEP_STAGING/bin/foo"
chmod 0755 "$MEMOH_DEP_STAGING/bin/foo"
if [ "$version" = 9.0.0 ]; then exit 19; fi
[ ! -e "$MEMOH_DEP_INSTALL_DIR" ] || exit 21
mv "$MEMOH_DEP_STAGING" "$MEMOH_DEP_INSTALL_DIR"
dep_switch "$MEMOH_DEP_INSTALL_DIR"
dep_result "{\"version\":\"$version\",\"entrypoints\":{\"foo\":\"$MEMOH_DEP_HOME/current/bin/foo\"}}"
`

const isolatedFooVersion = `version=$($MEMOH_DEP_CANDIDATE --version | sed 's/^foo //')
dep_result "{\"version\":\"$version\"}"
`

func isolatedFixture(t *testing.T) *serviceFixture {
	t.Helper()
	f := newServiceFixture(t)
	manifest := strings.Replace(strings.Replace(e2eFooYAML, ", libc: glibc", "", 1), "  remove: remove.sh", "  remove: remove.sh\n  version: version.sh", 1)
	cat, err := catalog.LoadFS(fstest.MapFS{
		"foo/dependency.yaml": &fstest.MapFile{Data: []byte(manifest)},
		"foo/install.sh":      &fstest.MapFile{Data: []byte(isolatedFooInstall)},
		"foo/version.sh":      &fstest.MapFile{Data: []byte(isolatedFooVersion)},
		"foo/remove.sh":       &fstest.MapFile{Data: []byte("dep_result '{}'\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.cat, f.svc.catalog = cat, cat
	f.svc.run, f.svc.discover = Run, Discover
	platform, err := ProbePlatform(f.ctx(), f.client)
	if err != nil {
		t.Fatal(err)
	}
	platform.TmpDir = t.TempDir()
	f.platform = platform
	f.svc.dependencyStoreRoot, err = filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestIsolatedSameVersionInstallPreservesCurrentUntilCommit(t *testing.T) {
	f := isolatedFixture(t)
	if _, err := f.svc.Install(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	first := f.readState(t, "foo")
	if first.PayloadPath == "" || first.InstallationID == "" || !strings.HasPrefix(first.PayloadPath, f.svc.dependencyStoreRoot+"/") {
		t.Fatalf("missing isolated identity: %+v", first)
	}
	if !strings.HasPrefix(first.Entrypoints["foo"], first.PayloadPath+"/") {
		t.Fatalf("launcher is not pinned: %+v", first)
	}
	// A failed replacement must preserve both its executable and stable pointer.
	if _, err := f.svc.Update(f.ctx(), testBot, "foo", "9.0.0", nil); err == nil {
		t.Fatal("expected download failure")
	}
	staging, err := filepath.Glob(filepath.Join(f.svc.dependencyStoreRoot, "foo", ".staging-*"))
	if err != nil || len(staging) != 0 {
		t.Fatalf("failed operation retained staging: %v %v", staging, err)
	}
	failed := f.readState(t, "foo")
	if failed.InstallationID != first.InstallationID {
		t.Fatal("failure replaced current installation")
	}
	output, err := exec.CommandContext(f.ctx(), f.shimPath("foo")).Output() //nolint:gosec // Executes the synthetic CLI installed in this test workspace.
	if err != nil || strings.TrimSpace(string(output)) != "foo 1.0.0" {
		t.Fatalf("old command broken: %s %v", output, err)
	}
	if _, err := f.svc.Reinstall(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	second := f.readState(t, "foo")
	if second.InstallationID == first.InstallationID || second.PayloadPath == first.PayloadPath {
		t.Fatal("same-version reinstall reused a payload directory")
	}
	output, err = exec.CommandContext(f.ctx(), f.shimPath("foo")).Output() //nolint:gosec // Executes the synthetic CLI installed in this test workspace.
	if err != nil || strings.TrimSpace(string(output)) != "foo 1.0.0" {
		t.Fatalf("new command broken: %s %v", output, err)
	}
}

func TestIsolatedVersionProbeRejectsBrokenCandidate(t *testing.T) {
	f := isolatedFixture(t)
	if _, err := f.svc.Install(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	before := f.readState(t, "foo")
	original := f.svc.run
	f.svc.run = func(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error) {
		result, err := original(ctx, client, spec, sink)
		if err == nil && spec.Action == catalog.ActionVersion {
			result.Version = "0.0.0"
		}
		return result, err
	}
	if _, err := f.svc.Update(f.ctx(), testBot, "foo", "2.0.0", nil); !errors.Is(err, errInvalidResult) {
		t.Fatalf("unexpected error: %v", err)
	}
	after := f.readState(t, "foo")
	if after.InstallationID != before.InstallationID {
		t.Fatal("unverified candidate became current")
	}
}

func TestRootFilesystemLossLeavesPersistentMetadataAndRestorableLayout(t *testing.T) {
	f := isolatedFixture(t)
	if _, err := f.svc.Install(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	first := f.readState(t, "foo")
	if err := os.RemoveAll(f.svc.dependencyStoreRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(StatePath(f.home("foo"))); err != nil {
		t.Fatal("persistent state lost", err)
	}
	list, err := f.svc.Refresh(f.ctx(), testBot)
	if err != nil {
		t.Fatal(err)
	}
	if f.entry(t, list, "foo").Observed.Present {
		t.Fatal("lost payload still reported present")
	}
	if _, err := f.svc.Reinstall(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	restored := f.readState(t, "foo")
	if restored.InstallationID == first.InstallationID {
		t.Fatal("repair reused missing installation identity")
	}
	if _, err := os.Stat(restored.Entrypoints["foo"]); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupNeverDeletesCurrentOrSymlinkedAncestor(t *testing.T) {
	f := isolatedFixture(t)
	if _, err := f.svc.Install(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	current := f.readState(t, "foo")
	job := PayloadCleanup{OperationID: strings.Repeat("a", 32), DependencyID: "foo", StoreRoot: current.StoreRoot, PayloadPath: current.PayloadPath}
	file := filepath.Join(t.TempDir(), "job.json")
	if err := os.WriteFile(file, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	script, err := cleanupScript(f.home("foo"), job, file, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := runFilesystemScript(f.ctx(), f.client, f.home("foo"), "foo", script); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(current.Entrypoints["foo"]); err != nil {
		t.Fatal("current payload was removed", err)
	}
	// Removing a namespace symlink must never traverse into its target.
	unsafeRoot := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(current.StoreRoot, unsafeRoot); err != nil {
		t.Fatal(err)
	}
	job.StoreRoot = unsafeRoot
	job.PayloadPath = filepath.Join(unsafeRoot, "foo", "installs", current.InstallationID)
	script, err = cleanupScript(f.home("foo"), job, file, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := runFilesystemScript(f.ctx(), f.client, f.home("foo"), "foo", script); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(current.Entrypoints["foo"]); err != nil {
		t.Fatal("symlink escaped namespace", err)
	}
}

func TestObsoletePayloadWaitsForRunningProcess(t *testing.T) {
	f := isolatedFixture(t)
	if _, err := f.svc.Install(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	first := f.readState(t, "foo")
	command := exec.CommandContext(f.ctx(), "sh", "-c", "cd \"$1\"; printf 'ready\\n'; read line", "sh", first.PayloadPath) //nolint:gosec // Holds only the test payload open until the test releases its stdin.
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
	if _, err := f.svc.Reinstall(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.PayloadPath); err != nil {
		t.Fatal("active payload was removed", err)
	}
	_, _ = io.WriteString(stdin, "exit\n")
	_ = stdin.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReapPayloads(f.ctx(), testBot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.PayloadPath); err != nil {
		t.Fatalf("same-lifetime payload must remain pending: %v", err)
	}
}

func TestRecoveryDoesNotCommitSuccessReceiptAfterPayloadLoss(t *testing.T) {
	f := isolatedFixture(t)
	run := f.svc.run
	f.svc.run = func(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error) {
		result, err := run(ctx, client, spec, sink)
		if err == nil && spec.Action == catalog.ActionInstall {
			return result, ErrOperationUncertain
		}
		return result, err
	}
	if _, err := f.svc.Install(f.ctx(), testBot, "foo", "1.0.0", nil); !errors.Is(err, ErrOperationUncertain) {
		t.Fatalf("expected uncertain result, got %v", err)
	}
	if err := os.RemoveAll(f.svc.dependencyStoreRoot); err != nil {
		t.Fatal(err)
	}
	f.svc.run = run
	_, _ = f.svc.ReapStale(f.ctx())
	record, ok := f.store.get(f.key("foo"))
	if !ok || record.Status == StatusInstalled {
		t.Fatalf("success receipt falsely proves missing payload installed: %+v", record)
	}
	if _, err := os.Stat(StatePath(f.home("foo"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost payload published state: %v", err)
	}
}

func TestIsolatedRemoveWithPersistentDefaultStoreUsesPayloadGC(t *testing.T) {
	f := isolatedFixture(t)
	root, err := filepath.EvalSymlinks(f.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	f.dataRoot, f.ws.dataRoot = root, root
	f.svc.dependencyStoreRoot = ""
	if _, err := f.svc.Install(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	state := f.readState(t, "foo")
	if !strings.HasPrefix(state.PayloadPath, f.home("foo")+"/") {
		t.Fatal("fixture did not exercise colocated data and payload")
	}
	if _, err := f.svc.Remove(f.ctx(), testBot, "foo", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(StatePath(f.home("foo"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state survived remove: %v", err)
	}
	if _, err := os.Stat(f.shimPath("foo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shim survived remove: %v", err)
	}
	if _, err := os.Stat(state.PayloadPath); err != nil {
		t.Fatalf("removed payload must wait for safe startup GC: %v", err)
	}
}

func TestDefaultCandidateProbeChecksActualVersionBeforeCommit(t *testing.T) {
	f := isolatedFixture(t)
	// Match core recipes that use the default --version probe, including Node's
	// v prefix. A lying recipe must not replace the last verified installation.
	cat, err := catalog.LoadFS(fstest.MapFS{
		"foo/dependency.yaml": &fstest.MapFile{Data: []byte(e2eFooYAML)},
		"foo/install.sh":      &fstest.MapFile{Data: []byte(strings.Replace(isolatedFooInstall, "echo \"foo %s\"", "echo \"v%s\"", 1))},
		"foo/remove.sh":       &fstest.MapFile{Data: []byte("dep_result '{}'\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.cat, f.svc.catalog = cat, cat
	if _, err := f.svc.Install(f.ctx(), testBot, "foo", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	before := f.readState(t, "foo")
	original := f.svc.run
	f.svc.run = func(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error) {
		result, err := original(ctx, client, spec, sink)
		if err == nil && spec.Action != catalog.ActionVersion {
			result.Version = "3.0.0"
		}
		return result, err
	}
	if _, err := f.svc.Update(f.ctx(), testBot, "foo", "2.0.0", nil); !errors.Is(err, errInvalidResult) {
		t.Fatalf("expected candidate rejection: %v", err)
	}
	if f.readState(t, "foo").InstallationID != before.InstallationID {
		t.Fatal("wrong version replaced current payload")
	}
}
