package providers

import (
	"context"

	"github.com/felinics/memoh/internal/chatgptplan"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/models"
)

func (s *Service) checkChatGPTProvider(ctx context.Context, id string) error {
	uuid, err := db.ParseUUID(id)
	if err != nil {
		return chatgptplan.ErrInvalidAuthorization
	}
	provider, err := s.queries.GetProviderByID(ctx, uuid)
	if err != nil {
		return err
	}
	if models.ClientType(provider.ClientType) != models.ClientTypeOpenAIChatGPT {
		return chatgptplan.ErrInvalidAuthorization
	}
	return nil
}

func (s *Service) BeginChatGPTAuthorization(ctx context.Context, id, owner string, req chatgptplan.BeginRequest) (chatgptplan.BeginResponse, error) {
	if err := s.checkChatGPTProvider(ctx, id); err != nil {
		return chatgptplan.BeginResponse{}, err
	}
	if s.planSessions == nil {
		return chatgptplan.BeginResponse{}, chatgptplan.ErrEncryptionUnavailable
	}
	return s.planSessions.Begin(ctx, id, owner, req)
}

func (s *Service) CompleteChatGPTAuthorization(ctx context.Context, id, owner string, req chatgptplan.CompleteRequest) (chatgptplan.Status, error) {
	if err := s.checkChatGPTProvider(ctx, id); err != nil {
		return chatgptplan.Status{}, err
	}
	if s.planSessions == nil {
		return chatgptplan.Status{}, chatgptplan.ErrEncryptionUnavailable
	}
	return s.planSessions.Complete(ctx, id, owner, req)
}

func (s *Service) RetainChatGPTRegistration(ctx context.Context, id, owner string, req chatgptplan.RegistrationRequest) error {
	if err := s.checkChatGPTProvider(ctx, id); err != nil {
		return err
	}
	if s.planSessions == nil {
		return chatgptplan.ErrEncryptionUnavailable
	}
	return s.planSessions.RetainRegistration(ctx, id, owner, req)
}

func (s *Service) ChatGPTAuthorizationStatus(ctx context.Context, id string) (chatgptplan.Status, error) {
	if err := s.checkChatGPTProvider(ctx, id); err != nil {
		return chatgptplan.Status{}, err
	}
	if s.planSessions == nil {
		return chatgptplan.Status{Available: false}, nil
	}
	return s.planSessions.Status(ctx, id)
}

func (s *Service) RevokeChatGPTAuthorization(ctx context.Context, id, owner string) error {
	if err := s.checkChatGPTProvider(ctx, id); err != nil {
		return err
	}
	if s.planSessions == nil {
		return chatgptplan.ErrEncryptionUnavailable
	}
	return s.planSessions.Revoke(ctx, id, owner)
}
