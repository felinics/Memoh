// Package vpath resolves user-supplied paths under a virtual workspace root
// without consulting the host filesystem. The server process cannot evaluate
// symlinks inside a workspace it does not mount, so containment is decided
// purely on the cleaned path shape. Shared by the ACP runtime and the workdir
// service so the clamping semantics cannot drift between them.
package vpath

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// DataMount is the canonical mount point of the workspace data volume as
// seen from inside a bot container.
const DataMount = "/data"

// ResolveUnderRoot resolves raw against root and rejects any result that
// escapes it. A raw path under DataMount is remapped onto root first, so
// callers may pass container-view paths even when root is a host-side
// staging directory.
func ResolveUnderRoot(root, raw string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("workspace root must be an absolute path")
	}
	target := strings.TrimSpace(raw)
	switch {
	case target == "":
		target = root
	case target == DataMount:
		target = root
	case strings.HasPrefix(target, DataMount+"/"):
		target = filepath.Join(root, strings.TrimPrefix(target, DataMount+"/"))
	case filepath.IsAbs(target):
		target = filepath.Clean(target)
	default:
		target = filepath.Join(root, target)
	}
	target = filepath.Clean(target)
	if !isUnderRoot(root, target) {
		return "", fmt.Errorf("path %q escapes workspace root %q", rawForError(raw), root)
	}
	return target, nil
}

func isUnderRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if target == root {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func rawForError(path string) string {
	if path == "" {
		return "."
	}
	return path
}
