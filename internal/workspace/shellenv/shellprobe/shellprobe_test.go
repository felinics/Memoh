package shellprobe

import (
	"strings"
	"testing"
	"time"
)

const seedPath = "/usr/bin:/bin"

func TestTerminalCommandIsUnchanged(t *testing.T) {
	// The browser terminal ran this exact line before it moved here.
	want := `if [ -x /bin/bash ]; then exec /bin/bash; elif [ -x /usr/bin/bash ]; then exec /usr/bin/bash; elif command -v bash >/dev/null 2>&1; then exec bash; else exec /bin/sh; fi`
	if got := TerminalCommand(); got != want {
		t.Fatalf("TerminalCommand() = %q, want %q", got, want)
	}
}

func TestProbeRunsEveryTerminalShellInteractivelyAndDetached(t *testing.T) {
	probe := PathProbeCommand(4 * time.Second)
	for _, shell := range []string{"/bin/bash", "/usr/bin/bash", "bash", "/bin/sh"} {
		if !strings.Contains(TerminalCommand(), "exec "+shell+";") {
			t.Fatalf("TerminalCommand() = %q, want it to launch %q", TerminalCommand(), shell)
		}
		if !strings.Contains(probe, "$memoh_detach "+shell+" -ic ") {
			t.Fatalf("PathProbeCommand() = %q, want %q run interactively through $memoh_detach", probe, shell)
		}
	}
	if got := strings.Count(probe, ">/dev/null 2>&1 </dev/null;"); got != 4 {
		t.Fatalf("PathProbeCommand() = %q, want every shell cut off from the exec's pipes, got %d", probe, got)
	}
	if !strings.Contains(probe, `memoh_detach="setsid -w timeout -k 1 4"`) {
		t.Fatalf("PathProbeCommand() = %q, want the detached shell to carry the probe timeout", probe)
	}
}

func TestMergePaths(t *testing.T) {
	tests := []struct {
		name      string
		shellPath string
		want      string
	}{
		{name: "shell additions stay ahead of the seed", shellPath: "/data/.local/bin:/usr/bin:/bin", want: "/data/.local/bin:/usr/bin:/bin"},
		{name: "seed survives an rc file that overwrites PATH", shellPath: "/data/.cargo/bin", want: "/data/.cargo/bin:/usr/bin:/bin"},
		{name: "empty and relative entries are dropped", shellPath: ":.:bin:./tools:/opt/x::", want: "/opt/x:/usr/bin:/bin"},
		{name: "duplicates collapse", shellPath: "/opt/x:/usr/bin:/opt/x", want: "/opt/x:/usr/bin:/bin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MergePaths(tt.shellPath, seedPath); got != tt.want {
				t.Fatalf("MergePaths() = %q, want %q", got, tt.want)
			}
		})
	}
}
