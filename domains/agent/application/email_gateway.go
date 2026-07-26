package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/api/identity/auth"
	"github.com/memohai/memoh/domains/iam/team"
)

const emailTriggerTokenTTL = 10 * time.Minute

// BotOwnerResolver resolves a bot's owner account ID.
// Consumer-owned port: string in, string out; no DB types.
type BotOwnerResolver interface {
	ResolveBotOwner(ctx context.Context, botID string) (ownerUserID string, err error)
}

// TurnStarter is the smallest Agent surface required by proactive email turns.
// Both the in-process application service and the authenticated RPC client
// implement it.
type TurnStarter interface {
	StartTurn(context.Context, agentdomain.StartTurnCommand) (agentdomain.RunHandle, error)
}

// EmailChatGateway implements Channel's email trigger contract while keeping
// Agent turn policy in the Agent application owner.
type EmailChatGateway struct {
	turns     TurnStarter
	owners    BotOwnerResolver
	jwtSecret string
	logger    *slog.Logger
}

func NewEmailChatGateway(turns TurnStarter, owners BotOwnerResolver, jwtSecret string, logger *slog.Logger) *EmailChatGateway {
	return &EmailChatGateway{
		turns:     turns,
		owners:    owners,
		jwtSecret: jwtSecret,
		logger:    logger,
	}
}

func (g *EmailChatGateway) TriggerBotChat(ctx context.Context, botID, content string) error {
	if g == nil || g.turns == nil {
		return errors.New("agent turn service not configured")
	}

	ownerUserID, err := g.resolveBotOwner(ctx, botID)
	if err != nil {
		return fmt.Errorf("resolve bot owner: %w", err)
	}

	token, err := g.generateToken(ownerUserID)
	if err != nil {
		return fmt.Errorf("generate trigger token: %w", err)
	}

	handle, err := g.turns.StartTurn(ctx, agentdomain.StartTurnCommand{
		SchemaVersion:  1,
		TeamID:         team.DefaultTeamID,
		Mode:           agentdomain.ModeChat,
		BotID:          botID,
		ChatID:         botID,
		Query:          content,
		UserID:         ownerUserID,
		Token:          token,
		CurrentChannel: "email",
	})
	if err != nil {
		return fmt.Errorf("start email turn: %w", err)
	}
	if handle == nil {
		return errors.New("start email turn: nil run handle")
	}
	defer handle.Cancel()

	events, errs := handle.Events(), handle.Errs()
	for events != nil || errs != nil {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case runErr, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if runErr != nil {
				return fmt.Errorf("run email turn: %w", runErr)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if g.logger != nil {
		g.logger.InfoContext(ctx, "email trigger chat completed", slog.String("bot_id", botID))
	}
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
