package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func releaseDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestFromArtifactRejectsTamperedReleaseAndArchive(t *testing.T) {
	metadata, err := os.ReadFile("testdata/remote/codex/release.json")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile("testdata/remote/codex/artifact.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	var original Release
	if err := json.Unmarshal(metadata, &original); err != nil {
		t.Fatal(err)
	}
	if _, err := FromArtifact(metadata, releaseDigest(metadata), archive, "https://supermarket.example"); err != nil {
		t.Fatalf("original fixture: %v", err)
	}

	t.Run("release revision", func(t *testing.T) {
		changed := append(bytes.Clone(metadata), '\n')
		if _, err := FromArtifact(changed, releaseDigest(metadata), archive, ""); err == nil {
			t.Fatal("accepted release bytes with the old revision")
		}
	})
	t.Run("compressed artifact digest", func(t *testing.T) {
		changed := bytes.Clone(archive)
		changed[len(changed)/2] ^= 1
		if _, err := FromArtifact(metadata, releaseDigest(metadata), changed, ""); err == nil {
			t.Fatal("accepted tampered archive with the old digest")
		}
	})
	t.Run("forged projected manifest", func(t *testing.T) {
		release := original
		var projected map[string]any
		if err := json.Unmarshal(release.Manifest, &projected); err != nil {
			t.Fatal(err)
		}
		projected["name"] = "Forged release metadata"
		release.Manifest, _ = json.Marshal(projected)
		changed, _ := json.Marshal(release)
		if _, err := FromArtifact(changed, releaseDigest(changed), archive, ""); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("forged projection: %v", err)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func([]archiveTestFile) []archiveTestFile
		want   string
	}{
		{"script digest", func(files []archiveTestFile) []archiveTestFile {
			for i := range files {
				if files[i].header.Name == "install.sh" {
					files[i].body = append(files[i].body, []byte("\necho tampered\n")...)
				}
			}
			return files
		}, "manifest identity mismatch"},
		{"path traversal", func(files []archiveTestFile) []archiveTestFile {
			files[0].header.Name = "../outside"
			return files
		}, "invalid archive entry"},
		{"symlink", func(files []archiveTestFile) []archiveTestFile {
			files[0].header.Typeflag = tar.TypeSymlink
			files[0].header.Linkname = "/outside"
			files[0].body = nil
			return files
		}, "invalid archive entry"},
		{"case alias", func(files []archiveTestFile) []archiveTestFile {
			alias := files[0]
			alias.header.Name = strings.ToUpper(alias.header.Name)
			return append(files, alias)
		}, "invalid archive entry"},
		{"undeclared file", func(files []archiveTestFile) []archiveTestFile {
			return append(files, archiveTestFile{header: tar.Header{Name: "surprise.sh", Mode: 0o644, Typeflag: tar.TypeReg}, body: []byte("echo surprise\n")})
		}, "undeclared archive file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changedArchive, descriptor := rewriteTestArtifact(t, archive, tc.mutate)
			release := original
			release.Artifact = descriptor
			changedMetadata, _ := json.Marshal(release)
			_, err := FromArtifact(changedMetadata, releaseDigest(changedMetadata), changedArchive, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %s", err, tc.want)
			}
		})
	}
}

type archiveTestFile struct {
	header tar.Header
	body   []byte
}

func rewriteTestArtifact(t *testing.T, archive []byte, mutate func([]archiveTestFile) []archiveTestFile) ([]byte, Artifact) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	var files []archiveTestFile
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, archiveTestFile{header: *header, body: body})
	}
	files = mutate(files)
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	var contentSize int64
	for _, file := range files {
		file.header.Size = int64(len(file.body))
		if err := writer.WriteHeader(&file.header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(file.body); err != nil {
			t.Fatal(err)
		}
		contentSize += int64(len(file.body))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	compressor := gzip.NewWriter(&compressed)
	if _, err := compressor.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes(), Artifact{Format: "memoh_dependency_v1", Digest: releaseDigest(compressed.Bytes()), Size: int64(compressed.Len()), ArchiveSize: int64(raw.Len()), UncompressedSize: contentSize, FileCount: len(files), ContentType: "application/gzip"}
}

func TestUsingRejectsEmptyDefinition(t *testing.T) {
	if _, err := Empty().Using(Definition{}); err == nil {
		t.Fatal("Using accepted an empty definition")
	}
}
