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

	telegramStickerRecognitionMaxAttempts     = 3
	telegramStickerRecognitionRetryBaseDelay  = 30 * time.Second
	telegramStickerRecognitionRetryMaxDelay   = 2 * time.Minute
	telegramStickerRecognitionRetryResetDelay = 30 * time.Minute
	telegramStickerRecognitionRetryStateTTL   = 24 * time.Hour
)

type telegramStickerRecognitionTask struct {
	BotID             string
	ChannelIdentityID string
	StickerID         string
	Model             string
	PromptVersion     string
	// Attempts is the number of completed attempts reported by the Sticker
	// service or retained by this process after a status write failed.
	Attempts int
}

type (
	// telegramStickerRecognitionFunc reports whether the model-visible Sticker
	// schema changed. Failure status updates deliberately report false because
	// pending and failed entries have the same model-facing fallback text.
	telegramStickerRecognitionFunc   func(context.Context, telegramStickerRecognitionTask) (schemaChanged bool, err error)
	telegramStickerSchemaRefreshFunc func(context.Context, string) error
)

type telegramStickerRecognitionRetryState struct {
	Model         string
	PromptVersion string
	Attempts      int
	RetryAfter    time.Time
	UpdatedAt     time.Time
}

type telegramStickerRecognitionQueue struct {
	ctx       context.Context
	cancel    context.CancelFunc
	jobs      chan telegramStickerRecognitionTask
	recognize telegramStickerRecognitionFunc
	refresh   telegramStickerSchemaRefreshFunc
	logger    *slog.Logger

	mu        sync.Mutex
	queued    map[string]telegramStickerRecognitionTask
	retries   map[string]telegramStickerRecognitionRetryState
	dirtyBots map[string]bool
	now       func() time.Time
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
		logger:    logger.With(slog.String("worker", "telegram_sticker_recognition")),
		queued:    make(map[string]telegramStickerRecognitionTask),
		retries:   make(map[string]telegramStickerRecognitionRetryState),
		dirtyBots: make(map[string]bool),
		now:       time.Now,
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
	now := time.Now()
	if q.now != nil {
		now = q.now()
	}
	accepted := make([]telegramStickerRecognitionTask, 0, len(tasks))
	q.mu.Lock()
	if q.ctx.Err() != nil {
		q.mu.Unlock()
		return 0
	}
	q.pruneRetryStatesLocked(now)
	availableSlots := cap(q.jobs) - len(q.jobs)
	dropped := 0
	for _, task := range tasks {
		var valid bool
		task, valid = normalizeTelegramStickerRecognitionTask(task)
		if !valid {
			continue
		}
		key := telegramStickerRecognitionTaskKey(task.BotID, task.StickerID)
		if _, exists := q.queued[key]; exists {
			continue
		}
		if !q.retryReadyLocked(&task, now) {
			continue
		}
		if len(accepted) >= availableSlots {
			dropped++
			continue
		}
		q.queued[key] = task
		accepted = append(accepted, task)
	}

	enqueued := 0
sendAccepted:
	for index, task := range accepted {
		select {
		case q.jobs <- task:
			enqueued++
		case <-q.ctx.Done():
			q.removeQueuedLocked(accepted[index:])
			break sendAccepted
		default:
			// The batch reserved every available buffered slot while holding mu,
			// so this is only a defensive fallback for a malformed queue.
			q.removeQueuedLocked(accepted[index:])
			dropped += len(accepted) - index
			break sendAccepted
		}
	}
	q.mu.Unlock()
	if dropped > 0 {
		q.logger.Warn("Telegram sticker recognition queue is full; tasks were deferred",
			slog.Int("dropped_count", dropped),
			slog.Int("queue_capacity", cap(q.jobs)),
		)
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
	var err error
	select {
	case <-done:
		err = nil
	case <-ctx.Done():
		err = ctx.Err()
	}
	q.mu.Lock()
	clear(q.queued)
	clear(q.retries)
	clear(q.dirtyBots)
	q.mu.Unlock()
	return err
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
	schemaChanged := false
	if q.recognize != nil {
		schemaChanged, err = q.recognize(ctx, task)
	}
	cancel()
	if err != nil && !errors.Is(err, context.Canceled) {
		q.logger.Warn("Telegram sticker background recognition failed",
			slog.String("bot_id", task.BotID),
			slog.String("sticker_id", task.StickerID),
			slog.Int("attempt", task.Attempts+1),
			slog.Int("max_attempts", telegramStickerRecognitionMaxAttempts),
			slog.Any("error", err),
		)
	} else if err == nil {
		q.logger.Debug("Telegram sticker background recognition completed",
			slog.String("bot_id", task.BotID),
			slog.String("sticker_id", task.StickerID),
		)
	}

	if !q.complete(task, schemaChanged, err) || q.refresh == nil || q.ctx.Err() != nil {
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

func (q *telegramStickerRecognitionQueue) complete(
	task telegramStickerRecognitionTask,
	schemaChanged bool,
	taskErr error,
) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	key := telegramStickerRecognitionTaskKey(task.BotID, task.StickerID)
	delete(q.queued, key)
	queueActive := q.ctx.Err() == nil
	if schemaChanged && queueActive {
		q.dirtyBots[task.BotID] = true
	}
	if taskErr == nil {
		delete(q.retries, key)
	} else if queueActive && !errors.Is(taskErr, context.Canceled) {
		now := time.Now()
		if q.now != nil {
			now = q.now()
		}
		attempts := max(task.Attempts+1, 1)
		if previous, exists := q.retries[key]; exists &&
			previous.Model == task.Model && previous.PromptVersion == task.PromptVersion {
			attempts = max(attempts, previous.Attempts+1)
		}
		q.retries[key] = telegramStickerRecognitionRetryState{
			Model: task.Model, PromptVersion: task.PromptVersion,
			Attempts: attempts, RetryAfter: now.Add(telegramStickerRecognitionRetryDelay(attempts)),
			UpdatedAt: now,
		}
	}
	for _, queued := range q.queued {
		if queued.BotID == task.BotID {
			return false
		}
	}
	dirty := q.dirtyBots[task.BotID]
	delete(q.dirtyBots, task.BotID)
	return dirty
}

func normalizeTelegramStickerRecognitionTask(task telegramStickerRecognitionTask) (telegramStickerRecognitionTask, bool) {
	task.BotID = strings.TrimSpace(task.BotID)
	task.ChannelIdentityID = strings.TrimSpace(task.ChannelIdentityID)
	task.StickerID = strings.TrimSpace(task.StickerID)
	task.Model = strings.TrimSpace(task.Model)
	task.PromptVersion = strings.TrimSpace(task.PromptVersion)
	task.Attempts = max(task.Attempts, 0)
	valid := task.BotID != "" && task.ChannelIdentityID != "" && task.StickerID != "" && task.Model != ""
	return task, valid
}

func (q *telegramStickerRecognitionQueue) retryReadyLocked(
	task *telegramStickerRecognitionTask,
	now time.Time,
) bool {
	key := telegramStickerRecognitionTaskKey(task.BotID, task.StickerID)
	state, exists := q.retries[key]
	if exists && (state.Model != task.Model || state.PromptVersion != task.PromptVersion) {
		delete(q.retries, key)
		exists = false
	}
	if !exists {
		return task.Attempts < telegramStickerRecognitionMaxAttempts
	}
	if state.Attempts >= telegramStickerRecognitionMaxAttempts {
		if now.Before(state.RetryAfter) {
			return false
		}
		delete(q.retries, key)
		task.Attempts = 0
		return true
	}
	if now.Before(state.RetryAfter) {
		return false
	}
	task.Attempts = max(task.Attempts, state.Attempts)
	return task.Attempts < telegramStickerRecognitionMaxAttempts
}

func (q *telegramStickerRecognitionQueue) pruneRetryStatesLocked(now time.Time) {
	for key, state := range q.retries {
		if now.Sub(state.UpdatedAt) >= telegramStickerRecognitionRetryStateTTL {
			delete(q.retries, key)
		}
	}
}

func (q *telegramStickerRecognitionQueue) removeQueuedLocked(tasks []telegramStickerRecognitionTask) {
	for _, task := range tasks {
		delete(q.queued, telegramStickerRecognitionTaskKey(task.BotID, task.StickerID))
	}
}

func telegramStickerRecognitionRetryDelay(attempts int) time.Duration {
	if attempts >= telegramStickerRecognitionMaxAttempts {
		return telegramStickerRecognitionRetryResetDelay
	}
	delay := telegramStickerRecognitionRetryBaseDelay
	for attempt := 1; attempt < attempts; attempt++ {
		delay = min(delay*2, telegramStickerRecognitionRetryMaxDelay)
	}
	return delay
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
			Model: model, PromptVersion: promptVersion, Attempts: max(entry.Attempts, 0),
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
