package acl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	aclpersistence "github.com/memohai/memoh/domains/api/bot/access/acl/persistence"
)

var (
	ErrInvalidRuleSubject = errors.New("invalid rule target")
	ErrInvalidSourceScope = errors.New("invalid source scope")
	ErrInvalidEffect      = errors.New("effect must be 'allow' or 'deny'")
)

type Service struct {
	store      aclpersistence.Store
	identities aclpersistence.ChannelIdentityReader
	observed   aclpersistence.ObservedConversationReader
	logger     *slog.Logger
}

func NewService(log *slog.Logger, store aclpersistence.Store, identities aclpersistence.ChannelIdentityReader, observed aclpersistence.ObservedConversationReader) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:      store,
		identities: identities,
		observed:   observed,
		logger:     log.With(slog.String("service", "acl")),
	}
}

func (s *Service) Evaluate(ctx context.Context, req aclpersistence.EvaluateRequest) (bool, error) {
	sourceScope, err := normalizeSourceScope(req.SourceScope)
	if err != nil {
		return false, err
	}
	if err := s.configured(); err != nil {
		return false, err
	}
	effect, err := s.store.EvaluateRule(ctx, aclpersistence.Evaluation{
		BotID:             strings.TrimSpace(req.BotID),
		Action:            aclpersistence.ActionChatTrigger,
		ChannelIdentityID: strings.TrimSpace(req.ChannelIdentityID),
		ChannelType:       strings.TrimSpace(req.ChannelType),
		SourceScope:       sourceScope,
	})
	if err != nil {
		return false, err
	}
	return effect == aclpersistence.EffectAllow, nil
}

func (s *Service) GetDefaultEffect(ctx context.Context, botID string) (string, error) {
	if err := s.configured(); err != nil {
		return "", err
	}
	return s.store.GetDefaultEffect(ctx, strings.TrimSpace(botID))
}

func (s *Service) SetDefaultEffect(ctx context.Context, botID, effect string) error {
	if err := s.configured(); err != nil {
		return err
	}
	effect = strings.TrimSpace(effect)
	if err := validateEffect(effect); err != nil {
		return err
	}
	return s.store.SetDefaultEffect(ctx, strings.TrimSpace(botID), effect)
}

func (s *Service) ListRules(ctx context.Context, botID string) ([]aclpersistence.Rule, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	items, err := s.store.ListRules(ctx, strings.TrimSpace(botID))
	if err != nil {
		return nil, err
	}
	identities, err := s.identityIndex(ctx, ruleIdentityIDs(items))
	if err != nil {
		return nil, err
	}
	for i := range items {
		enrichRule(&items[i], identities[items[i].ChannelIdentityID])
	}
	return items, nil
}

func (s *Service) CreateRule(ctx context.Context, botID, createdByUserID string, req aclpersistence.CreateRuleRequest) (aclpersistence.Rule, error) {
	if err := s.configured(); err != nil {
		return aclpersistence.Rule{}, err
	}
	write, err := s.ruleWrite(ctx, req.Enabled, req.Description, req.Effect, req.ChannelIdentityID, req.SubjectChannelType, req.SourceScope)
	if err != nil {
		return aclpersistence.Rule{}, err
	}
	write.BotID = strings.TrimSpace(botID)
	write.CreatedByUserID = strings.TrimSpace(createdByUserID)
	return s.store.CreateRule(ctx, write)
}

func (s *Service) UpdateRule(ctx context.Context, ruleID string, req aclpersistence.UpdateRuleRequest) (aclpersistence.Rule, error) {
	if err := s.configured(); err != nil {
		return aclpersistence.Rule{}, err
	}
	write, err := s.ruleWrite(ctx, req.Enabled, req.Description, req.Effect, req.ChannelIdentityID, req.SubjectChannelType, req.SourceScope)
	if err != nil {
		return aclpersistence.Rule{}, err
	}
	write.ID = strings.TrimSpace(ruleID)
	return s.store.UpdateRule(ctx, write)
}

func (s *Service) ruleWrite(ctx context.Context, enabled bool, description, effect, channelIdentityID, subjectChannelType string, scope *aclpersistence.SourceScope) (aclpersistence.RuleWrite, error) {
	effect = strings.TrimSpace(effect)
	if err := validateEffect(effect); err != nil {
		return aclpersistence.RuleWrite{}, err
	}
	identity, err := s.targetIdentity(ctx, channelIdentityID, subjectChannelType)
	if err != nil {
		return aclpersistence.RuleWrite{}, err
	}
	sourceScope, err := normalizeOptionalSourceScope(scope)
	if err != nil {
		return aclpersistence.RuleWrite{}, err
	}
	sourceChannel := resolveSourceChannel(sourceScope, subjectChannelType, identity)
	return aclpersistence.RuleWrite{
		Enabled:            enabled,
		Description:        strings.TrimSpace(description),
		Effect:             effect,
		ChannelIdentityID:  strings.TrimSpace(channelIdentityID),
		SubjectChannelType: strings.TrimSpace(subjectChannelType),
		SourceChannel:      sourceChannel,
		SourceScope:        sourceScope,
	}, nil
}

func resolveSourceChannel(scope aclpersistence.SourceScope, subjectChannelType string, identity aclpersistence.ChannelIdentity) string {
	if scope.IsZero() {
		return ""
	}
	if channelType := strings.TrimSpace(subjectChannelType); channelType != "" {
		return channelType
	}
	return strings.TrimSpace(identity.Channel)
}

func (s *Service) DeleteRule(ctx context.Context, ruleID string) error {
	if err := s.configured(); err != nil {
		return err
	}
	return s.store.DeleteRule(ctx, strings.TrimSpace(ruleID))
}

func (s *Service) GetManageOverride(ctx context.Context, botID, channelIdentityID string) (bool, bool, error) {
	if err := s.configured(); err != nil {
		return false, false, err
	}
	override, exists, err := s.store.GetManageOverride(ctx, strings.TrimSpace(botID), strings.TrimSpace(channelIdentityID))
	if err != nil || !exists {
		return false, exists, err
	}
	return override.Granted, true, nil
}

func (s *Service) ListManageOverrides(ctx context.Context, botID string) ([]aclpersistence.ManageOverride, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	items, err := s.store.ListManageOverrides(ctx, strings.TrimSpace(botID))
	if err != nil {
		return nil, err
	}
	identities, err := s.identityIndex(ctx, manageOverrideIdentityIDs(items))
	if err != nil {
		return nil, err
	}
	for i := range items {
		enrichManageOverride(&items[i], identities[items[i].ChannelIdentityID])
	}
	return items, nil
}

func (s *Service) SetManageOverride(ctx context.Context, botID, channelIdentityID string, granted bool, createdByUserID string) (aclpersistence.ManageOverride, error) {
	if err := s.configured(); err != nil {
		return aclpersistence.ManageOverride{}, err
	}
	channelIdentityID = strings.TrimSpace(channelIdentityID)
	if channelIdentityID == "" {
		return aclpersistence.ManageOverride{}, ErrInvalidRuleSubject
	}
	_, exists, err := s.findChannelIdentity(ctx, channelIdentityID)
	if err != nil {
		return aclpersistence.ManageOverride{}, err
	}
	if !exists {
		return aclpersistence.ManageOverride{}, ErrInvalidRuleSubject
	}
	return s.store.UpsertManageOverride(ctx, aclpersistence.ManageOverrideWrite{
		BotID: strings.TrimSpace(botID), ChannelIdentityID: channelIdentityID,
		Granted: granted, CreatedByUserID: strings.TrimSpace(createdByUserID),
	})
}

func (s *Service) DeleteManageOverride(ctx context.Context, botID, channelIdentityID string) error {
	if err := s.configured(); err != nil {
		return err
	}
	return s.store.DeleteManageOverride(ctx, strings.TrimSpace(botID), strings.TrimSpace(channelIdentityID))
}

func (s *Service) ListObservedConversationsByChannelIdentity(ctx context.Context, botID, channelIdentityID string) ([]aclpersistence.ObservedConversationCandidate, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	if s.observed == nil {
		return nil, errors.New("acl observed conversation reader not configured")
	}
	return s.observed.ListObservedConversations(ctx, aclpersistence.ObservedConversationQuery{
		BotID: strings.TrimSpace(botID), ChannelIdentityID: strings.TrimSpace(channelIdentityID),
	})
}

func (s *Service) ListObservedConversationsByChannelType(ctx context.Context, botID, channelType string) ([]aclpersistence.ObservedConversationCandidate, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	channelType = strings.TrimSpace(channelType)
	if channelType == "" {
		return nil, errors.New("channel_type is required")
	}
	if s.observed == nil {
		return nil, errors.New("acl observed conversation reader not configured")
	}
	return s.observed.ListObservedConversations(ctx, aclpersistence.ObservedConversationQuery{
		BotID: strings.TrimSpace(botID), ChannelType: channelType,
	})
}

func (s *Service) configured() error {
	if s == nil || s.store == nil {
		return errors.New("acl service not configured")
	}
	return nil
}

func validateEffect(effect string) error {
	switch strings.TrimSpace(effect) {
	case aclpersistence.EffectAllow, aclpersistence.EffectDeny:
		return nil
	}
	return ErrInvalidEffect
}

func (s *Service) validateTarget(ctx context.Context, channelIdentityID, channelType string) error {
	_, err := s.targetIdentity(ctx, channelIdentityID, channelType)
	return err
}

func (s *Service) targetIdentity(ctx context.Context, channelIdentityID, channelType string) (aclpersistence.ChannelIdentity, error) {
	channelIdentityID = strings.TrimSpace(channelIdentityID)
	channelType = strings.TrimSpace(channelType)
	if channelIdentityID == "" {
		return aclpersistence.ChannelIdentity{}, nil
	}
	identity, exists, err := s.findChannelIdentity(ctx, channelIdentityID)
	if err != nil {
		return aclpersistence.ChannelIdentity{}, err
	}
	if !exists || (channelType != "" && strings.TrimSpace(identity.Channel) != channelType) {
		return aclpersistence.ChannelIdentity{}, ErrInvalidRuleSubject
	}
	return identity, nil
}

func (s *Service) findChannelIdentity(ctx context.Context, id string) (aclpersistence.ChannelIdentity, bool, error) {
	identities, err := s.identityIndex(ctx, []string{strings.TrimSpace(id)})
	if err != nil {
		return aclpersistence.ChannelIdentity{}, false, err
	}
	identity, exists := identities[strings.TrimSpace(id)]
	return identity, exists, nil
}

func (s *Service) identityIndex(ctx context.Context, ids []string) (map[string]aclpersistence.ChannelIdentity, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if s.identities == nil {
		return nil, errors.New("acl channel identity reader not configured")
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
	items, err := s.identities.ListChannelIdentities(ctx, unique)
	if err != nil {
		return nil, fmt.Errorf("list channel identities: %w", err)
	}
	index := make(map[string]aclpersistence.ChannelIdentity, len(items))
	for _, item := range items {
		if id := strings.TrimSpace(item.ID); id != "" {
			index[id] = item
		}
	}
	return index, nil
}

func ruleIdentityIDs(items []aclpersistence.Rule) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id := strings.TrimSpace(item.ChannelIdentityID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func manageOverrideIdentityIDs(items []aclpersistence.ManageOverride) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id := strings.TrimSpace(item.ChannelIdentityID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func enrichRule(rule *aclpersistence.Rule, identity aclpersistence.ChannelIdentity) {
	if rule == nil || identity.ID == "" {
		return
	}
	rule.ChannelType = identity.Channel
	rule.ChannelSubjectID = identity.ChannelSubjectID
	rule.ChannelIdentityDisplayName = identity.DisplayName
	rule.ChannelIdentityAvatarURL = identity.AvatarURL
}

func enrichManageOverride(override *aclpersistence.ManageOverride, identity aclpersistence.ChannelIdentity) {
	if override == nil || identity.ID == "" {
		return
	}
	override.ChannelType = identity.Channel
	override.ChannelSubjectID = identity.ChannelSubjectID
	override.ChannelIdentityDisplayName = identity.DisplayName
	override.ChannelIdentityAvatarURL = identity.AvatarURL
}

func normalizeSourceScope(scope aclpersistence.SourceScope) (aclpersistence.SourceScope, error) {
	normalized := scope.Normalize()
	if normalized.ThreadID != "" && normalized.ConversationID == "" {
		return aclpersistence.SourceScope{}, ErrInvalidSourceScope
	}
	return normalized, nil
}

func normalizeOptionalSourceScope(scope *aclpersistence.SourceScope) (aclpersistence.SourceScope, error) {
	if scope == nil {
		return aclpersistence.SourceScope{}, nil
	}
	return normalizeSourceScope(*scope)
}
