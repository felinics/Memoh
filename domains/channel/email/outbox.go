package email

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

// OutboxService manages the email outbox audit log.
type OutboxService struct {
	store  emailport.OutboxStore
	logger *slog.Logger
}

func NewOutboxService(log *slog.Logger, store emailport.OutboxStore) *OutboxService {
	return &OutboxService{
		store:  store,
		logger: log.With(slog.String("service", "email_outbox")),
	}
}

// Create records a pending outbound email.
func (s *OutboxService) Create(ctx context.Context, providerID, botID string, msg OutboundEmail, fromAddr string) (string, error) {
	toJSON, _ := json.Marshal(msg.To)
	bodyText, bodyHTML := msg.Body, ""
	if msg.HTML {
		bodyHTML = msg.Body
		bodyText = ""
	}

	row, err := s.store.CreateOutbox(ctx, emailport.CreateOutboxInput{
		ProviderID:  providerID,
		BotID:       botID,
		FromAddress: fromAddr,
		ToAddresses: toJSON,
		Subject:     msg.Subject,
		BodyText:    bodyText,
		BodyHTML:    bodyHTML,
		Attachments: []byte("[]"),
		Status:      "pending",
	})
	if err != nil {
		return "", fmt.Errorf("create outbox: %w", err)
	}
	return row.ID, nil
}

// MarkSent updates the outbox record with a successful send.
func (s *OutboxService) MarkSent(ctx context.Context, id, messageID string) error {
	return s.store.MarkOutboxSent(ctx, id, messageID)
}

// MarkFailed updates the outbox record with an error.
func (s *OutboxService) MarkFailed(ctx context.Context, id, errMsg string) error {
	return s.store.MarkOutboxFailed(ctx, id, errMsg)
}

func (s *OutboxService) Get(ctx context.Context, id string) (OutboxItemResponse, error) {
	row, err := s.store.FindOutbox(ctx, id)
	if err != nil {
		return OutboxItemResponse{}, fmt.Errorf("get outbox: %w", err)
	}
	return s.toOutboxResponse(row), nil
}

func (s *OutboxService) ListByBot(ctx context.Context, botID string, limit, offset int32) ([]OutboxItemResponse, int64, error) {
	rows, err := s.store.ListOutboxByBot(ctx, botID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list outbox: %w", err)
	}
	count, err := s.store.CountOutboxByBot(ctx, botID)
	if err != nil {
		return nil, 0, fmt.Errorf("count outbox: %w", err)
	}
	items := make([]OutboxItemResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.toOutboxResponse(row))
	}
	return items, count, nil
}

func (*OutboxService) toOutboxResponse(row emailport.OutboxRecord) OutboxItemResponse {
	var to []string
	_ = json.Unmarshal(row.ToAddresses, &to)
	var attachments []any
	_ = json.Unmarshal(row.Attachments, &attachments)

	resp := OutboxItemResponse{
		ID:          row.ID,
		ProviderID:  row.ProviderID,
		BotID:       row.BotID,
		MessageID:   row.MessageID,
		From:        row.FromAddress,
		To:          to,
		Subject:     row.Subject,
		BodyText:    row.BodyText,
		BodyHTML:    row.BodyHTML,
		Attachments: attachments,
		Status:      row.Status,
		Error:       row.Error,
		CreatedAt:   row.CreatedAt,
	}
	if !row.SentAt.IsZero() {
		resp.SentAt = row.SentAt
	}
	return resp
}
