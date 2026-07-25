package application

import (
	"context"
	"strings"
)

// resolveDisplayName returns the best available display name for the request identity.
func (s *Service) resolveDisplayName(ctx context.Context, req ChatRequest) string {
	if name := strings.TrimSpace(req.DisplayName); name != "" {
		return name
	}
	if s.channelIdentities == nil {
		return "User"
	}
	channelIdentityID := strings.TrimSpace(req.SourceChannelIdentityID)
	if channelIdentityID == "" {
		return "User"
	}
	identity, err := s.channelIdentities.GetByID(ctx, channelIdentityID)
	if err == nil {
		if name := strings.TrimSpace(identity.DisplayName); name != "" {
			return name
		}
	}
	return "User"
}

func (s *Service) isExistingChannelIdentityID(ctx context.Context, id string) bool {
	if s.channelIdentities == nil || strings.TrimSpace(id) == "" {
		return false
	}
	_, err := s.channelIdentities.GetByID(ctx, strings.TrimSpace(id))
	return err == nil
}

func (s *Service) isExistingUserID(ctx context.Context, id string) bool {
	if s.accounts == nil || strings.TrimSpace(id) == "" {
		return false
	}
	_, err := s.accounts.Get(ctx, strings.TrimSpace(id))
	return err == nil
}
