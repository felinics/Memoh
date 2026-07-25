package container

import "testing"

func TestParseBackendKnownValues(t *testing.T) {
	t.Parallel()

	for _, want := range []string{BackendDocker, BackendContainerd, BackendApple} {
		got, err := ParseBackend(want)
		if err != nil {
			t.Fatalf("ParseBackend(%q) error: %v", want, err)
		}
		if got.String() != want {
			t.Fatalf("ParseBackend(%q) = %q, want %q", want, got, want)
		}
	}
}

func TestParseBackendRequiresValue(t *testing.T) {
	t.Parallel()

	if _, err := ParseBackend(""); err == nil {
		t.Fatal("expected missing container backend error")
	}
}

func TestParseBackendIgnoresContainerBackendEnv(t *testing.T) {
	t.Setenv("CONTAINER_BACKEND", "apple")

	got, err := ParseBackend("docker")
	if err != nil {
		t.Fatalf("ParseBackend returned error: %v", err)
	}
	if got != BackendDocker {
		t.Fatalf("ParseBackend() = %q, want docker", got)
	}
}
