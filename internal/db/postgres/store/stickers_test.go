package postgresstore

import (
	"testing"

	"github.com/felinics/memoh/internal/attachment"
)

// 哪个 id 是"能发出去的那个"取决于入库时有没有发生预览替换：只有替换发生时
// 适配器才写 sticker_file_id（#1239 的收窄），其余情况附件本身就是贴纸。取错
// 一边的后果是发出一张动图的静态截图，或者整个库认不出静态贴纸。
func TestStickerSightingFromMetadata(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		metadata map[string]any
		wantOK   bool
		wantRef  string
	}{
		{
			name: "预览替换后取贴纸自己的 id",
			metadata: map[string]any{
				"file_id":                             "thumb-file",
				attachment.MetadataKeyStickerFileID:   "webm-file",
				attachment.MetadataKeyStickerUniqueID: "unique-1",
				attachment.MetadataKeyStickerSet:      "KleePack",
				attachment.MetadataKeyStickerEmoji:    "🎉",
				attachment.MetadataKeyStickerKind:     "video",
			},
			wantOK:  true,
			wantRef: "webm-file",
		},
		{
			name: "没有替换时附件自己就是贴纸",
			metadata: map[string]any{
				"file_id":                             "tgs-file",
				attachment.MetadataKeyStickerUniqueID: "unique-2",
				attachment.MetadataKeyStickerKind:     "animated",
			},
			wantOK:  true,
			wantRef: "tgs-file",
		},
		{
			name:     "普通图片不是贴纸",
			metadata: map[string]any{"file_id": "photo-file"},
		},
		{
			name:     "认得出是贴纸但发不出去的行要跳过",
			metadata: map[string]any{attachment.MetadataKeyStickerUniqueID: "unique-3"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sighting, ok := stickerSightingFromMetadata(tc.metadata)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (sighting %+v)", ok, tc.wantOK, sighting)
			}
			if !tc.wantOK {
				return
			}
			if sighting.Ref != tc.wantRef {
				t.Fatalf("ref = %q, want %q", sighting.Ref, tc.wantRef)
			}
			if sighting.UniqueID == "" {
				t.Fatalf("sighting = %+v, want the stable identity", sighting)
			}
		})
	}
}
