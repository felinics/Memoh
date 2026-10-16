package botbackup

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/botbackup/secure"
	"github.com/felinics/memoh/internal/bots"
)

func zipWithManifest(t *testing.T, manifest string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(ManifestPath)
	if err != nil {
		t.Fatalf("create manifest: %v", err)
	}
	if _, err := w.Write([]byte(manifest)); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestImportAndPreviewMarkUnreadableBundlesInvalid(t *testing.T) {
	t.Parallel()
	cases := map[string][]byte{
		"not a zip":          []byte("not a zip"),
		"manifest not json":  zipWithManifest(t, "{"),
		"unsupported schema": zipWithManifest(t, `{"schema_version":99}`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc := &Service{}
			if _, err := svc.Import(context.Background(), "user", raw, ImportOptions{}, ""); !errors.Is(err, ErrInvalidBundle) {
				t.Fatalf("Import() error = %v, want ErrInvalidBundle", err)
			}
		})
	}
	t.Run("preview not a zip", func(t *testing.T) {
		t.Parallel()
		svc := &Service{}
		if _, err := svc.Preview(context.Background(), cases["not a zip"], ImportOptions{}, ""); !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Preview() error = %v, want ErrInvalidBundle", err)
		}
	})
}

func TestImportEncryptedBundleErrors(t *testing.T) {
	t.Parallel()
	var enc bytes.Buffer
	if err := secure.Encrypt(&enc, bytes.NewReader(zipWithManifest(t, `{"schema_version":1}`)), "pw"); err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	svc := &Service{}
	if _, err := svc.Import(context.Background(), "user", enc.Bytes(), ImportOptions{}, "wrong"); !errors.Is(err, secure.ErrAuth) || errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("Import(wrong passphrase) error = %v, want ErrAuth only", err)
	}
	truncated := enc.Bytes()[:enc.Len()-8]
	if _, err := svc.Import(context.Background(), "user", truncated, ImportOptions{}, "pw"); !errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("Import(truncated) error = %v, want ErrInvalidBundle", err)
	}
}

func TestRestoreBotOverwriteWithoutTargetIsATargetError(t *testing.T) {
	t.Parallel()
	svc := &Service{}
	_, _, err := svc.restoreBot(context.Background(), "user", bots.Bot{}, ImportOptions{Mode: ImportModeOverwrite})
	if !errors.Is(err, ErrTargetBotRequired) {
		t.Fatalf("restoreBot() error = %v, want ErrTargetBotRequired", err)
	}
}
