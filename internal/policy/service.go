package policy

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/bots"
)

type Decision struct {
	BotID string
}

type Service struct {
	bots   *bots.Service
	logger *slog.Logger
}

func NewService(log *slog.Logger, botsService *bots.Service) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		bots:   botsService,
		logger: log.With(slog.String("service", "policy")),
	}
}

// Resolve evaluates the full access policy for a bot.
func (s *Service) Resolve(ctx context.Context, botID string) (Decision, error) {
	if s == nil || s.bots == nil {
		return Decision{}, errors.New("policy service not configured")
	}
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return Decision{}, errors.New("bot id is required")
	}
	if _, err := s.bots.Get(ctx, botID); err != nil {
		return Decision{}, err
	}
	return Decision{BotID: botID}, nil
}

// BotOwnerUserID returns bot owner's user id. Implements router.PolicyService.
func (s *Service) BotOwnerUserID(ctx context.Context, botID string) (string, error) {
	if s == nil || s.bots == nil {
		return "", errors.New("policy service not configured")
	}
	bot, err := s.bots.Get(ctx, strings.TrimSpace(botID))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(bot.OwnerUserID), nil
}
