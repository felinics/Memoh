package runtimeauth

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const StaleAfter = 24 * time.Hour

// CleanupStale removes abandoned runtime authentication directories without
// following symlinks. A missing root is the normal cold-start state.
func CleanupStale(now time.Time) error {
	entries, err := os.ReadDir(Root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var firstErr error
	for _, entry := range entries {
		candidate, pathErr := RootFor(entry.Name())
		if pathErr != nil {
			continue
		}
		info, statErr := os.Lstat(candidate)
		if statErr != nil {
			if firstErr == nil {
				firstErr = statErr
			}
			continue
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || now.Sub(info.ModTime()) < StaleAfter {
			continue
		}
		if filepath.Dir(candidate) != Root {
			continue
		}
		if removeErr := os.RemoveAll(candidate); removeErr != nil && firstErr == nil {
			firstErr = removeErr
		}
	}
	return firstErr
}
