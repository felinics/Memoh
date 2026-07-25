// Package runtime defines stable workspace runtime contracts shared across
// composition roots and internal packages.
package runtime

import "strings"

// DefaultDataMount is the canonical container workspace data mount path.
const DefaultDataMount = "/data"

// DataMountPath joins a relative path under the canonical container data mount.
func DataMountPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultDataMount
	}
	return strings.TrimRight(DefaultDataMount, "/") + "/" + strings.TrimLeft(raw, "/")
}

// DataSubpath strips the canonical data-mount prefix and returns the
// container-relative path.
func DataSubpath(containerPath string) (string, bool) {
	raw := strings.TrimSpace(containerPath)
	prefix := strings.TrimRight(DefaultDataMount, "/") + "/"
	if !strings.HasPrefix(raw, prefix) {
		return "", false
	}
	subPath := raw[len(prefix):]
	if strings.TrimSpace(subPath) == "" {
		return "", false
	}
	return subPath, true
}

// IsDataPath reports whether the reference points under the data mount.
func IsDataPath(raw string) bool {
	_, ok := DataSubpath(raw)
	return ok
}
