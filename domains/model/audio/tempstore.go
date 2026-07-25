package audio

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultTTL      = 10 * time.Minute
	cleanupInterval = 1 * time.Minute
	tempDirName     = "audio_temp"
)

// TempStore manages temporary audio files on disk with automatic TTL-based cleanup.
// MIME type and other metadata are NOT stored here — they travel in the tool
// result JSON through the SSE stream.
type TempStore struct {
	dir string

	mu      sync.RWMutex
	entries map[string]time.Time
}

// NewTempStore creates a TempStore under the given base directory.
func NewTempStore(baseDir string) (*TempStore, error) {
	dir := filepath.Join(baseDir, tempDirName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create audio temp dir: %w", err)
	}
	return &TempStore{
		dir:     dir,
		entries: make(map[string]time.Time),
	}, nil
}

// Create opens a new temporary file for writing. The caller writes audio data
// into the returned file and must close it when done.
func (s *TempStore) Create() (id string, f *os.File, err error) {
	id = uuid.New().String()
	path := filepath.Join(s.dir, id)
	f, err = os.Create(path) //nolint:gosec // Path is generated from controlled base dir + UUID.
	if err != nil {
		return "", nil, fmt.Errorf("create temp file: %w", err)
	}

	s.mu.Lock()
	s.entries[id] = time.Now()
	s.mu.Unlock()

	return id, f, nil
}

// FileSize returns the size of the temp file in bytes.
func (s *TempStore) FileSize(id string) (int64, error) {
	path := filepath.Join(s.dir, id)
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// ReadAndDelete reads the full file contents and removes the entry.
func (s *TempStore) ReadAndDelete(id string) ([]byte, error) {
	s.mu.RLock()
	_, exists := s.entries[id]
	s.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("temp entry not found: %s", id)
	}

	path := filepath.Join(s.dir, id)
	data, err := os.ReadFile(path) //nolint:gosec // Path is generated from controlled base dir + validated entry ID.
	if err != nil {
		return nil, fmt.Errorf("read temp file: %w", err)
	}
	s.Delete(id)
	return data, nil
}

// Delete removes a temp file and its tracking entry.
func (s *TempStore) Delete(id string) {
	s.mu.Lock()
	delete(s.entries, id)
	s.mu.Unlock()
	_ = os.Remove(filepath.Join(s.dir, id))
}

// StartCleanup runs a background goroutine that removes expired entries.
func (s *TempStore) StartCleanup(done <-chan struct{}) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			s.cleanup()
		}
	}
}

func (s *TempStore) cleanup() {
	now := time.Now()
	s.mu.Lock()
	var expired []string
	for id, created := range s.entries {
		if now.Sub(created) > defaultTTL {
			expired = append(expired, id)
		}
	}
	for _, id := range expired {
		delete(s.entries, id)
	}
	s.mu.Unlock()

	for _, id := range expired {
		_ = os.Remove(filepath.Join(s.dir, id))
	}
}
