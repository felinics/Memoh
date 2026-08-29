package boot

import (
	"testing"

	"github.com/felinics/memoh/internal/config"
)

func TestProvideRuntimeConfig_DefaultTimezone(t *testing.T) {
	// Isolate the ambient TZ so a developer's shell TZ does not override the
	// configured default. Can't use t.Parallel() alongside t.Setenv.
	t.Setenv("TZ", "")

	cfg := config.Config{
		Auth: config.AuthConfig{
			JWTSecret:    "secret",
			JWTExpiresIn: "24h",
		},
		Timezone: config.DefaultTimezone,
		Container: config.ContainerConfig{
			Backend: "containerd",
		},
		Containerd: config.ContainerdConfig{
			SocketPath: "/run/containerd/containerd.sock",
			Namespace:  "default",
		},
		Server: config.ServerConfig{
			Addr: ":8080",
		},
	}

	rc, err := ProvideRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("ProvideRuntimeConfig returned error: %v", err)
	}
	if rc.Timezone != config.DefaultTimezone {
		t.Fatalf("Timezone = %q, want %q", rc.Timezone, config.DefaultTimezone)
	}
	if rc.TimezoneLocation == nil {
		t.Fatal("TimezoneLocation is nil")
	}
	if rc.TimezoneLocation.String() != config.DefaultTimezone {
		t.Fatalf("TimezoneLocation = %q, want %q", rc.TimezoneLocation.String(), config.DefaultTimezone)
	}
}

func TestProvideRuntimeConfig_ResolvesTZEnv(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")

	cfg := config.Config{
		Auth: config.AuthConfig{
			JWTSecret:    "secret",
			JWTExpiresIn: "24h",
		},
		Timezone: "UTC",
		Container: config.ContainerConfig{
			Backend: "containerd",
		},
		Containerd: config.ContainerdConfig{
			SocketPath: "/run/containerd/containerd.sock",
			Namespace:  "default",
		},
		Server: config.ServerConfig{
			Addr: ":8080",
		},
	}

	rc, err := ProvideRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("ProvideRuntimeConfig returned error: %v", err)
	}
	if rc.Timezone != "Asia/Tokyo" {
		t.Fatalf("Timezone = %q, want Asia/Tokyo", rc.Timezone)
	}
	if rc.TimezoneLocation == nil {
		t.Fatal("TimezoneLocation is nil")
	}
	if rc.TimezoneLocation.String() != "Asia/Tokyo" {
		t.Fatalf("TimezoneLocation = %q, want Asia/Tokyo", rc.TimezoneLocation.String())
	}
}

func TestProvideRuntimeConfigRequiresContainerBackend(t *testing.T) {
	cfg := config.Config{
		Auth: config.AuthConfig{
			JWTSecret:    "secret",
			JWTExpiresIn: "24h",
		},
		Timezone: config.DefaultTimezone,
	}
	if _, err := ProvideRuntimeConfig(cfg); err == nil {
		t.Fatal("expected missing container backend error")
	}
}

func TestProvideRuntimeConfigBackendIgnoresEnvOverride(t *testing.T) {
	t.Setenv("CONTAINER_BACKEND", "apple")
	cfg := config.Config{
		Auth: config.AuthConfig{
			JWTSecret:    "secret",
			JWTExpiresIn: "24h",
		},
		Timezone: config.DefaultTimezone,
		Container: config.ContainerConfig{
			Backend: "docker",
		},
	}
	rc, err := ProvideRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("ProvideRuntimeConfig returned error: %v", err)
	}
	if rc.ContainerBackend != "docker" {
		t.Fatalf("ContainerBackend = %q, want docker", rc.ContainerBackend)
	}
}
