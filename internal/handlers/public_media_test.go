package handlers

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/channel/publicmedia"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/storage/providers/localfs"
)

const testPublicMediaSecret = "public-media-test-secret" //nolint:gosec // G101: test-only fixed secret.

func TestPublicMediaHandlerServesOriginalImage(t *testing.T) {
	t.Parallel()

	e, asset := newPublicMediaTestServer(t)
	req := httptest.NewRequest(http.MethodGet, signedPublicMediaPath(publicmedia.OriginalPath("line", "bot-1", asset.ContentHash, "image.png")), nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(echo.HeaderContentType); got != "image/png" {
		t.Fatalf("content type = %q, want image/png", got)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("expected original image body")
	}
}

func TestPublicMediaHandlerServesPreviewImage(t *testing.T) {
	t.Parallel()

	e, asset := newPublicMediaTestServer(t)
	req := httptest.NewRequest(http.MethodGet, signedPublicMediaPath(publicmedia.PreviewPath("line", "bot-1", asset.ContentHash)), nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(echo.HeaderContentType); got != "image/jpeg" {
		t.Fatalf("content type = %q, want image/jpeg", got)
	}
	if int64(rec.Body.Len()) > publicmedia.PreviewMaxBytes {
		t.Fatalf("preview size = %d, want <= %d", rec.Body.Len(), publicmedia.PreviewMaxBytes)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("expected preview image body")
	}
}

func TestPublicMediaHandlerServesOtherChannelSignedPath(t *testing.T) {
	t.Parallel()

	e, asset := newPublicMediaTestServer(t)
	req := httptest.NewRequest(http.MethodGet, signedPublicMediaPath(publicmedia.PreviewPath("telegram", "bot-1", asset.ContentHash)), nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestPublicMediaHandlerRejectsOversizedOriginalImage(t *testing.T) {
	t.Parallel()

	service := media.NewService(slog.Default(), localfs.New(filepath.Join(t.TempDir(), "media")))
	payload := bytes.Repeat([]byte{0xff}, int(publicmedia.OriginalMaxBytes)+1)
	asset, err := service.Ingest(context.Background(), media.IngestInput{
		BotID:       "bot-1",
		Mime:        "image/png",
		Reader:      bytes.NewReader(payload),
		OriginalExt: ".png",
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	e := echo.New()
	NewPublicMediaHandler(slog.Default(), service, testPublicMediaSecret).Register(e)

	req := httptest.NewRequest(http.MethodGet, signedPublicMediaPath(publicmedia.OriginalPath("line", "bot-1", asset.ContentHash, "image.png")), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestPublicMediaHandlerRejectsOversizedPreviewDimensions(t *testing.T) {
	t.Parallel()

	service := media.NewService(slog.Default(), localfs.New(filepath.Join(t.TempDir(), "media")))
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, publicmedia.PreviewMaxDimension+1, 1))
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	asset, err := service.Ingest(context.Background(), media.IngestInput{
		BotID:       "bot-1",
		Mime:        "image/png",
		Reader:      bytes.NewReader(buf.Bytes()),
		OriginalExt: ".png",
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	e := echo.New()
	NewPublicMediaHandler(slog.Default(), service, testPublicMediaSecret).Register(e)

	req := httptest.NewRequest(http.MethodGet, signedPublicMediaPath(publicmedia.PreviewPath("line", "bot-1", asset.ContentHash)), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestPublicMediaHandlerRejectsUnsignedURL(t *testing.T) {
	t.Parallel()

	e, asset := newPublicMediaTestServer(t)
	req := httptest.NewRequest(http.MethodGet, publicmedia.PreviewPath("line", "bot-1", asset.ContentHash), nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestPublicMediaHandlerRejectsExpiredURL(t *testing.T) {
	t.Parallel()

	e, asset := newPublicMediaTestServer(t)
	signer := publicmedia.NewSigner(testPublicMediaSecret, time.Second)
	path, ok := signer.SignPath(publicmedia.PreviewPath("line", "bot-1", asset.ContentHash), time.Now().Add(-time.Hour))
	if !ok {
		t.Fatal("failed to sign public media path")
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestConfiguredPublicMediaHandlerOnlyRegistersWithPublicBase(t *testing.T) {
	t.Parallel()

	service := media.NewService(slog.Default(), localfs.New(filepath.Join(t.TempDir(), "media")))
	e := echo.New()
	NewConfiguredPublicMediaHandler(slog.Default(), config.Config{}, service).Register(e)

	req := httptest.NewRequest(http.MethodGet, "/channels/line/public/media/bot-1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/preview.jpg", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when public base is not configured", rec.Code)
	}
}

func newPublicMediaTestServer(t *testing.T) (*echo.Echo, media.Asset) {
	t.Helper()
	service := media.NewService(slog.Default(), localfs.New(filepath.Join(t.TempDir(), "media")))
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 7), G: uint8(y * 9), B: 120, A: 255})
		}
	}
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	asset, err := service.Ingest(context.Background(), media.IngestInput{
		BotID:       "bot-1",
		Mime:        "image/png",
		Reader:      bytes.NewReader(buf.Bytes()),
		OriginalExt: ".png",
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	e := echo.New()
	NewPublicMediaHandler(slog.Default(), service, testPublicMediaSecret).Register(e)
	return e, asset
}

func signedPublicMediaPath(path string) string {
	signer := publicmedia.NewSigner(testPublicMediaSecret, publicmedia.SignedURLTTL)
	signed, ok := signer.SignPath(path, time.Now().UTC())
	if !ok {
		panic("failed to sign public media test path")
	}
	return signed
}
