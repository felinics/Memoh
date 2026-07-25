package client

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const dataMountPath = "/data"

// ResolvePathUnderVirtualRoot resolves raw under root without consulting the
// host filesystem. Use this for container paths, where the server process
// cannot evaluate symlinks inside the workspace filesystem directly.
func ResolvePathUnderVirtualRoot(root, raw string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("workspace root must be an absolute path")
	}
	target := strings.TrimSpace(raw)
	switch {
	case target == "":
		target = root
	case target == dataMountPath:
		target = root
	case strings.HasPrefix(target, dataMountPath+"/"):
		target = filepath.Join(root, strings.TrimPrefix(target, dataMountPath+"/"))
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
