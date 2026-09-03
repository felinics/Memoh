package bridgesvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// setDepsShimDir points the shim directory at dir for the test. An empty dir
// disables the PATH rewrite so tests that assert the raw environment do not
// depend on whether /data/.memoh/deps/bin exists on the machine.
func setDepsShimDir(t *testing.T, dir string) {
	t.Helper()
	previous := depsShimDir
	depsShimDir = dir
	t.Cleanup(func() { depsShimDir = previous })
}

func TestExecEnvUnsetsInheritedEnvBeforeAppendingOverrides(t *testing.T) {
	setDepsShimDir(t, "")
	t.Setenv("OPENAI_API_KEY", "host-secret")
	t.Setenv("CUSTOM_AGENT_HOME", "/host/custom-agent")
	env := execEnv(&pb.ExecInput{
		Env:      []string{"CUSTOM_AGENT_HOME=/data/.custom-agent", "PATH=/toolkit"},
		UnsetEnv: []string{"OPENAI_API_KEY", "CUSTOM_AGENT_*"},
	})
	if hasEnvValue(env, "OPENAI_API_KEY", "host-secret") {
		t.Fatalf("host OPENAI_API_KEY leaked: %v", env)
	}
	if hasEnvValue(env, "CUSTOM_AGENT_HOME", "/host/custom-agent") {
		t.Fatalf("host CUSTOM_AGENT_HOME leaked: %v", env)
	}
	if !hasEnvValue(env, "CUSTOM_AGENT_HOME", "/data/.custom-agent") {
		t.Fatalf("explicit CUSTOM_AGENT_HOME missing: %v", env)
	}
}

func TestExecEnvCleanStartsFromOnlyExplicitEnv(t *testing.T) {
	setDepsShimDir(t, "")
	t.Setenv("OPENAI_API_KEY", "host-secret")
	env := execEnv(&pb.ExecInput{
		CleanEnv: true,
		Env:      []string{"PATH=/toolkit"},
	})
	if len(env) != 1 || env[0] != "PATH=/toolkit" {
		t.Fatalf("clean env = %#v, want only explicit PATH", env)
	}
}

func TestExecPTYEnvInheritsDefaultEnvironment(t *testing.T) {
	setDepsShimDir(t, "")
	t.Setenv("MEMOH_TEST_PTY_SENTINEL", "present")
	env := execPTYEnv(&pb.ExecInput{})
	if !hasEnvValue(env, "MEMOH_TEST_PTY_SENTINEL", "present") {
		t.Fatalf("default PTY env did not inherit process environment: %v", env)
	}
	if !hasEnvValue(env, "TERM", "xterm-256color") {
		t.Fatalf("default PTY env missing TERM: %v", env)
	}
}

func TestExecPTYEnvCleanKeepsOnlyExplicitEnvAndTerm(t *testing.T) {
	setDepsShimDir(t, "")
	t.Setenv("MEMOH_TEST_PTY_SENTINEL", "present")
	env := execPTYEnv(&pb.ExecInput{
		CleanEnv: true,
		Env:      []string{"PATH=/toolkit"},
	})
	if hasEnvValue(env, "MEMOH_TEST_PTY_SENTINEL", "present") {
		t.Fatalf("clean PTY env leaked process environment: %v", env)
	}
	if !hasEnvValue(env, "PATH", "/toolkit") {
		t.Fatalf("clean PTY env missing explicit PATH: %v", env)
	}
	if !hasEnvValue(env, "TERM", "xterm-256color") {
		t.Fatalf("clean PTY env missing TERM: %v", env)
	}
}

func TestExecEnvPutsDepsShimDirFirstOnPath(t *testing.T) {
	shims := t.TempDir()
	setDepsShimDir(t, shims)
	t.Setenv("PATH", "/opt/memoh/toolkit/bin:/usr/bin")

	// Inherited PATH: the shim directory is prepended.
	if got := envValue(execEnv(&pb.ExecInput{}), "PATH"); got != shims+":/opt/memoh/toolkit/bin:/usr/bin" {
		t.Fatalf("inherited PATH = %q", got)
	}
	// An explicit PATH wins over the inherited one and is prepended too; the
	// inherited copy earlier in env is left as it was.
	env := execEnv(&pb.ExecInput{Env: []string{"PATH=/toolkit"}})
	if got := env[len(env)-1]; got != "PATH="+shims+":/toolkit" {
		t.Fatalf("explicit PATH = %q (env %v)", got, env)
	}
	if !hasEnvValue(env, "PATH", "/opt/memoh/toolkit/bin:/usr/bin") {
		t.Fatalf("inherited PATH entry rewritten: %v", env)
	}
	// Clean env with an explicit PATH.
	if env := execEnv(&pb.ExecInput{CleanEnv: true, Env: []string{"PATH=/toolkit"}}); len(env) != 1 || env[0] != "PATH="+shims+":/toolkit" {
		t.Fatalf("clean env = %#v", env)
	}
	// Clean env without PATH gets the baseline behind the shims.
	if env := execEnv(&pb.ExecInput{CleanEnv: true, Env: []string{"HOME=/data"}}); len(env) != 2 || env[1] != "PATH="+shims+":"+defaultExecPath {
		t.Fatalf("clean env without PATH = %#v", env)
	}
	// A PATH that already starts with the shim directory is not stacked.
	if env := execEnv(&pb.ExecInput{CleanEnv: true, Env: []string{"PATH=" + shims + ":/toolkit"}}); len(env) != 1 || env[0] != "PATH="+shims+":/toolkit" {
		t.Fatalf("already-prefixed PATH = %#v", env)
	}
	// The PTY path shares the rewrite.
	if got := envValue(execPTYEnv(&pb.ExecInput{}), "PATH"); got != shims+":/opt/memoh/toolkit/bin:/usr/bin" {
		t.Fatalf("PTY PATH = %q", got)
	}
}

func TestExecEnvLeavesPathAloneWithoutDepsShimDir(t *testing.T) {
	setDepsShimDir(t, filepath.Join(t.TempDir(), "absent"))
	t.Setenv("PATH", "/usr/bin")
	if got := envValue(execEnv(&pb.ExecInput{}), "PATH"); got != "/usr/bin" {
		t.Fatalf("PATH = %q, want untouched when the shim directory does not exist", got)
	}
	if env := execEnv(&pb.ExecInput{CleanEnv: true, Env: []string{"HOME=/data"}}); len(env) != 1 {
		t.Fatalf("clean env = %#v, want no PATH invented", env)
	}
	// A file at the shim path is not a directory either.
	file := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setDepsShimDir(t, file)
	if got := envValue(execEnv(&pb.ExecInput{}), "PATH"); got != "/usr/bin" {
		t.Fatalf("PATH = %q, want untouched for a non-directory", got)
	}
}

// envValue returns the value of the last key entry, which is the one exec
// honours.
func envValue(env []string, key string) string {
	value := ""
	for _, item := range env {
		if v, ok := strings.CutPrefix(item, key+"="); ok {
			value = v
		}
	}
	return value
}

func hasEnvValue(env []string, key, value string) bool {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) && strings.TrimPrefix(item, prefix) == value {
			return true
		}
	}
	return false
}
