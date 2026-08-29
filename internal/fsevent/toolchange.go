package fsevent

import "strings"

// ToolChange classifies a completed agent tool call by the canonical tool
// vocabulary shared by the native runtime and the ACP event mapper. It
// reports whether the tool mutates the workspace filesystem and the touched
// absolute paths; nil paths with mutating=true means unknown scope (exec,
// apply_patch, or an unresolvable path).
func ToolChange(toolName string, input any) (paths []string, mutating bool) {
	switch strings.TrimSpace(toolName) {
	case "write", "edit":
		return inputPath(input), true
	case "exec", "apply_patch":
		return nil, true
	default:
		return nil, false
	}
}

func inputPath(input any) []string {
	m, ok := input.(map[string]any)
	if !ok {
		return nil
	}
	path, ok := m["path"].(string)
	if !ok {
		return nil
	}
	path = strings.TrimSpace(path)
	if !strings.HasPrefix(path, "/") {
		return nil
	}
	return []string{path}
}
