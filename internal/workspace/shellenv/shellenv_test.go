package shellenv

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
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
	probe := pathProbeCommand(4 * time.Second)
	for _, shell := range []string{"/bin/bash", "/usr/bin/bash", "bash", "/bin/sh"} {
		if !strings.Contains(TerminalCommand(), "exec "+shell+";") {
			t.Fatalf("TerminalCommand() = %q, want it to launch %q", TerminalCommand(), shell)
		}
		if !strings.Contains(probe, "$memoh_detach "+shell+" -ic ") {
			t.Fatalf("pathProbeCommand() = %q, want %q run interactively through $memoh_detach", probe, shell)
		}
	}
	if got := strings.Count(probe, ">/dev/null 2>&1 </dev/null;"); got != 4 {
		t.Fatalf("pathProbeCommand() = %q, want every shell cut off from the exec's pipes, got %d", probe, got)
	}
	if !strings.Contains(probe, `memoh_detach="setsid -w timeout -k 1 4"`) {
		t.Fatalf("pathProbeCommand() = %q, want the detached shell to carry the probe timeout", probe)
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
			if got := mergePaths(tt.shellPath, seedPath); got != tt.want {
				t.Fatalf("mergePaths() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolvePathReadsTheUsersShellConfiguration(t *testing.T) {
	// The rc file prints to stdout and uses a bashism, as real installer-managed
	// blocks do: neither may leak into or break the result.
	root, got, err := resolveWithRC(t, `echo "welcome to the workspace"
if [[ ":$PATH:" != *":$HOME/.local/bin:"* ]]; then
  export PATH="$HOME/.local/bin:$PATH"
fi
`)
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}
	if want := root + "/.local/bin:" + seedPath; got != want {
		t.Fatalf("ResolvePath() = %q, want %q", got, want)
	}
}

func TestResolvePathWithoutShellConfigurationKeepsTheSeed(t *testing.T) {
	_, got, err := resolveWithRC(t, "")
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}
	if got != seedPath {
		t.Fatalf("ResolvePath() = %q, want the seed PATH", got)
	}
}

func TestResolvePathReportsAShellThatNeverAnswers(t *testing.T) {
	if _, got, err := resolveWithRC(t, "exit 3\n"); err == nil {
		t.Fatalf("ResolvePath() = %q, want an error when the rc file exits before the probe runs", got)
	}
}

func TestResolvePathIsNotHeldByBackgroundProcessesFromTheRCFile(t *testing.T) {
	start := time.Now()
	root, got, err := resolveWithRC(t, "export PATH=\"$HOME/.local/bin:$PATH\"\nsleep 30 &\n")
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}
	if want := root + "/.local/bin:" + seedPath; got != want {
		t.Fatalf("ResolvePath() = %q, want %q", got, want)
	}
	// A background process that inherited the exec's output pipe would hold the
	// probe until probeTimeout on every launch.
	if elapsed := time.Since(start); elapsed > probeTimeout/2 {
		t.Fatalf("ResolvePath() took %s, want it not to wait for the rc file's background process", elapsed)
	}
}

func TestResolvePathIsNotHeldByAnRCFileThatReadsInput(t *testing.T) {
	root, got, err := resolveWithRC(t, "read -r answer\nexport PATH=\"$HOME/.local/bin:$PATH\"\n")
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}
	if want := root + "/.local/bin:" + seedPath; got != want {
		t.Fatalf("ResolvePath() = %q, want %q", got, want)
	}
}

func TestResolvePathIsBoundedForAnRCFileThatNeverReturns(t *testing.T) {
	previous := probeTimeout
	probeTimeout = time.Second
	t.Cleanup(func() { probeTimeout = previous })

	start := time.Now()
	_, got, err := resolveWithRC(t, "sleep 30\n")
	if elapsed := time.Since(start); elapsed > probeTimeout+probeKillBackstop+2*time.Second {
		t.Fatalf("ResolvePath() took %s, want it bounded by the probe timeout", elapsed)
	}
	// Either outcome is sound. Killed by the bridge, the shell never answers.
	// Under its own timeout, an interactive bash ignores SIGTERM: only the
	// stuck command dies, the rc file finishes, and the shell still answers.
	if err == nil && got != seedPath {
		t.Fatalf("ResolvePath() = %q, want the seed PATH when the interrupted rc file added nothing", got)
	}
}

func TestResolvePathRequiresClient(t *testing.T) {
	if _, err := ResolvePath(context.Background(), nil, seedPath, nil, nil); err == nil {
		t.Fatal("ResolvePath() error = nil, want a missing client error")
	}
}

// resolveWithRC resolves the PATH of a real shell whose HOME holds rc as its
// .bashrc, through an in-process bridge.
func resolveWithRC(t *testing.T, rc string) (root, resolved string, err error) {
	t.Helper()
	if _, lookErr := exec.LookPath("bash"); lookErr != nil {
		t.Skip("bash is required to read a .bashrc")
	}
	root = t.TempDir()
	if rc != "" {
		if writeErr := os.WriteFile(filepath.Join(root, ".bashrc"), []byte(rc), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	resolved, err = ResolvePath(context.Background(), newBridgeClient(t, root), seedPath, []string{"HOME=" + root}, []string{"PATH", "HOME"})
	return root, resolved, err
}

func newBridgeClient(t *testing.T, root string) *bridge.Client {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir:    root,
		WorkspaceRoot:     root,
		DataMount:         config.DefaultDataMount,
		AllowHostAbsolute: true,
	}))
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///shellenv-test",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn)
}
