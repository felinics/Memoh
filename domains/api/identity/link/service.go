// Package link owns the global link between a web account and a channel
// identity, including one-time link-code issuance and redemption.
package link

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	linkpersistence "github.com/memohai/memoh/domains/api/identity/link/persistence"
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
	store      linkpersistence.Store
	identities linkpersistence.ChannelIdentityReader
	logger     *slog.Logger
	now        func() time.Time
	token      func() (string, error)
	codeTTL    time.Duration
}

func NewService(log *slog.Logger, store linkpersistence.Store, identities linkpersistence.ChannelIdentityReader) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:      store,
		identities: identities,
		logger:     log.With(slog.String("service", "identity_link")),
		now:        func() time.Time { return time.Now().UTC() },
		token:      generateToken,
		codeTTL:    defaultCodeTTL,
	}
}

// IssueLinkCode generates a one-time code the user sends as /link <code> in IM.
func (s *Service) IssueLinkCode(ctx context.Context, userID, channelType string) (linkpersistence.LinkCode, error) {
	if s == nil || s.store == nil {
		return linkpersistence.LinkCode{}, errors.New("identity link service not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return linkpersistence.LinkCode{}, ErrInvalidInput
	}
	token, err := s.token()
	if err != nil {
		return linkpersistence.LinkCode{}, err
	}
	return s.store.CreateLinkCode(ctx, linkpersistence.CreateLinkCodeCommand{
		Token:       token,
		UserID:      userID,
		ChannelType: strings.TrimSpace(channelType),
		ExpiresAt:   s.now().Add(s.codeTTL),
	})
}

// ConsumeLinkCode binds the given channel identity to the user that owns the code.
func (s *Service) ConsumeLinkCode(ctx context.Context, token, channelIdentityID string) (linkpersistence.Binding, error) {
	if s == nil || s.store == nil {
		return linkpersistence.Binding{}, errors.New("identity link service not configured")
	}
	token = normalizeToken(token)
	channelIdentityID = strings.TrimSpace(channelIdentityID)
	if token == "" || channelIdentityID == "" {
		return linkpersistence.Binding{}, ErrInvalidInput
	}
	binding, ok, err := s.store.RedeemLinkCode(ctx, token, channelIdentityID)
	if err != nil {
		return linkpersistence.Binding{}, err
	}
	if !ok {
		return linkpersistence.Binding{}, s.classifyRedeemNoRow(ctx, token)
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
func (s *Service) ListUserBindings(ctx context.Context, userID string) ([]linkpersistence.Binding, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("identity link service not configured")
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
		return errors.New("identity link service not configured")
	}
	return s.store.DeleteBinding(ctx, strings.TrimSpace(userID), strings.TrimSpace(channelIdentityID))
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
		return nil, errors.New("identity link channel identity reader not configured")
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

func bindingIdentityIDs(items []linkpersistence.Binding) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id := strings.TrimSpace(item.ChannelIdentityID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func enrichBinding(binding *linkpersistence.Binding, identity linkpersistence.ChannelIdentity) {
	if binding == nil || identity.ID == "" {
		return
	}
	binding.ChannelType = identity.Channel
	binding.ChannelSubjectID = identity.ChannelSubjectID
	binding.ChannelIdentityDisplayName = identity.DisplayName
	binding.ChannelIdentityAvatarURL = identity.AvatarURL
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
