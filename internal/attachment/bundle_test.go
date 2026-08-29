package attachment

import (
	"testing"

	"github.com/felinics/memoh/internal/media"
)

func TestBundleNormalize_DataURLBecomesBase64(t *testing.T) {
	bundle := Bundle{
		URL: "data:image/png;base64,AAAA",
	}.Normalize()

	if bundle.URL != "" {
		t.Fatalf("expected URL cleared after data-url normalization, got %q", bundle.URL)
	}
	if bundle.Base64 != "data:image/png;base64,AAAA" {
		t.Fatalf("unexpected base64 payload: %q", bundle.Base64)
	}
	if bundle.Mime != "image/png" {
		t.Fatalf("expected mime image/png, got %q", bundle.Mime)
	}
	if bundle.Type != "image" {
		t.Fatalf("expected type image, got %q", bundle.Type)
	}
}

func TestBundleNormalize_LocalURLBecomesPath(t *testing.T) {
	bundle := Bundle{
		URL: "/data/screenshots/demo.png",
	}.Normalize()

	if bundle.Path != "/data/screenshots/demo.png" {
		t.Fatalf("expected local URL moved to path, got %q", bundle.Path)
	}
	if bundle.URL != "" {
		t.Fatalf("expected URL cleared after local-path normalization, got %q", bundle.URL)
	}
	if bundle.Name != "demo.png" {
		t.Fatalf("expected inferred name demo.png, got %q", bundle.Name)
	}
	if bundle.Type != "image" {
		t.Fatalf("expected inferred type image, got %q", bundle.Type)
	}
}

func TestParseToolInputBundles(t *testing.T) {
	bundles, ok := ParseToolInputBundles([]any{
		"screenshot.png",
		"https://example.com/demo.jpg",
		"data:image/png;base64,AAAA",
		map[string]any{
			"content_hash": "asset-1",
			"type":         "image",
		},
	})
	if !ok {
		t.Fatal("expected tool input parsing to succeed")
	}
	if len(bundles) != 4 {
		t.Fatalf("expected 4 bundles, got %d", len(bundles))
	}

	if bundles[0].Path != "/data/screenshot.png" {
		t.Fatalf("expected bare path to resolve under /data, got %q", bundles[0].Path)
	}
	if bundles[0].Type != "image" {
		t.Fatalf("expected inferred image type for png path, got %q", bundles[0].Type)
	}

	if bundles[1].URL != "https://example.com/demo.jpg" {
		t.Fatalf("unexpected remote URL bundle: %q", bundles[1].URL)
	}
	if bundles[1].Name != "demo.jpg" {
		t.Fatalf("expected inferred URL name demo.jpg, got %q", bundles[1].Name)
	}

	if bundles[2].Base64 != "data:image/png;base64,AAAA" {
		t.Fatalf("unexpected inline data bundle: %q", bundles[2].Base64)
	}
	if bundles[2].Type != "image" {
		t.Fatalf("expected inferred image type for inline data, got %q", bundles[2].Type)
	}

	if bundles[3].ContentHash != "asset-1" {
		t.Fatalf("expected content hash asset-1, got %q", bundles[3].ContentHash)
	}
	if bundles[3].Type != "image" {
		t.Fatalf("expected explicit image type preserved, got %q", bundles[3].Type)
	}
}

func TestParseToolInputBundles_InvalidTopLevelType(t *testing.T) {
	if _, ok := ParseToolInputBundles(42); ok {
		t.Fatal("expected invalid top-level input to be rejected")
	}
}

func TestBundleFromMapAndToMap(t *testing.T) {
	raw := map[string]any{
		"type": "image",
		"url":  "/data/images/demo.png",
		"mime": "IMAGE/PNG; charset=utf-8",
		"size": float64(12),
		"metadata": map[string]any{
			"source": "tool",
		},
	}

	bundle := BundleFromMap(raw)
	if bundle.Path != "/data/images/demo.png" {
		t.Fatalf("expected path normalized from map, got %q", bundle.Path)
	}
	if bundle.Mime != "image/png" {
		t.Fatalf("expected normalized mime image/png, got %q", bundle.Mime)
	}
	if bundle.Size != 12 {
		t.Fatalf("expected size 12, got %d", bundle.Size)
	}

	roundTrip := bundle.ToMap()
	if roundTrip["path"] != "/data/images/demo.png" {
		t.Fatalf("expected serialized path preserved, got %#v", roundTrip["path"])
	}
	if roundTrip["mime"] != "image/png" {
		t.Fatalf("expected serialized mime image/png, got %#v", roundTrip["mime"])
	}
}

func TestBundleWithAsset(t *testing.T) {
	bundle := Bundle{
		Type: "image",
		URL:  "https://example.com/demo.png",
	}.WithAsset("bot-1", media.Asset{
		ContentHash: "asset-1",
		Mime:        "image/png",
		SizeBytes:   42,
		StorageKey:  "aa/asset-1.png",
	})

	if bundle.ContentHash != "asset-1" {
		t.Fatalf("expected content hash asset-1, got %q", bundle.ContentHash)
	}
	if bundle.URL != "" || bundle.Path != "" || bundle.Base64 != "" {
		t.Fatalf("expected transport refs cleared after asset rewrite: %#v", bundle)
	}
	if bundle.Metadata[MetadataKeyBotID] != "bot-1" {
		t.Fatalf("expected bot_id metadata, got %#v", bundle.Metadata[MetadataKeyBotID])
	}
	if bundle.Metadata[MetadataKeyStorageKey] != "aa/asset-1.png" {
		t.Fatalf("expected storage key metadata, got %#v", bundle.Metadata[MetadataKeyStorageKey])
	}
	if bundle.Metadata[MetadataKeySourceURL] != "https://example.com/demo.png" {
		t.Fatalf("expected source_url metadata, got %#v", bundle.Metadata[MetadataKeySourceURL])
	}
	if bundle.Name != "demo.png" {
		t.Fatalf("expected inferred name demo.png, got %q", bundle.Name)
	}
}

func TestBundleWithAssetAccess(t *testing.T) {
	t.Parallel()

	bundle := Bundle{
		Type: "file",
		Path: "/data/work/demo.txt",
	}.WithAssetAccess("bot-1", media.Asset{
		ContentHash: "asset-2",
		Mime:        "text/plain",
		SizeBytes:   7,
		StorageKey:  "bb/asset-2.txt",
	}, "/data/.memoh/media/bb/asset-2.txt")

	if bundle.Path != "/data/.memoh/media/bb/asset-2.txt" {
		t.Fatalf("expected access path preserved, got %q", bundle.Path)
	}
	if MetadataString(bundle.Metadata, MetadataKeySourcePath) != "/data/work/demo.txt" {
		t.Fatalf("expected source_path metadata preserved, got %#v", bundle.Metadata[MetadataKeySourcePath])
	}
}

func TestExtractStorageKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "current", path: "/data/.memoh/media/aa/demo.png", want: "aa/demo.png"},
		{name: "legacy", path: "/data/media/aa/demo.png", want: "aa/demo.png"},
		{name: "other", path: "/tmp/demo.png", want: ""},
	}
	for _, tt := range tests {
		if got := ExtractStorageKey(tt.path); got != tt.want {
			t.Errorf("%s: ExtractStorageKey(%q) = %q, want %q", tt.name, tt.path, got, tt.want)
		}
	}
}

func TestIsMediaAccessPath(t *testing.T) {
	t.Parallel()

	for _, mediaPath := range []string{
		"/data/.memoh/media",
		"/data/.memoh/media/aa/demo.png",
		"/data/media",
		"data/media/aa/demo.png",
	} {
		if !IsMediaAccessPath(mediaPath) {
			t.Errorf("IsMediaAccessPath(%q) = false, want true", mediaPath)
		}
	}
	for _, otherPath := range []string{"/data/mediakit/demo.png", "/data/.memoh/mediakit/demo.png", "/tmp/demo.png"} {
		if IsMediaAccessPath(otherPath) {
			t.Errorf("IsMediaAccessPath(%q) = true, want false", otherPath)
		}
	}
}

func TestMediaAccessPath(t *testing.T) {
	t.Parallel()

	if got := MediaAccessPath("aa/demo.png"); got != "/data/.memoh/media/aa/demo.png" {
		t.Fatalf("unexpected media access path: %q", got)
	}
	if got := MediaAccessPath(""); got != "/data/.memoh/media" {
		t.Fatalf("unexpected media root path: %q", got)
	}
}

func TestDataSubpath(t *testing.T) {
	t.Parallel()

	if got, ok := DataSubpath("/data/work/demo.txt"); !ok || got != "work/demo.txt" {
		t.Fatalf("expected work/demo.txt, got %q ok=%v", got, ok)
	}
	if _, ok := DataSubpath("/tmp/work/demo.txt"); ok {
		t.Fatal("expected non-data path to be rejected")
	}
	if _, ok := DataSubpath("/data"); ok {
		t.Fatal("expected bare data mount to be rejected")
	}
}
