package application

import (
	"bytes"
	"context"
	"io"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/models"
)

func countingStickerService(t *testing.T, opens *int) *Service {
	t.Helper()
	data := movingStickerTGS(t)
	return &Service{assetLoader: &fakeGatewayAssetLoader{
		openFn: func(context.Context, string, string) (io.ReadCloser, string, error) {
			*opens++
			return io.NopCloser(bytes.NewReader(data)), "application/x-tgsticker", nil
		},
		accessPathFn: func(_ context.Context, _, contentHash string) (string, error) {
			return "/data/" + contentHash, nil
		},
	}}
}

// Preparing an image costs real work — an animated sticker is rasterised — and
// a model without vision routes every image to a file reference anyway.
func TestNonVisionModelDoesNotPrepareImages(t *testing.T) {
	opens := 0
	s := countingStickerService(t, &opens)
	req := ChatRequest{BotID: "bot-1", Attachments: []ChatAttachment{
		{Type: "image", ContentHash: "sticker", Mime: "application/x-tgsticker"},
	}}

	merged := s.routeAndMergeAttachments(context.Background(),
		models.GetResponse{Model: models.Model{Config: models.ModelConfig{Compatibilities: []string{}}}}, req)
	if opens != 0 {
		t.Fatalf("asset opened %d time(s) for a model that cannot use images", opens)
	}
	if len(merged) != 1 {
		t.Fatalf("routeAndMergeAttachments() = %d attachments, want the file reference", len(merged))
	}
	item := merged[0].(gatewayAttachment)
	if item.Type != "file" || item.Payload != "/data/sticker" {
		t.Fatalf("routed attachment = %#v, want the original as a file reference", item)
	}
	if parts := extractNativeImageParts(merged); len(parts) != 0 {
		t.Fatalf("extractNativeImageParts() = %d, want none", len(parts))
	}

	// The same request on a vision model still renders.
	merged = s.routeAndMergeAttachments(context.Background(),
		models.GetResponse{Model: models.Model{Config: models.ModelConfig{
			Compatibilities: []string{models.CompatVision},
		}}}, req)
	if opens == 0 {
		t.Fatal("a vision model must still get the rendered frames")
	}
	if parts := extractNativeImageParts(merged); len(parts) != animationFrameCount {
		t.Fatalf("extractNativeImageParts() = %d, want %d frames", len(parts), animationFrameCount)
	}
}

// A sticker that stays in a discussion's context is re-prepared on every model
// call, and re-rendering it produces byte-identical frames.
func TestRenderedFramesAreCachedAcrossTurns(t *testing.T) {
	opens := 0
	s := countingStickerService(t, &opens)
	refs := []timeline.ImageAttachmentRef{{ContentHash: "sticker", Mime: "application/x-tgsticker"}}

	first := inlineImageParts(context.Background(), s, "bot-1", refs)
	if len(first) != animationFrameCount || opens != 1 {
		t.Fatalf("first turn: %d parts, %d opens", len(first), opens)
	}
	for turn := range 3 {
		again := inlineImageParts(context.Background(), s, "bot-1", refs)
		if opens != 1 {
			t.Fatalf("turn %d re-read the asset: %d opens", turn+2, opens)
		}
		if len(again) != len(first) {
			t.Fatalf("turn %d: %d parts, want %d", turn+2, len(again), len(first))
		}
		for i := range first {
			if again[i].Image != first[i].Image {
				t.Fatalf("turn %d frame %d differs from the cached render", turn+2, i)
			}
		}
	}
}

// Cached frames must not cross bots: the media store is scoped per bot.
func TestFrameCacheIsScopedPerBot(t *testing.T) {
	opens := 0
	s := countingStickerService(t, &opens)
	refs := []timeline.ImageAttachmentRef{{ContentHash: "sticker", Mime: "application/x-tgsticker"}}
	inlineImageParts(context.Background(), s, "bot-a", refs)
	inlineImageParts(context.Background(), s, "bot-b", refs)
	if opens != 2 {
		t.Fatalf("asset opened %d time(s), want one per bot", opens)
	}
}

func TestFrameCacheEvictsOldestEntries(t *testing.T) {
	var cache animationCache
	for i := range animationCacheEntries + 10 {
		cache.put(animationCacheKey("bot", string(rune('a'+i%26))+string(rune('a'+i/26))), []string{"frame"})
	}
	cache.mu.Lock()
	entries, order, size := len(cache.entries), len(cache.order), cache.bytes
	cache.mu.Unlock()
	if entries > animationCacheEntries || order != entries {
		t.Fatalf("cache holds %d entries with %d in the order list, want at most %d and consistent",
			entries, order, animationCacheEntries)
	}
	if size != entries*len("frame") {
		t.Fatalf("byte accounting = %d, want %d", size, entries*len("frame"))
	}
}

// A discussion turn gathers attachments from every new message, and each
// animation multiplies into frames. Past the budget an attachment contributes
// its first frame only, so every attachment stays visible.
func TestTurnVisionBudgetShedsExpansionFirst(t *testing.T) {
	data := movingStickerTGS(t)
	s := &Service{assetLoader: &fakeGatewayAssetLoader{
		openFn: func(context.Context, string, string) (io.ReadCloser, string, error) {
			return io.NopCloser(bytes.NewReader(data)), "application/x-tgsticker", nil
		},
	}}
	const stickers = 10
	refs := make([]timeline.ImageAttachmentRef, 0, stickers)
	for i := range stickers {
		refs = append(refs, timeline.ImageAttachmentRef{
			ContentHash: "sticker-" + string(rune('a'+i)),
			Mime:        "application/x-tgsticker",
		})
	}

	parts := inlineImageParts(context.Background(), s, "bot-1", refs)
	if len(parts) > maxTurnVisionImages {
		t.Fatalf("InlineImageAttachments() = %d parts, over the %d budget", len(parts), maxTurnVisionImages)
	}
	// Four stickers fit whole (20 images); the rest still contribute one frame
	// each until the budget is gone, so nothing disappears silently while
	// there is room.
	if len(parts) != maxTurnVisionImages {
		t.Fatalf("InlineImageAttachments() = %d parts, want the budget spent in full", len(parts))
	}
}

func TestVisionBudgetKeepsOneFrameWhenExpansionDoesNotFit(t *testing.T) {
	budget := visionBudget{used: maxTurnVisionImages - 2}
	five := make([]sdk.ImagePart, 5)
	if got := budget.take(five); len(got) != 1 {
		t.Fatalf("take(5 frames) with 2 left = %d, want the single representative frame", len(got))
	}
	if got := budget.take(five); len(got) != 1 {
		t.Fatalf("take(5 frames) with 1 left = %d, want one", len(got))
	}
	if got := budget.take(five); got != nil {
		t.Fatalf("take(5 frames) with nothing left = %d, want none", len(got))
	}
}
