package client

import (
	"strings"
	"testing"
)

func TestResolveSessionContextRejectsUnknownBackend(t *testing.T) {
	_, err := ResolveSessionContext(SessionContextInput{Backend: "virtual-machine"})
	if err == nil || !strings.Contains(err.Error(), "unsupported workspace backend") {
		t.Fatalf("ResolveSessionContext() error = %v, want unsupported backend", err)
	}
}

func TestResolveSessionContextRemoteResolvesHostPaths(t *testing.T) {
	tests := []struct {
		name        string
		home        string
		projectPath string
		want        string
	}{
		{name: "data alias", home: "/Users/alice", projectPath: "/data/projects/memoh", want: "/Users/alice/projects/memoh"},
		{name: "absolute folder", home: "/home/alice", projectPath: "/srv/project", want: "/srv/project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := ResolveSessionContext(SessionContextInput{
				Backend:        "remote",
				OS:             "darwin",
				DefaultWorkDir: tt.home,
				ProjectPath:    tt.projectPath,
			})
			if err != nil {
				t.Fatalf("ResolveSessionContext() error = %v", err)
			}
			if resolved.WorkspaceRoot != "/" || resolved.ProjectPath != tt.want || resolved.CWD != tt.want {
				t.Fatalf("remote context = %#v, want root / and path %q", resolved, tt.want)
			}
		})
	}
}

func TestResolveSessionContextRemoteRejectsUnsupportedPlatformOrHome(t *testing.T) {
	tests := []struct {
		name  string
		input SessionContextInput
		want  string
	}{
		{
			name:  "windows",
			input: SessionContextInput{Backend: "remote", OS: "win32", DefaultWorkDir: `C:\\Users\\alice`},
			want:  "unsupported remote ACP operating system",
		},
		{
			name:  "relative home",
			input: SessionContextInput{Backend: "remote", OS: "linux", DefaultWorkDir: "home/alice"},
			want:  "workspace home must be an absolute path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveSessionContext(tt.input)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ResolveSessionContext() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestResolveSessionContextHermesManagedHome(t *testing.T) {
	resolved, err := ResolveSessionContext(SessionContextInput{
		AgentID:     "hermes",
		SetupMode:   SetupModeAPIKey,
		Backend:     "container",
		ProjectPath: "/data/project",
	})
	if err != nil {
		t.Fatalf("ResolveSessionContext() error = %v", err)
	}
	if resolved.HermesHome != HermesContainerHome {
		t.Fatalf("HermesHome = %q, want %q", resolved.HermesHome, HermesContainerHome)
	}
	if resolved.CWD != "/data/project" {
		t.Fatalf("CWD = %q, want /data/project", resolved.CWD)
	}
}

func TestResolveSessionContextHermesSelfDoesNotSetManagedHome(t *testing.T) {
	resolved, err := ResolveSessionContext(SessionContextInput{
		AgentID:     "hermes",
		SetupMode:   SetupModeSelf,
		Backend:     "container",
		ProjectPath: "/data",
	})
	if err != nil {
		t.Fatalf("ResolveSessionContext() error = %v", err)
	}
	if resolved.HermesHome != "" {
		t.Fatalf("HermesHome = %q, want empty", resolved.HermesHome)
	}
}
