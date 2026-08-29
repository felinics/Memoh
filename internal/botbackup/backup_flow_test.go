package botbackup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/botbackup/secure"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/mcp"
)

// buildSampleBundle assembles a complete plaintext .memoh.zip the way Export
// would, covering every section, so the preview/import readers can be exercised
// without a database.
func buildSampleBundle(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	manifest := Manifest{SchemaVersion: BackupSchemaVersion, SourceBotID: "src-bot", SourceBotName: "Src Bot"}
	w := &zipBackupWriter{zw: zip.NewWriter(&buf), manifest: &manifest, checksum: map[string]string{}}

	write := func(path, kind string, value any) {
		if err := w.writeJSON(path, kind, value, ExportOptions{}); err != nil {
			t.Fatalf("writeJSON(%s) error = %v", path, err)
		}
	}
	write("bot/profile.json", "profile", bots.Bot{ID: "src-bot", DisplayName: "Src Bot", Timezone: "UTC", IsActive: true})
	write("bot/settings.json", "settings", map[string]any{"language": "en", "reasoning_enabled": true})
	write("dependencies/models.json", "models", []map[string]any{{"name": "m1", "model_id": "gpt"}, {"name": "m2", "model_id": "claude"}})
	write("bot/acl_rules.json", "acl", []map[string]any{{"description": "rule", "subject_channel_type": "telegram"}})
	write("bot/channel_configs.json", "channels", []map[string]any{{"channel_type": "telegram"}, {"channel_type": "discord"}})
	write("bot/mcp_connections.json", "mcp", []map[string]any{{"name": "srv"}})
	write("bot/schedules.json", "schedules", []map[string]any{{"name": "job"}})
	write("bot/email_bindings.json", "email", []map[string]any{{"email_address": "a@b.com"}})
	write("history/sessions.json", "history", []map[string]any{{"title": "chat 1", "type": "conversation"}})
	write("history/messages.json", "history", []map[string]any{{"id": "1"}, {"id": "2"}, {"id": "3"}})
	write("assets/message_assets.json", "assets", []map[string]any{{"name": "image.png"}})

	if err := w.writeStream(workspaceArchivePath, bytes.NewReader(sampleTarGz(t)), 0o640, time.Time{}, zip.Store); err != nil {
		t.Fatalf("writeStream(workspace) error = %v", err)
	}
	if err := w.writeManifest(); err != nil {
		t.Fatalf("writeManifest() error = %v", err)
	}
	if err := w.zw.Close(); err != nil {
		t.Fatalf("zip Close() error = %v", err)
	}
	return buf.Bytes()
}

func sampleTarGz(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	body := []byte("workspace file")
	if err := tw.WriteHeader(&tar.Header{Name: "data/notes.txt", Typeflag: tar.TypeReg, Mode: 0o640, Size: int64(len(body))}); err != nil {
		t.Fatalf("tar WriteHeader() error = %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("tar Write() error = %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close() error = %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	return buf.Bytes()
}

func sectionCount(sections []SectionSummary, key Section) int {
	for _, s := range sections {
		if s.Key == key {
			return s.Count
		}
	}
	return -2 // not found sentinel distinct from the -1 "unknown" count
}

func TestPreviewPlainBundleRoundTrip(t *testing.T) {
	svc := &Service{}
	preview, err := svc.Preview(context.Background(), buildSampleBundle(t), ImportOptions{}, "")
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if preview.Encrypted {
		t.Fatal("plain bundle reported as encrypted")
	}
	if preview.Profile == nil || preview.Profile.DisplayName != "Src Bot" {
		t.Fatalf("profile preview = %+v, want display name 'Src Bot'", preview.Profile)
	}
	if got := sectionCount(preview.Sections, SectionHistory); got != 3 {
		t.Fatalf("history count = %d, want 3", got)
	}
	if got := sectionCount(preview.Sections, SectionChannels); got != 2 {
		t.Fatalf("channels count = %d, want 2", got)
	}
	if got := sectionCount(preview.Sections, SectionWorkspace); got != 1 {
		t.Fatalf("workspace count = %d, want 1", got)
	}
	if preview.RestorePlan.Mode != ImportModeCreate {
		t.Fatalf("restore mode = %q, want create", preview.RestorePlan.Mode)
	}
	if !preview.RestorePlan.WillCreateBot {
		t.Fatal("create-mode preview should set WillCreateBot")
	}
	if !preview.RestorePlan.WillRestoreWorkspace {
		t.Fatal("bundle with workspace should set WillRestoreWorkspace")
	}
}

func TestPreviewEncryptedBundle(t *testing.T) {
	var enc bytes.Buffer
	if err := secure.Encrypt(&enc, bytes.NewReader(buildSampleBundle(t)), "pw"); err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	svc := &Service{}

	// Locked: no passphrase supplied.
	locked, err := svc.Preview(context.Background(), enc.Bytes(), ImportOptions{}, "")
	if err != nil {
		t.Fatalf("Preview(locked) error = %v", err)
	}
	if !locked.Encrypted || !locked.RequiresPassphrase {
		t.Fatalf("locked preview = %+v, want Encrypted && RequiresPassphrase", locked)
	}
	if len(locked.Sections) != 0 {
		t.Fatal("locked preview should not expose sections")
	}

	// Wrong passphrase: still locked, with a conflict hint.
	wrong, err := svc.Preview(context.Background(), enc.Bytes(), ImportOptions{}, "nope")
	if err != nil {
		t.Fatalf("Preview(wrong) error = %v", err)
	}
	if !wrong.RequiresPassphrase || len(wrong.Conflicts) == 0 {
		t.Fatalf("wrong passphrase preview = %+v, want RequiresPassphrase && a conflict", wrong)
	}

	// Correct passphrase: fully readable.
	ok, err := svc.Preview(context.Background(), enc.Bytes(), ImportOptions{}, "pw")
	if err != nil {
		t.Fatalf("Preview(correct) error = %v", err)
	}
	if !ok.Encrypted || ok.RequiresPassphrase {
		t.Fatalf("correct preview = %+v, want Encrypted && !RequiresPassphrase", ok)
	}
	if got := sectionCount(ok.Sections, SectionHistory); got != 3 {
		t.Fatalf("decrypted history count = %d, want 3", got)
	}
}

func TestImportEncryptedWithoutPassphraseErrors(t *testing.T) {
	var enc bytes.Buffer
	if err := secure.Encrypt(&enc, bytes.NewReader(buildSampleBundle(t)), "pw"); err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	svc := &Service{}
	if _, err := svc.Import(context.Background(), "user", enc.Bytes(), ImportOptions{}, ""); err == nil ||
		!strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("Import(no passphrase) error = %v, want an 'encrypted' error", err)
	}
}

func TestDecodeBundle(t *testing.T) {
	plain := []byte("PK\x03\x04 plain zip bytes")
	out, encrypted, err := decodeBundle(plain, "")
	if err != nil || encrypted || !bytes.Equal(out, plain) {
		t.Fatalf("decodeBundle(plain) = (%q, %v, %v)", out, encrypted, err)
	}

	var enc bytes.Buffer
	if err := secure.Encrypt(&enc, bytes.NewReader(plain), "pw"); err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	out, encrypted, err = decodeBundle(enc.Bytes(), "pw")
	if err != nil || !encrypted || !bytes.Equal(out, plain) {
		t.Fatalf("decodeBundle(encrypted, pw) = (%q, %v, %v)", out, encrypted, err)
	}
	if _, encrypted, err = decodeBundle(enc.Bytes(), ""); !encrypted || !errors.Is(err, secure.ErrPassphraseRequired) {
		t.Fatalf("decodeBundle(encrypted, empty) encrypted=%v err=%v, want true / ErrPassphraseRequired", encrypted, err)
	}
}

func TestImportStateItemErr(t *testing.T) {
	// Create mode: an item failure is fatal so the caller can roll back.
	create := &importState{createMode: true}
	if err := create.itemErr("acl rule", errString("boom")); err == nil {
		t.Fatal("create mode itemErr should be fatal")
	}
	if len(create.warnings) != 0 {
		t.Fatal("create mode should not record a warning when aborting")
	}

	// Overwrite mode: the same failure degrades to a warning and continues.
	overwrite := &importState{createMode: false}
	if err := overwrite.itemErr("acl rule", errString("boom")); err != nil {
		t.Fatalf("overwrite mode itemErr should not be fatal, got %v", err)
	}
	if len(overwrite.warnings) != 1 || !strings.Contains(overwrite.warnings[0], "acl rule") {
		t.Fatalf("overwrite warnings = %v, want one mentioning 'acl rule'", overwrite.warnings)
	}
}

func TestRestoredSessionDescriptorRejectsSystemACPRuntime(t *testing.T) {
	// Schedule sessions may run through an ACP agent, while retired automation
	// sessions are filtered before descriptor restoration.
	if _, _, _, err := restoredSessionDescriptor("schedule", "schedule", "acp_agent"); err != nil {
		t.Fatalf("restoredSessionDescriptor(schedule/acp_agent) error = %v, want nil", err)
	}
	if !isRetiredAutomationSession("heartbeat", "") || !isRetiredAutomationSession("", "heartbeat") {
		t.Fatal("retired automation descriptors must be recognized before restore")
	}
	if isRetiredAutomationSession("schedule", "schedule") {
		t.Fatal("schedule descriptor must not be classified as retired automation")
	}
}

func TestMCPRequestFromConnection(t *testing.T) {
	stdio := mcpRequestFromConnection(mcp.Connection{
		Name:     "local",
		Type:     "stdio",
		Active:   true,
		AuthType: "none",
		Config: map[string]any{
			"command": "node",
			"args":    []any{"server.js", "--port"},
			"env":     map[string]any{"TOKEN": "abc"},
		},
	})
	if stdio.Name != "local" || stdio.Command != "node" {
		t.Fatalf("stdio request = %+v", stdio)
	}
	if stdio.Active == nil || !*stdio.Active {
		t.Fatal("stdio request should be active")
	}
	if len(stdio.Args) != 2 || stdio.Args[0] != "server.js" {
		t.Fatalf("stdio args = %v", stdio.Args)
	}
	if stdio.Env["TOKEN"] != "abc" {
		t.Fatalf("stdio env = %v", stdio.Env)
	}

	sse := mcpRequestFromConnection(mcp.Connection{
		Name: "remote",
		Type: "sse",
		Config: map[string]any{
			"url":     "https://example.com/sse",
			"headers": map[string]any{"Authorization": "Bearer x"},
		},
	})
	if sse.Transport != "sse" || sse.URL != "https://example.com/sse" {
		t.Fatalf("sse request = %+v", sse)
	}
	if sse.Headers["Authorization"] != "Bearer x" {
		t.Fatalf("sse headers = %v", sse.Headers)
	}
}
