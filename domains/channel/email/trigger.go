package email

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

// ChatTriggerer triggers a proactive bot conversation (e.g. when a new email arrives).
type ChatTriggerer interface {
	TriggerBotChat(ctx context.Context, botID, content string) error
}

// Trigger notifies bots when a new email arrives and immediately triggers
// the bot's LLM to process it.
type Trigger struct {
	logger        *slog.Logger
	emailService  *Service
	chatTriggerer ChatTriggerer

	mu      sync.Mutex
	tasks   sync.WaitGroup
	runCtx  context.Context
	cancel  context.CancelFunc
	stopped bool
}

func NewTrigger(log *slog.Logger, emailService *Service, chatTriggerer ChatTriggerer) *Trigger {
	return &Trigger{
		logger:        log.With(slog.String("component", "email_trigger")),
		emailService:  emailService,
		chatTriggerer: chatTriggerer,
	}
}

// Start establishes the application-lifetime context used by asynchronous
// email turns. Request cancellation must not abort a turn after a webhook or
// receiver callback has returned.
func (t *Trigger) Start(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return errors.New("email trigger is stopped")
	}
	if t.runCtx != nil {
		return nil
	}
	t.runCtx, t.cancel = context.WithCancel(context.WithoutCancel(ctx))
	return nil
}

// HandleInbound triggers a conversation for each bound bot so it can process
// the incoming email.
func (t *Trigger) HandleInbound(ctx context.Context, providerID string, mail InboundEmail) error {
	t.logger.InfoContext(ctx, "new email arrived",
		slog.String("provider_id", providerID),
		slog.String("from", mail.From),
		slog.String("subject", mail.Subject))

	bindings, err := t.emailService.ListReadableBindingsByProvider(ctx, providerID)
	if err != nil {
		t.logger.ErrorContext(ctx, "failed to list readable bindings", slog.Any("error", err))
		return err
	}

	for _, binding := range bindings {
		content := fmt.Sprintf("New email received at %s from %s — %s", binding.EmailAddress, mail.From, mail.Subject)

		t.logger.InfoContext(ctx, "bot notified of new email",
			slog.String("bot_id", binding.BotID),
			slog.String("from", mail.From))

		if t.chatTriggerer != nil {
			taskCtx, ok := t.startTask()
			if !ok {
				continue
			}
			go func(taskCtx context.Context, botID, text string) {
				defer t.tasks.Done()
				if err := t.chatTriggerer.TriggerBotChat(taskCtx, botID, text); err != nil {
					t.logger.ErrorContext(taskCtx, "failed to trigger bot chat for email",
						slog.String("bot_id", botID),
						slog.Any("error", err))
				}
			}(taskCtx, binding.BotID, content)
		}
	}

	return nil
}

func (t *Trigger) startTask() (context.Context, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.runCtx == nil {
		return nil, false
	}
	t.tasks.Add(1)
	return t.runCtx, true
}

// Shutdown prevents new triggers and waits for active bot turns to finish.
func (t *Trigger) Shutdown(ctx context.Context) error {
	t.mu.Lock()
	t.stopped = true
	cancel := t.cancel
	t.cancel = nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		t.tasks.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
