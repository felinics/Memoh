package skills

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
	"testing"
)

const validArchiveManifest = "---\nname: skill\ndescription: Demo\n---\n\n# Demo\n"

func TestReadArchiveReadsContentRoot(t *testing.T) {
	archive, err := ReadArchive(testArchive(t, []archiveTestEntry{
		{name: "SKILL.md", content: validArchiveManifest},
		{name: "scripts/run.sh", content: "#!/bin/sh\n", mode: 0o755},
	}))
	if err != nil {
		t.Fatalf("ReadArchive() error = %v", err)
	}
	if archive.FileCount() != 2 {
		t.Fatalf("files = %d, want 2", archive.FileCount())
	}
	if archive.UncompressedSize() != int64(len(validArchiveManifest)+len("#!/bin/sh\n")) {
		t.Fatalf("uncompressed bytes = %d", archive.UncompressedSize())
	}
	if archive.ArchiveSize() != decompressedArchiveSize(t, testArchive(t, []archiveTestEntry{
		{name: "SKILL.md", content: validArchiveManifest},
		{name: "scripts/run.sh", content: "#!/bin/sh\n", mode: 0o755},
	})) {
		t.Fatalf("archive bytes = %d, want complete tar stream size", archive.ArchiveSize())
	}
	if !strings.Contains(string(archiveFileByPath(t, archive, "SKILL.md").content), "# Demo") {
		t.Fatal("manifest body was not preserved")
	}
	if !archiveFileByPath(t, archive, "scripts/run.sh").executable {
		t.Fatal("executable mode was not retained")
	}
}

func TestReadArchiveRejectsUnsafeEntries(t *testing.T) {
	tests := map[string][]archiveTestEntry{
		"path traversal":  {{name: "SKILL.md", content: validArchiveManifest}, {name: "../escape", content: "bad"}},
		"backslash":       {{name: "SKILL.md", content: validArchiveManifest}, {name: `scripts\escape`, content: "bad"}},
		"symlink":         {{name: "SKILL.md", content: validArchiveManifest}, {name: "link", entryType: tar.TypeSymlink, linkName: "../../escape"}},
		"duplicate":       {{name: "SKILL.md", content: validArchiveManifest}, {name: "SKILL.md", content: validArchiveManifest}},
		"case alias":      {{name: "SKILL.md", content: validArchiveManifest}, {name: "skill.md", content: validArchiveManifest}},
		"file conflict":   {{name: "SKILL.md", content: validArchiveManifest}, {name: "scripts", content: "file"}, {name: "scripts/run.sh", content: "bad"}},
		"nested manifest": {{name: "other/SKILL.md", content: validArchiveManifest}},
		"trailing space":  {{name: "SKILL.md", content: validArchiveManifest}, {name: "SKILL.md ", content: "alias"}},
		"nested whitespace": {{name: "SKILL.md", content: validArchiveManifest}, {
			name: "scripts /run.sh", content: "bad",
		}},
		"windows alias":           {{name: "SKILL.md", content: validArchiveManifest}, {name: "scripts/run.sh.", content: "bad"}},
		"windows device":          {{name: "SKILL.md", content: validArchiveManifest}, {name: "scripts/NUL.txt", content: "bad"}},
		"windows numbered device": {{name: "SKILL.md", content: validArchiveManifest}, {name: "references/COM1", content: "bad"}},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadArchive(testArchive(t, entries)); err == nil {
				t.Fatal("ReadArchive() error = nil, want rejection")
			}
		})
	}
}

func TestReadArchiveHonorsCallerUncompressedLimit(t *testing.T) {
	content := testArchive(t, []archiveTestEntry{{name: "SKILL.md", content: validArchiveManifest}})
	if _, err := ReadArchiveWithUncompressedLimit(content, int64(len(validArchiveManifest)-1)); err == nil ||
		!strings.Contains(err.Error(), "uncompressed size limit") {
		t.Fatalf("ReadArchiveWithUncompressedLimit() error = %v", err)
	}
}

func TestReadArchiveHonorsCallerArchiveAndFileLimits(t *testing.T) {
	content := testArchive(t, []archiveTestEntry{
		{name: "SKILL.md", content: validArchiveManifest},
		{name: "reference.md", content: "reference"},
	})
	archiveSize := decompressedArchiveSize(t, content)
	if _, err := ReadArchiveWithLimits(
		content,
		maxArchiveUncompressedBytes,
		archiveSize-1,
		maxArchiveFiles,
	); err == nil || !strings.Contains(err.Error(), "decompressed stream limit") {
		t.Fatalf("ReadArchiveWithLimits() archive error = %v", err)
	}
	if _, err := ReadArchiveWithLimits(
		content,
		maxArchiveUncompressedBytes,
		archiveSize,
		1,
	); err == nil || !strings.Contains(err.Error(), "file limit") {
		t.Fatalf("ReadArchiveWithLimits() file error = %v", err)
	}
}

func TestReadArchiveRejectsEntryFlood(t *testing.T) {
	entries := []archiveTestEntry{{name: "SKILL.md", content: validArchiveManifest}}
	for i := 0; i <= maxArchiveEntries; i++ {
		entries = append(entries, archiveTestEntry{name: fmt.Sprintf("dir%d/", i), entryType: tar.TypeDir})
	}
	if _, err := ReadArchive(testArchive(t, entries)); err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("want too-many-entries rejection, got %v", err)
	}
}

func TestReadArchiveRejectsMalformedFrontmatter(t *testing.T) {
	for name, manifest := range map[string]string{
		"missing closing fence": "---\nname: skill\n",
		"not a mapping":         "---\n- a\n- b\n---\n# Body\n",
		"missing frontmatter":   "# Body only\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadArchive(testArchive(t, []archiveTestEntry{{name: "SKILL.md", content: manifest}})); err == nil {
				t.Fatal("ReadArchive() accepted malformed SKILL.md frontmatter")
			}
		})
	}
}

type archiveTestEntry struct {
	name      string
	content   string
	mode      int64
	entryType byte
	linkName  string
}

func testArchive(t *testing.T, entries []archiveTestEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		kind := entry.entryType
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{Name: entry.name, Mode: mode, Typeflag: kind, Linkname: entry.linkName}
		if kind == tar.TypeReg {
			header.Size = int64(len(entry.content))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader(%q): %v", entry.name, err)
		}
		if kind == tar.TypeReg {
			if _, err := tw.Write([]byte(entry.content)); err != nil {
				t.Fatalf("Write(%q): %v", entry.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return output.Bytes()
}

func decompressedArchiveSize(t *testing.T, content []byte) int64 {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	decompressed, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("read decompressed archive: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return int64(len(decompressed))
}

func archiveFileByPath(t *testing.T, archive Archive, name string) archiveFile {
	t.Helper()
	for _, file := range archive.files {
		if file.path == name {
			return file
		}
	}
	t.Fatalf("archive does not contain %q", name)
	return archiveFile{}
}
