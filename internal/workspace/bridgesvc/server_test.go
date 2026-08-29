//go:build !windows

package bridgesvc

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func TestExecPipePreservesExitCodeAcrossStreamCancellation(t *testing.T) {
	stream := newCancelOnStdoutExecStream()
	srv := New(Options{DefaultWorkDir: "/tmp", AllowHostAbsolute: true})

	err := srv.execPipe(stream, &pb.ExecInput{
		Command:        "printf ok; sleep 0.2",
		WorkDir:        "/tmp",
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("execPipe returned error: %v", err)
	}

	var stdout string
	var exitCode int32 = -999
	for _, output := range stream.outputs {
		switch output.GetStream() {
		case pb.ExecOutput_STDOUT:
			stdout += string(output.GetData())
		case pb.ExecOutput_EXIT:
			exitCode = output.GetExitCode()
		}
	}

	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
}

func TestExecPipeNegativeTimeoutCancelsProcessOnStreamClose(t *testing.T) {
	stream := newCancelOnStdoutExecStream()
	srv := New(Options{DefaultWorkDir: "/tmp", AllowHostAbsolute: true})

	start := time.Now()
	err := srv.execPipe(stream, &pb.ExecInput{
		Command:        "printf ok; sleep 5; printf late",
		WorkDir:        "/tmp",
		TimeoutSeconds: -1,
	})
	if err != nil {
		t.Fatalf("execPipe returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("execPipe took %s after stream cancellation, want quick process cancellation", elapsed)
	}

	var stdout string
	var exitCode int32 = -999
	for _, output := range stream.outputs {
		switch output.GetStream() {
		case pb.ExecOutput_STDOUT:
			stdout += string(output.GetData())
		case pb.ExecOutput_EXIT:
			exitCode = output.GetExitCode()
		}
	}

	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}
	if strings.Contains(stdout, "late") {
		t.Fatalf("process was not canceled before trailing output: %q", stdout)
	}
	if exitCode == 0 {
		t.Fatalf("exit code = %d, want non-zero after cancellation", exitCode)
	}
}

func TestExecPipeCancellationKillsChildProcessGroup(t *testing.T) {
	stream := newCancelOnStdoutExecStream()
	srv := New(Options{DefaultWorkDir: "/tmp", AllowHostAbsolute: true})

	err := srv.execPipe(stream, &pb.ExecInput{
		Command:        "sleep 30 & echo $!; wait",
		WorkDir:        "/tmp",
		TimeoutSeconds: -1,
	})
	if err != nil {
		t.Fatalf("execPipe returned error: %v", err)
	}

	pidText := strings.TrimSpace(collectStdout(stream.outputs))
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		t.Fatalf("stdout child pid = %q: %v", pidText, err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})

	if waitForProcessExit(pid, 2*time.Second) {
		return
	}
	t.Fatalf("child process %d is still alive after exec stream cancellation", pid)
}

func TestExecPTYStartsWithCancellationConfigured(t *testing.T) {
	stream := newCancelOnStdoutExecStream()
	srv := New(Options{DefaultWorkDir: "/tmp", AllowHostAbsolute: true})

	err := srv.execPTY(stream, &pb.ExecInput{
		Command:        "printf PTY_OK",
		WorkDir:        "/tmp",
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("execPTY returned error: %v", err)
	}
	if stdout := collectStdout(stream.outputs); !strings.Contains(stdout, "PTY_OK") {
		t.Fatalf("stdout = %q, want PTY_OK", stdout)
	}
}

func TestPTYCancellationDoesNotPreconfigureProcessGroup(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "true") //nolint:gosec // G204: test fixture executing a known shell snippet.
	configurePTYCommandCancellation(cmd)
	if cmd.SysProcAttr != nil {
		t.Fatalf("PTY command SysProcAttr = %#v, want nil so pty can configure session", cmd.SysProcAttr)
	}
	if cmd.Cancel == nil {
		t.Fatalf("PTY command Cancel is nil")
	}
}

func TestValidateTunnelAddressRequiresLoopback(t *testing.T) {
	ctx := context.Background()
	if _, err := validateTunnelAddress(ctx, "127.0.0.1:5999"); err != nil {
		t.Fatalf("loopback address rejected: %v", err)
	}
	if _, err := validateTunnelAddress(ctx, "8.8.8.8:53"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-loopback error = %v, want permission denied", err)
	}
}

func collectStdout(outputs []*pb.ExecOutput) string {
	var stdout strings.Builder
	for _, output := range outputs {
		if output.GetStream() == pb.ExecOutput_STDOUT {
			_, _ = stdout.Write(output.GetData())
		}
	}
	return stdout.String()
}

func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return syscall.Kill(pid, 0) == syscall.ESRCH
}
