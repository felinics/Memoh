package email

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// OutboxService manages the email outbox audit log.
type OutboxService struct {
	queries dbstore.Queries
	logger  *slog.Logger
}

func NewOutboxService(log *slog.Logger, queries dbstore.Queries) *OutboxService {
	return &OutboxService{
		queries: queries,
		logger:  log.With(slog.String("service", "email_outbox")),
	}
}

// Create records a pending outbound email.
func (s *OutboxService) Create(ctx context.Context, providerID, botID string, msg OutboundEmail, fromAddr string) (string, error) {
	pgProviderID, err := db.ParseUUID(providerID)
	if err != nil {
		return "", fmt.Errorf("invalid provider_id: %w", err)
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return "", fmt.Errorf("invalid bot_id: %w", err)
	}
	toJSON, _ := json.Marshal(msg.To)
	bodyText, bodyHTML := msg.Body, ""
	if msg.HTML {
		bodyHTML = msg.Body
		bodyText = ""
	}

	row, err := s.queries.CreateEmailOutbox(ctx, sqlc.CreateEmailOutboxParams{
		ProviderID:  pgProviderID,
		BotID:       pgBotID,
		FromAddress: fromAddr,
		ToAddresses: toJSON,
		Subject:     msg.Subject,
		BodyText:    bodyText,
		BodyHtml:    bodyHTML,
		Attachments: []byte("[]"),
		Status:      "pending",
	})
	if err != nil {
		return "", fmt.Errorf("create outbox: %w", err)
	}
	return row.ID.String(), nil
}

// MarkSent updates the outbox record with a successful send.
func (s *OutboxService) MarkSent(ctx context.Context, id, messageID string) error {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.UpdateEmailOutboxSent(ctx, sqlc.UpdateEmailOutboxSentParams{
		ID:        pgID,
		MessageID: messageID,
	})
}

// MarkFailed updates the outbox record with an error.
func (s *OutboxService) MarkFailed(ctx context.Context, id, errMsg string) error {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.UpdateEmailOutboxFailed(ctx, sqlc.UpdateEmailOutboxFailedParams{
		ID:    pgID,
		Error: errMsg,
	})
}

func (s *OutboxService) Get(ctx context.Context, id string) (OutboxItemResponse, error) {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return OutboxItemResponse{}, err
	}
	row, err := s.queries.GetEmailOutboxByID(ctx, pgID)
	if err != nil {
		return OutboxItemResponse{}, fmt.Errorf("get outbox: %w", err)
	}
	return s.toOutboxResponse(row), nil
}

func (s *OutboxService) ListByBot(ctx context.Context, botID string, limit, offset int32) ([]OutboxItemResponse, int64, error) {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.queries.ListEmailOutboxByBot(ctx, sqlc.ListEmailOutboxByBotParams{
		BotID: pgBotID,
		Lim:   limit,
		Off:   offset,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list outbox: %w", err)
	}
	count, err := s.queries.CountEmailOutboxByBot(ctx, pgBotID)
	if err != nil {
		return nil, 0, fmt.Errorf("count outbox: %w", err)
	}
	items := make([]OutboxItemResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.toOutboxResponse(row))
	}
	return items, count, nil
}

func (*OutboxService) toOutboxResponse(row sqlc.EmailOutbox) OutboxItemResponse {
	var to []string
	_ = json.Unmarshal(row.ToAddresses, &to)
	var attachments []any
	_ = json.Unmarshal(row.Attachments, &attachments)

	var sentAt interface{ IsZero() bool }
	resp := OutboxItemResponse{
		ID:          row.ID.String(),
		ProviderID:  row.ProviderID.String(),
		BotID:       row.BotID.String(),
		MessageID:   row.MessageID,
		From:        row.FromAddress,
		To:          to,
		Subject:     row.Subject,
		BodyText:    row.BodyText,
		BodyHTML:    row.BodyHtml,
		Attachments: attachments,
		Status:      row.Status,
		Error:       row.Error,
		CreatedAt:   row.CreatedAt.Time,
	}
	_ = sentAt
	if row.SentAt.Valid {
		resp.SentAt = row.SentAt.Time
	}
	return resp
}
