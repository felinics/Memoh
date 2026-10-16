package application

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/models"
)

// rasterPNG returns real PNG bytes. Image input is now decided by the bytes, so
// fixtures have to be actual images rather than a label and a placeholder.
func rasterPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	// Noise, not a gradient: the fixture has to survive PNG compression so it
	// stays larger than the sniff window the validator reads.
	state := uint32(0x9E3779B9)
	for y := range 64 {
		for x := range 64 {
			state = state*1664525 + 1013904223
			//nolint:gosec // G115: deliberate byte extraction from the noise state.
			img.Set(x, y, color.RGBA{R: uint8(state >> 24), G: uint8(state >> 16), B: uint8(state >> 8), A: 0xFF})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if buf.Len() <= 512 {
		t.Fatalf("fixture must exceed the 512-byte sniff window, got %d bytes", buf.Len())
	}
	return buf.Bytes()
}

// webmSticker mimics a Telegram video sticker: EBML container bytes that the
// media store happily kept under an image label.
func webmSticker() []byte {
	return append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x11}, 600)...)
}

// tgsSticker mimics a Telegram animated sticker: gzipped Lottie JSON.
func tgsSticker(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(`{"v":"5.5.2","w":512,"h":512,"ip":0,"op":60,"fr":30,"layers":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func imageInputService(t *testing.T, assets map[string][]byte) *Service {
	t.Helper()
	return &Service{
		logger: slog.Default(),
		assetLoader: &fakeGatewayAssetLoader{
			openFn: func(_ context.Context, _, contentHash string) (io.ReadCloser, string, error) {
				data, ok := assets[contentHash]
				if !ok {
					t.Fatalf("unexpected content hash %q", contentHash)
				}
				return io.NopCloser(bytes.NewReader(data)), "image/png", nil
			},
			accessPathFn: func(_ context.Context, _, contentHash string) (string, error) {
				return "/data/.memoh/media/" + contentHash, nil
			},
		},
	}
}

func inlineImageParts(ctx context.Context, s *Service, botID string, refs []timeline.ImageAttachmentRef) []sdk.ImagePart {
	return slices.Concat(s.InlineImageAttachments(ctx, botID, refs)...)
}

// The reported failure: an unusable attachment sitting in pending discussion
// context made every later turn fail image parsing. It must be skipped without
// taking the other images of the turn down with it.
func TestInlineImageAttachmentsRendersAnimationsAndSkipsUnparsableBytes(t *testing.T) {
	s := imageInputService(t, map[string][]byte{
		"video-sticker":    webmSticker(),
		"animated-sticker": movingStickerTGS(t),
		"photo":            rasterPNG(t),
	})

	parts := inlineImageParts(context.Background(), s, "bot-1", []timeline.ImageAttachmentRef{
		// WebM has no renderer here, so it stays out of the vision lane.
		{ContentHash: "video-sticker", Mime: "video/webm"},
		// TGS is rendered into frames, whatever the stored label claims.
		{ContentHash: "animated-sticker", Mime: "application/x-gzip"},
		{ContentHash: "photo", Mime: "image/png"},
	})

	if len(parts) != animationFrameCount+1 {
		t.Fatalf("InlineImageAttachments() = %d parts, want %d sticker frames plus the photo",
			len(parts), animationFrameCount)
	}
	for i, part := range parts {
		if part.MediaType != "image/png" || !strings.HasPrefix(part.Image, "data:image/png;base64,") {
			t.Fatalf("part %d = %#v, want a PNG data URL", i, part)
		}
	}
	if parts[len(parts)-1].Image != dataURL("image/png", rasterPNG(t)) {
		t.Fatal("the ordinary photo was not passed through untouched")
	}
}

// Historical records label sticker bytes as image/png. The label must not be
// enough to get them into a vision request.
func TestInlineImageAttachmentsIgnoresMislabeledStickerBytes(t *testing.T) {
	s := imageInputService(t, map[string][]byte{"legacy": webmSticker()})
	parts := inlineImageParts(context.Background(), s, "bot-1", []timeline.ImageAttachmentRef{
		{ContentHash: "legacy", Mime: "image/png"},
	})
	if len(parts) != 0 {
		t.Fatalf("InlineImageAttachments() = %#v, want nothing for WebM bytes labelled image/png", parts)
	}
}

func TestInlineImageAttachmentsLoadsEachAssetOnce(t *testing.T) {
	opens := 0
	s := &Service{
		logger: slog.Default(),
		assetLoader: &fakeGatewayAssetLoader{
			openFn: func(context.Context, string, string) (io.ReadCloser, string, error) {
				opens++
				return io.NopCloser(bytes.NewReader(rasterPNG(t))), "image/png", nil
			},
		},
	}
	refs := []timeline.ImageAttachmentRef{
		{ContentHash: "photo", Mime: "image/png"},
		{ContentHash: "photo", Mime: "image/png"},
	}
	if parts := inlineImageParts(context.Background(), s, "bot-1", refs); len(parts) != 1 {
		t.Fatalf("InlineImageAttachments() = %d parts, want the repeated reference collapsed", len(parts))
	}
	if opens != 1 {
		t.Fatalf("asset opened %d times, want 1", opens)
	}
}

func TestInlineInjectAttachmentsRendersAnimations(t *testing.T) {
	s := imageInputService(t, map[string][]byte{
		"animated-sticker": movingStickerTGS(t),
		"video-sticker":    webmSticker(),
	})
	parts := s.inlineInjectAttachments(context.Background(), "bot-1", []ChatAttachment{
		{Type: "image", ContentHash: "animated-sticker", Mime: "image/webp"},
		{Type: "image", ContentHash: "video-sticker", Mime: "image/png"},
	})
	if len(parts) != animationFrameCount {
		t.Fatalf("inlineInjectAttachments() = %d parts, want %d frames from the animation and nothing from the WebM",
			len(parts), animationFrameCount)
	}
}

// Ordinary chat already refused these bytes at the capability router. It must
// keep doing so through the file lane, never as a vision part.
func TestGatewayStickerBytesRouteToFileFallback(t *testing.T) {
	s := imageInputService(t, map[string][]byte{"video-sticker": webmSticker()})
	req := ChatRequest{
		BotID:       "bot-1",
		Attachments: []ChatAttachment{{Type: "image", ContentHash: "video-sticker", Mime: "image/png"}},
	}
	model := models.GetResponse{Model: models.Model{Config: models.ModelConfig{
		Compatibilities: []string{models.CompatVision},
	}}}

	merged := s.routeAndMergeAttachments(context.Background(), model, req)
	if len(merged) != 1 {
		t.Fatalf("routeAndMergeAttachments() = %d attachments, want 1", len(merged))
	}
	item, ok := merged[0].(gatewayAttachment)
	if !ok || item.Type != "file" || item.Transport != gatewayTransportToolFileRef {
		t.Fatalf("routed attachment = %#v, want a file reference", merged[0])
	}
	if item.Payload != "/data/.memoh/media/video-sticker" {
		t.Fatalf("file reference = %q, want the stored original", item.Payload)
	}
	if parts := extractNativeImageParts(merged); len(parts) != 0 {
		t.Fatalf("extractNativeImageParts() = %#v, want no vision input", parts)
	}
}

// An upload is bytes too: a renamed video must not reach the model just because
// the browser announced it as a PNG.
func TestGatewayInlineDataURLWithNonRasterBytesIsWithheld(t *testing.T) {
	s := &Service{logger: slog.Default()}
	item := gatewayAttachment{
		Type:      "image",
		Mime:      "image/png",
		Transport: gatewayTransportInlineDataURL,
		Payload:   dataURL("image/png", webmSticker()),
	}
	got := s.inlineImageAttachmentAssetIfNeeded(context.Background(), "bot-1", item)
	if isNativeImageAttachment(got) {
		t.Fatalf("attachment = %#v, want it demoted out of the vision lane", got)
	}
}

// A wrong subtype is a provider error of its own, and the correction has to
// reach the data URL: the payload and the MIME field travel to different
// consumers from here on.
func TestGatewayInlineDataURLRestampsMislabeledSubtype(t *testing.T) {
	s := &Service{logger: slog.Default()}
	data := rasterPNG(t)
	item := gatewayAttachment{
		Type:      "image",
		Mime:      "image/jpeg",
		Transport: gatewayTransportInlineDataURL,
		Payload:   dataURL("image/jpeg", data),
	}
	got := s.inlineImageAttachmentAssetIfNeeded(context.Background(), "bot-1", item)
	if got.Mime != "image/png" || got.Payload != dataURL("image/png", data) {
		t.Fatalf("attachment = %#v, want both the MIME and the data URL corrected to image/png", got)
	}
	parts := extractNativeImageParts([]any{got})
	if len(parts) != 1 || parts[0].MediaType != "image/png" ||
		!strings.HasPrefix(parts[0].Image, "data:image/png;base64,") {
		t.Fatalf("image part = %#v, want a consistent image/png", parts)
	}
	image, err := runtimePromptImageFromDataURL(got.Payload, got.Mime)
	if err != nil || image.MimeType != "image/png" {
		t.Fatalf("External Agent image = %#v (err=%v), want image/png", image, err)
	}
}

// dripReader returns one byte per call: a legal Reader, and the shape a short
// read takes when the media store is not a plain file.
type dripReader struct{ data []byte }

func (d *dripReader) Read(p []byte) (int, error) {
	if len(d.data) == 0 {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = d.data[0]
	d.data = d.data[1:]
	return 1, nil
}

func TestStoredImageSurvivesShortReads(t *testing.T) {
	data := rasterPNG(t)
	s := &Service{
		logger: slog.Default(),
		assetLoader: &fakeGatewayAssetLoader{
			openFn: func(context.Context, string, string) (io.ReadCloser, string, error) {
				return io.NopCloser(&dripReader{data: bytes.Clone(data)}), "image/png", nil
			},
		},
	}
	parts := inlineImageParts(context.Background(), s, "bot-1", []timeline.ImageAttachmentRef{
		{ContentHash: "photo", Mime: "image/png"},
	})
	if len(parts) != 1 || parts[0].MediaType != "image/png" {
		t.Fatalf("InlineImageAttachments() = %#v, want the image through a drip-feeding reader", parts)
	}
	if parts[0].Image != dataURL("image/png", data) {
		t.Fatal("drip-feeding reader truncated the encoded image")
	}
}

// An image shorter than the sniff window still has to be recognised.
func TestStoredImageShorterThanSniffWindow(t *testing.T) {
	jpegBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01, 0xFF, 0xD9}
	s := imageInputService(t, map[string][]byte{"tiny": jpegBytes})
	parts := inlineImageParts(context.Background(), s, "bot-1", []timeline.ImageAttachmentRef{
		{ContentHash: "tiny", Mime: "image/jpeg"},
	})
	if len(parts) != 1 || parts[0].Image != dataURL("image/jpeg", jpegBytes) {
		t.Fatalf("InlineImageAttachments() = %#v, want the whole short image", parts)
	}
}

func TestGatewayInlineDataURLKeepsRealImage(t *testing.T) {
	s := &Service{logger: slog.Default()}
	item := gatewayAttachment{
		Type:      "image",
		Mime:      "image/png",
		Transport: gatewayTransportInlineDataURL,
		Payload:   dataURL("image/png", rasterPNG(t)),
	}
	got := s.inlineImageAttachmentAssetIfNeeded(context.Background(), "bot-1", item)
	if got.Mime != "image/png" || got.Payload != item.Payload || !isNativeImageAttachment(got) {
		t.Fatalf("attachment = %#v, want it untouched and still native", got)
	}
}

// A wrong subtype is its own source of provider 400s, and the bytes know better.
func TestStoredImageMimeIsCorrectedFromBytes(t *testing.T) {
	jpegBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01, 0xFF, 0xD9}
	s := imageInputService(t, map[string][]byte{"mislabeled": jpegBytes})
	parts := inlineImageParts(context.Background(), s, "bot-1", []timeline.ImageAttachmentRef{
		{ContentHash: "mislabeled", Mime: "image/png"},
	})
	if len(parts) != 1 || parts[0].MediaType != "image/jpeg" {
		t.Fatalf("InlineImageAttachments() = %#v, want the sniffed image/jpeg", parts)
	}
}

func TestEncodeReaderAsDataURLRejectsOversizedImage(t *testing.T) {
	data := rasterPNG(t)
	_, _, err := encodeReaderAsDataURL(bytes.NewReader(data), int64(len(data)-1), "image", "image/png")
	if err == nil || !strings.Contains(err.Error(), "asset too large to inline") {
		t.Fatalf("encodeReaderAsDataURL() error = %v, want the size guard", err)
	}
}

func dataURL(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func decodeDataURLImage(t *testing.T, dataURL string) image.Image {
	t.Helper()
	_, body, found := strings.Cut(dataURL, ",")
	if !found {
		t.Fatalf("not a data URL: %.40q", dataURL)
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
