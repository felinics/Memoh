// Package access wires together the global account binding (channel
// identity <-> web user) and the per-bot Manage capability, resolving the
// effective Manage as "local override ?? inherited from bound web member".
package access

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/memohai/memoh/domains/api/access/acl"
	"github.com/memohai/memoh/domains/api/bot"
)

const (
	defaultCodeTTL = 10 * time.Minute
	tokenAlphabet  = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	tokenLength    = 8
)

var (
	ErrCodeNotFound = errors.New("link code not found")
	ErrCodeExpired  = errors.New("link code expired")
	ErrCodeConsumed = errors.New("link code already used")
	ErrInvalidInput = errors.New("invalid input")
)

type Service struct {
	store      Store
	identities ChannelIdentityReader
	acl        manageOverrideStore
	bots       botPermissionResolver
	logger     *slog.Logger
	now        func() time.Time
	token      func() (string, error)
	codeTTL    time.Duration
}

type manageOverrideStore interface {
	GetManageOverride(ctx context.Context, botID, channelIdentityID string) (granted bool, exists bool, err error)
	ListManageOverrides(ctx context.Context, botID string) ([]acl.ManageOverride, error)
	SetManageOverride(ctx context.Context, botID, channelIdentityID string, granted bool, createdByUserID string) (acl.ManageOverride, error)
	DeleteManageOverride(ctx context.Context, botID, channelIdentityID string) error
}

type botPermissionResolver interface {
	ResolveUserPermissions(ctx context.Context, botID, userID string, isAdmin bool) ([]string, error)
	ListUserGrants(ctx context.Context, botID string) ([]bot.UserGrant, error)
}

func NewService(log *slog.Logger, store Store, identities ChannelIdentityReader, aclService *acl.Service, botService *bot.Service) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:      store,
		identities: identities,
		acl:        aclService,
		bots:       botService,
		logger:     log.With(slog.String("service", "channelaccess")),
		now:        func() time.Time { return time.Now().UTC() },
		token:      generateToken,
		codeTTL:    defaultCodeTTL,
	}
}

// HasManageGrant reports the effective Manage capability for a channel identity on
// a bot: a local Channel Access override wins; otherwise it is inherited when the
// identity is bound to a web member that carries Manage (owner or manage grant).
// It satisfies the command package's ChannelManageResolver.
func (s *Service) HasManageGrant(ctx context.Context, botID, channelIdentityID string) (bool, error) {
	if s == nil {
		return false, errors.New("channelaccess service not configured")
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
	if s.store == nil || s.bots == nil {
		return false, nil
	}
	userIDs, err := s.store.ListUserIDsByChannelIdentity(ctx, channelIdentityID)
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
		if errors.Is(err, bot.ErrBotNotFound) {
			return false, nil
		}
		return false, err
	}
	return bot.HasPermission(perms, bot.PermissionManage), nil
}

// IssueLinkCode generates a one-time code the user sends as /link <code> in IM.
func (s *Service) IssueLinkCode(ctx context.Context, userID, channelType string) (LinkCode, error) {
	if s == nil || s.store == nil {
		return LinkCode{}, errors.New("channelaccess service not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return LinkCode{}, ErrInvalidInput
	}
	token, err := s.token()
	if err != nil {
		return LinkCode{}, err
	}
	return s.store.CreateLinkCode(ctx, CreateLinkCodeCommand{
		Token:       token,
		UserID:      userID,
		ChannelType: strings.TrimSpace(channelType),
		ExpiresAt:   s.now().Add(s.codeTTL),
	})
}

// ConsumeLinkCode binds the given channel identity to the user that owns the code.
func (s *Service) ConsumeLinkCode(ctx context.Context, token, channelIdentityID string) (Binding, error) {
	if s == nil || s.store == nil {
		return Binding{}, errors.New("channelaccess service not configured")
	}
	token = normalizeToken(token)
	channelIdentityID = strings.TrimSpace(channelIdentityID)
	if token == "" || channelIdentityID == "" {
		return Binding{}, ErrInvalidInput
	}
	binding, ok, err := s.store.RedeemLinkCode(ctx, token, channelIdentityID)
	if err != nil {
		return Binding{}, err
	}
	if !ok {
		return Binding{}, s.classifyRedeemNoRow(ctx, token)
	}
	return binding, nil
}

func (s *Service) classifyRedeemNoRow(ctx context.Context, token string) error {
	state, found, err := s.store.FindLinkCode(ctx, token)
	if err != nil {
		return err
	}
	if !found {
		return ErrCodeNotFound
	}
	if state.Consumed {
		return ErrCodeConsumed
	}
	if state.ExpiresAt.IsZero() || !state.ExpiresAt.After(s.now()) {
		return ErrCodeExpired
	}
	return ErrCodeConsumed
}

// ListUserBindings returns the channel identities bound to a user's account.
func (s *Service) ListUserBindings(ctx context.Context, userID string) ([]Binding, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("channelaccess service not configured")
	}
	items, err := s.store.ListBindingsForUser(ctx, strings.TrimSpace(userID))
	if err != nil {
		return nil, err
	}
	identities, err := s.identityIndex(ctx, bindingIdentityIDs(items))
	if err != nil {
		return nil, err
	}
	for i := range items {
		enrichBinding(&items[i], identities[strings.TrimSpace(items[i].ChannelIdentityID)])
	}
	return items, nil
}

// Unbind removes a channel identity binding from a user's account.
func (s *Service) Unbind(ctx context.Context, userID, channelIdentityID string) error {
	if s == nil || s.store == nil {
		return errors.New("channelaccess service not configured")
	}
	return s.store.DeleteBinding(ctx, strings.TrimSpace(userID), strings.TrimSpace(channelIdentityID))
}

// SetManager sets a local Manage override (ON/OFF) for a channel identity on a bot.
func (s *Service) SetManager(ctx context.Context, botID, channelIdentityID string, granted bool, actorUserID string) error {
	if s == nil || s.acl == nil {
		return errors.New("channelaccess service not configured")
	}
	_, err := s.acl.SetManageOverride(ctx, botID, channelIdentityID, granted, actorUserID)
	return err
}

// ClearManagerOverride removes the local Manage override so the identity falls back
// to inheritance.
func (s *Service) ClearManagerOverride(ctx context.Context, botID, channelIdentityID string) error {
	if s == nil || s.acl == nil {
		return errors.New("channelaccess service not configured")
	}
	return s.acl.DeleteManageOverride(ctx, botID, channelIdentityID)
}

// ListManagers returns the effective Manage state per channel identity on a bot,
// merging inherited members (bound web members carrying Manage) with local overrides.
func (s *Service) ListManagers(ctx context.Context, botID string) ([]Manager, error) {
	if s == nil || s.acl == nil {
		return nil, errors.New("channelaccess service not configured")
	}
	byIdentity := map[string]*Manager{}
	boundIdentityIDs := make([]string, 0)

	if s.bots != nil && s.store != nil {
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
			bindings, err := s.store.ListBindingsForUser(ctx, userID)
			if err != nil {
				return nil, err
			}
			for _, b := range bindings {
				mergeManagerBinding(byIdentity, b, carriesManage)
				boundIdentityIDs = append(boundIdentityIDs, b.ChannelIdentityID)
			}
		}
		if everyoneCarriesManage {
			bindings, err := s.store.ListBindingsForBot(ctx, botID)
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

func mergeManagerBinding(byIdentity map[string]*Manager, binding Binding, carriesManage bool) {
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

func (s *Service) identityIndex(ctx context.Context, ids []string) (map[string]ChannelIdentity, error) {
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
		return nil, errors.New("channelaccess channel identity reader not configured")
	}
	items, err := s.identities.ListChannelIdentities(ctx, unique)
	if err != nil {
		return nil, fmt.Errorf("list channel identities: %w", err)
	}
	index := make(map[string]ChannelIdentity, len(items))
	for _, item := range items {
		if id := strings.TrimSpace(item.ID); id != "" {
			index[id] = item
		}
	}
	return index, nil
}

func bindingIdentityIDs(items []Binding) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id := strings.TrimSpace(item.ChannelIdentityID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func enrichBinding(binding *Binding, identity ChannelIdentity) {
	if binding == nil || identity.ID == "" {
		return
	}
	binding.ChannelType = identity.Channel
	binding.ChannelSubjectID = identity.ChannelSubjectID
	binding.ChannelIdentityDisplayName = identity.DisplayName
	binding.ChannelIdentityAvatarURL = identity.AvatarURL
}

func enrichManager(manager *Manager, identity ChannelIdentity) {
	if manager == nil || identity.ID == "" {
		return
	}
	manager.ChannelType = identity.Channel
	manager.ChannelSubjectID = identity.ChannelSubjectID
	manager.ChannelIdentityDisplayName = identity.DisplayName
	manager.ChannelIdentityAvatarURL = identity.AvatarURL
}

func normalizeToken(token string) string {
	return strings.ToUpper(strings.TrimSpace(token))
}

func generateToken() (string, error) {
	buf := make([]byte, tokenLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, tokenLength)
	for i, b := range buf {
		out[i] = tokenAlphabet[int(b)%len(tokenAlphabet)]
	}
	return string(out), nil
}
