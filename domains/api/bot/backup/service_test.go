package backup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/memohai/memoh/domains/api/bot"
)

func TestReadZipEntriesRejectsZipSlip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../manifest.json")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := w.Write([]byte(`{}`)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := readZipEntries(buf.Bytes()); err == nil {
		t.Fatal("readZipEntries() accepted zip slip path")
	}
}

func TestNormalizeExportOptionsDefaultsAllSections(t *testing.T) {
	opts := NormalizeExportOptions(ExportOptions{})
	if len(opts.Sections) != len(AllExportSections) {
		t.Fatalf("default export should include all sections, got %v", opts.Sections)
	}
	opts = NormalizeExportOptions(ExportOptions{Sections: []Section{SectionHistory}})
	if opts.wants(SectionWorkspace) {
		t.Fatal("explicit non-default scope should not include workspace")
	}
	if !opts.wants(SectionHistory) {
		t.Fatal("explicit history scope should include history")
	}
	if !opts.wants(SectionProfile) {
		t.Fatal("profile is always exported")
	}
}

func TestWriteJSONPreservesSensitiveValues(t *testing.T) {
	var buf bytes.Buffer
	manifest := Manifest{}
	writer := &zipBackupWriter{
		zw:       zip.NewWriter(&buf),
		manifest: &manifest,
		checksum: map[string]string{},
	}
	value := []map[string]any{{
		"name": "provider",
		"config": map[string]any{
			"api_key":  "secret-value",
			"base_url": "https://example.com",
		},
	}}
	if err := writer.writeJSON("dependencies/providers.json", "providers", value, ExportOptions{}); err != nil {
		t.Fatalf("writeJSON() error = %v", err)
	}
	if err := writer.zw.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	entries, err := readZipEntries(buf.Bytes())
	if err != nil {
		t.Fatalf("readZipEntries() error = %v", err)
	}
	raw := string(entries["dependencies/providers.json"].data)
	if !strings.Contains(raw, "secret-value") {
		t.Fatalf("sensitive value was not preserved: %s", raw)
	}
}

func TestScrubImportedProfileACPSecrets(t *testing.T) {
	for _, tc := range []struct {
		name         string
		warnings     []string
		wantWarnings []string
	}{
		{
			name:         "adds warning",
			wantWarnings: []string{acpManagedSecretsWarning},
		},
		{
			name:         "dedupes warning",
			warnings:     []string{acpManagedSecretsWarning},
			wantWarnings: []string{acpManagedSecretsWarning},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &importState{warnings: append([]string(nil), tc.warnings...)}
			profile := bot.Bot{
				Metadata: map[string]any{
					"acp": map[string]any{
						"agents": map[string]any{
							"hermes": map[string]any{
								"enabled":    true,
								"setup_mode": "api_key",
								"managed": map[string]any{
									"provider": "openrouter",
									"model":    "nousresearch/hermes",
									"api_key":  "secret-value",
								},
							},
						},
					},
				},
			}

			scrubbed := scrubImportedProfileACPSecrets(profile, state)
			raw, err := json.Marshal(scrubbed.Metadata)
			if err != nil {
				t.Fatalf("marshal metadata: %v", err)
			}
			if strings.Contains(string(raw), "secret-value") || strings.Contains(string(raw), `"api_key":`) {
				t.Fatalf("imported profile kept ACP secret: %s", raw)
			}
			if len(state.warnings) != len(tc.wantWarnings) {
				t.Fatalf("warnings = %v, want %v", state.warnings, tc.wantWarnings)
			}
			for i := range tc.wantWarnings {
				if state.warnings[i] != tc.wantWarnings[i] {
					t.Fatalf("warnings = %v, want %v", state.warnings, tc.wantWarnings)
				}
			}
		})
	}
}

func TestWorkspaceStoredVerbatimAsTarGz(t *testing.T) {
	// Build a workspace tar.gz as the container would return it.
	var workspace bytes.Buffer
	gw := gzip.NewWriter(&workspace)
	tw := tar.NewWriter(gw)
	body := []byte("hello workspace")
	if err := tw.WriteHeader(&tar.Header{Name: "notes/readme.txt", Typeflag: tar.TypeReg, Mode: 0o640, Size: int64(len(body))}); err != nil {
		t.Fatalf("WriteHeader(file) error = %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("Write(file) error = %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close() error = %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	original := workspace.Bytes()

	// Store it verbatim as the single workspace entry, as writeWorkspace does.
	var backup bytes.Buffer
	manifest := Manifest{}
	writer := &zipBackupWriter{
		zw:       zip.NewWriter(&backup),
		manifest: &manifest,
		checksum: map[string]string{},
	}
	if err := writer.writeStream(workspaceArchivePath, bytes.NewReader(original), 0o640, time.Time{}, zip.Store); err != nil {
		t.Fatalf("writeStream() error = %v", err)
	}
	if err := writer.zw.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	entries, err := readZipEntries(backup.Bytes())
	if err != nil {
		t.Fatalf("readZipEntries() error = %v", err)
	}
	// The workspace must be a single nested tar.gz, not exploded files.
	if !hasEntry(entries, workspaceArchivePath) {
		t.Fatalf("workspace archive entry missing; entries=%v", entries)
	}
	if !hasWorkspaceEntries(entries) {
		t.Fatal("hasWorkspaceEntries should be true")
	}

	// The already-gzipped blob must be stored (not deflated again) to avoid
	// pointless double compression.
	if method := workspaceEntryMethod(t, backup.Bytes()); method != zip.Store {
		t.Fatalf("workspace entry method = %d, want zip.Store (%d)", method, zip.Store)
	}

	// The blob round-trips byte-for-byte (no re-packing).
	got, err := workspaceArchive(entries)
	if err != nil {
		t.Fatalf("workspaceArchive() error = %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatal("workspace archive was not preserved verbatim")
	}

	// File listing reads names straight from the tar.gz headers.
	names := workspaceFileList(entries, sectionItemLimit)
	if len(names) != 1 || names[0] != "notes/readme.txt" {
		t.Fatalf("workspaceFileList = %v, want [notes/readme.txt]", names)
	}
	if n := countWorkspaceFiles(entries); n != 1 {
		t.Fatalf("countWorkspaceFiles = %d, want 1", n)
	}

	plain, err := readTarGzFile(got, "notes/readme.txt")
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	if string(plain) != string(body) {
		t.Fatalf("workspace file = %q, want %q", plain, body)
	}
}

// workspaceEntryMethod returns the zip compression method used for the
// workspace archive entry within a backup zip.
func workspaceEntryMethod(t *testing.T, raw []byte) uint16 {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip.NewReader() error = %v", err)
	}
	for _, file := range zr.File {
		if file.Name == workspaceArchivePath {
			return file.Method
		}
	}
	t.Fatalf("workspace entry %q not found in zip", workspaceArchivePath)
	return 0
}

func readTarGzFile(raw []byte, name string) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gr.Close() }()
	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		if err != nil {
			return nil, err
		}
		if header.Name != name {
			continue
		}
		return io.ReadAll(tr)
	}
}

func TestIsWorkspaceRestoreRetryable(t *testing.T) {
	retryable := []string{
		"get workspace runtime: not found",
		"No such container: workspace-123",
		"workspace is not reachable: connection refused",
	}
	for _, msg := range retryable {
		if !isWorkspaceRestoreRetryable(errString(msg)) {
			t.Fatalf("expected retryable error: %s", msg)
		}
	}
	if isWorkspaceRestoreRetryable(io.ErrUnexpectedEOF) {
		t.Fatal("unexpected retryable generic error")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
