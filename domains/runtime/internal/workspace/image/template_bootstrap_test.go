package image

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"testing"
	"testing/fstest"
)

func TestTemplateBootstrapperPreservesUserFilesAndRefreshesManagedSkills(t *testing.T) {
	t.Parallel()

	source := fstest.MapFS{
		"AGENTS.md":                                {Data: []byte("default agents\n")},
		"MEMORY.md":                                {Data: []byte("default memory\n")},
		".memoh/hooks.json":                        {Data: []byte("{}\n")},
		".memoh/skills/.gitkeep":                   {Data: nil},
		".memoh/skills/skill-creator/SKILL.md":     {Data: []byte("managed v2\n")},
		".memoh/skills/skill-creator/scripts/x.py": {Data: []byte("print('managed')\n")},
	}
	target := newFakeWorkspaceFS()
	target.files["/data/AGENTS.md"] = []byte("user agents\n")
	target.files["/data/.memoh/skills/skill-creator/SKILL.md"] = []byte("managed v1\n")
	target.files["/data/.memoh/skills/user-skill/SKILL.md"] = []byte("user skill\n")

	err := NewTemplateBootstrapper(source).Bootstrap(context.Background(), target, "/data")
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	assertWorkspaceFile(t, target, "/data/AGENTS.md", "user agents\n")
	assertWorkspaceFile(t, target, "/data/MEMORY.md", "default memory\n")
	assertWorkspaceFile(t, target, "/data/.memoh/hooks.json", "{}\n")
	assertWorkspaceFile(t, target, "/data/.memoh/skills/skill-creator/SKILL.md", "managed v2\n")
	assertWorkspaceFile(t, target, "/data/.memoh/skills/skill-creator/scripts/x.py", "print('managed')\n")
	assertWorkspaceFile(t, target, "/data/.memoh/skills/user-skill/SKILL.md", "user skill\n")
	if _, ok := target.files["/data/.memoh/skills/.gitkeep"]; ok {
		t.Fatal(".gitkeep was written into workspace")
	}
}

func TestTemplateBootstrapperMigratesLegacyIdentityBeforeSeeding(t *testing.T) {
	t.Parallel()

	target := newFakeWorkspaceFS()
	target.files["/data/IDENTITY.md"] = []byte("legacy identity\n")
	source := fstest.MapFS{
		"AGENTS.md": {Data: []byte("default agents\n")},
	}

	if err := NewTemplateBootstrapper(source).Bootstrap(context.Background(), target, "/data"); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	assertWorkspaceFile(t, target, "/data/AGENTS.md", "legacy identity\n")
	if _, ok := target.files["/data/IDENTITY.md"]; ok {
		t.Fatal("legacy IDENTITY.md still exists")
	}
}

func TestTemplateBootstrapperDoesNotReplaceExistingAgentsDuringLegacyMigration(t *testing.T) {
	t.Parallel()

	target := newFakeWorkspaceFS()
	target.files["/data/AGENTS.md"] = []byte("user agents\n")
	target.files["/data/IDENTITY.md"] = []byte("legacy identity\n")

	if err := NewTemplateBootstrapper(fstest.MapFS{}).Bootstrap(context.Background(), target, "/data"); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	assertWorkspaceFile(t, target, "/data/AGENTS.md", "user agents\n")
	assertWorkspaceFile(t, target, "/data/IDENTITY.md", "legacy identity\n")
}

func TestTemplateBootstrapperRequiresProviderNeutralNotFoundError(t *testing.T) {
	t.Parallel()

	target := newFakeWorkspaceFS()
	target.statErr = errors.New("provider failed")
	err := NewTemplateBootstrapper(fstest.MapFS{
		"AGENTS.md": {Data: []byte("default agents\n")},
	}).Bootstrap(context.Background(), target, "/data")
	if err == nil {
		t.Fatal("Bootstrap() error = nil")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Bootstrap() error = %v, unexpectedly classified as not found", err)
	}
}

type fakeWorkspaceFS struct {
	files   map[string][]byte
	dirs    map[string]struct{}
	statErr error
}

func newFakeWorkspaceFS() *fakeWorkspaceFS {
	return &fakeWorkspaceFS{
		files: make(map[string][]byte),
		dirs:  map[string]struct{}{"/": {}},
	}
}

func (f *fakeWorkspaceFS) Stat(_ context.Context, filePath string) (WorkspaceFileInfo, error) {
	if f.statErr != nil {
		return WorkspaceFileInfo{}, f.statErr
	}
	filePath = path.Clean(filePath)
	if _, ok := f.dirs[filePath]; ok {
		return WorkspaceFileInfo{IsDir: true}, nil
	}
	if _, ok := f.files[filePath]; ok {
		return WorkspaceFileInfo{}, nil
	}
	return WorkspaceFileInfo{}, fs.ErrNotExist
}

func (f *fakeWorkspaceFS) Mkdir(_ context.Context, dirPath string) error {
	for current := path.Clean(dirPath); current != "." && current != "/"; current = path.Dir(current) {
		f.dirs[current] = struct{}{}
	}
	f.dirs["/"] = struct{}{}
	return nil
}

func (f *fakeWorkspaceFS) WriteFile(_ context.Context, filePath string, content []byte) error {
	filePath = path.Clean(filePath)
	f.files[filePath] = append([]byte(nil), content...)
	return nil
}

func (f *fakeWorkspaceFS) Rename(_ context.Context, oldPath, newPath string) error {
	oldPath = path.Clean(oldPath)
	newPath = path.Clean(newPath)
	content, ok := f.files[oldPath]
	if !ok {
		return fs.ErrNotExist
	}
	f.files[newPath] = content
	delete(f.files, oldPath)
	return nil
}

func assertWorkspaceFile(t *testing.T, target *fakeWorkspaceFS, filePath, want string) {
	t.Helper()
	got, ok := target.files[filePath]
	if !ok {
		t.Fatalf("workspace file %s is missing", filePath)
	}
	if string(got) != want {
		t.Fatalf("workspace file %s = %q, want %q", filePath, string(got), want)
	}
}
