package line

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/line/line-bot-sdk-go/v8/linebot/webhook"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/media"
)

type eventResult string

const (
	eventResultOK        eventResult = ""
	eventResultQueueFull eventResult = "queue_full_drop"
	eventResultRetryable eventResult = "retryable_failure"
)

type callbackBudget struct {
	mediaEvents int
	mediaBytes  int64
}

func (a *Adapter) HandleWebhook(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler, r *http.Request, w http.ResponseWriter) error {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
	if r.Body != nil {
		defer func() { _ = r.Body.Close() }()
	}
	creds, err := parseConfigForUse(cfg.Credentials)
	if err != nil {
		a.logWarn("line webhook credentials invalid",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.String("reason", "credentials_invalid"),
		)
		return a.httpError(http.StatusInternalServerError, "line channel not configured")
	}
	var body []byte
	if r.Body != nil {
		var err error
		body, err = media.ReadAllWithLimit(r.Body, lineMaxWebhookBodyBytes)
		if err != nil {
			if errors.Is(err, media.ErrAssetTooLarge) {
				return a.httpError(http.StatusRequestEntityTooLarge, "payload too large")
			}
			a.logWarn("line webhook body read failed",
				slog.String("config_id", cfg.ID),
				slog.String("bot_id", cfg.BotID),
				slog.String("reason", "read_failed"),
			)
			return a.httpError(http.StatusInternalServerError, "failed to read request body")
		}
	}
	if !webhook.ValidateSignature(creds.ChannelSecret, r.Header.Get("x-line-signature"), body) {
		return a.httpError(http.StatusForbidden, "invalid signature")
	}

	var cb webhook.CallbackRequest
	if err := json.Unmarshal(body, &cb); err != nil {
		a.logWarn("line webhook parse failed",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.String("reason", "parse_failed"),
		)
		w.WriteHeader(http.StatusOK)
		return nil
	}

	selfID := lineSelfUserID(cfg)
	if len(cb.Events) == 0 {
		if selfID == "" {
			a.logWarn("line webhook empty events without self identity",
				slog.String("config_id", cfg.ID),
				slog.String("bot_id", cfg.BotID),
				slog.String("reason", "missing_self_identity"),
			)
		}
		w.WriteHeader(http.StatusOK)
		return nil
	}
	if selfID == "" {
		return a.httpError(http.StatusInternalServerError, "line channel identity not configured")
	}
	destination := strings.TrimSpace(cb.Destination)
	if destination == "" {
		total := a.incrementCounter("line_destination_mismatch_total")
		a.logWarn("line webhook missing destination",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.String("self_id_hash", hashValue(selfID)),
			slog.String("reason", "missing_destination"),
			slog.Int64("total", total),
		)
		w.WriteHeader(http.StatusOK)
		return nil
	}
	if destination != selfID {
		total := a.incrementCounter("line_destination_mismatch_total")
		a.logWarn("line webhook destination mismatch",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.String("destination_hash", hashValue(destination)),
			slog.String("self_id_hash", hashValue(selfID)),
			slog.String("reason", "destination_mismatch"),
			slog.Int64("total", total),
		)
		w.WriteHeader(http.StatusOK)
		return nil
	}

	var budget callbackBudget
	var hasRetryable bool
	var hasQueueFull bool
	for _, rawEvent := range cb.Events {
		result := a.handleCallbackEvent(ctx, cfg, handler, rawEvent, &budget)
		if result == eventResultQueueFull {
			hasQueueFull = true
		}
		if result == eventResultRetryable {
			hasRetryable = true
		}
	}
	if hasQueueFull {
		return a.httpError(http.StatusServiceUnavailable, "line inbound queue full")
	}
	if hasRetryable {
		return a.httpError(http.StatusInternalServerError, "line webhook processing failed")
	}
	w.WriteHeader(http.StatusOK)
	return nil
}

func (a *Adapter) handleCallbackEvent(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler, rawEvent webhook.EventInterface, budget *callbackBudget) eventResult {
	ev, ok := asMessageEvent(rawEvent)
	if !ok {
		a.logDebug("line webhook event skipped",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.String("reason", "unsupported_event"),
		)
		return eventResultOK
	}
	eventID := strings.TrimSpace(ev.WebhookEventId)
	eventIDPresent := eventID != ""
	isRedelivery := ev.DeliveryContext != nil && ev.DeliveryContext.IsRedelivery
	dedupeKey := ""
	if eventID == "" {
		a.logWarn("line webhook event missing id",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.String("reason", "missing_event_id"),
			slog.Bool("is_redelivery", isRedelivery),
		)
	} else {
		dedupeKey = cfg.ID + ":" + eventID
	}
	if dedupeKey != "" && !a.claimEvent(dedupeKey, time.Now().UTC()) {
		total := a.incrementCounter("line_webhook_dedup_hit_total")
		a.logDebug("line webhook dedup hit",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.Bool("event_id_present", true),
			slog.Bool("is_redelivery", isRedelivery),
			slog.Int64("total", total),
		)
		return eventResultOK
	}
	if ev.Mode == webhook.EventMode_STANDBY {
		total := a.incrementCounter("line_webhook_standby_skipped_total")
		a.logDebug("line webhook standby skipped",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.Bool("event_id_present", eventIDPresent),
			slog.Bool("is_redelivery", isRedelivery),
			slog.String("reason", "standby"),
			slog.Int64("total", total),
		)
		return eventResultOK
	}
	source, ok := asUserSource(ev.Source)
	if !ok {
		a.logDebug("line webhook non-user source skipped",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.Bool("event_id_present", eventIDPresent),
			slog.Bool("is_redelivery", isRedelivery),
			slog.String("reason", "non_user_source"),
		)
		return eventResultOK
	}
	userID := strings.TrimSpace(source.UserId)
	if userID == "" {
		total := a.incrementCounter("line_webhook_missing_user_id_total")
		a.logWarn("line webhook user source missing user id",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.Bool("event_id_present", eventIDPresent),
			slog.Bool("is_redelivery", isRedelivery),
			slog.String("reason", "missing_user_id"),
			slog.Int64("total", total),
		)
		return eventResultOK
	}

	msg, ok := buildInboundMessage(cfg, ev, userID, budget)
	if !ok {
		total := a.incrementCounter("line_webhook_unsupported_message_total")
		a.logDebug("line webhook message skipped",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.Bool("event_id_present", eventIDPresent),
			slog.Bool("is_redelivery", isRedelivery),
			slog.String("source_user_hash", hashValue(userID)),
			slog.String("reason", "unsupported_or_budget_exceeded"),
			slog.Int64("total", total),
		)
		return eventResultOK
	}
	if handler == nil {
		a.forgetEvent(dedupeKey)
		return eventResultRetryable
	}
	if err := handler(ctx, cfg, msg); err != nil {
		if channel.IsInboundQueueFull(err) {
			a.forgetEvent(dedupeKey)
			total := a.incrementCounter("line_inbound_dropped_queue_full_total")
			a.logWarn("line inbound dropped queue full",
				slog.String("config_id", cfg.ID),
				slog.String("bot_id", cfg.BotID),
				slog.Bool("event_id_present", eventIDPresent),
				slog.Bool("is_redelivery", isRedelivery),
				slog.String("source_type", "user"),
				slog.String("reason", "queue_full"),
				slog.Int64("total", total),
			)
			return eventResultQueueFull
		}
		a.forgetEvent(dedupeKey)
		a.logWarn("line inbound enqueue failed",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.Bool("event_id_present", eventIDPresent),
			slog.Bool("is_redelivery", isRedelivery),
			slog.String("reason", "enqueue_failed"),
		)
		return eventResultRetryable
	}
	if dedupeKey != "" {
		a.markDone(dedupeKey, time.Now().UTC())
	}
	return eventResultOK
}

func lineSelfUserID(cfg channel.ChannelConfig) string {
	value, ok := cfg.SelfIdentity["bot_user_id"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func asMessageEvent(raw webhook.EventInterface) (webhook.MessageEvent, bool) {
	switch ev := raw.(type) {
	case webhook.MessageEvent:
		return ev, true
	case *webhook.MessageEvent:
		if ev != nil {
			return *ev, true
		}
	}
	return webhook.MessageEvent{}, false
}

func asUserSource(raw webhook.SourceInterface) (webhook.UserSource, bool) {
	switch src := raw.(type) {
	case webhook.UserSource:
		return src, true
	case *webhook.UserSource:
		if src != nil {
			return *src, true
		}
	}
	return webhook.UserSource{}, false
}

func buildInboundMessage(cfg channel.ChannelConfig, ev webhook.MessageEvent, userID string, budget *callbackBudget) (channel.InboundMessage, bool) {
	message, ok := buildMessageContent(ev.Message, budget)
	if !ok || message.IsEmpty() {
		return channel.InboundMessage{}, false
	}
	receivedAt := time.Now().UTC()
	if ev.Timestamp > 0 {
		receivedAt = time.UnixMilli(ev.Timestamp).UTC()
	}
	metadata := map[string]any{
		"webhook_event_id": strings.TrimSpace(ev.WebhookEventId),
		"is_redelivery":    ev.DeliveryContext != nil && ev.DeliveryContext.IsRedelivery,
	}
	return channel.InboundMessage{
		Channel:     Type,
		Message:     message,
		BotID:       cfg.BotID,
		ReplyTarget: userID,
		RouteKey:    channel.GenerateRoutingKey(Type.String(), cfg.BotID, userID, channel.ConversationTypePrivate, userID),
		Sender: channel.Identity{
			SubjectID: userID,
			Attributes: map[string]string{
				userConfigKeyUserID: userID,
			},
		},
		Conversation: channel.Conversation{
			ID:   userID,
			Type: channel.ConversationTypePrivate,
		},
		ReceivedAt: receivedAt,
		Source:     Type.String(),
		Metadata:   metadata,
	}, true
}

func buildMessageContent(raw webhook.MessageContentInterface, budget *callbackBudget) (channel.Message, bool) {
	switch msg := raw.(type) {
	case webhook.TextMessageContent:
		return lineTextMessage(msg), true
	case *webhook.TextMessageContent:
		if msg != nil {
			return lineTextMessage(*msg), true
		}
	case webhook.ImageMessageContent:
		return lineImageMessage(msg, budget)
	case *webhook.ImageMessageContent:
		if msg != nil {
			return lineImageMessage(*msg, budget)
		}
	case webhook.FileMessageContent:
		return lineFileMessage(msg, budget)
	case *webhook.FileMessageContent:
		if msg != nil {
			return lineFileMessage(*msg, budget)
		}
	}
	return channel.Message{}, false
}

func lineTextMessage(msg webhook.TextMessageContent) channel.Message {
	return channel.Message{
		ID:     strings.TrimSpace(msg.Id),
		Format: channel.MessageFormatPlain,
		Text:   strings.TrimSpace(msg.Text),
	}
}

func lineImageMessage(msg webhook.ImageMessageContent, budget *callbackBudget) (channel.Message, bool) {
	messageID := strings.TrimSpace(msg.Id)
	if messageID == "" || msg.ContentProvider == nil || msg.ContentProvider.Type != webhook.ContentProviderTYPE_LINE {
		return channel.Message{}, false
	}
	if !consumeMediaBudget(budget, lineBlobMaxBytes) {
		return channel.Message{}, false
	}
	att := channel.NormalizeInboundChannelAttachment(channel.Attachment{
		Type:           channel.AttachmentImage,
		PlatformKey:    messageID,
		SourcePlatform: Type.String(),
		Metadata: map[string]any{
			"message_id":       messageID,
			"content_provider": "line",
		},
	})
	return channel.Message{
		ID:          messageID,
		Format:      channel.MessageFormatPlain,
		Attachments: []channel.Attachment{att},
	}, true
}

func lineFileMessage(msg webhook.FileMessageContent, budget *callbackBudget) (channel.Message, bool) {
	messageID := strings.TrimSpace(msg.Id)
	if messageID == "" {
		return channel.Message{}, false
	}
	size := int64(msg.FileSize)
	budgetSize := size
	if budgetSize <= 0 {
		budgetSize = lineBlobMaxBytes
	}
	if !consumeMediaBudget(budget, budgetSize) {
		return channel.Message{}, false
	}
	att := channel.NormalizeInboundChannelAttachment(channel.Attachment{
		Type:           channel.AttachmentFile,
		PlatformKey:    messageID,
		SourcePlatform: Type.String(),
		Name:           strings.TrimSpace(msg.FileName),
		Size:           size,
		Metadata: map[string]any{
			"message_id":       messageID,
			"content_provider": "line",
			"file_size":        size,
		},
	})
	return channel.Message{
		ID:          messageID,
		Format:      channel.MessageFormatPlain,
		Attachments: []channel.Attachment{att},
	}, true
}

func consumeMediaBudget(budget *callbackBudget, size int64) bool {
	if budget == nil {
		return true
	}
	if budget.mediaEvents+1 > lineMaxMediaEventsPerCallback {
		return false
	}
	if size > 0 && budget.mediaBytes+size > lineCallbackBlobBudgetBytes {
		return false
	}
	budget.mediaEvents++
	if size > 0 {
		budget.mediaBytes += size
	}
	return true
}
