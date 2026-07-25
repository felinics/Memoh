package container

import (
	"errors"
	"strings"
)

const (
	DefaultSocketPath = "/run/containerd/containerd.sock"
	DefaultNamespace  = "default"

	BackendContainerd = "containerd"
	BackendApple      = "apple"
	BackendDocker     = "docker"
)

// Backend is a normalized workspace container backend name.
type Backend string

// ParseBackend normalizes a known operator-facing backend name. Empty values
// fail fast; unknown non-empty values are preserved for later assembly errors.
func ParseBackend(value string) (Backend, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errors.New("container backend is required; set [container].backend to docker, containerd, or apple")
	}
	normalized := strings.ToLower(trimmed)
	switch normalized {
	case BackendApple, BackendContainerd, BackendDocker:
		return Backend(normalized), nil
	default:
		return Backend(trimmed), nil
	}
}

func (b Backend) String() string { return string(b) }
