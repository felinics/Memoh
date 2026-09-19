package email

import (
	"context"
	"fmt"
	"log/slog"
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
}

func NewTrigger(log *slog.Logger, emailService *Service, chatTriggerer ChatTriggerer) *Trigger {
	return &Trigger{
		logger:        log.With(slog.String("component", "email_trigger")),
		emailService:  emailService,
		chatTriggerer: chatTriggerer,
	}
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
			go func(botID, text string) {
				if err := t.chatTriggerer.TriggerBotChat(ctx, botID, text); err != nil {
					t.logger.ErrorContext(ctx, "failed to trigger bot chat for email",
						slog.String("bot_id", botID),
						slog.Any("error", err))
				}
			}(binding.BotID, content)
		}
	}

	return nil
}
