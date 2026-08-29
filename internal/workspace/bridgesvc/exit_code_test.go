package bridgesvc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"testing"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// TestResolveExitCodeFromCommands runs real subprocesses via /bin/sh -c so we
// exercise the same code path Exec uses. We assert that signal-killed
// processes get mapped to 128+signal instead of -1.
func TestResolveExitCodeFromCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal mapping doesn't apply on windows")
	}
	cases := []struct {
		name    string
		command string
		want    int32
	}{
		{"clean_exit", "true", 0},
		{"explicit_exit_42", "exit 42", 42},
		{"shell_sigterm", "echo hi; kill -TERM $$", 128 + 15},
		{"direct_sigkill", "exec sh -c 'echo hi; kill -KILL $$'", 128 + 9},
		{"direct_sigterm", "exec sh -c 'kill -TERM $$'", 128 + 15},
		{"direct_sigint", "exec sh -c 'kill -INT $$'", 128 + 2},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", tc.command) //nolint:gosec // G204: test fixture executing known shell snippets.
			waitErr := cmd.Run()
			got := resolveExitCode(waitErr)
			if got != tc.want {
				t.Fatalf("resolveExitCode(%q) = %d, want %d (err=%v)", tc.command, got, tc.want, waitErr)
			}
		})
	}
}

// TestResolveExitCodeContextTimeout pins down the scenario the user actually
// hit: a long-running process killed by procCtx timeout via SIGKILL must report
// 137 (= 128+9) instead of -1.
func TestResolveExitCodeContextTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal mapping doesn't apply on windows")
	}
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "echo first; sleep 10") //nolint:gosec // G204: test fixture executing a known shell snippet.
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	got := resolveExitCode(cmd.Wait())
	if got != 137 {
		t.Fatalf("got exit code %d, want 137 (SIGKILL)", got)
	}
}

func TestResolveExitCodeNonExitError(t *testing.T) {
	got := resolveExitCode(errors.New("bogus i/o failure"))
	if got != -1 {
		t.Fatalf("got %d, want -1 for non-ExitError", got)
	}
}

func TestResolveExitCodeNil(t *testing.T) {
	if got := resolveExitCode(nil); got != 0 {
		t.Fatalf("got %d, want 0 for nil error", got)
	}
}

// Ensure the public Exec path (execPipe) reports 137 when the wrapper shell
// itself is signal-killed — this is the realistic failure mode when the bridge
// procCtx hits its timeout and SIGKILLs the shell.
func TestExecPipeReportsSignalAsShellConvention(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal mapping doesn't apply on windows")
	}
	stream := newCancelOnStdoutExecStream()
	srv := New(Options{DefaultWorkDir: "/tmp", AllowHostAbsolute: true})

	// `exec` replaces the shell with sh, then that sh signals itself.
	// Wrapping outer /bin/sh -c isn't actually involved because exec replaces
	// the process image — so the kill propagates as a real signal back to
	// cmd.Wait().
	err := srv.execPipe(stream, &pb.ExecInput{
		Command:        fmt.Sprintf("exec sh -c %q", "kill -KILL $$"),
		WorkDir:        "/tmp",
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("execPipe returned error: %v", err)
	}

	var exitCode int32 = -999
	for _, output := range stream.outputs {
		if output.GetStream() == pb.ExecOutput_EXIT {
			exitCode = output.GetExitCode()
		}
	}
	if exitCode != 137 {
		t.Fatalf("exit code = %d, want 137", exitCode)
	}
}
