package application

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

type reaperBridges struct {
	client *bridge.Client
	err    error
}

func (b reaperBridges) NativeMCPClient(context.Context, string) (*bridge.Client, error) {
	return b.client, b.err
}

func reaperBridgeClient(t *testing.T) *bridge.Client {
	t.Helper()
	root := t.TempDir()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{DefaultWorkDir: root, WorkspaceRoot: root, DataMount: config.DefaultDataMount, AllowHostAbsolute: true}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///reaper-test", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn)
}

func markedProcesses(marker string) []int {
	entries, _ := filepath.Glob("/proc/[0-9]*/environ")
	var pids []int
	for _, entry := range entries {
		data, err := os.ReadFile(entry) //nolint:gosec // test scans its own process table.
		if err != nil || !slices.Contains(strings.Split(string(data), "\x00"), marker) {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(entry, "/proc/"), "/environ")); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func waitMarked(t *testing.T, marker string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(markedProcesses(marker)) != want {
		if time.Now().After(deadline) {
			t.Fatalf("marked processes=%v, want %d", markedProcesses(marker), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSubagentProcessReaperStopsEscapedDescendants(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the reaper reads /proc")
	}
	client := reaperBridgeClient(t)
	childID := uuid.NewString()
	marker := external.SubagentThreadEnvEntry(childID)[0]
	t.Cleanup(func() {
		for _, pid := range markedProcesses(marker) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	streamCtx, cancelStream := context.WithCancel(t.Context())
	if _, err := client.ExecStreamWithOptions(streamCtx, "setsid sleep 300 </dev/null >/dev/null 2>&1 & exec sleep 300", "", -1, bridge.ExecOptions{Env: []string{marker}}); err != nil {
		t.Fatal(err)
	}
	waitMarked(t, marker, 2)
	cancelStream()
	waitMarked(t, marker, 1)

	reaper := NewSubagentProcessReaper(reaperBridges{client: client})
	if err := reaper.Reap(t.Context(), uuid.NewString(), childID); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if left := markedProcesses(marker); len(left) != 0 {
		t.Fatalf("escaped descendant survived: %v", left)
	}
}

func TestSubagentProcessReaperNeverConfirmsWithoutScanning(t *testing.T) {
	err := NewSubagentProcessReaper(reaperBridges{err: errors.New("workspace unreachable")}).Reap(t.Context(), uuid.NewString(), uuid.NewString())
	if err == nil {
		t.Fatal("an unreachable workspace confirmed termination")
	}
}

func TestSubagentProcessReaperStopsDescendantsForkedDuringTheScan(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the reaper reads /proc")
	}
	client := reaperBridgeClient(t)
	childID := uuid.NewString()
	marker := external.SubagentThreadEnvEntry(childID)[0]
	t.Cleanup(func() {
		for range 3 {
			for _, pid := range markedProcesses(marker) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	if _, err := client.ExecStreamWithOptions(t.Context(), "setsid sh -c 'while :; do setsid sleep 300 </dev/null >/dev/null 2>&1 & sleep 0.01; done' </dev/null >/dev/null 2>&1 & exec sleep 300", "", -1, bridge.ExecOptions{Env: []string{marker}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(markedProcesses(marker)) < 20 {
		if time.Now().After(deadline) {
			t.Fatalf("respawner did not start: %v", markedProcesses(marker))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := NewSubagentProcessReaper(reaperBridges{client: client}).Reap(t.Context(), uuid.NewString(), childID); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if left := markedProcesses(marker); len(left) != 0 {
		t.Fatalf("descendants forked during the scan survived: %v", left)
	}
}

func TestSubagentProcessReaperStopsDescendantsThatResetTheirEnvironment(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the reaper reads /proc")
	}
	client := reaperBridgeClient(t)
	childID := uuid.NewString()
	marker := external.SubagentThreadEnvEntry(childID)[0]
	pidFile := filepath.Join(t.TempDir(), "pids")
	alive := func() []int {
		data, _ := os.ReadFile(pidFile) //nolint:gosec // test-owned path.
		var pids []int
		for _, field := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(field); err == nil && syscall.Kill(pid, 0) == nil {
				pids = append(pids, pid)
			}
		}
		return pids
	}
	t.Cleanup(func() {
		for _, pid := range append(markedProcesses(marker), alive()...) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	if _, err := client.ExecStreamWithOptions(t.Context(), "(while :; do env -i sleep 300 </dev/null >/dev/null 2>&1 & echo $! >> "+pidFile+"; sleep 0.01; done) & exec sleep 300", "", -1, bridge.ExecOptions{Env: []string{marker}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(alive()) < 20 {
		if time.Now().After(deadline) {
			t.Fatalf("respawner did not start: %v", alive())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := NewSubagentProcessReaper(reaperBridges{client: client}).Reap(t.Context(), uuid.NewString(), childID); err != nil {
		t.Fatalf("reap: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if left := alive(); len(left) != 0 {
		t.Fatalf("descendants without the marker survived: %d", len(left))
	}
}

func TestSubagentProcessReaperFreezesUnmarkedForkersBeforeKilling(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the reaper reads /proc")
	}
	client := reaperBridgeClient(t)
	childID := uuid.NewString()
	marker := external.SubagentThreadEnvEntry(childID)[0]
	pidFile := filepath.Join(t.TempDir(), "pids")
	alive := func() []int {
		data, _ := os.ReadFile(pidFile) //nolint:gosec // test-owned path.
		var pids []int
		for _, field := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(field); err == nil && syscall.Kill(pid, 0) == nil {
				pids = append(pids, pid)
			}
		}
		return pids
	}
	t.Cleanup(func() {
		for range 3 {
			for _, pid := range append(markedProcesses(marker), alive()...) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	forker := "env -i sh -c 'echo $$ >> " + pidFile + "; while :; do sleep 300 </dev/null >/dev/null 2>&1 & echo $! >> " + pidFile + "; sleep 0.005; done' & exec sleep 300"
	if _, err := client.ExecStreamWithOptions(t.Context(), forker, "", -1, bridge.ExecOptions{Env: []string{marker}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(alive()) < 40 {
		if time.Now().After(deadline) {
			t.Fatalf("forker did not start: %v", alive())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := NewSubagentProcessReaper(reaperBridges{client: client}).Reap(t.Context(), uuid.NewString(), childID); err != nil {
		t.Fatalf("reap: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if left := alive(); len(left) != 0 {
		t.Fatalf("processes forked by an unmarked descendant survived: %d", len(left))
	}
}

// Reap reports what a scan of marked processes found, not that every effect
// of the child stopped: a process that dropped the marker and left the marked
// tree is out of its sight.
func TestSubagentProcessReaperCannotSeeAProcessThatLeftTheMarkedTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the reaper reads /proc")
	}
	client := reaperBridgeClient(t)
	childID := uuid.NewString()
	marker := external.SubagentThreadEnvEntry(childID)[0]
	started, err := client.ExecWithEnv(t.Context(), "setsid sh -c 'exec env -i sleep 300' </dev/null >/dev/null 2>&1 & echo $!", "", 10, []string{marker})
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(started.Stdout))
	if err != nil {
		t.Fatalf("escaped pid %q: %v", started.Stdout, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	waitMarked(t, marker, 0)
	if err := NewSubagentProcessReaper(reaperBridges{client: client}).Reap(t.Context(), uuid.NewString(), childID); err != nil {
		t.Fatalf("reap: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if !processRunning(pid) {
		t.Fatal("the reaper now stops processes outside the marked tree; update its documented limits")
	}
}

func processRunning(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") //nolint:gosec // test reads its own process table.
	if err != nil {
		return false
	}
	end := strings.LastIndex(string(stat), ") ")
	return end >= 0 && !strings.HasPrefix(string(stat[end+2:]), "Z") && !strings.HasPrefix(string(stat[end+2:]), "X")
}

// Cleanup is per child session: the marker is the child thread id, since an
// ACP agent process serves many runs, so a run that does not finish also stops
// what an earlier, completed run left.
func TestUnfinishedSubagentRunStopsServicesLeftByEarlierRuns(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the reaper reads /proc")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client := reaperBridgeClient(t)
	childID := uuid.NewString()
	marker := external.SubagentThreadEnvEntry(childID)[0]
	t.Cleanup(func() {
		for _, pid := range markedProcesses(marker) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	f := newReapTestFixtureFor(t, childID)
	f.mu.Lock()
	delete(f.meta, "external_execution_run")
	delete(f.meta, "external_termination_unconfirmed")
	f.mu.Unlock()
	f.svc.SetSubagentProcessReaper(NewSubagentProcessReaper(reaperBridges{client: client}))
	f.svc.SetExternalRuntimes(noticeTestDriver{kind: "codex", prompt: func(ctx context.Context, input external.PromptInput) (external.PromptResult, error) {
		if input.Prompt != "serve" {
			return external.PromptResult{}, external.Unavailable(errors.New("launcher missing"))
		}
		if _, err := client.ExecWithEnv(ctx, "setsid sleep 300 </dev/null >/dev/null 2>&1 &", "", 10, input.ProcessEnv()); err != nil {
			return external.PromptResult{}, err
		}
		return external.PromptResult{TurnCompleted: true, Output: []sdk.Message{sdk.AssistantMessage("serving")}}, nil
	}})

	if result, err := f.run(ctx, uuid.NewString(), "serve"); err != nil || result == nil || result.Text != "serving" {
		t.Fatalf("serving run = %+v, %v", result, err)
	}
	waitMarked(t, marker, 1)
	if _, err := f.run(ctx, uuid.NewString(), "work"); err == nil {
		t.Fatal("failed run reported success")
	}
	if left := markedProcesses(marker); len(left) != 0 {
		t.Fatalf("a run that did not finish left the session's earlier service running: %v", left)
	}
	if meta := f.child().RuntimeMetadata; meta["external_execution_run"] != nil || meta["external_termination_unconfirmed"] != nil {
		t.Fatalf("reaped child stayed blocked: %v", meta)
	}
}
