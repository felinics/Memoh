package httpfixture

import (
	"context"
	"time"

	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/db"
)

// BotStore is a bots persistence double for HTTP tests.
type BotStore struct {
	Bot         bot.Record
	Permissions []byte
	CreateID    string
}

// NewBotStore builds a bot/grant fake from a domain bot record.
func NewBotStore(record bot.Record, permissions ...[]byte) *BotStore {
	store := &BotStore{Bot: record}
	if len(permissions) > 0 {
		store.Permissions = append([]byte(nil), permissions[0]...)
	}
	if store.Permissions == nil {
		store.Permissions = []byte(`["chat"]`)
	}
	return store
}

func (s *BotStore) GetBotByID(_ context.Context, botID string) (bot.Record, error) {
	if s.Bot.ID != "" && s.Bot.ID != botID {
		return bot.Record{}, bot.ErrBotNotFound
	}
	return s.Bot, nil
}

func (s *BotStore) GetBotByName(_ context.Context, name string) (bot.Record, error) {
	if s.Bot.ID == "" || s.Bot.Name != name {
		return bot.Record{}, bot.ErrBotNotFound
	}
	return s.Bot, nil
}

func (s *BotStore) ListBotsByOwner(context.Context, string) ([]bot.Record, error) {
	return []bot.Record{s.Bot}, nil
}

func (s *BotStore) ListAccessibleBots(context.Context, string) ([]bot.Record, error) {
	return []bot.Record{s.Bot}, nil
}

func (s *BotStore) CreateBot(_ context.Context, input bot.CreateInput) (bot.Record, error) {
	id := s.CreateID
	if id == "" {
		id = "22222222-2222-2222-2222-222222222222"
	}
	s.Bot = bot.Record{
		ID:          id,
		OwnerUserID: input.OwnerUserID,
		Name:        input.Name,
		DisplayName: input.DisplayName,
		AvatarURL:   input.AvatarURL,
		Timezone:    input.Timezone,
		IsActive:    input.IsActive,
		Status:      input.Status,
		Metadata:    append([]byte(nil), input.Metadata...),
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	return s.Bot, nil
}

func (s *BotStore) UpdateBot(_ context.Context, input bot.UpdateInput) (bot.Record, error) {
	s.Bot.Name = input.Name
	s.Bot.DisplayName = input.DisplayName
	s.Bot.AvatarURL = input.AvatarURL
	s.Bot.Timezone = input.Timezone
	s.Bot.IsActive = input.IsActive
	s.Bot.Metadata = append([]byte(nil), input.Metadata...)
	s.Bot.UpdatedAt = time.Now().UTC()
	return s.Bot, nil
}

func (s *BotStore) UpdateBotOwner(_ context.Context, _, ownerUserID string) (bot.Record, error) {
	s.Bot.OwnerUserID = ownerUserID
	return s.Bot, nil
}

func (s *BotStore) UpdateBotStatus(_ context.Context, _, status string) error {
	s.Bot.Status = status
	return nil
}

func (*BotStore) DeleteBot(context.Context, string) error { return nil }

func (*BotStore) ListGrants(context.Context, string) ([]bot.GrantRecord, error) {
	return nil, nil
}

func (s *BotStore) ListGrantsForUser(_ context.Context, botID, userID string) ([]bot.GrantRecord, error) {
	return []bot.GrantRecord{{
		ID:          "33333333-3333-3333-3333-333333333333",
		BotID:       botID,
		SubjectType: "user",
		UserID:      userID,
		Permissions: append([]byte(nil), s.Permissions...),
	}}, nil
}

func (*BotStore) GetGrant(context.Context, string) (bot.GrantRecord, error) {
	return bot.GrantRecord{}, bot.ErrGrantNotFound
}

func (*BotStore) CreateGrant(context.Context, bot.CreateGrantInput) (bot.GrantRecord, error) {
	return bot.GrantRecord{}, nil
}

func (*BotStore) UpdateGrantPermissions(context.Context, string, []byte) (bot.GrantRecord, error) {
	return bot.GrantRecord{}, bot.ErrGrantNotFound
}

func (*BotStore) DeleteGrant(context.Context, string) error { return nil }

func (s *BotStore) LookupWorkspacePreferences(_ context.Context, botID string) (workspace.WorkspacePreferences, bool, error) {
	if s.Bot.ID != "" && s.Bot.ID != botID {
		return workspace.WorkspacePreferences{}, false, nil
	}
	preferences, err := workspace.DecodeWorkspacePreferences(s.Bot.Metadata)
	return preferences, true, err
}

func (s *BotStore) RequireBot(_ context.Context, botID string) error {
	if s.Bot.ID != "" && s.Bot.ID != botID {
		return db.ErrNotFound
	}
	return nil
}

func (s *BotStore) SetWorkspaceImagePreference(ctx context.Context, botID, image string) error {
	if err := s.RequireBot(ctx, botID); err != nil {
		return err
	}
	metadata, err := workspace.PatchWorkspaceImagePreference(s.Bot.Metadata, &image)
	if err != nil {
		return err
	}
	s.Bot.Metadata = metadata
	return nil
}

func (s *BotStore) ClearWorkspaceImagePreference(ctx context.Context, botID string) error {
	if err := s.RequireBot(ctx, botID); err != nil {
		return err
	}
	metadata, err := workspace.PatchWorkspaceImagePreference(s.Bot.Metadata, nil)
	if err != nil {
		return err
	}
	s.Bot.Metadata = metadata
	return nil
}

func (s *BotStore) SetWorkspaceGPUPreference(ctx context.Context, botID string, gpu workspace.WorkspaceGPUConfig) error {
	if err := s.RequireBot(ctx, botID); err != nil {
		return err
	}
	metadata, err := workspace.PatchWorkspaceGPUPreference(s.Bot.Metadata, &gpu)
	if err != nil {
		return err
	}
	s.Bot.Metadata = metadata
	return nil
}

func (s *BotStore) ClearWorkspaceGPUPreference(ctx context.Context, botID string) error {
	if err := s.RequireBot(ctx, botID); err != nil {
		return err
	}
	metadata, err := workspace.PatchWorkspaceGPUPreference(s.Bot.Metadata, nil)
	if err != nil {
		return err
	}
	s.Bot.Metadata = metadata
	return nil
}

func (s *BotStore) BotOwnerUserID(ctx context.Context, botID string) (string, error) {
	if err := s.RequireBot(ctx, botID); err != nil {
		return "", err
	}
	return s.Bot.OwnerUserID, nil
}

type stubUserReader struct{}

func (stubUserReader) GetUser(_ context.Context, userID string) (bot.UserRecord, error) {
	return bot.UserRecord{ID: userID, Username: "user"}, nil
}

type stubContainerReader struct{}

func (stubContainerReader) GetContainerByBotID(context.Context, string) (bot.ContainerRecord, error) {
	return bot.ContainerRecord{}, bot.ErrContainerNotFound
}
