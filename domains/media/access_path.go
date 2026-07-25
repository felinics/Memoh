package media

import (
	"path"
	"strings"
)

const mediaAccessSubdir = "media"

// AccessPath returns the consumer-facing media access path for a storage key
// under the provided data mount. dataMount is supplied by the caller so this
// package stays free of Runtime imports.
func AccessPath(dataMount, storageKey string) string {
	dataMount = strings.TrimSpace(dataMount)
	storageKey = strings.TrimSpace(storageKey)
	if storageKey == "" {
		return path.Join(dataMount, mediaAccessSubdir)
	}
	return path.Join(dataMount, mediaAccessSubdir, storageKey)
}

// StorageKeyFromAccessPath derives the media storage key from a consumer-facing
// access path of the form <dataMount>/media/<storage_key>.
func StorageKeyFromAccessPath(dataMount, accessPath string) string {
	marker := strings.TrimRight(AccessPath(dataMount, ""), "/") + "/"
	idx := strings.Index(strings.TrimSpace(accessPath), marker)
	if idx < 0 {
		return ""
	}
	return accessPath[idx+len(marker):]
}
