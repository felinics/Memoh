package claudecode

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const (
	projectsDirName = "projects"
	// maxProjectEntries bounds the transcript search; the projects tree holds
	// one directory per working directory with one JSONL per session, so
	// thousands of entries already indicate an unexpected layout.
	maxProjectEntries = 8192
)

// transcriptFS is the workspace file surface transcript discovery needs;
// *bridge.Client satisfies it.
type transcriptFS interface {
	ListDirBounded(ctx context.Context, path string, recursive bool, maxEntries int32) ([]*pb.FileEntry, error)
	Stat(ctx context.Context, path string) (*pb.FileEntry, error)
}

// locateSessionTranscript finds the session's transcript under the config
// dir's projects tree by its session-id file name, returning the transcript
// path relative to the config dir. The project directory name encodes the
// working directory in a CLI-private way, so discovery goes by file name
// instead of reimplementing that encoding.
func locateSessionTranscript(ctx context.Context, fs transcriptFS, sessionID string) (string, bool, error) {
	if err := validateClaudeSessionID(sessionID); err != nil {
		return "", false, err
	}
	projectsRoot := path.Join(configDir, projectsDirName)
	if _, err := fs.Stat(ctx, projectsRoot); err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("stat claude projects dir: %w", err)
	}
	entries, err := fs.ListDirBounded(ctx, projectsRoot, true, maxProjectEntries)
	if err != nil {
		return "", false, fmt.Errorf("list claude projects dir: %w", err)
	}
	wanted := sessionID + ".jsonl"
	for _, entry := range entries {
		if entry.GetIsDir() || strings.HasPrefix(entry.GetMode(), "L") {
			continue
		}
		rel := cleanTranscriptRelPath(entry.GetPath())
		if rel == "" || path.Base(rel) != wanted {
			continue
		}
		return path.Join(projectsDirName, rel), true, nil
	}
	return "", false, nil
}

// cleanTranscriptRelPath normalizes a listed entry path and rejects anything
// that could escape the projects tree.
func cleanTranscriptRelPath(value string) string {
	value = strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"), "/")
	if value == "" || path.Clean(value) != value {
		return ""
	}
	for _, component := range strings.Split(value, "/") {
		if component == "." || component == ".." || component == "" {
			return ""
		}
	}
	return value
}

func validateClaudeSessionID(value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 256 ||
		strings.ContainsAny(value, "\x00\r\n/\\") || value == "." || value == ".." {
		return errors.New("claude session id is not a safe file name")
	}
	return nil
}
