package application

import (
	"bytes"
	"compress/gzip"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/models"
)

// movingStickerTGS is a red square crossing a 512x512 canvas, so the sampled
// frames are genuinely different from one another.
func movingStickerTGS(t *testing.T) []byte {
	t.Helper()
	const document = `{"v":"5.5.2","w":512,"h":512,"ip":0,"op":60,"fr":30,"layers":[{"ty":1,"ind":1,"ip":0,"op":60,"st":0,"sw":128,"sh":128,"sc":"#ff0000","ks":{"o":{"a":0,"k":100},"r":{"a":0,"k":0},"p":{"a":1,"k":[{"t":0,"s":[0,128,0],"e":[384,128,0]},{"t":59,"s":[384,128,0]}]},"a":{"a":0,"k":[0,0,0]},"s":{"a":0,"k":[100,100,100]}}}]}`
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write([]byte(document)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The failure #1232 shipped: expanding frames into separate attachments makes
// one sticker look like five to everything downstream. The attachment has to
// stay singular — it is one file, with one path, counted once — and fan out
// only where images are handed to the model.
func TestAnimatedStickerStaysOneAttachmentWithManyImages(t *testing.T) {
	s := imageInputService(t, map[string][]byte{"sticker": movingStickerTGS(t)})
	req := ChatRequest{
		BotID:       "bot-1",
		Attachments: []ChatAttachment{{Type: "image", ContentHash: "sticker", Mime: "image/webp", Name: "sticker.tgs"}},
	}
	model := models.GetResponse{Model: models.Model{Config: models.ModelConfig{
		Compatibilities: []string{models.CompatVision},
	}}}

	merged := s.routeAndMergeAttachments(context.Background(), model, req)
	if len(merged) != 1 {
		t.Fatalf("routeAndMergeAttachments() = %d attachments, want the sticker counted once", len(merged))
	}
	if paths := extractAttachmentPaths(merged); len(paths) != 1 {
		t.Fatalf("extractAttachmentPaths() = %v, want a single reference to the original", paths)
	}

	item, ok := merged[0].(gatewayAttachment)
	if !ok || item.Type != "image" || item.Mime != "image/png" {
		t.Fatalf("routed attachment = %#v, want a PNG image attachment", merged[0])
	}
	if len(item.Frames) != animationFrameCount {
		t.Fatalf("Frames = %d, want %d rendered frames", len(item.Frames), animationFrameCount)
	}
	if item.Payload != item.Frames[0] {
		t.Fatal("Payload must stay the first frame so routing and size checks see a real image")
	}
	// The budget has to see every frame the model will receive, not just one.
	if want := totalInlineDataURLBytes(item.Frames); item.Size != want {
		t.Fatalf("Size = %d, want %d — the whole frame set", item.Size, want)
	}

	parts := extractNativeImageParts(merged)
	if len(parts) != animationFrameCount {
		t.Fatalf("extractNativeImageParts() = %d parts, want %d", len(parts), animationFrameCount)
	}
	for i, part := range parts {
		if part.MediaType != "image/png" || !strings.HasPrefix(part.Image, "data:image/png;base64,") {
			t.Fatalf("part %d = %#v, want a PNG data URL", i, part)
		}
	}
	// Distinct frames, not the same image five times.
	unique := map[string]bool{}
	for _, part := range parts {
		unique[part.Image] = true
	}
	if len(unique) != animationFrameCount {
		t.Fatalf("%d distinct frames out of %d — the sampled frames are not distinct", len(unique), len(parts))
	}
}

// Every entry point has to see the same frames; that divergence is what let
// the original bug reach the model from one path and not another.
func TestAnimatedStickerIsIdenticalAcrossEntryPoints(t *testing.T) {
	data := movingStickerTGS(t)
	s := imageInputService(t, map[string][]byte{"sticker": data})
	req := ChatRequest{
		BotID:       "bot-1",
		Attachments: []ChatAttachment{{Type: "image", ContentHash: "sticker", Mime: "application/x-gzip"}},
	}
	model := models.GetResponse{Model: models.Model{Config: models.ModelConfig{
		Compatibilities: []string{models.CompatVision},
	}}}

	gateway := extractNativeImageParts(s.routeAndMergeAttachments(context.Background(), model, req))
	discuss := inlineImageParts(context.Background(), s, "bot-1",
		[]timeline.ImageAttachmentRef{{ContentHash: "sticker", Mime: "application/x-gzip"}})
	injected := s.inlineInjectAttachments(context.Background(), "bot-1", req.Attachments)

	if len(gateway) != animationFrameCount || len(discuss) != len(gateway) || len(injected) != len(gateway) {
		t.Fatalf("frame counts differ: gateway=%d discuss=%d injected=%d", len(gateway), len(discuss), len(injected))
	}
	for i := range gateway {
		if discuss[i].Image != gateway[i].Image || injected[i].Image != gateway[i].Image {
			t.Fatalf("frame %d differs between entry points", i)
		}
	}
}

// An External Agent gets the frames too, and still one reachable original.
func TestAnimatedStickerReachesExternalAgentAsFrames(t *testing.T) {
	s := imageInputService(t, map[string][]byte{"sticker": movingStickerTGS(t)})
	prepared, err := s.prepareRuntimeAttachments(context.Background(), ChatRequest{
		BotID:       "bot-1",
		Attachments: []ChatAttachment{{Type: "image", ContentHash: "sticker", Mime: "image/webp", Name: "sticker.tgs"}},
	})
	if err != nil {
		t.Fatalf("prepareRuntimeAttachments() error = %v", err)
	}
	if len(prepared.Images) != animationFrameCount {
		t.Fatalf("Images = %d, want %d frames", len(prepared.Images), animationFrameCount)
	}
	for i, image := range prepared.Images {
		if image.MimeType != "image/png" || !bytes.HasPrefix(image.Data, []byte{0x89, 'P', 'N', 'G'}) {
			t.Fatalf("image %d = %q with %d bytes, want PNG", i, image.MimeType, len(image.Data))
		}
	}
	if len(prepared.References) != 1 || len(prepared.Context) != 1 {
		t.Fatalf("references=%v context=%d, want the original counted once", prepared.References, len(prepared.Context))
	}
}

// A sticker that never moves is five copies of one picture. Sending it five
// times costs five times as much and says nothing more.
func TestStaticAnimationCollapsesToOneFrame(t *testing.T) {
	s := imageInputService(t, map[string][]byte{"still": tgsSticker(t)})
	parts := inlineImageParts(context.Background(), s, "bot-1",
		[]timeline.ImageAttachmentRef{{ContentHash: "still", Mime: "application/x-gzip"}})
	if len(parts) != 1 {
		t.Fatalf("InlineImageAttachments() = %d parts, want identical frames collapsed to one", len(parts))
	}
}

// Stickers are drawn for a chat background; an unflattened frame reaches some
// providers as a subject on black.
func TestAnimatedStickerFramesAreFlattenedOnWhite(t *testing.T) {
	s := imageInputService(t, map[string][]byte{"sticker": movingStickerTGS(t)})
	parts := inlineImageParts(context.Background(), s, "bot-1",
		[]timeline.ImageAttachmentRef{{ContentHash: "sticker", Mime: "application/x-gzip"}})
	if len(parts) == 0 {
		t.Fatal("no frames rendered")
	}
	frame := decodeDataURLImage(t, parts[0].Image)
	// Top-right corner is outside the square at every sampled frame.
	r, g, b, a := frame.At(frame.Bounds().Dx()-1, 0).RGBA()
	if r != 0xffff || g != 0xffff || b != 0xffff || a != 0xffff {
		t.Fatalf("corner pixel = (%d,%d,%d,%d), want opaque white", r, g, b, a)
	}
}

// A render failure must not take the turn's other images down with it.
func TestAnimationFailureLeavesOtherImagesAlone(t *testing.T) {
	s := imageInputService(t, map[string][]byte{
		// gzip magic, but nothing behind it that ThorVG can parse.
		"broken": append([]byte{0x1f, 0x8b, 0x08}, bytes.Repeat([]byte{0x00}, 64)...),
		"photo":  rasterPNG(t),
	})
	s.logger = slog.Default()
	parts := inlineImageParts(context.Background(), s, "bot-1", []timeline.ImageAttachmentRef{
		{ContentHash: "broken", Mime: "application/x-gzip"},
		{ContentHash: "photo", Mime: "image/png"},
	})
	if len(parts) != 1 || parts[0].Image != dataURL("image/png", rasterPNG(t)) {
		t.Fatalf("InlineImageAttachments() = %#v, want only the intact photo", parts)
	}
}

// The Telegram adapter now labels an animated sticker application/x-tgsticker
// and stores the Lottie itself. That label is not one the raster allowlist
// knows, so this is the regression guard that the two layers still agree: the
// bytes decide, and the sticker reaches the model as frames.
func TestAdapterLabelledAnimatedStickerRenders(t *testing.T) {
	s := imageInputService(t, map[string][]byte{"sticker": movingStickerTGS(t)})
	parts := inlineImageParts(context.Background(), s, "bot-1",
		[]timeline.ImageAttachmentRef{{ContentHash: "sticker", Mime: "application/x-tgsticker"}})
	if len(parts) != animationFrameCount {
		t.Fatalf("InlineImageAttachments() = %d parts, want %d frames", len(parts), animationFrameCount)
	}
}
