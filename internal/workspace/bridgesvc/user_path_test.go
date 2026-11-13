package bridgesvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func setUserBinDir(t *testing.T, dir string) {
	t.Helper()
	previous := userBinDir
	userBinDir = dir
	t.Cleanup(func() { userBinDir = previous })
}

// setShellPathProbe replaces the shell probe with fn and counts its calls.
func setShellPathProbe(t *testing.T, fn func(env []string, home string) (string, bool)) *int {
	t.Helper()
	previous := userShellPaths
	calls := 0
	userShellPaths = &shellPathCache{
		probe: func(env []string, home string) (string, bool) {
			calls++
			return fn(env, home)
		},
		entries: map[string]shellPathEntry{},
	}
	t.Cleanup(func() { userShellPaths = previous })
	return &calls
}

func TestExecEnvPutsTheUsersBinDirAheadOfTheImagePath(t *testing.T) {
	setDepsShimDir(t, "")
	bin := t.TempDir()
	setUserBinDir(t, bin)
	t.Setenv("PATH", "/usr/local/go/bin:/usr/bin")

	// The image's PATH is kept, the user's directory goes in front of it.
	if got := envValue(execEnv(&pb.ExecInput{}), "PATH"); got != bin+":/usr/local/go/bin:/usr/bin" {
		t.Fatalf("PATH = %q", got)
	}
	// A PATH that lists it anywhere is left as it is.
	listed := "/usr/bin:" + bin
	if got := envValue(execEnv(&pb.ExecInput{CleanEnv: true, Env: []string{"PATH=" + listed}}), "PATH"); got != listed {
		t.Fatalf("PATH = %q, want %q unchanged", got, listed)
	}
	// Not there yet: nothing to add.
	setUserBinDir(t, filepath.Join(bin, "absent"))
	if got := envValue(execEnv(&pb.ExecInput{}), "PATH"); got != "/usr/local/go/bin:/usr/bin" {
		t.Fatalf("PATH = %q, want the image PATH untouched", got)
	}
}

func TestUserShellPathGoesAheadOfTheCommandAndImagePath(t *testing.T) {
	setDepsShimDir(t, "")
	setUserBinDir(t, "")
	t.Setenv("PATH", "/usr/local/go/bin:/usr/bin")
	setShellPathProbe(t, func(env []string, home string) (string, bool) {
		if home != "/data" {
			t.Errorf("probe HOME = %q", home)
		}
		if got := lastEnvValue(env, "PATH"); got != "/agent/bin:/usr/local/go/bin:/usr/bin" {
			t.Errorf("probe seed PATH = %q", got)
		}
		return "/data/.bun/bin:" + lastEnvValue(env, "PATH"), true
	})

	// An agent started on a clean environment still sees what the image puts
	// on PATH, after what the user's shell adds.
	env := execEnv(&pb.ExecInput{CleanEnv: true, UserShellPath: true, Env: []string{"HOME=/data", "PATH=/agent/bin"}})
	if got := envValue(env, "PATH"); got != "/data/.bun/bin:/agent/bin:/usr/local/go/bin:/usr/bin" {
		t.Fatalf("PATH = %q", got)
	}
	if strings.Count(strings.Join(env, "\n"), "PATH=") != 1 {
		t.Fatalf("env carries more than one PATH: %v", env)
	}
}

func TestUserShellPathIsOnlyForCommandsThatAskAndNotForTerminals(t *testing.T) {
	setDepsShimDir(t, "")
	setUserBinDir(t, "")
	t.Setenv("PATH", "/usr/bin")
	calls := setShellPathProbe(t, func([]string, string) (string, bool) { return "/data/.bun/bin", true })

	if got := envValue(execEnv(&pb.ExecInput{Env: []string{"HOME=/data"}}), "PATH"); got != "/usr/bin" {
		t.Fatalf("PATH without the option = %q", got)
	}
	if got := envValue(execPTYEnv(&pb.ExecInput{Pty: true, UserShellPath: true, Env: []string{"HOME=/data"}}), "PATH"); got != "/usr/bin" {
		t.Fatalf("terminal PATH = %q, want the shell to read its rc files itself", got)
	}
	if *calls != 0 {
		t.Fatalf("probe ran %d times", *calls)
	}
}

func TestUserShellPathIsCachedUntilTheRCFileChanges(t *testing.T) {
	setDepsShimDir(t, "")
	setUserBinDir(t, "")
	t.Setenv("PATH", "/usr/bin")
	home := t.TempDir()
	answer := "/first"
	calls := setShellPathProbe(t, func([]string, string) (string, bool) { return answer, true })
	input := &pb.ExecInput{UserShellPath: true, Env: []string{"HOME=" + home}}

	execEnv(input)
	execEnv(input)
	if *calls != 1 {
		t.Fatalf("probe ran %d times for an unchanged rc file", *calls)
	}
	answer = "/second"
	rc := filepath.Join(home, ".bashrc")
	if err := os.WriteFile(rc, []byte("export PATH=/second:$PATH\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := envValue(execEnv(input), "PATH"); got != "/second:/usr/bin" || *calls != 2 {
		t.Fatalf("PATH = %q after %d probes, want the new rc file read", got, *calls)
	}
}

func TestUserShellPathFallsBackWhenTheShellNeverAnswers(t *testing.T) {
	setDepsShimDir(t, "")
	setUserBinDir(t, "")
	t.Setenv("PATH", "/usr/bin")
	calls := setShellPathProbe(t, func([]string, string) (string, bool) { return "", false })
	input := &pb.ExecInput{UserShellPath: true, Env: []string{"HOME=/data"}}
	if got := envValue(execEnv(input), "PATH"); got != "/usr/bin" {
		t.Fatalf("PATH = %q, want the command's own", got)
	}
	execEnv(input)
	if *calls != 1 {
		t.Fatalf("a failed probe was retried at once (%d calls)", *calls)
	}
}

func TestUserShellPathReadsARealBashrc(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is required to read a .bashrc")
	}
	setDepsShimDir(t, "")
	setUserBinDir(t, "")
	setShellPathProbe(t, probeShellPath)
	home := t.TempDir()
	// Installers append after the interactive guard most .bashrc files start
	// with, which is why the probe runs the shell interactively.
	rc := "case $- in *i*) ;; *) return;; esac\nexport PATH=\"$HOME/.bun/bin:$PATH\"\n"
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	start := time.Now()
	got := envValue(execEnv(&pb.ExecInput{UserShellPath: true, Env: []string{"HOME=" + home}}), "PATH")
	if !strings.HasPrefix(got, home+"/.bun/bin:") {
		t.Fatalf("PATH = %q, want the .bashrc addition first", got)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("probe took %s", time.Since(start))
	}
}
