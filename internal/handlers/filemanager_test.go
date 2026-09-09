package handlers

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

func TestResolveContainerPathUsesPOSIXSeparators(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "root", path: `\`, want: "/"},
		{name: "windows separators", path: `\data\projects`, want: "/data/projects"},
		{name: "mixed separators", path: `/data\projects//demo`, want: "/data/projects/demo"},
		{name: "relative path", path: `data\projects`, want: "/data/projects"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveContainerPath(tt.path)
			if err != nil {
				t.Fatalf("resolveContainerPath(%q) error = %v", tt.path, err)
			}
			if got != tt.want {
				t.Fatalf("resolveContainerPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestFSFileInfoFromEntryUsesPOSIXSeparators(t *testing.T) {
	got := fsFileInfoFromEntry("/data", "projects", true, 0, "drwxr-xr-x", "2026-05-13T00:00:00Z")
	if got.Path != "/data/projects" {
		t.Fatalf("fsFileInfoFromEntry path = %q, want %q", got.Path, "/data/projects")
	}
}

func TestIsContainerMediaPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/data/.memoh/media", want: true},
		{path: "/data/.memoh/media/0f/demo.jpg", want: true},
		{path: "data/.memoh/media/0f/demo.jpg", want: true},
		{path: "/data/.memoh/mediakit/demo.jpg", want: false},
		{path: "/data/media", want: true},
		{path: "/data/media/0f/demo.jpg", want: true},
		{path: "data/media/0f/demo.jpg", want: true},
		{path: "/data/mediakit/demo.jpg", want: false},
		{path: "/etc/passwd", want: false},
	}

	for _, tt := range tests {
		if got := isContainerMediaPath(tt.path); got != tt.want {
			t.Fatalf("isContainerMediaPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestFSDownloadFileReturnsOriginalBytes(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeSkillFile(t, "/data/readme.txt", "hello workspace")

	rec, err := env.callFileManager(t, http.MethodGet, "/bots/:bot_id/container/fs/download?path=/data/readme.txt", nil, env.handler.FSDownload)
	if err != nil {
		t.Fatalf("FSDownload returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "hello workspace" {
		t.Fatalf("download body = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="readme.txt"`) {
		t.Fatalf("content disposition = %q", got)
	}
}

func TestFSDownloadDirectoryReturnsTarGz(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeSkillFile(t, "/data/project/main.go", "package main\n")
	env.writeSkillFile(t, "/data/project/docs/readme.md", "# docs\n")
	if err := os.MkdirAll(env.localPath("/data/project/empty"), 0o750); err != nil {
		t.Fatalf("mkdir empty dir: %v", err)
	}

	rec, err := env.callFileManager(t, http.MethodGet, "/bots/:bot_id/container/fs/download?path=/data/project", nil, env.handler.FSDownload)
	if err != nil {
		t.Fatalf("FSDownload returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	entries := readTarGzEntries(t, rec.Body.Bytes())
	assertTarEntry(t, entries, "project/main.go", "package main\n")
	assertTarEntry(t, entries, "project/docs/readme.md", "# docs\n")
	if _, ok := entries["project/empty/"]; !ok {
		t.Fatalf("expected empty directory entry, got %#v", entries)
	}
}

func TestFSReadReturnsRevision(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeSkillFile(t, "/data/rev.txt", "hello workspace")

	rec, err := env.callFileManager(t, http.MethodGet, "/bots/:bot_id/container/fs/read?path=/data/rev.txt", nil, env.handler.FSRead)
	if err != nil {
		t.Fatalf("FSRead returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, _ := resp["revision"].(string); got == "" {
		t.Fatalf("revision is empty in response %#v", resp)
	}
}

func TestFSWriteRejectsStaleExpectedRevision(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeSkillFile(t, "/data/rev.txt", "base")

	_, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/write", map[string]any{
		"path":             "/data/rev.txt",
		"content":          "mine",
		"expectedRevision": "sha256:stale",
	}, env.handler.FSWrite)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusConflict {
		t.Fatalf("expected conflict error, got %v", err)
	}
	assertLocalFile(t, env.localPath("/data/rev.txt"), "base")
}

func TestFSWriteAcceptsMatchingExpectedRevision(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeSkillFile(t, "/data/rev.txt", "base")

	readRec, err := env.callFileManager(t, http.MethodGet, "/bots/:bot_id/container/fs/read?path=/data/rev.txt", nil, env.handler.FSRead)
	if err != nil {
		t.Fatalf("FSRead returned error: %v", err)
	}
	var readResp map[string]any
	if err := json.Unmarshal(readRec.Body.Bytes(), &readResp); err != nil {
		t.Fatalf("decode read response: %v", err)
	}
	revision, _ := readResp["revision"].(string)
	if revision == "" {
		t.Fatalf("revision is empty in response %#v", readResp)
	}

	writeRec, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/write", map[string]any{
		"path":             "/data/rev.txt",
		"content":          "updated",
		"expectedRevision": revision,
	}, env.handler.FSWrite)
	if err != nil {
		t.Fatalf("FSWrite returned error: %v", err)
	}
	if writeRec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", writeRec.Code, writeRec.Body.String())
	}
	assertLocalFile(t, env.localPath("/data/rev.txt"), "updated")
}

func TestFSWriteRejectsEmptyExpectedRevision(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeSkillFile(t, "/data/rev.txt", "base")

	_, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/write", map[string]any{
		"path":             "/data/rev.txt",
		"content":          "mine",
		"expectedRevision": "",
	}, env.handler.FSWrite)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 bad request for empty expectedRevision, got %v", err)
	}
	// Buffer must not have been written under the ambiguous condition.
	assertLocalFile(t, env.localPath("/data/rev.txt"), "base")
}

func TestFSWriteOmittedExpectedRevisionWritesUnconditionally(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeSkillFile(t, "/data/rev.txt", "base")

	// Body without an expectedRevision field — the FE's "no known baseline"
	// path (e.g. stale read, force-save, deleted-restore). Must succeed.
	rec, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/write", map[string]any{
		"path":    "/data/rev.txt",
		"content": "mine",
	}, env.handler.FSWrite)
	if err != nil {
		t.Fatalf("FSWrite returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertLocalFile(t, env.localPath("/data/rev.txt"), "mine")
}

func TestFSArchiveMultiplePathsDedupesNestedSelection(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeSkillFile(t, "/data/project/main.go", "package main\n")
	env.writeSkillFile(t, "/data/project/docs/readme.md", "# docs\n")
	env.writeSkillFile(t, "/data/notes.txt", "notes")

	req := FSArchiveRequest{Paths: []string{"/data/project", "/data/project/docs/readme.md", "/data/notes.txt"}}
	rec, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/archive", req, env.handler.FSArchive)
	if err != nil {
		t.Fatalf("FSArchive returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	entries := readTarGzEntries(t, rec.Body.Bytes())
	assertTarEntry(t, entries, "project/main.go", "package main\n")
	assertTarEntry(t, entries, "project/docs/readme.md", "# docs\n")
	assertTarEntry(t, entries, "notes.txt", "notes")
	if _, ok := entries["readme.md"]; ok {
		t.Fatalf("nested selection was archived separately: %#v", entries)
	}
}

func TestFSExtractZipArchive(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeBinaryFile(t, "/data/bundle.zip", buildZipArchive(t, map[string]string{
		"src/main.go": "package main\n",
		"README.md":   "# bundle\n",
	}, []string{"empty/"}))

	rec, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/extract", FSExtractRequest{Path: "/data/bundle.zip"}, env.handler.FSExtract)
	if err != nil {
		t.Fatalf("FSExtract returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp FSExtractResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Destination != "/data/bundle" || resp.Files != 2 || resp.Directories != 1 {
		t.Fatalf("response = %+v", resp)
	}
	assertLocalFile(t, env.localPath("/data/bundle/src/main.go"), "package main\n")
	assertLocalFile(t, env.localPath("/data/bundle/README.md"), "# bundle\n")
	if info, err := os.Stat(env.localPath("/data/bundle/empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty dir not extracted: info=%v err=%v", info, err)
	}
}

func TestFSExtractTarGzArchive(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeBinaryFile(t, "/data/bundle.tgz", buildTarGzArchive(t, map[string]string{
		"app/package.json": "{}\n",
	}, []string{"app/logs/"}))

	rec, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/extract", FSExtractRequest{Path: "/data/bundle.tgz"}, env.handler.FSExtract)
	if err != nil {
		t.Fatalf("FSExtract returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertLocalFile(t, env.localPath("/data/bundle/app/package.json"), "{}\n")
	if info, err := os.Stat(env.localPath("/data/bundle/app/logs")); err != nil || !info.IsDir() {
		t.Fatalf("tar dir not extracted: info=%v err=%v", info, err)
	}
}

func TestFSExtractRejectsExistingDestination(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeBinaryFile(t, "/data/bundle.zip", buildZipArchive(t, map[string]string{"file.txt": "content"}, nil))
	if err := os.MkdirAll(env.localPath("/data/bundle"), 0o750); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}

	_, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/extract", FSExtractRequest{Path: "/data/bundle.zip"}, env.handler.FSExtract)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusConflict {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestFSExtractRejectsZipSlipEntry(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeBinaryFile(t, "/data/bundle.zip", buildZipArchive(t, map[string]string{"../escape.txt": "nope"}, nil))

	_, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/extract", FSExtractRequest{Path: "/data/bundle.zip"}, env.handler.FSExtract)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(env.dataRoot, "escape.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("zip-slip entry escaped destination: %v", statErr)
	}
}

func (e *skillsTestEnv) callFileManager(t *testing.T, method, routePath string, body any, fn func(echo.Context) error) (*httptest.ResponseRecorder, error) {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(data)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, routePath, bodyReader)
	if body != nil {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	ctx.SetPath(strings.Split(routePath, "?")[0])
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(e.botID)
	ctx.Set("user", &jwt.Token{
		Valid:  true,
		Claims: jwt.MapClaims{"user_id": e.userID, "sub": e.userID},
	})
	return rec, fn(ctx)
}

func (e *skillsTestEnv) writeBinaryFile(t *testing.T, containerPath string, data []byte) {
	t.Helper()
	local := e.localPath(containerPath)
	if err := os.MkdirAll(filepath.Dir(local), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(local), err)
	}
	if err := os.WriteFile(local, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", local, err)
	}
}

func readTarGzEntries(t *testing.T, data []byte) map[string]string {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer func() { _ = gr.Close() }()
	tr := tar.NewReader(gr)
	entries := make(map[string]string)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		if header.Typeflag == tar.TypeDir {
			entries[header.Name] = ""
			continue
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read entry %s: %v", header.Name, err)
		}
		entries[header.Name] = string(content)
	}
	return entries
}

func assertTarEntry(t *testing.T, entries map[string]string, name, want string) {
	t.Helper()
	got, ok := entries[name]
	if !ok {
		t.Fatalf("missing tar entry %q in %#v", name, entries)
	}
	if got != want {
		t.Fatalf("tar entry %q = %q, want %q", name, got, want)
	}
}

func assertLocalFile(t *testing.T, localPath, want string) {
	t.Helper()
	//nolint:gosec // test-only temp workspace path
	data, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read %s: %v", localPath, err)
	}
	if got := string(data); got != want {
		t.Fatalf("%s = %q, want %q", localPath, got, want)
	}
}

func buildZipArchive(t *testing.T, files map[string]string, dirs []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, dir := range dirs {
		if _, err := zw.Create(strings.TrimRight(dir, "/") + "/"); err != nil {
			t.Fatalf("zip create dir: %v", err)
		}
	}
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create file: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write file: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func buildTarGzArchive(t *testing.T, files map[string]string, dirs []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, dir := range dirs {
		if err := tw.WriteHeader(&tar.Header{Name: strings.TrimRight(dir, "/") + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatalf("tar write dir: %v", err)
		}
	}
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatalf("tar write file: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar file content: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}
