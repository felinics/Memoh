package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/sticker"
)

type fakeStickerLibrary struct {
	entries []sticker.Entry
	saved   []sticker.Entry
	query   string
	limit   int
}

func (f *fakeStickerLibrary) Save(_ context.Context, _ string, entry sticker.Entry) (sticker.Entry, error) {
	if strings.TrimSpace(entry.Ref) == "" {
		return sticker.Entry{}, errors.New("ref is required")
	}
	entry.Path = "/data/stickers/test-entry.md"
	f.saved = append(f.saved, entry)
	return entry, nil
}

func (f *fakeStickerLibrary) Search(_ context.Context, _ string, query string, limit int) ([]sticker.Entry, error) {
	f.query = query
	f.limit = limit
	return f.entries, nil
}

func (f *fakeStickerLibrary) FindByRef(_ context.Context, _ string, ref string) (sticker.Entry, bool, error) {
	for _, entry := range f.entries {
		if entry.Ref == ref || entry.UniqueID == ref {
			return entry, true, nil
		}
	}
	return sticker.Entry{}, false, nil
}

type fakeStickerSightings struct {
	sightings []dbstore.StickerSighting
	err       error
	// before records the visibility cutoff the tool asked for, which is the
	// whole difference between "the sticker this turn saw" and "whatever
	// arrived while the model was still writing".
	before time.Time
}

func (f *fakeStickerSightings) RecentStickerSightings(_ context.Context, _ string, _ int, before time.Time) ([]dbstore.StickerSighting, error) {
	f.before = before
	visible := make([]dbstore.StickerSighting, 0, len(f.sightings))
	for _, sighting := range f.sightings {
		if !before.IsZero() && sighting.SeenAt.After(before) {
			continue
		}
		visible = append(visible, sighting)
	}
	return visible, f.err
}

func stickerToolSession() SessionContext {
	return SessionContext{BotID: "bot-1", SessionID: "session-1"}
}

func TestStickerProvider_SaveBindsToMostRecentSighting(t *testing.T) {
	t.Parallel()

	library := &fakeStickerLibrary{}
	sightings := &fakeStickerSightings{sightings: []dbstore.StickerSighting{
		{
			Ref:         "sticker-file-new",
			UniqueID:    "unique-new",
			Pack:        "猫猫日常",
			Emoji:       "😺",
			Kind:        sticker.KindAnimated,
			ContentHash: "hash-new",
			SeenAt:      time.Now(),
		},
		{Ref: "sticker-file-old", UniqueID: "unique-old"},
	}}
	provider := NewStickerProvider(nil, library, sightings)
	toolList, err := provider.Tools(context.Background(), stickerToolSession())
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}

	tool := toolByNameForTest(t, toolList, ToolSaveSticker())
	raw, err := tool.Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{
		"description": "猫猫开心地挥手",
		"tags":        []any{"开心"},
	})
	if err != nil {
		t.Fatalf("save_sticker error = %v", err)
	}
	if len(library.saved) != 1 {
		t.Fatalf("saved %d entries, want 1", len(library.saved))
	}
	saved := library.saved[0]
	// 动画贴纸存下来的必须是贴纸本身的 file_id，而不是入库时那张静态预览。
	if saved.Ref != "sticker-file-new" || saved.UniqueID != "unique-new" {
		t.Fatalf("saved identity = %+v, want the newest sighting", saved)
	}
	if saved.Pack != "猫猫日常" || saved.Emoji != "😺" || saved.Kind != sticker.KindAnimated || saved.ContentHash != "hash-new" {
		t.Fatalf("saved metadata = %+v, want the sighting's metadata", saved)
	}
	if saved.Description != "猫猫开心地挥手" || len(saved.Tags) != 1 {
		t.Fatalf("saved prose = %+v", saved)
	}

	result, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("save_sticker result = %T", raw)
	}
	if result["platform_key"] != "sticker-file-new" || result["type"] != "sticker" {
		t.Fatalf("result = %+v, want a ready-to-send attachment reference", result)
	}
	if result["source_platform"] != sticker.PlatformTelegram {
		t.Fatalf("result = %+v, want the key's own platform, so another platform cannot take it for one of its own", result)
	}
}

func TestStickerProvider_SaveRejectsUnseenPlatformKey(t *testing.T) {
	t.Parallel()

	library := &fakeStickerLibrary{}
	sightings := &fakeStickerSightings{sightings: []dbstore.StickerSighting{{Ref: "sticker-file", UniqueID: "unique-1"}}}
	provider := NewStickerProvider(nil, library, sightings)
	toolList, err := provider.Tools(context.Background(), stickerToolSession())
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}

	tool := toolByNameForTest(t, toolList, ToolSaveSticker())
	// 编出来的 platform_key 存下去就是一个发出去必然失败的引用，必须当场拒绝。
	if _, err := tool.Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{
		"description":  "随便写的",
		"platform_key": "never-seen",
	}); err == nil {
		t.Fatal("save_sticker should reject a sticker this conversation never received")
	}
	if len(library.saved) != 0 {
		t.Fatalf("saved %d entries, want 0", len(library.saved))
	}
}

func TestStickerProvider_SaveRewritesStoredStickerOutOfSightingRange(t *testing.T) {
	t.Parallel()

	// 几周前存的贴纸早已不在会话的最近若干条里，但改写它的描述只是普通编辑，
	// 不该因为"最近没见过"而被拒绝。
	library := &fakeStickerLibrary{entries: []sticker.Entry{{
		Platform: sticker.PlatformTelegram,
		Ref:      "old-sticker",
		UniqueID: "unique-old",
		Pack:     "猫猫日常",
		Emoji:    "😺",
		Kind:     sticker.KindStatic,
	}}}
	sightings := &fakeStickerSightings{sightings: []dbstore.StickerSighting{{Ref: "unrelated", UniqueID: "unique-other"}}}
	provider := NewStickerProvider(nil, library, sightings)
	toolList, err := provider.Tools(context.Background(), stickerToolSession())
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}

	tool := toolByNameForTest(t, toolList, ToolSaveSticker())
	if _, err := tool.Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{
		"description":  "换一句更准的描述",
		"platform_key": "old-sticker",
	}); err != nil {
		t.Fatalf("save_sticker error = %v", err)
	}
	if len(library.saved) != 1 {
		t.Fatalf("saved %d entries, want 1", len(library.saved))
	}
	saved := library.saved[0]
	if saved.Ref != "old-sticker" || saved.Pack != "猫猫日常" || saved.Emoji != "😺" {
		t.Fatalf("saved = %+v, want the stored sticker's identity preserved", saved)
	}
	if saved.Description != "换一句更准的描述" {
		t.Fatalf("description = %q", saved.Description)
	}
}

func TestStickerProvider_SaveWithoutSightingsExplainsWhy(t *testing.T) {
	t.Parallel()

	provider := NewStickerProvider(nil, &fakeStickerLibrary{}, &fakeStickerSightings{})
	toolList, err := provider.Tools(context.Background(), stickerToolSession())
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	tool := toolByNameForTest(t, toolList, ToolSaveSticker())
	_, err = tool.Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{"description": "没有贴纸"})
	if err == nil || !strings.Contains(err.Error(), "no sticker") {
		t.Fatalf("error = %v, want an explanation that nothing was seen", err)
	}
}

func TestStickerProvider_SearchReturnsSendableReference(t *testing.T) {
	t.Parallel()

	library := &fakeStickerLibrary{entries: []sticker.Entry{{
		Platform:    sticker.PlatformTelegram,
		Ref:         "sticker-file",
		UniqueID:    "unique-1",
		Pack:        "猫猫日常",
		Emoji:       "😺",
		Kind:        sticker.KindStatic,
		Description: "猫猫挥手",
	}}}
	provider := NewStickerProvider(nil, library, &fakeStickerSightings{})
	toolList, err := provider.Tools(context.Background(), stickerToolSession())
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}

	tool := toolByNameForTest(t, toolList, ToolSearchStickers())
	raw, err := tool.Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{
		"query": "猫猫",
		"limit": 5,
	})
	if err != nil {
		t.Fatalf("search_stickers error = %v", err)
	}
	if library.query != "猫猫" || library.limit != 5 {
		t.Fatalf("search args = %q/%d", library.query, library.limit)
	}
	result, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("search result = %T", raw)
	}
	items, ok := result["stickers"].([]map[string]any)
	if !ok || len(items) != 1 {
		t.Fatalf("stickers = %#v", result["stickers"])
	}
	if items[0]["source_platform"] != sticker.PlatformTelegram {
		t.Fatalf("item = %+v, want the key tagged with the platform that issued it", items[0])
	}
	if items[0]["platform_key"] != "sticker-file" || items[0]["type"] != "sticker" {
		t.Fatalf("item = %+v, want a send-ready attachment reference", items[0])
	}
}

func TestStickerProvider_WithoutSightingReaderOnlySearches(t *testing.T) {
	t.Parallel()

	// 没有会话历史可查时，保存无从绑定到具体贴纸，工具就不该出现在模型面前。
	provider := NewStickerProvider(nil, &fakeStickerLibrary{}, nil)
	toolList, err := provider.Tools(context.Background(), stickerToolSession())
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	if len(toolList) != 1 || toolList[0].Name != ToolSearchStickers().String() {
		t.Fatalf("tools = %+v, want only search_stickers", toolList)
	}
}

func TestStickerProvider_WithoutLibraryExposesNoTools(t *testing.T) {
	t.Parallel()

	provider := NewStickerProvider(nil, nil, &fakeStickerSightings{})
	toolList, err := provider.Tools(context.Background(), stickerToolSession())
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	if len(toolList) != 0 {
		t.Fatalf("tools = %+v, want none", toolList)
	}
}

// 群里其他人发的消息不触发 agent 也会立刻入库。模型正在描述 A 的时候 B 到达,
// 保存必须仍然绑定 A——工具在装配时就定下了本轮能看见什么,而不是执行到那一刻
// 再去问数据库"最新的是哪张"。
func TestStickerProvider_SaveIgnoresStickersArrivingMidTurn(t *testing.T) {
	t.Parallel()

	turnStart := time.Now()
	library := &fakeStickerLibrary{}
	sightings := &fakeStickerSightings{sightings: []dbstore.StickerSighting{
		{Ref: "arrived-mid-turn", UniqueID: "unique-late", SeenAt: turnStart.Add(2 * time.Second)},
		{Ref: "the-one-in-context", UniqueID: "unique-seen", SeenAt: turnStart.Add(-2 * time.Second)},
	}}
	provider := NewStickerProvider(nil, library, sightings)
	provider.now = func() time.Time { return turnStart }

	toolList, err := provider.Tools(context.Background(), stickerToolSession())
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	tool := toolByNameForTest(t, toolList, ToolSaveSticker())
	if _, err := tool.Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{
		"description": "描述的是本轮看到的那张",
	}); err != nil {
		t.Fatalf("save_sticker error = %v", err)
	}
	if len(library.saved) != 1 || library.saved[0].Ref != "the-one-in-context" {
		t.Fatalf("saved = %+v, want the sticker this turn could see", library.saved)
	}
	if !sightings.before.Equal(turnStart) {
		t.Fatalf("visibility cutoff = %v, want the turn boundary %v", sightings.before, turnStart)
	}
}
