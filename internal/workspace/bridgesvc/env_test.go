package bridgesvc

import (
	"strings"
	"testing"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func TestExecEnvUnsetsInheritedEnvBeforeAppendingOverrides(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "host-secret")
	t.Setenv("HERMES_HOME", "/host/hermes")
	env := execEnv(&pb.ExecInput{
		Env:      []string{"HERMES_HOME=/data/.memoh-hermes", "PATH=/toolkit"},
		UnsetEnv: []string{"OPENAI_API_KEY", "HERMES_*"},
	})
	if hasEnvValue(env, "OPENAI_API_KEY", "host-secret") {
		t.Fatalf("host OPENAI_API_KEY leaked: %v", env)
	}
	if hasEnvValue(env, "HERMES_HOME", "/host/hermes") {
		t.Fatalf("host HERMES_HOME leaked: %v", env)
	}
	if !hasEnvValue(env, "HERMES_HOME", "/data/.memoh-hermes") {
		t.Fatalf("explicit HERMES_HOME missing: %v", env)
	}
}

func TestExecEnvCleanStartsFromOnlyExplicitEnv(t *testing.T) {
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

func hasEnvValue(env []string, key, value string) bool {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) && strings.TrimPrefix(item, prefix) == value {
			return true
		}
	}
	return false
}
