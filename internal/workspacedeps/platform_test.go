package workspacedeps

import (
	"runtime"
	"strings"
	"testing"
)

func TestProbePlatformAgainstLocalShell(t *testing.T) {
	client := newExecTestClient(t)
	platform, err := ProbePlatform(testContext(t), client)
	if err != nil {
		t.Fatalf("ProbePlatform: %v", err)
	}
	if platform.OS != runtime.GOOS {
		t.Errorf("OS = %q, want %q", platform.OS, runtime.GOOS)
	}
	if platform.Arch != normalizeArch(runtime.GOARCH) {
		t.Errorf("Arch = %q, want %q", platform.Arch, runtime.GOARCH)
	}
	switch runtime.GOOS {
	case "linux":
		if platform.Libc != "glibc" && platform.Libc != "musl" {
			t.Errorf("Libc = %q, want glibc or musl on linux", platform.Libc)
		}
	case "darwin":
		if platform.Libc != "" {
			t.Errorf("Libc = %q, want empty on darwin", platform.Libc)
		}
	}
	if platform.TmpDir == "" || !strings.HasPrefix(platform.TmpDir, "/") || strings.HasSuffix(platform.TmpDir, "/") {
		t.Errorf("TmpDir = %q, want an absolute path without trailing slash", platform.TmpDir)
	}
}

func TestParsePlatformOutput(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		want   Platform
	}{
		{
			name:   "linux glibc",
			stdout: "Linux\nx86_64\n/tmp\n",
			want:   Platform{OS: "linux", Arch: "amd64", Libc: "glibc", TmpDir: "/tmp"},
		},
		{
			name:   "linux musl",
			stdout: "Linux\naarch64\n/lib/ld-musl-aarch64.so.1\n/tmp\n",
			want:   Platform{OS: "linux", Arch: "arm64", Libc: "musl", TmpDir: "/tmp"},
		},
		{
			name:   "darwin",
			stdout: "Darwin\narm64\n/var/folders/zz/T/\n",
			want:   Platform{OS: "darwin", Arch: "arm64", Libc: "", TmpDir: "/var/folders/zz/T"},
		},
		{
			name:   "empty tmpdir falls back",
			stdout: "Linux\nx86_64\n\n",
			want:   Platform{OS: "linux", Arch: "amd64", Libc: "glibc", TmpDir: "/tmp"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePlatformOutput(tc.stdout)
			if err != nil {
				t.Fatalf("parsePlatformOutput: %v", err)
			}
			if got != tc.want {
				t.Errorf("parsePlatformOutput = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParsePlatformOutputRejectsShortOutput(t *testing.T) {
	for _, stdout := range []string{"", "Linux\n", "Linux\n\n/tmp\n"} {
		if _, err := parsePlatformOutput(stdout); err == nil {
			t.Errorf("parsePlatformOutput(%q) succeeded, want error", stdout)
		}
	}
}

func TestPlatformEnv(t *testing.T) {
	env := strings.Join(Platform{OS: "linux", Arch: "arm64", Libc: "musl"}.env(), ",")
	if env != "MEMOH_DEP_OS=linux,MEMOH_DEP_ARCH=arm64,MEMOH_DEP_LIBC=musl" {
		t.Errorf("env = %q", env)
	}
}
