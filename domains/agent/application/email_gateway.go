package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/memohai/memoh/domains/api/auth"
)

const emailTriggerTokenTTL = 10 * time.Minute

// BotOwnerResolver resolves a bot's owner account ID.
// Consumer-owned port: string in, string out; no DB types.
type BotOwnerResolver interface {
	ResolveBotOwner(ctx context.Context, botID string) (ownerUserID string, err error)
}

// EmailChatGateway implements email.ChatTriggerer by delegating to the Service.
type EmailChatGateway struct {
	service   *Service
	owners    BotOwnerResolver
	jwtSecret string
	logger    *slog.Logger
}

func NewEmailChatGateway(service *Service, owners BotOwnerResolver, jwtSecret string, logger *slog.Logger) *EmailChatGateway {
	return &EmailChatGateway{
		service:   service,
		owners:    owners,
		jwtSecret: jwtSecret,
		logger:    logger,
	}
}

func (g *EmailChatGateway) TriggerBotChat(ctx context.Context, botID, content string) error {
	if g == nil || g.service == nil {
		return errors.New("agent application service not configured")
	}

	ownerUserID, err := g.resolveBotOwner(ctx, botID)
	if err != nil {
		return fmt.Errorf("resolve bot owner: %w", err)
	}

	token, err := g.generateToken(ownerUserID)
	if err != nil {
		return fmt.Errorf("generate trigger token: %w", err)
	}

	_, err = g.service.Chat(ctx, ChatRequest{
		BotID:          botID,
		ChatID:         botID,
		Query:          content,
		UserID:         ownerUserID,
		Token:          token,
		CurrentChannel: "email",
	})
	if err != nil {
		return fmt.Errorf("trigger chat: %w", err)
	}

	g.logger.InfoContext(ctx, "email trigger chat completed",
		slog.String("bot_id", botID))
	return nil
}

func (g *EmailChatGateway) resolveBotOwner(ctx context.Context, botID string) (string, error) {
	if g.owners == nil {
		return "", errors.New("bot owner resolver not configured")
	}
	ownerID, err := g.owners.ResolveBotOwner(ctx, botID)
	if err != nil {
		return "", err
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return "", errors.New("bot owner not found")
	}
	return ownerID, nil
}

func (g *EmailChatGateway) generateToken(userID string) (string, error) {
	if strings.TrimSpace(g.jwtSecret) == "" {
		return "", errors.New("jwt secret not configured")
	}
	signed, _, err := auth.GenerateToken(userID, g.jwtSecret, emailTriggerTokenTTL)
	if err != nil {
		return "", err
	}
	return "Bearer " + signed, nil
}
