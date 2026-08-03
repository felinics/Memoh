package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStickerToolSchemaBytesAreStableAcrossCatalogOrder(t *testing.T) {
	t.Parallel()
	stickers := []describedSticker{
		{ID: "S002", Description: "哭泣"},
		{ID: "S001", Description: "挥手"},
	}
	first, err := json.Marshal(stickerSendInputSchema(describedStickerSet{Name: "set", Loaded: true, TotalCount: 2, Stickers: stickers}, ""))
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(stickerSendInputSchema(describedStickerSet{Name: "set", Loaded: true, TotalCount: 2, Stickers: []describedSticker{stickers[1], stickers[0]}}, ""))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("schema bytes changed with source order:\n%s\n%s", first, second)
	}
}

func TestCandidateIDIsContentStableAcrossCatalogOrder(t *testing.T) {
	t.Parallel()

	first := []telegramSticker{
		{FileUniqueID: "unique-a"},
		{FileUniqueID: "unique-b"},
	}
	before := map[string]string{}
	for _, sticker := range first {
		before[sticker.FileUniqueID] = candidateID(sticker.FileUniqueID)
	}
	second := []telegramSticker{first[1], {FileUniqueID: "unique-new"}, first[0]}
	for _, sticker := range second {
		if previous, exists := before[sticker.FileUniqueID]; exists && candidateID(sticker.FileUniqueID) != previous {
			t.Fatalf("candidate ID changed after reorder: %q", sticker.FileUniqueID)
		}
	}
	if before["unique-a"] == before["unique-b"] || len(before["unique-a"]) != 17 {
		t.Fatalf("candidate IDs are not distinct 64-bit hashes: %#v", before)
	}
}

type fakeTelegramAPI struct {
	mu            sync.Mutex
	set           telegramStickerSet
	media         map[string][]byte
	getSetCalls   int
	downloadCalls int
	sentChatID    string
	sentFileID    string
	sentText      string
	getSetStarted chan struct{}
	getSetRelease chan struct{}
	startOnce     sync.Once
}

func (f *fakeTelegramAPI) GetStickerSet(ctx context.Context, _ string) (telegramStickerSet, error) {
	f.mu.Lock()
	f.getSetCalls++
	set := f.set
	started := f.getSetStarted
	release := f.getSetRelease
	f.mu.Unlock()
	if started != nil {
		f.startOnce.Do(func() { close(started) })
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return telegramStickerSet{}, ctx.Err()
		}
	}
	return set, nil
}

func (f *fakeTelegramAPI) DownloadStickerMedia(
	_ context.Context,
	sticker telegramSticker,
) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloadCalls++
	data := f.media[sticker.FileUniqueID]
	if len(data) == 0 {
		return nil, "", errors.New("missing fake media")
	}
	return data, "image/webp", nil
}

func (f *fakeTelegramAPI) SendSticker(
	_ context.Context,
	chatID, fileID string,
) (telegramMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentChatID = chatID
	f.sentFileID = fileID
	return telegramMessage{MessageID: 42}, nil
}

func (f *fakeTelegramAPI) SendText(
	_ context.Context,
	chatID, text string,
) (telegramMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentChatID = chatID
	f.sentText = text
	return telegramMessage{MessageID: 41}, nil
}

type fakeStickerDescriber struct {
	mu           sync.Mutex
	descriptions map[string]string
	err          error
	failures     int
	calls        int
}

func (f *fakeStickerDescriber) Describe(
	_ context.Context,
	inputs []stickerVisionInput,
) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failures > 0 {
		f.failures--
		return nil, errors.New("temporary malformed response")
	}
	if f.err != nil {
		return nil, f.err
	}
	result := make(map[string]string, len(inputs))
	for _, input := range inputs {
		result[input.ID] = f.descriptions[string(input.Data)]
	}
	return result, nil
}

func (*fakeStickerDescriber) Model() string {
	return "vision-test"
}

func (*fakeStickerDescriber) PromptVersion() string {
	return "prompt-test"
}

func newTestCatalog(
	t *testing.T,
	path string,
	describer stickerDescriber,
) (*stickerCatalog, *stickerDescriptionCache) {
	t.Helper()
	cache, err := openStickerDescriptionCache(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newStickerCatalog(cache, describer)
	if err != nil {
		_ = cache.Close()
		t.Fatal(err)
	}
	return catalog, cache
}

func testStickerSet() telegramStickerSet {
	return telegramStickerSet{
		Name: "set",
		Stickers: []telegramSticker{
			{FileID: "laugh-file", FileUniqueID: "laugh-unique", Emoji: "😂"},
			{FileID: "cry-file", FileUniqueID: "cry-unique", Emoji: "😂"},
		},
	}
}

func TestSendByIDDistinguishesStickersWithTheSameEmoji(t *testing.T) {
	api := &fakeTelegramAPI{
		set: testStickerSet(),
		media: map[string][]byte{
			"laugh-unique": []byte("laugh-image"),
			"cry-unique":   []byte("cry-image"),
		},
	}
	describer := &fakeStickerDescriber{descriptions: map[string]string{
		"laugh-image": "角色捧腹大笑，语气非常开心",
		"cry-image":   "角色泪流满面，显得委屈难过",
	}}
	catalog, cache := newTestCatalog(
		t,
		filepath.Join(t.TempDir(), "cache.sqlite"),
		describer,
	)
	defer func() { _ = cache.Close() }()
	service, err := newStickerService(api, "set", catalog)
	if err != nil {
		t.Fatal(err)
	}

	described, err := service.DescribedSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	laughID := candidateID("laugh-unique")
	cryID := candidateID("cry-unique")
	if len(described.Stickers) != 2 ||
		described.Stickers[0].ID != laughID ||
		described.Stickers[1].ID != cryID ||
		!strings.Contains(described.Stickers[1].Description, "委屈") {
		t.Fatalf("unexpected described stickers: %#v", described.Stickers)
	}
	result, err := service.SendByID(context.Background(), sendStickerInput{
		ChatID:    "-100123",
		Text:      "给你",
		StickerID: strings.ToLower(cryID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || api.sentFileID != "cry-file" || api.sentText != "给你" {
		t.Fatalf("unexpected send result: %#v, file=%q text=%q", result, api.sentFileID, api.sentText)
	}
}

func TestDescriptionCachePersistsAcrossCatalogInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.sqlite")
	api := &fakeTelegramAPI{
		set: telegramStickerSet{
			Name:     "set",
			Stickers: []telegramSticker{{FileID: "f1", FileUniqueID: "u1"}},
		},
		media: map[string][]byte{"u1": []byte("image")},
	}
	describer := &fakeStickerDescriber{descriptions: map[string]string{"image": "挥手问候"}}

	catalog1, cache1 := newTestCatalog(t, path, describer)
	service1, err := newStickerService(api, "set", catalog1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service1.DescribedSet(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := cache1.Close(); err != nil {
		t.Fatal(err)
	}

	catalog2, cache2 := newTestCatalog(t, path, describer)
	defer func() { _ = cache2.Close() }()
	service2, err := newStickerService(api, "set", catalog2)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service2.DescribedSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stickers) != 1 || result.Stickers[0].Description != "挥手问候" {
		t.Fatalf("unexpected cached result: %#v", result)
	}
	if describer.calls != 1 || api.downloadCalls != 1 {
		t.Fatalf("cache miss: describe calls=%d downloads=%d", describer.calls, api.downloadCalls)
	}
}

func TestExternalDescriptionProfileSwitchesCacheWithoutAutomaticRecognition(t *testing.T) {
	t.Parallel()

	api := &fakeTelegramAPI{
		set: telegramStickerSet{Name: "set", Stickers: []telegramSticker{{
			FileID: "f1", FileUniqueID: "u1", Emoji: "👋",
		}}},
		media: map[string][]byte{"u1": []byte("image")},
	}
	describer := &fakeStickerDescriber{descriptions: map[string]string{"image": "旧模型描述"}}
	catalog, cache := newTestCatalog(t, filepath.Join(t.TempDir(), "cache.sqlite"), describer)
	defer func() { _ = cache.Close() }()
	service, err := newStickerService(api, "set", catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DescribedSet(context.Background()); err != nil {
		t.Fatal(err)
	}
	if describer.calls != 1 {
		t.Fatalf("legacy describer calls = %d", describer.calls)
	}
	if err := service.ActivateDescriptionProfile(context.Background(), "web-model", "web-prompt-v1"); err != nil {
		t.Fatal(err)
	}
	view, err := service.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.PendingCount != 1 || view.ReadyCount != 0 || describer.calls != 1 {
		t.Fatalf("profile switch should be cache-only: view=%#v calls=%d", view, describer.calls)
	}
	exposed, err := service.CachedDescribedSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stickerID := candidateID("u1")
	if len(exposed.Stickers) != 1 || exposed.Stickers[0].ID != stickerID || exposed.Stickers[0].Description != "" {
		t.Fatalf("pending sticker was not exposed in stable catalog: %#v", exposed)
	}
	if _, err := service.SendByID(context.Background(), sendStickerInput{
		ChatID: "chat-1", StickerID: stickerID,
	}); err != nil {
		t.Fatalf("send pending sticker by stable ID: %v", err)
	}
	if api.sentChatID != "chat-1" || api.sentFileID != "f1" {
		t.Fatalf("pending sticker delivery = chat %q file %q", api.sentChatID, api.sentFileID)
	}
	stored, err := service.StoreDescription(context.Background(), stickerID, "web-model", "web-prompt-v1", "新模型描述")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Description != "新模型描述" {
		t.Fatalf("stored result = %#v", stored)
	}
	view, err = service.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.ReadyCount != 1 || view.Stickers[0].Description != "新模型描述" {
		t.Fatalf("external profile catalog = %#v", view)
	}
	if err := service.ActivateDescriptionProfile(context.Background(), describer.Model(), describer.PromptVersion()); err != nil {
		t.Fatal(err)
	}
	view, err = service.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Stickers[0].Description != "旧模型描述" || describer.calls != 1 {
		t.Fatalf("old cache was not reused: view=%#v calls=%d", view, describer.calls)
	}
}

func TestExternalRecognitionFailureIsStoredForManualRetry(t *testing.T) {
	t.Parallel()

	api := &fakeTelegramAPI{
		set: telegramStickerSet{Name: "set", Stickers: []telegramSticker{{
			FileID: "f1", FileUniqueID: "u1", Emoji: "👋",
		}}},
		media: map[string][]byte{"u1": []byte("image")},
	}
	describer := &fakeStickerDescriber{}
	catalog, cache := newTestCatalog(t, filepath.Join(t.TempDir(), "cache.sqlite"), describer)
	defer func() { _ = cache.Close() }()
	service, err := newStickerService(api, "set", catalog)
	if err != nil {
		t.Fatal(err)
	}
	stickerID := candidateID("u1")
	entry, err := service.StoreRecognition(
		context.Background(), stickerID, "web-model", "web-prompt-v1",
		descriptionStatusFailed, "private provider error", 3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Status != descriptionStatusFailed || entry.Description != "" || entry.Attempts != 3 {
		t.Fatalf("stored failure = %#v", entry)
	}
	view, err := service.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.FailedCount != 1 || view.PendingCount != 0 || view.Stickers[0].Status != descriptionStatusFailed {
		t.Fatalf("failure catalog = %#v", view)
	}
}

func TestStickerSetMetadataCacheIsPermanentAcrossServiceRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.sqlite")
	firstAPI := &fakeTelegramAPI{set: telegramStickerSet{
		Name: "set",
		Stickers: []telegramSticker{{
			FileID:       "file-original",
			FileUniqueID: "unique-original",
			Emoji:        "👋",
		}},
	}}
	catalog1, cache1 := newTestCatalog(t, path, &fakeStickerDescriber{})
	service1, err := newStickerService(firstAPI, "set", catalog1)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		set, getErr := service1.getStickerSet(context.Background())
		if getErr != nil {
			t.Fatal(getErr)
		}
		if set.Stickers[0].FileID != "file-original" {
			t.Fatalf("unexpected first-process set: %#v", set)
		}
	}
	if firstAPI.getSetCalls != 1 {
		t.Fatalf("Telegram getStickerSet calls = %d, want 1", firstAPI.getSetCalls)
	}
	if err := cache1.Close(); err != nil {
		t.Fatal(err)
	}

	secondAPI := &fakeTelegramAPI{set: telegramStickerSet{
		Name: "set",
		Stickers: []telegramSticker{{
			FileID:       "file-remote-newer",
			FileUniqueID: "unique-remote-newer",
		}},
	}}
	catalog2, cache2 := newTestCatalog(t, path, &fakeStickerDescriber{})
	defer func() { _ = cache2.Close() }()
	service2, err := newStickerService(secondAPI, "set", catalog2)
	if err != nil {
		t.Fatal(err)
	}
	set, err := service2.getStickerSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if set.Stickers[0].FileID != "file-original" || secondAPI.getSetCalls != 0 {
		t.Fatalf(
			"persistent metadata cache missed: set=%#v Telegram calls=%d",
			set,
			secondAPI.getSetCalls,
		)
	}
}

func TestStickerSetMetadataChangesOnlyOnExplicitRefresh(t *testing.T) {
	api := &fakeTelegramAPI{set: telegramStickerSet{
		Name:     "set",
		Stickers: []telegramSticker{{FileID: "file-old", FileUniqueID: "unique-old"}},
	}}
	catalog, cache := newTestCatalog(
		t,
		filepath.Join(t.TempDir(), "cache.sqlite"),
		&fakeStickerDescriber{},
	)
	defer func() { _ = cache.Close() }()
	service, err := newStickerService(api, "set", catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.getStickerSet(context.Background()); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	api.set = telegramStickerSet{
		Name:     "set",
		Stickers: []telegramSticker{{FileID: "file-new", FileUniqueID: "unique-new"}},
	}
	api.mu.Unlock()

	set, err := service.getStickerSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if set.Stickers[0].FileID != "file-old" || api.getSetCalls != 1 {
		t.Fatalf("ordinary read refreshed metadata: set=%#v calls=%d", set, api.getSetCalls)
	}
	set, err = service.RefreshStickerSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if set.Stickers[0].FileID != "file-new" || api.getSetCalls != 2 {
		t.Fatalf("explicit refresh failed: set=%#v calls=%d", set, api.getSetCalls)
	}
}

func TestFailedDescriptionIsCachedWithoutInfiniteRetry(t *testing.T) {
	api := &fakeTelegramAPI{
		set: telegramStickerSet{
			Name:     "set",
			Stickers: []telegramSticker{{FileID: "f1", FileUniqueID: "u1"}},
		},
		media: map[string][]byte{"u1": []byte("image")},
	}
	describer := &fakeStickerDescriber{err: errors.New("vision unavailable")}
	catalog, cache := newTestCatalog(
		t,
		filepath.Join(t.TempDir(), "cache.sqlite"),
		describer,
	)
	defer func() { _ = cache.Close() }()
	service, err := newStickerService(api, "set", catalog)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, err := service.DescribedSet(context.Background())
		if err == nil || !strings.Contains(err.Error(), "no successfully described stickers") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if describer.calls != stickerDescriptionMaxAttempts || api.downloadCalls != 1 {
		t.Fatalf("failed conversion retried: describe calls=%d downloads=%d", describer.calls, api.downloadCalls)
	}
	entry, found, err := cache.Get(
		context.Background(),
		"u1",
		describer.Model(),
		describer.PromptVersion(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !found || entry.Status != descriptionStatusFailed ||
		entry.Attempts != stickerDescriptionMaxAttempts {
		t.Fatalf("unexpected failure cache: %#v found=%v", entry, found)
	}
}

func TestCatalogAndManualRecognitionRetry(t *testing.T) {
	api := &fakeTelegramAPI{
		set: telegramStickerSet{
			Name:     "set",
			Stickers: []telegramSticker{{FileID: "f1", FileUniqueID: "u1", Emoji: "👋"}},
		},
		media: map[string][]byte{"u1": []byte("image")},
	}
	describer := &fakeStickerDescriber{err: errors.New("vision unavailable")}
	catalog, cache := newTestCatalog(
		t,
		filepath.Join(t.TempDir(), "cache.sqlite"),
		describer,
	)
	defer func() { _ = cache.Close() }()
	service, err := newStickerService(api, "set", catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DescribedSet(context.Background()); err == nil {
		t.Fatal("initial recognition should fail")
	}
	view, err := service.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.FailedCount != 1 || len(view.Stickers) != 1 || view.Stickers[0].Status != descriptionStatusFailed {
		t.Fatalf("failed catalog = %#v", view)
	}

	describer.mu.Lock()
	describer.err = nil
	describer.descriptions = map[string]string{"image": "角色微笑挥手打招呼"}
	describer.mu.Unlock()
	stickerID := candidateID("u1")
	entry, err := service.RetryDescription(context.Background(), strings.ToLower(stickerID))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Status != descriptionStatusReady || !strings.Contains(entry.Description, "挥手") {
		t.Fatalf("retried entry = %#v", entry)
	}
	results, err := service.Search(context.Background(), "挥手", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != stickerID {
		t.Fatalf("search results = %#v", results)
	}
}

func TestDescriptionRetrySucceedsAndCachesResult(t *testing.T) {
	api := &fakeTelegramAPI{
		set: telegramStickerSet{
			Name:     "set",
			Stickers: []telegramSticker{{FileID: "f1", FileUniqueID: "u1"}},
		},
		media: map[string][]byte{"u1": []byte("image")},
	}
	describer := &fakeStickerDescriber{
		descriptions: map[string]string{"image": "角色开心挥手"},
		failures:     2,
	}
	catalog, cache := newTestCatalog(
		t,
		filepath.Join(t.TempDir(), "cache.sqlite"),
		describer,
	)
	defer func() { _ = cache.Close() }()
	service, err := newStickerService(api, "set", catalog)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := service.DescribedSet(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Stickers) != 1 || result.Stickers[0].Description != "角色开心挥手" {
			t.Fatalf("unexpected result: %#v", result)
		}
	}
	if describer.calls != 3 {
		t.Fatalf("describe calls = %d, want 3", describer.calls)
	}
	entry, found, err := cache.Get(
		context.Background(),
		"u1",
		describer.Model(),
		describer.PromptVersion(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !found || entry.Status != descriptionStatusReady || entry.Attempts != 3 {
		t.Fatalf("unexpected success cache: %#v found=%v", entry, found)
	}
}

func TestStickerGuideAndMCPExposeStableCompactTools(t *testing.T) {
	api := &fakeTelegramAPI{
		set: telegramStickerSet{
			Name:     "set",
			Stickers: []telegramSticker{{FileID: "f1", FileUniqueID: "u1", Emoji: "👋"}},
		},
		media: map[string][]byte{"u1": []byte("image")},
	}
	catalog, cache := newTestCatalog(
		t,
		filepath.Join(t.TempDir(), "cache.sqlite"),
		&fakeStickerDescriber{descriptions: map[string]string{"image": "角色微笑挥手打招呼"}},
	)
	defer func() { _ = cache.Close() }()
	service, err := newStickerService(api, "set", catalog)
	if err != nil {
		t.Fatal(err)
	}
	guide, err := stickerGuide(context.Background(), service)
	if err != nil {
		t.Fatal(err)
	}
	stickerID := candidateID("u1")
	if !strings.Contains(guide, stickerID+"：角色微笑挥手打招呼") ||
		!strings.Contains(guide, "不要只看原始 emoji") {
		t.Fatalf("unexpected guide: %s", guide)
	}

	described, err := service.CachedDescribedSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := newMCPServer(func(*mcp.CallToolRequest) (*stickerService, error) {
		return service, nil
	}, described, "")
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clientSession.Close() }()

	tools, err := clientSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 ||
		tools.Tools[0].Name != sendToolName ||
		tools.Tools[0].Description != sendToolDescription {
		t.Fatalf("unexpected tools: %#v", tools.Tools)
	}
	schema, ok := tools.Tools[0].InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("input schema = %#v", tools.Tools[0].InputSchema)
	}
	properties, _ := schema["properties"].(map[string]any)
	stickerSchema, _ := properties["sticker_id"].(map[string]any)
	description, _ := stickerSchema["description"].(string)
	if !strings.Contains(description, stickerID+"：角色微笑挥手打招呼") {
		t.Fatalf("sticker catalog missing from schema: %#v", stickerSchema)
	}
}

func TestCachedStickerGuideDoesNotWaitForBackgroundParsing(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	api := &fakeTelegramAPI{
		set: telegramStickerSet{
			Name:     "set",
			Stickers: []telegramSticker{{FileID: "f1", FileUniqueID: "u1", Emoji: "👋"}},
		},
		media:         map[string][]byte{"u1": []byte("image")},
		getSetStarted: started,
		getSetRelease: release,
	}
	catalog, cache := newTestCatalog(
		t,
		filepath.Join(t.TempDir(), "cache.sqlite"),
		&fakeStickerDescriber{descriptions: map[string]string{"image": "角色微笑挥手"}},
	)
	defer func() { _ = cache.Close() }()
	service, err := newStickerService(api, "set", catalog)
	if err != nil {
		t.Fatal(err)
	}

	guideResult := make(chan string, 1)
	go func() {
		guide, guideErr := cachedStickerGuide(context.Background(), service)
		if guideErr != nil {
			guideResult <- guideErr.Error()
			return
		}
		guideResult <- guide
	}()
	select {
	case guide := <-guideResult:
		if !strings.Contains(guide, "正在后台解析") {
			t.Fatalf("unexpected first guide: %s", guide)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("cached guide waited for Telegram sticker parsing")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background parsing did not start")
	}
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for {
		cached, cacheErr := service.CachedDescribedSet(context.Background())
		if cacheErr != nil {
			t.Fatal(cacheErr)
		}
		if len(cached.Stickers) == 1 {
			guide, guideErr := cachedStickerGuide(context.Background(), service)
			if guideErr != nil {
				t.Fatal(guideErr)
			}
			if !strings.Contains(guide, candidateID("u1")+"：角色微笑挥手") {
				t.Fatalf("unexpected cached guide: %s", guide)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background parsing did not populate the cache")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStickerServiceProviderIsolatesTokenAndStickerSet(t *testing.T) {
	catalog, cache := newTestCatalog(
		t,
		filepath.Join(t.TempDir(), "cache.sqlite"),
		&fakeStickerDescriber{},
	)
	defer func() { _ = cache.Close() }()
	provider, err := newStickerServiceProvider(
		"set",
		"stdio-fallback",
		"http://telegram.test",
		&http.Client{},
		catalog,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := func(token, setName string) *mcp.CallToolRequest {
		header := make(http.Header)
		header.Set(telegramBotHeader, token)
		header.Set(telegramStickerSetHeader, setName)
		return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: header}}
	}
	serviceA1, err := provider.Resolve(request("bot-a-token", "set-a"))
	if err != nil {
		t.Fatal(err)
	}
	serviceA2, err := provider.Resolve(request("bot-a-token", "set-a"))
	if err != nil {
		t.Fatal(err)
	}
	serviceB, err := provider.Resolve(request("bot-b-token", "set-b"))
	if err != nil {
		t.Fatal(err)
	}
	serviceAOtherSet, err := provider.Resolve(request("bot-a-token", "set-b"))
	if err != nil {
		t.Fatal(err)
	}
	if serviceA1 != serviceA2 || serviceA1 == serviceB || serviceA1 == serviceAOtherSet {
		t.Fatal("unexpected provider cache isolation")
	}
	if serviceA1.setName != "set-a" || serviceB.setName != "set-b" {
		t.Fatalf("unexpected sets: %q %q", serviceA1.setName, serviceB.setName)
	}
}

func TestStickerServiceProviderMergesSetsWithStableIDs(t *testing.T) {
	catalog, cache := newTestCatalog(
		t,
		filepath.Join(t.TempDir(), "cache.sqlite"),
		&fakeStickerDescriber{},
	)
	defer func() { _ = cache.Close() }()
	provider, err := newStickerServiceProvider(
		"fallback",
		"stdio-fallback",
		"http://telegram.test",
		&http.Client{},
		catalog,
	)
	if err != nil {
		t.Fatal(err)
	}
	header := func(sets string) http.Header {
		value := make(http.Header)
		value.Set(telegramBotHeader, "bot-token")
		value.Set(telegramStickerSetHeader, sets)
		return value
	}
	first, err := provider.ResolveHeader(header("beta,alpha"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.ResolveHeader(header("alpha,beta"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("set list order changed the cached service")
	}
	if len(first.sets) != 2 || first.sets[0].setName != "alpha" || first.sets[1].setName != "beta" {
		t.Fatalf("merged sets = %#v", first.sets)
	}
	if collectionStickerID("alpha", "S001") == collectionStickerID("beta", "S001") {
		t.Fatal("stable IDs collided across Sticker Sets")
	}
}

func TestTelegramClientGetsSetDownloadsMediaAndSends(t *testing.T) {
	var methods []string
	var forms []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, filepath.Base(req.URL.Path))
		if strings.HasPrefix(req.URL.Path, "/file/") {
			w.Header().Set("Content-Type", "image/webp")
			_, _ = w.Write([]byte("webp-data"))
			return
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		forms = append(forms, req.Form)
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(req.URL.Path) {
		case "getStickerSet":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"name":"set","stickers":[{"file_id":"f1","file_unique_id":"u1","emoji":"😂"}]}}`))
		case "getFile":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"f1","file_unique_id":"u1","file_path":"stickers/a.webp"}}`))
		case "sendSticker":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77}}`))
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	client, err := newTelegramClient("123:secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL
	set, err := client.GetStickerSet(context.Background(), "set")
	if err != nil {
		t.Fatal(err)
	}
	data, mimeType, err := client.DownloadStickerMedia(context.Background(), set.Stickers[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "webp-data" || mimeType != "image/webp" {
		t.Fatalf("unexpected media: %q %q", data, mimeType)
	}
	message, err := client.SendSticker(context.Background(), "-1001", "f1")
	if err != nil {
		t.Fatal(err)
	}
	if message.MessageID != 77 ||
		strings.Join(methods, ",") != "getStickerSet,getFile,a.webp,sendSticker" ||
		forms[0].Get("name") != "set" ||
		forms[1].Get("file_id") != "f1" ||
		forms[2].Get("chat_id") != "-1001" {
		t.Fatalf("unexpected Telegram calls: methods=%#v forms=%#v", methods, forms)
	}
}

func TestLoadDotenvPreservesExistingEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(
		"TELEGRAM_STICKER_MCP_SET_NAME='set_name'\n"+
			"TELEGRAM_STICKER_MCP_ADDR=\"127.0.0.1:9000\"\n"+
			"TELEGRAM_STICKER_MCP_BOT_TOKEN=file-token\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEGRAM_STICKER_MCP_BOT_TOKEN", "existing-token")
	_ = os.Unsetenv("TELEGRAM_STICKER_MCP_SET_NAME")
	_ = os.Unsetenv("TELEGRAM_STICKER_MCP_ADDR")
	if err := loadDotenv(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("TELEGRAM_STICKER_MCP_BOT_TOKEN") != "existing-token" ||
		os.Getenv("TELEGRAM_STICKER_MCP_SET_NAME") != "set_name" ||
		os.Getenv("TELEGRAM_STICKER_MCP_ADDR") != "127.0.0.1:9000" {
		t.Fatal("dotenv values were not loaded with environment precedence")
	}
}

func setRequiredConfigEnvironment(t *testing.T, transport string) {
	t.Helper()
	t.Setenv("TELEGRAM_STICKER_MCP_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_STICKER_MCP_SET_NAME", "set")
	t.Setenv("TELEGRAM_STICKER_MCP_TRANSPORT", transport)
	t.Setenv("TELEGRAM_STICKER_MCP_VISION_API_KEY", "vision-key")
	t.Setenv("TELEGRAM_STICKER_MCP_VISION_MODEL", "legacy-vision-model-id")
	t.Setenv("TELEGRAM_STICKER_MCP_CACHE_PATH", filepath.Join(t.TempDir(), "cache.sqlite"))
	t.Setenv("TELEGRAM_STICKER_MCP_ADDR", "")
	t.Setenv("TELEGRAM_STICKER_MCP_AUTH_TOKEN", "")
}

func TestLoadConfigAllowsHTTPWithoutGlobalTelegramToken(t *testing.T) {
	setRequiredConfigEnvironment(t, "http")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BotToken != "" ||
		cfg.Transport != "http" ||
		cfg.VisionModel != "legacy-vision-model-id" {
		t.Fatalf("unexpected HTTP config: %#v", cfg)
	}
}

func TestLoadConfigRequiresBearerForNonLoopbackHTTP(t *testing.T) {
	setRequiredConfigEnvironment(t, "http")
	t.Setenv("TELEGRAM_STICKER_MCP_ADDR", "0.0.0.0:8091")
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_STICKER_MCP_AUTH_TOKEN") {
		t.Fatalf("unexpected public HTTP config error: %v", err)
	}

	t.Setenv("TELEGRAM_STICKER_MCP_AUTH_TOKEN", "service-secret")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "0.0.0.0:8091" || cfg.AuthToken != "service-secret" {
		t.Fatalf("authenticated public HTTP config = %#v", cfg)
	}
}

func TestLoadConfigAllowsDynamicSetAndIPv6LoopbackHTTP(t *testing.T) {
	setRequiredConfigEnvironment(t, "http")
	t.Setenv("TELEGRAM_STICKER_MCP_SET_NAME", "")
	t.Setenv("TELEGRAM_STICKER_MCP_ADDR", "[::1]:8091")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SetName != "" || !isLoopbackListenAddr(cfg.Addr) {
		t.Fatalf("dynamic-set loopback config = %#v", cfg)
	}
}

func TestLoadConfigRequiresTelegramTokenForStdio(t *testing.T) {
	setRequiredConfigEnvironment(t, "stdio")
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_STICKER_MCP_BOT_TOKEN") {
		t.Fatalf("unexpected stdio config error: %v", err)
	}
}

func TestRequireBearer(t *testing.T) {
	handler := requireBearer("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		name   string
		header string
		status int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "wrong", header: "Bearer wrong", status: http.StatusUnauthorized},
		{name: "valid", header: "Bearer secret", status: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
			req.Header.Set("Authorization", tc.header)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
		})
	}
}

func TestSafeStickerPreviewContentType(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "image/webp; charset=binary", want: "image/webp"},
		{input: "video/webm", want: "video/webm"},
		{input: "text/html", want: "application/octet-stream"},
		{input: "image/svg+xml", want: "application/octet-stream"},
	} {
		if got := safeStickerPreviewContentType(test.input); got != test.want {
			t.Fatalf("safeStickerPreviewContentType(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}
