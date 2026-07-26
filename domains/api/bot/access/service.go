// Package access resolves the effective per-bot Manage capability for channel
// identities from local overrides and linked web-member grants.
package access

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/bot/access/acl"
	aclpersistence "github.com/memohai/memoh/domains/api/bot/access/acl/persistence"
	botpersistence "github.com/memohai/memoh/domains/api/bot/persistence"
	linkpersistence "github.com/memohai/memoh/domains/api/identity/link/persistence"
)

type Service struct {
	links      linkpersistence.Store
	identities linkpersistence.ChannelIdentityReader
	acl        manageOverrideStore
	bots       botPermissionResolver
	logger     *slog.Logger
}

type manageOverrideStore interface {
	GetManageOverride(ctx context.Context, botID, channelIdentityID string) (granted bool, exists bool, err error)
	ListManageOverrides(ctx context.Context, botID string) ([]aclpersistence.ManageOverride, error)
	SetManageOverride(ctx context.Context, botID, channelIdentityID string, granted bool, createdByUserID string) (aclpersistence.ManageOverride, error)
	DeleteManageOverride(ctx context.Context, botID, channelIdentityID string) error
}

type botPermissionResolver interface {
	ResolveUserPermissions(ctx context.Context, botID, userID string, isAdmin bool) ([]string, error)
	ListUserGrants(ctx context.Context, botID string) ([]bot.UserGrant, error)
}

func NewService(log *slog.Logger, links linkpersistence.Store, identities linkpersistence.ChannelIdentityReader, aclService *acl.Service, botService *bot.Service) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		links:      links,
		identities: identities,
		acl:        aclService,
		bots:       botService,
		logger:     log.With(slog.String("service", "bot_access")),
	}
}

// HasManageGrant reports the effective Manage capability for a channel identity on
// a bot: a local Channel Access override wins; otherwise it is inherited when the
// identity is bound to a web member that carries Manage (owner or manage grant).
// It satisfies the command package's ChannelManageResolver.
func (s *Service) HasManageGrant(ctx context.Context, botID, channelIdentityID string) (bool, error) {
	if s == nil {
		return false, errors.New("bot access service not configured")
	}
	channelIdentityID = strings.TrimSpace(channelIdentityID)
	if channelIdentityID == "" {
		return false, nil
	}
	if s.acl != nil {
		granted, exists, err := s.acl.GetManageOverride(ctx, botID, channelIdentityID)
		if err != nil {
			return false, err
		}
		if exists {
			return granted, nil
		}
	}
	return s.inheritedManage(ctx, botID, channelIdentityID)
}

func (s *Service) inheritedManage(ctx context.Context, botID, channelIdentityID string) (bool, error) {
	if s.links == nil || s.bots == nil {
		return false, nil
	}
	userIDs, err := s.links.ListUserIDsByChannelIdentity(ctx, channelIdentityID)
	if err != nil {
		return false, err
	}
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		ok, err := s.userHasManage(ctx, botID, userID)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) userHasManage(ctx context.Context, botID, userID string) (bool, error) {
	perms, err := s.bots.ResolveUserPermissions(ctx, botID, userID, false)
	if err != nil {
		if errors.Is(err, botpersistence.ErrBotNotFound) {
			return false, nil
		}
		return false, err
	}
	return bot.HasPermission(perms, bot.PermissionManage), nil
}

// SetManager sets a local Manage override (ON/OFF) for a channel identity on a bot.
func (s *Service) SetManager(ctx context.Context, botID, channelIdentityID string, granted bool, actorUserID string) error {
	if s == nil || s.acl == nil {
		return errors.New("bot access service not configured")
	}
	_, err := s.acl.SetManageOverride(ctx, botID, channelIdentityID, granted, actorUserID)
	return err
}

// ClearManagerOverride removes the local Manage override so the identity falls back
// to inheritance.
func (s *Service) ClearManagerOverride(ctx context.Context, botID, channelIdentityID string) error {
	if s == nil || s.acl == nil {
		return errors.New("bot access service not configured")
	}
	return s.acl.DeleteManageOverride(ctx, botID, channelIdentityID)
}

// ListManagers returns the effective Manage state per channel identity on a bot,
// merging inherited members (bound web members carrying Manage) with local overrides.
func (s *Service) ListManagers(ctx context.Context, botID string) ([]Manager, error) {
	if s == nil || s.acl == nil {
		return nil, errors.New("bot access service not configured")
	}
	byIdentity := map[string]*Manager{}
	boundIdentityIDs := make([]string, 0)

	if s.bots != nil && s.links != nil {
		grants, err := s.bots.ListUserGrants(ctx, botID)
		if err != nil {
			return nil, err
		}
		everyoneCarriesManage := false
		for _, g := range grants {
			userID := strings.TrimSpace(g.UserID)
			carriesManage := g.IsOwner || bot.HasPermission(g.Permissions, bot.PermissionManage)
			if g.SubjectType == bot.GrantSubjectEveryone {
				everyoneCarriesManage = everyoneCarriesManage || carriesManage
				continue
			}
			if userID == "" || g.SubjectType != bot.GrantSubjectUser {
				continue
			}
			bindings, err := s.links.ListBindingsForUser(ctx, userID)
			if err != nil {
				return nil, err
			}
			for _, b := range bindings {
				mergeManagerBinding(byIdentity, b, carriesManage)
				boundIdentityIDs = append(boundIdentityIDs, b.ChannelIdentityID)
			}
		}
		if everyoneCarriesManage {
			bindings, err := s.links.ListBindingsForBot(ctx, botID)
			if err != nil {
				return nil, err
			}
			for _, b := range bindings {
				mergeManagerBinding(byIdentity, b, true)
				boundIdentityIDs = append(boundIdentityIDs, b.ChannelIdentityID)
			}
		}
	}

	overrides, err := s.acl.ListManageOverrides(ctx, botID)
	if err != nil {
		return nil, err
	}
	for _, o := range overrides {
		ciID := strings.TrimSpace(o.ChannelIdentityID)
		if ciID == "" {
			continue
		}
		m := byIdentity[ciID]
		if m == nil {
			m = &Manager{ChannelIdentityID: ciID}
			byIdentity[ciID] = m
		}
		m.HasOverride = true
		m.Manage = o.Granted
		if o.ChannelType != "" {
			m.ChannelType = o.ChannelType
		}
		if o.ChannelSubjectID != "" {
			m.ChannelSubjectID = o.ChannelSubjectID
		}
		if o.ChannelIdentityDisplayName != "" {
			m.ChannelIdentityDisplayName = o.ChannelIdentityDisplayName
		}
		if o.ChannelIdentityAvatarURL != "" {
			m.ChannelIdentityAvatarURL = o.ChannelIdentityAvatarURL
		}
	}

	identities, err := s.identityIndex(ctx, boundIdentityIDs)
	if err != nil {
		return nil, err
	}
	for id, identity := range identities {
		enrichManager(byIdentity[id], identity)
	}

	items := make([]Manager, 0, len(byIdentity))
	for _, m := range byIdentity {
		items = append(items, *m)
	}
	sort.Slice(items, func(i, j int) bool {
		ni := items[i].ChannelIdentityDisplayName
		nj := items[j].ChannelIdentityDisplayName
		if ni != nj {
			return ni < nj
		}
		return items[i].ChannelIdentityID < items[j].ChannelIdentityID
	})
	return items, nil
}

func mergeManagerBinding(byIdentity map[string]*Manager, binding linkpersistence.Binding, carriesManage bool) {
	ciID := strings.TrimSpace(binding.ChannelIdentityID)
	if ciID == "" {
		return
	}
	m := byIdentity[ciID]
	if m == nil {
		m = &Manager{ChannelIdentityID: ciID}
		byIdentity[ciID] = m
	}
	m.Bound = true
	if carriesManage {
		m.Inherited = true
		m.Manage = true
	}
	if m.ChannelType == "" {
		m.ChannelType = binding.ChannelType
	}
	if m.ChannelSubjectID == "" {
		m.ChannelSubjectID = binding.ChannelSubjectID
	}
	if m.ChannelIdentityDisplayName == "" {
		m.ChannelIdentityDisplayName = binding.ChannelIdentityDisplayName
	}
	if m.ChannelIdentityAvatarURL == "" {
		m.ChannelIdentityAvatarURL = binding.ChannelIdentityAvatarURL
	}
}

func (s *Service) identityIndex(ctx context.Context, ids []string) (map[string]linkpersistence.ChannelIdentity, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil, nil
	}
	if s.identities == nil {
		return nil, errors.New("bot access channel identity reader not configured")
	}
	items, err := s.identities.ListChannelIdentities(ctx, unique)
	if err != nil {
		return nil, fmt.Errorf("list channel identities: %w", err)
	}
	index := make(map[string]linkpersistence.ChannelIdentity, len(items))
	for _, item := range items {
		if id := strings.TrimSpace(item.ID); id != "" {
			index[id] = item
		}
	}
	return index, nil
}

func enrichManager(manager *Manager, identity linkpersistence.ChannelIdentity) {
	if manager == nil || identity.ID == "" {
		return
	}
	manager.ChannelType = identity.Channel
	manager.ChannelSubjectID = identity.ChannelSubjectID
	manager.ChannelIdentityDisplayName = identity.DisplayName
	manager.ChannelIdentityAvatarURL = identity.AvatarURL
}
