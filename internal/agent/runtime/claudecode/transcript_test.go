package claudecode

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const claudeTestSession = "6f1f8a44-9a1a-4a7e-9df1-0f6f8f3f1a10"

type fakeTranscriptFS struct {
	files map[string]string
	dirs  map[string]bool
}

func newFakeTranscriptFS() *fakeTranscriptFS {
	return &fakeTranscriptFS{files: map[string]string{}, dirs: map[string]bool{}}
}

func (f *fakeTranscriptFS) addFile(fullPath, content string) {
	f.files[fullPath] = content
	dir := path.Dir(fullPath)
	for dir != "/" && dir != "." {
		f.dirs[dir] = true
		dir = path.Dir(dir)
	}
}

func (f *fakeTranscriptFS) ListDirBounded(_ context.Context, root string, _ bool, _ int32) ([]*pb.FileEntry, error) {
	if !f.dirs[root] {
		return nil, errors.New("no such directory")
	}
	entries := []*pb.FileEntry{}
	prefix := root + "/"
	for full := range f.files {
		if strings.HasPrefix(full, prefix) {
			entries = append(entries, &pb.FileEntry{Path: strings.TrimPrefix(full, prefix), Mode: "-rw-r--r--"})
		}
	}
	for dir := range f.dirs {
		if strings.HasPrefix(dir, prefix) {
			entries = append(entries, &pb.FileEntry{Path: strings.TrimPrefix(dir, prefix), IsDir: true, Mode: "drwxr-xr-x"})
		}
	}
	return entries, nil
}

func (f *fakeTranscriptFS) Stat(_ context.Context, target string) (*pb.FileEntry, error) {
	if f.dirs[target] {
		return &pb.FileEntry{Path: target, IsDir: true}, nil
	}
	if _, ok := f.files[target]; ok {
		return &pb.FileEntry{Path: target}, nil
	}
	return nil, errors.New("not found")
}

func transcriptFullPath() string {
	return path.Join(configDir, projectsDirName, "-data-project", claudeTestSession+".jsonl")
}

func TestLocateSessionTranscript(t *testing.T) {
	fs := newFakeTranscriptFS()
	fs.addFile(transcriptFullPath(), `{"type":"user"}`+"\n")
	fs.addFile(path.Join(configDir, projectsDirName, "-data-project", "other.jsonl"), "{}\n")

	rel, found, err := locateSessionTranscript(t.Context(), fs, claudeTestSession)
	if err != nil || !found {
		t.Fatalf("locate = (%q, %t, %v), want found", rel, found, err)
	}
	if rel != path.Join(projectsDirName, "-data-project", claudeTestSession+".jsonl") {
		t.Fatalf("rel = %q", rel)
	}

	_, found, err = locateSessionTranscript(t.Context(), fs, "00000000-0000-0000-0000-000000000000")
	if err != nil || found {
		t.Fatalf("missing session: found=%t err=%v", found, err)
	}

	if _, _, err := locateSessionTranscript(t.Context(), fs, "../escape"); err == nil {
		t.Fatal("traversal session id must be rejected")
	}
}

// A transcript the workspace no longer holds starts a fresh session and says
// so; a lookup that merely failed still hands the CLI the stored id.
func TestEnsureResumableSession(t *testing.T) {
	sink := &recordingSink{}
	input := external.PromptInput{Sink: sink}
	d := &Driver{logger: slog.Default()}

	fs := newFakeTranscriptFS()
	fs.addFile(transcriptFullPath(), "{}\n")
	if got := d.ensureResumableSession(t.Context(), fs, input, claudeTestSession); got != claudeTestSession {
		t.Fatalf("present transcript resumed %q", got)
	}

	if got := d.ensureResumableSession(t.Context(), newFakeTranscriptFS(), input, claudeTestSession); got != claudeTestSession {
		t.Fatalf("lookup failure must still attempt the resume, got %q", got)
	}

	fs = newFakeTranscriptFS()
	fs.addFile(path.Join(configDir, projectsDirName, "-data-project", "other.jsonl"), "{}\n")
	if got := d.ensureResumableSession(t.Context(), fs, input, claudeTestSession); got != "" {
		t.Fatalf("missing transcript resumed %q", got)
	}
	if len(sink.events) != 1 || sink.events[0].Code != "native_history_lost" {
		t.Fatalf("lost history was not announced: %+v", sink.events)
	}
}
