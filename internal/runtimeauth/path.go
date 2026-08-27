package runtimeauth

import (
	"errors"
	"path"
	"strings"

	"github.com/google/uuid"
)

const Root = "/tmp/memoh-auth"

var ErrInvalidRuntimeID = errors.New("invalid runtime auth ID")

// RootFor returns the private, runtime-owned authentication directory. Only
// server-generated rt_<uuid> identifiers are accepted so cleanup can never
// escape the fixed runtime auth root.
func RootFor(runtimeID string) (string, error) {
	runtimeID = strings.TrimSpace(runtimeID)
	if !strings.HasPrefix(runtimeID, "rt_") {
		return "", ErrInvalidRuntimeID
	}
	if _, err := uuid.Parse(strings.TrimPrefix(runtimeID, "rt_")); err != nil {
		return "", ErrInvalidRuntimeID
	}
	return path.Join(Root, runtimeID), nil
}

func Child(root, name string) (string, error) {
	root = path.Clean(strings.TrimSpace(root))
	name = strings.TrimSpace(name)
	if !IsRuntimeRoot(root) || name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
		return "", ErrInvalidRuntimeID
	}
	return path.Join(root, name), nil
}

func IsRuntimeRoot(value string) bool {
	value = path.Clean(strings.TrimSpace(value))
	if path.Dir(value) != Root {
		return false
	}
	_, err := RootFor(path.Base(value))
	return err == nil
}
