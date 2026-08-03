package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/mcp"
)

func TestDeriveStickerAPIEndpoint(t *testing.T) {
	t.Parallel()

	got, err := deriveStickerAPIEndpoint(
		"http://sticker.internal:8091/nested/mcp?ignored=true",
		"/api/stickers/S001/preview",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://sticker.internal:8091/nested/api/stickers/S001/preview" {
		t.Fatalf("endpoint = %q", got)
	}
	for _, invalid := range []string{
		"file:///tmp/sticker/mcp",
		"http://user:secret@sticker.internal/mcp",
		"http://sticker.internal/not-mcp",
	} {
		if _, err := deriveStickerAPIEndpoint(invalid, "/api/catalog"); err == nil {
			t.Fatalf("deriveStickerAPIEndpoint(%q) should fail", invalid)
		}
	}
}

func TestTelegramStickerConnectionDetection(t *testing.T) {
	t.Parallel()

	if !isTelegramStickerConnection(mcp.Connection{
		Name: "custom",
		ToolsCache: []mcp.ToolDescriptor{{
			Name: "search_telegram_stickers",
		}},
	}) {
		t.Fatal("sticker tool cache should identify the connection")
	}
	if !isTelegramStickerConnection(mcp.Connection{Name: "Sticker"}) {
		t.Fatal("legacy Sticker connection name should remain supported")
	}
	if isTelegramStickerConnection(mcp.Connection{Name: "search"}) {
		t.Fatal("unrelated connection was classified as Sticker")
	}
}

func TestTelegramStickerEndpointKeepsInvalidConnectionForFailurePersistence(t *testing.T) {
	t.Parallel()

	invalid := mcp.Connection{
		ID: "invalid-sticker", Name: "Sticker", Type: "http",
		Config: map[string]any{"url": "file:///tmp/sticker/mcp"},
	}
	conn, endpoint, err := telegramStickerEndpointFromConnections(
		[]mcp.Connection{invalid}, "/api/stickers/S001/preview",
	)
	if err == nil || conn.ID != invalid.ID || endpoint != "" {
		t.Fatalf("invalid endpoint result = conn=%#v endpoint=%q err=%v", conn, endpoint, err)
	}

	valid := mcp.Connection{
		ID: "valid-sticker", Name: "Sticker", Type: "http",
		Config: map[string]any{"url": "http://sticker.internal/mcp"},
	}
	conn, endpoint, err = telegramStickerEndpointFromConnections(
		[]mcp.Connection{invalid, valid}, "/api/catalog",
	)
	if err != nil || conn.ID != valid.ID || endpoint != "http://sticker.internal/api/catalog" {
		t.Fatalf("valid fallback result = conn=%#v endpoint=%q err=%v", conn, endpoint, err)
	}
}

func TestTelegramStickerRequestForwardsStoredHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got := req.Header.Get("X-Telegram-Sticker-Set"); got != "set-name" {
			t.Errorf("forwarded header = %q", got)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	original := telegramStickerHTTPClient
	telegramStickerHTTPClient = server.Client()
	defer func() { telegramStickerHTTPClient = original }()

	handler := &MCPHandler{}
	resp, err := handler.doTelegramStickerRequest(context.Background(), mcp.Connection{
		Config: map[string]any{
			"headers": map[string]any{"X-Telegram-Sticker-Set": "set-name"},
		},
	}, http.MethodGet, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestStoreTelegramStickerRecognitionFailurePersistsAttemptCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/stickers/S001/recognition" {
			t.Errorf("request path = %q", req.URL.Path)
		}
		var result telegramStickerRecognitionResult
		if err := json.NewDecoder(req.Body).Decode(&result); err != nil {
			t.Errorf("decode recognition result: %v", err)
		}
		if result.Status != "failed" || result.Attempts != 3 || result.Model != "vision" || result.PromptVersion != "v1" {
			t.Errorf("recognition failure result = %#v", result)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	original := telegramStickerHTTPClient
	telegramStickerHTTPClient = server.Client()
	defer func() { telegramStickerHTTPClient = original }()

	handler := &MCPHandler{}
	err := handler.storeTelegramStickerRecognitionFailure(
		context.Background(),
		mcp.Connection{Config: map[string]any{"url": server.URL + "/mcp"}},
		"S001", "vision", "v1", 3,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeTelegramStickerSetNamesIsStableAndRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	names, err := normalizeTelegramStickerSetNames([]string{"Zeta_by_bot", "alpha_by_bot", "ALPHA_by_bot"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, ","); got != "alpha_by_bot,Zeta_by_bot" {
		t.Fatalf("normalized names = %q", got)
	}
	for _, input := range [][]string{nil, {"contains space"}, {"set-a"}} {
		if _, err := normalizeTelegramStickerSetNames(input); err == nil {
			t.Fatalf("input %#v should be rejected", input)
		}
	}
}

func TestTelegramStickerConnectionWithSetsPreservesPrivateHeaders(t *testing.T) {
	t.Parallel()
	candidate, headers := telegramStickerConnectionWithSets(mcp.Connection{Config: map[string]any{
		"url": "http://sticker/mcp",
		"headers": map[string]any{
			"Authorization":          "Bearer private",
			"X-Telegram-Bot-Token":   "bot-private",
			"x-telegram-sticker-set": "old",
		},
	}}, []string{"alpha", "beta"})
	if headers[telegramStickerSetHeader] != "alpha,beta" || headers["Authorization"] != "Bearer private" || headers["X-Telegram-Bot-Token"] != "bot-private" {
		t.Fatalf("updated headers = %#v", headers)
	}
	stored, _ := candidate.Config["headers"].(map[string]any)
	if stored[telegramStickerSetHeader] != "alpha,beta" {
		t.Fatalf("candidate headers = %#v", stored)
	}
}

func TestDecodeBoundedJSONRejectsOversizedPayload(t *testing.T) {
	t.Parallel()

	var output map[string]any
	err := decodeBoundedJSON(io.NopCloser(strings.NewReader(`{"value":"too long"}`)), 8, &output)
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("error = %v", err)
	}
}

func TestPendingTelegramStickerRecognitionTasksDeduplicatesMergedCatalog(t *testing.T) {
	t.Parallel()

	catalog := TelegramStickerCatalog{
		PendingCount: 2,
		Stickers: []TelegramStickerCatalogEntry{
			{ID: "set-a:S001", Status: "pending", Attempts: 2},
			{ID: "set-a:S002", Status: "ready"},
		},
		Sets: []TelegramStickerSetCatalog{{
			Name: "set-a",
			Stickers: []TelegramStickerCatalogEntry{
				{ID: "set-a:S001", Status: "pending"},
				{ID: "set-a:S003", Status: ""},
				{ID: "set-a:S004", Status: "failed"},
			},
		}},
	}
	tasks := pendingTelegramStickerRecognitionTasks(
		catalog, "bot-1", "identity-1", "vision-1", "prompt-1",
	)
	if len(tasks) != 2 || tasks[0].StickerID != "set-a:S001" || tasks[1].StickerID != "set-a:S003" {
		t.Fatalf("pending tasks = %#v", tasks)
	}
	if tasks[0].BotID != "bot-1" || tasks[0].ChannelIdentityID != "identity-1" || tasks[0].Model != "vision-1" || tasks[0].Attempts != 2 {
		t.Fatalf("task routing = %#v", tasks[0])
	}
	if withoutModel := pendingTelegramStickerRecognitionTasks(catalog, "bot-1", "identity-1", "", "prompt-1"); withoutModel != nil {
		t.Fatalf("tasks without a recognition model = %#v", withoutModel)
	}
}

func TestTelegramStickerRecognitionQueueDeduplicatesAndRefreshesAfterBatch(t *testing.T) {
	recognized := make(chan string, 2)
	refreshed := make(chan string, 1)
	queue := newTelegramStickerRecognitionQueue(
		slog.New(slog.DiscardHandler),
		1,
		func(_ context.Context, task telegramStickerRecognitionTask) (bool, error) {
			recognized <- task.StickerID
			return true, nil
		},
		func(_ context.Context, botID string) error {
			refreshed <- botID
			return nil
		},
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := queue.Close(ctx); err != nil {
			t.Errorf("close queue: %v", err)
		}
	})

	tasks := []telegramStickerRecognitionTask{
		{BotID: "bot-1", ChannelIdentityID: "identity-1", StickerID: "S001", Model: "vision", PromptVersion: "v1"},
		{BotID: "bot-1", ChannelIdentityID: "identity-1", StickerID: "S001", Model: "vision", PromptVersion: "v1"},
		{BotID: "bot-1", ChannelIdentityID: "identity-1", StickerID: "S002", Model: "vision", PromptVersion: "v1"},
	}
	if count := queue.Enqueue(tasks); count != 2 {
		t.Fatalf("enqueued = %d, want 2", count)
	}

	seen := map[string]bool{}
	for range 2 {
		select {
		case stickerID := <-recognized:
			seen[stickerID] = true
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for recognition")
		}
	}
	if !seen["S001"] || !seen["S002"] || len(seen) != 2 {
		t.Fatalf("recognized = %#v", seen)
	}
	select {
	case botID := <-refreshed:
		if botID != "bot-1" {
			t.Fatalf("refreshed bot = %q", botID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for schema refresh")
	}
}

func TestTelegramStickerRecognitionQueueBacksOffAndCapsAttempts(t *testing.T) {
	t.Parallel()

	current := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var seenAttempts []int
	task := telegramStickerRecognitionTask{
		BotID: "bot-1", ChannelIdentityID: "identity-1", StickerID: "S001",
		Model: "vision", PromptVersion: "v1",
	}
	queue := &telegramStickerRecognitionQueue{
		ctx: ctx, cancel: cancel,
		jobs: make(chan telegramStickerRecognitionTask, 1),
		recognize: func(_ context.Context, task telegramStickerRecognitionTask) (bool, error) {
			seenAttempts = append(seenAttempts, task.Attempts)
			return false, errors.New("recognition failed before status could be stored")
		},
		refresh: func(context.Context, string) error {
			t.Fatal("non-visible failure status must not refresh the model schema")
			return nil
		},
		logger:    slog.New(slog.DiscardHandler),
		queued:    map[string]telegramStickerRecognitionTask{telegramStickerRecognitionTaskKey(task.BotID, task.StickerID): task},
		retries:   make(map[string]telegramStickerRecognitionRetryState),
		dirtyBots: make(map[string]bool),
		now:       func() time.Time { return current },
	}

	queue.runTask(task)
	if count := queue.Enqueue([]telegramStickerRecognitionTask{task}); count != 0 {
		t.Fatalf("immediate retry enqueued %d tasks", count)
	}

	current = current.Add(telegramStickerRecognitionRetryBaseDelay)
	if count := queue.Enqueue([]telegramStickerRecognitionTask{task}); count != 1 {
		t.Fatalf("first backoff retry enqueued %d tasks, want 1", count)
	}
	second := <-queue.jobs
	queue.runTask(second)

	current = current.Add(telegramStickerRecognitionRetryDelay(2))
	if count := queue.Enqueue([]telegramStickerRecognitionTask{task}); count != 1 {
		t.Fatalf("second backoff retry enqueued %d tasks, want 1", count)
	}
	third := <-queue.jobs
	queue.runTask(third)

	current = current.Add(telegramStickerRecognitionRetryMaxDelay)
	if count := queue.Enqueue([]telegramStickerRecognitionTask{task}); count != 0 {
		t.Fatalf("retry past max attempts enqueued %d tasks before reset window", count)
	}
	current = current.Add(telegramStickerRecognitionRetryResetDelay)
	if count := queue.Enqueue([]telegramStickerRecognitionTask{task}); count != 1 {
		t.Fatalf("retry after reset window enqueued %d tasks, want 1", count)
	}
	restarted := <-queue.jobs
	if restarted.Attempts != 0 {
		t.Fatalf("reset retry retained %d prior attempts", restarted.Attempts)
	}
	if len(seenAttempts) != telegramStickerRecognitionMaxAttempts ||
		seenAttempts[0] != 0 || seenAttempts[1] != 1 || seenAttempts[2] != 2 {
		t.Fatalf("recognition attempts = %#v", seenAttempts)
	}
}

func TestTelegramStickerRecognitionQueueFullIsNonBlockingAndClean(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	queue := &telegramStickerRecognitionQueue{
		ctx: ctx, cancel: cancel,
		jobs:      make(chan telegramStickerRecognitionTask, 1),
		logger:    slog.New(slog.DiscardHandler),
		queued:    make(map[string]telegramStickerRecognitionTask),
		retries:   make(map[string]telegramStickerRecognitionRetryState),
		dirtyBots: make(map[string]bool),
		now:       time.Now,
	}
	queue.jobs <- telegramStickerRecognitionTask{StickerID: "already-buffered"}
	task := telegramStickerRecognitionTask{
		BotID: "bot-1", ChannelIdentityID: "identity-1", StickerID: "S001",
		Model: "vision", PromptVersion: "v1",
	}
	result := make(chan int, 1)
	go func() { result <- queue.Enqueue([]telegramStickerRecognitionTask{task}) }()
	select {
	case count := <-result:
		if count != 0 {
			t.Fatalf("full queue accepted %d tasks", count)
		}
	case <-time.After(time.Second):
		t.Fatal("Enqueue blocked on a full queue")
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.queued) != 0 {
		t.Fatalf("full queue retained deferred tasks: %#v", queue.queued)
	}
}

func TestTelegramStickerRecognitionQueueCloseClearsQueuedEntries(t *testing.T) {
	t.Parallel()

	queueCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	queue := &telegramStickerRecognitionQueue{
		ctx: queueCtx, cancel: cancel,
		jobs:   make(chan telegramStickerRecognitionTask, 1),
		logger: slog.New(slog.DiscardHandler),
		queued: map[string]telegramStickerRecognitionTask{
			telegramStickerRecognitionTaskKey("bot-1", "S001"): {BotID: "bot-1", StickerID: "S001"},
		},
		retries: map[string]telegramStickerRecognitionRetryState{
			telegramStickerRecognitionTaskKey("bot-1", "S001"): {Attempts: 1},
		},
		dirtyBots: map[string]bool{"bot-1": true},
		now:       time.Now,
	}
	if err := queue.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(queue.queued) != 0 || len(queue.retries) != 0 || len(queue.dirtyBots) != 0 {
		t.Fatalf("closed queue retained state: queued=%#v retries=%#v dirty=%#v", queue.queued, queue.retries, queue.dirtyBots)
	}
}
