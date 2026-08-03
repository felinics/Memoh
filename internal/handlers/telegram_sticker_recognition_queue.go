package handlers

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	telegramStickerRecognitionWorkerCount = 2
	telegramStickerRecognitionQueueSize   = 8192
	telegramStickerRecognitionTaskTimeout = 10 * time.Minute
	telegramStickerSchemaRefreshTimeout   = 30 * time.Second
)

type telegramStickerRecognitionTask struct {
	BotID             string
	ChannelIdentityID string
	StickerID         string
	Model             string
	PromptVersion     string
}

type (
	telegramStickerRecognitionFunc   func(context.Context, telegramStickerRecognitionTask) error
	telegramStickerSchemaRefreshFunc func(context.Context, string) error
)

type telegramStickerRecognitionQueue struct {
	ctx       context.Context
	cancel    context.CancelFunc
	jobs      chan telegramStickerRecognitionTask
	recognize telegramStickerRecognitionFunc
	refresh   telegramStickerSchemaRefreshFunc
	logger    *slog.Logger

	mu        sync.Mutex
	queued    map[string]telegramStickerRecognitionTask
	workers   sync.WaitGroup
	closeOnce sync.Once
}

func newTelegramStickerRecognitionQueue(
	logger *slog.Logger,
	workerCount int,
	recognize telegramStickerRecognitionFunc,
	refresh telegramStickerSchemaRefreshFunc,
) *telegramStickerRecognitionQueue {
	if logger == nil {
		logger = slog.Default()
	}
	if workerCount <= 0 {
		workerCount = 1
	}
	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // G118: cancel is retained by the queue and called by Close.
	queue := &telegramStickerRecognitionQueue{
		ctx: ctx, cancel: cancel,
		jobs:      make(chan telegramStickerRecognitionTask, telegramStickerRecognitionQueueSize),
		recognize: recognize, refresh: refresh,
		logger: logger.With(slog.String("worker", "telegram_sticker_recognition")),
		queued: make(map[string]telegramStickerRecognitionTask),
	}
	queue.workers.Add(workerCount)
	for range workerCount {
		go queue.runWorker()
	}
	return queue
}

func (q *telegramStickerRecognitionQueue) Enqueue(tasks []telegramStickerRecognitionTask) int {
	if q == nil || len(tasks) == 0 || q.ctx.Err() != nil {
		return 0
	}
	accepted := make([]telegramStickerRecognitionTask, 0, len(tasks))
	q.mu.Lock()
	for _, task := range tasks {
		task.BotID = strings.TrimSpace(task.BotID)
		task.ChannelIdentityID = strings.TrimSpace(task.ChannelIdentityID)
		task.StickerID = strings.TrimSpace(task.StickerID)
		task.Model = strings.TrimSpace(task.Model)
		task.PromptVersion = strings.TrimSpace(task.PromptVersion)
		if task.BotID == "" || task.ChannelIdentityID == "" || task.StickerID == "" || task.Model == "" {
			continue
		}
		key := telegramStickerRecognitionTaskKey(task.BotID, task.StickerID)
		if _, exists := q.queued[key]; exists {
			continue
		}
		q.queued[key] = task
		accepted = append(accepted, task)
	}
	q.mu.Unlock()

	enqueued := 0
	for _, task := range accepted {
		select {
		case q.jobs <- task:
			enqueued++
		case <-q.ctx.Done():
			q.complete(task)
			return enqueued
		}
	}
	return enqueued
}

func (q *telegramStickerRecognitionQueue) Close(ctx context.Context) error {
	if q == nil {
		return nil
	}
	q.closeOnce.Do(q.cancel)
	done := make(chan struct{})
	go func() {
		q.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *telegramStickerRecognitionQueue) runWorker() {
	defer q.workers.Done()
	for {
		if q.ctx.Err() != nil {
			return
		}
		select {
		case <-q.ctx.Done():
			return
		case task := <-q.jobs:
			q.runTask(task)
		}
	}
}

func (q *telegramStickerRecognitionQueue) runTask(task telegramStickerRecognitionTask) {
	ctx, cancel := context.WithTimeout(q.ctx, telegramStickerRecognitionTaskTimeout)
	err := errors.New("telegram sticker recognizer is unavailable")
	if q.recognize != nil {
		err = q.recognize(ctx, task)
	}
	cancel()
	if err != nil && !errors.Is(err, context.Canceled) {
		q.logger.Warn("Telegram sticker background recognition failed",
			slog.String("bot_id", task.BotID),
			slog.String("sticker_id", task.StickerID),
			slog.Any("error", err),
		)
	} else if err == nil {
		q.logger.Debug("Telegram sticker background recognition completed",
			slog.String("bot_id", task.BotID),
			slog.String("sticker_id", task.StickerID),
		)
	}

	if !q.complete(task) || q.refresh == nil || q.ctx.Err() != nil {
		return
	}
	refreshCtx, refreshCancel := context.WithTimeout(q.ctx, telegramStickerSchemaRefreshTimeout)
	defer refreshCancel()
	if refreshErr := q.refresh(refreshCtx, task.BotID); refreshErr != nil && !errors.Is(refreshErr, context.Canceled) {
		q.logger.Warn("Telegram sticker tool schema refresh failed",
			slog.String("bot_id", task.BotID),
			slog.Any("error", refreshErr),
		)
	}
}

func (q *telegramStickerRecognitionQueue) complete(task telegramStickerRecognitionTask) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.queued, telegramStickerRecognitionTaskKey(task.BotID, task.StickerID))
	for _, queued := range q.queued {
		if queued.BotID == task.BotID {
			return false
		}
	}
	return true
}

func telegramStickerRecognitionTaskKey(botID, stickerID string) string {
	return strings.TrimSpace(botID) + "\x00" + strings.TrimSpace(stickerID)
}

func pendingTelegramStickerRecognitionTasks(
	catalog TelegramStickerCatalog,
	botID, channelIdentityID, model, promptVersion string,
) []telegramStickerRecognitionTask {
	if strings.TrimSpace(model) == "" {
		return nil
	}
	seen := make(map[string]struct{}, catalog.PendingCount)
	tasks := make([]telegramStickerRecognitionTask, 0, catalog.PendingCount)
	appendEntry := func(entry TelegramStickerCatalogEntry) {
		stickerID := strings.TrimSpace(entry.ID)
		status := strings.ToLower(strings.TrimSpace(entry.Status))
		if stickerID == "" || status == "ready" || status == "failed" {
			return
		}
		if _, exists := seen[stickerID]; exists {
			return
		}
		seen[stickerID] = struct{}{}
		tasks = append(tasks, telegramStickerRecognitionTask{
			BotID: botID, ChannelIdentityID: channelIdentityID, StickerID: stickerID,
			Model: model, PromptVersion: promptVersion,
		})
	}
	for _, entry := range catalog.Stickers {
		appendEntry(entry)
	}
	for _, set := range catalog.Sets {
		for _, entry := range set.Stickers {
			appendEntry(entry)
		}
	}
	return tasks
}
