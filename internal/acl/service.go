package acl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

var (
	ErrInvalidRuleSubject = errors.New("invalid rule target")
	ErrInvalidSourceScope = errors.New("invalid source scope")
	ErrInvalidEffect      = errors.New("effect must be 'allow' or 'deny'")
)

type Service struct {
	queries dbstore.Queries
	logger  *slog.Logger
}

func NewService(log *slog.Logger, queries dbstore.Queries) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		queries: queries,
		logger:  log.With(slog.String("service", "acl")),
	}
}

// Evaluate checks whether the given request is allowed to perform chat.trigger.
// Rules only override the bot's default mode: deny rules matter in blacklist mode,
// and allow rules matter in whitelist mode.
func (s *Service) Evaluate(ctx context.Context, req EvaluateRequest) (bool, error) {
	// Validate scope before any service nil checks so callers get meaningful errors.
	sourceScope, err := normalizeSourceScope(req.SourceScope)
	if err != nil {
		return false, err
	}

	if s == nil || s.queries == nil {
		return false, errors.New("acl service not configured")
	}

	botID := strings.TrimSpace(req.BotID)
	channelIdentityID := strings.TrimSpace(req.ChannelIdentityID)
	channelType := strings.TrimSpace(req.ChannelType)

	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return false, err
	}

	effect, err := s.queries.EvaluateBotACLRule(ctx, sqlc.EvaluateBotACLRuleParams{
		BotID:                  pgBotID,
		Action:                 ActionChatTrigger,
		ChannelIdentityID:      optionalUUID(channelIdentityID),
		SubjectChannelType:     optionalText(channelType),
		SourceConversationType: optionalText(sourceScope.ConversationType),
		SourceConversationID:   optionalText(sourceScope.ConversationID),
		SourceThreadID:         optionalText(sourceScope.ThreadID),
	})
	if err != nil {
		return false, err
	}
	return effect == EffectAllow, nil
}

// GetDefaultEffect returns the bot's fallback ACL effect.
func (s *Service) GetDefaultEffect(ctx context.Context, botID string) (string, error) {
	if s == nil || s.queries == nil {
		return "", errors.New("acl service not configured")
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return "", err
	}
	return s.queries.GetBotACLDefaultEffect(ctx, pgBotID)
}

// SetDefaultEffect sets the bot's fallback ACL effect.
func (s *Service) SetDefaultEffect(ctx context.Context, botID, effect string) error {
	if s == nil || s.queries == nil {
		return errors.New("acl service not configured")
	}
	effect = strings.TrimSpace(effect)
	if effect != EffectAllow && effect != EffectDeny {
		return ErrInvalidEffect
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return s.queries.SetBotACLDefaultEffect(ctx, sqlc.SetBotACLDefaultEffectParams{
		ID:               pgBotID,
		AclDefaultEffect: effect,
	})
}

// ListRules returns all ACL rules for a bot, newest first.
func (s *Service) ListRules(ctx context.Context, botID string) ([]Rule, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("acl service not configured")
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotACLRules(ctx, pgBotID)
	if err != nil {
		return nil, err
	}
	items := make([]Rule, 0, len(rows))
	for _, row := range rows {
		items = append(items, ruleFromListRow(row))
	}
	return items, nil
}

// CreateRule creates a new ACL rule.
func (s *Service) CreateRule(ctx context.Context, botID, createdByUserID string, req CreateRuleRequest) (Rule, error) {
	if s == nil || s.queries == nil {
		return Rule{}, errors.New("acl service not configured")
	}
	if err := validateEffect(req.Effect); err != nil {
		return Rule{}, err
	}
	if err := s.validateTarget(ctx, req.ChannelIdentityID, req.SubjectChannelType); err != nil {
		return Rule{}, err
	}
	sourceScope, err := normalizeOptionalSourceScope(req.SourceScope)
	if err != nil {
		return Rule{}, err
	}
	sourceChannel, err := s.resolveSourceChannel(ctx, sourceScope, req.SubjectChannelType, req.ChannelIdentityID)
	if err != nil {
		return Rule{}, err
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return Rule{}, err
	}
	row, err := s.queries.CreateBotACLRule(ctx, sqlc.CreateBotACLRuleParams{
		BotID:                  pgBotID,
		Enabled:                req.Enabled,
		Description:            optionalText(req.Description),
		Effect:                 req.Effect,
		ChannelIdentityID:      optionalUUID(req.ChannelIdentityID),
		SubjectChannelType:     optionalText(req.SubjectChannelType),
		SourceChannel:          optionalText(sourceChannel),
		SourceConversationType: optionalText(sourceScope.ConversationType),
		SourceConversationID:   optionalText(sourceScope.ConversationID),
		SourceThreadID:         optionalText(sourceScope.ThreadID),
		CreatedByUserID:        optionalUUID(createdByUserID),
	})
	if err != nil {
		return Rule{}, err
	}
	return ruleFromWrite(row), nil
}

// UpdateRule updates an existing ACL rule.
func (s *Service) UpdateRule(ctx context.Context, ruleID string, req UpdateRuleRequest) (Rule, error) {
	if s == nil || s.queries == nil {
		return Rule{}, errors.New("acl service not configured")
	}
	if err := validateEffect(req.Effect); err != nil {
		return Rule{}, err
	}
	if err := s.validateTarget(ctx, req.ChannelIdentityID, req.SubjectChannelType); err != nil {
		return Rule{}, err
	}
	sourceScope, err := normalizeOptionalSourceScope(req.SourceScope)
	if err != nil {
		return Rule{}, err
	}
	sourceChannel, err := s.resolveSourceChannel(ctx, sourceScope, req.SubjectChannelType, req.ChannelIdentityID)
	if err != nil {
		return Rule{}, err
	}
	pgRuleID, err := db.ParseUUID(ruleID)
	if err != nil {
		return Rule{}, err
	}
	row, err := s.queries.UpdateBotACLRule(ctx, sqlc.UpdateBotACLRuleParams{
		ID:                     pgRuleID,
		Enabled:                req.Enabled,
		Description:            optionalText(req.Description),
		Effect:                 req.Effect,
		ChannelIdentityID:      optionalUUID(req.ChannelIdentityID),
		SubjectChannelType:     optionalText(req.SubjectChannelType),
		SourceChannel:          optionalText(sourceChannel),
		SourceConversationType: optionalText(sourceScope.ConversationType),
		SourceConversationID:   optionalText(sourceScope.ConversationID),
		SourceThreadID:         optionalText(sourceScope.ThreadID),
	})
	if err != nil {
		return Rule{}, err
	}
	return ruleFromUpdateRow(row), nil
}

// resolveSourceChannel derives the source_channel value from the rule's target context.
// source_channel is required by DB constraint whenever source_conversation_id or source_thread_id is set.
func (s *Service) resolveSourceChannel(ctx context.Context, scope SourceScope, subjectChannelType, channelIdentityID string) (string, error) {
	if scope.IsZero() {
		return "", nil
	}
	subjectChannelType = strings.TrimSpace(subjectChannelType)
	if subjectChannelType != "" {
		return subjectChannelType, nil
	}
	channelIdentityID = strings.TrimSpace(channelIdentityID)
	if channelIdentityID != "" {
		pgID, err := db.ParseUUID(strings.TrimSpace(channelIdentityID))
		if err != nil {
			return "", fmt.Errorf("resolve source channel: %w", err)
		}
		identity, err := s.queries.GetChannelIdentityByID(ctx, pgID)
		if err != nil {
			return "", fmt.Errorf("resolve source channel: get identity: %w", err)
		}
		return strings.TrimSpace(identity.ChannelType), nil
	}
	return "", nil
}

// DeleteRule removes an ACL rule by ID.
func (s *Service) DeleteRule(ctx context.Context, ruleID string) error {
	if s == nil || s.queries == nil {
		return errors.New("acl service not configured")
	}
	pgRuleID, err := db.ParseUUID(ruleID)
	if err != nil {
		return err
	}
	return s.queries.DeleteBotACLRuleByID(ctx, pgRuleID)
}

// GetManageOverride returns the local Manage override for a channel identity on a
// bot. exists reports whether a row is present; when false, granted is meaningless
// and callers should fall back to inheritance.
func (s *Service) GetManageOverride(ctx context.Context, botID, channelIdentityID string) (granted bool, exists bool, err error) {
	if s == nil || s.queries == nil {
		return false, false, errors.New("acl service not configured")
	}
	pgBotID, err := db.ParseUUID(strings.TrimSpace(botID))
	if err != nil {
		return false, false, err
	}
	pgIdentityID, err := db.ParseUUID(strings.TrimSpace(channelIdentityID))
	if err != nil {
		return false, false, err
	}
	row, err := s.queries.GetBotChannelAdmin(ctx, sqlc.GetBotChannelAdminParams{
		BotID:             pgBotID,
		ChannelIdentityID: pgIdentityID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, false, nil
		}
		return false, false, err
	}
	return row.Granted, true, nil
}

// ListManageOverrides returns the local Manage overrides for a bot.
func (s *Service) ListManageOverrides(ctx context.Context, botID string) ([]ManageOverride, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("acl service not configured")
	}
	pgBotID, err := db.ParseUUID(strings.TrimSpace(botID))
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotChannelAdmins(ctx, pgBotID)
	if err != nil {
		return nil, err
	}
	items := make([]ManageOverride, 0, len(rows))
	for _, row := range rows {
		items = append(items, ManageOverride{
			ID:                         uuid.UUID(row.ID.Bytes).String(),
			BotID:                      uuid.UUID(row.BotID.Bytes).String(),
			ChannelIdentityID:          uuid.UUID(row.ChannelIdentityID.Bytes).String(),
			Granted:                    row.Granted,
			ChannelType:                strings.TrimSpace(row.ChannelType.String),
			ChannelSubjectID:           strings.TrimSpace(row.ChannelSubjectID.String),
			ChannelIdentityDisplayName: strings.TrimSpace(row.ChannelIdentityDisplayName.String),
			ChannelIdentityAvatarURL:   strings.TrimSpace(row.ChannelIdentityAvatarUrl.String),
			CreatedAt:                  timeFromPg(row.CreatedAt),
		})
	}
	return items, nil
}

// SetManageOverride upserts a local Manage override (granted ON/OFF) for a channel
// identity on a bot.
func (s *Service) SetManageOverride(ctx context.Context, botID, channelIdentityID string, granted bool, createdByUserID string) (ManageOverride, error) {
	if s == nil || s.queries == nil {
		return ManageOverride{}, errors.New("acl service not configured")
	}
	channelIdentityID = strings.TrimSpace(channelIdentityID)
	if channelIdentityID == "" {
		return ManageOverride{}, ErrInvalidRuleSubject
	}
	pgBotID, err := db.ParseUUID(strings.TrimSpace(botID))
	if err != nil {
		return ManageOverride{}, err
	}
	pgIdentityID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return ManageOverride{}, err
	}
	if _, err := s.queries.GetChannelIdentityByID(ctx, pgIdentityID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ManageOverride{}, ErrInvalidRuleSubject
		}
		return ManageOverride{}, err
	}
	row, err := s.queries.UpsertBotChannelAdmin(ctx, sqlc.UpsertBotChannelAdminParams{
		BotID:             pgBotID,
		ChannelIdentityID: pgIdentityID,
		Granted:           granted,
		CreatedByUserID:   optionalUUID(createdByUserID),
	})
	if err != nil {
		return ManageOverride{}, err
	}
	return ManageOverride{
		ID:                uuid.UUID(row.ID.Bytes).String(),
		BotID:             uuid.UUID(row.BotID.Bytes).String(),
		ChannelIdentityID: uuid.UUID(row.ChannelIdentityID.Bytes).String(),
		Granted:           row.Granted,
		CreatedAt:         timeFromPg(row.CreatedAt),
	}, nil
}

// DeleteManageOverride removes a local Manage override, so the channel identity
// falls back to its inherited grant.
func (s *Service) DeleteManageOverride(ctx context.Context, botID, channelIdentityID string) error {
	if s == nil || s.queries == nil {
		return errors.New("acl service not configured")
	}
	pgBotID, err := db.ParseUUID(strings.TrimSpace(botID))
	if err != nil {
		return err
	}
	pgIdentityID, err := db.ParseUUID(strings.TrimSpace(channelIdentityID))
	if err != nil {
		return err
	}
	return s.queries.DeleteBotChannelAdmin(ctx, sqlc.DeleteBotChannelAdminParams{
		BotID:             pgBotID,
		ChannelIdentityID: pgIdentityID,
	})
}

// ListObservedConversationsByChannelIdentity returns conversations observed for a specific
// channel identity under a bot, useful for building scoped rule source selectors.
func (s *Service) ListObservedConversationsByChannelIdentity(ctx context.Context, botID, channelIdentityID string) ([]ObservedConversationCandidate, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("acl service not configured")
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	pgIdentityID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListObservedConversationsByChannelIdentity(ctx, sqlc.ListObservedConversationsByChannelIdentityParams{
		BotID:             pgBotID,
		ChannelIdentityID: pgIdentityID,
	})
	if err != nil {
		return nil, err
	}
	items := make([]ObservedConversationCandidate, 0, len(rows))
	for _, row := range rows {
		items = append(items, ObservedConversationCandidate{
			RouteID:               row.RouteID.String(),
			Channel:               strings.TrimSpace(row.Channel),
			ConversationType:      strings.TrimSpace(row.ConversationType),
			ConversationID:        strings.TrimSpace(row.ConversationID),
			ThreadID:              strings.TrimSpace(row.ThreadID),
			ConversationName:      strings.TrimSpace(row.ConversationName),
			ConversationAvatarURL: strings.TrimSpace(row.ConversationAvatarUrl),
			LastObservedAt:        timeFromPg(row.LastObservedAt),
		})
	}
	return items, nil
}

// ListObservedConversationsByChannelType returns conversations observed on a platform type
// for this bot (any sender), for scoped rule building when subject is channel_type.
func (s *Service) ListObservedConversationsByChannelType(ctx context.Context, botID, channelType string) ([]ObservedConversationCandidate, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("acl service not configured")
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	channelType = strings.TrimSpace(channelType)
	if channelType == "" {
		return nil, errors.New("channel_type is required")
	}
	rows, err := s.queries.ListObservedConversationsByChannelType(ctx, sqlc.ListObservedConversationsByChannelTypeParams{
		BotID:       pgBotID,
		ChannelType: channelType,
	})
	if err != nil {
		return nil, err
	}
	items := make([]ObservedConversationCandidate, 0, len(rows))
	for _, row := range rows {
		items = append(items, ObservedConversationCandidate{
			RouteID:               row.RouteID.String(),
			Channel:               strings.TrimSpace(row.Channel),
			ConversationType:      strings.TrimSpace(row.ConversationType),
			ConversationID:        strings.TrimSpace(row.ConversationID),
			ThreadID:              strings.TrimSpace(row.ThreadID),
			ConversationName:      strings.TrimSpace(row.ConversationName),
			ConversationAvatarURL: strings.TrimSpace(row.ConversationAvatarUrl),
			LastObservedAt:        timeFromPg(row.LastObservedAt),
		})
	}
	return items, nil
}

// ---- helpers ----

func validateEffect(effect string) error {
	switch strings.TrimSpace(effect) {
	case EffectAllow, EffectDeny:
		return nil
	}
	return ErrInvalidEffect
}

func (s *Service) validateTarget(ctx context.Context, channelIdentityID, channelType string) error {
	channelIdentityID = strings.TrimSpace(channelIdentityID)
	channelType = strings.TrimSpace(channelType)
	if channelIdentityID == "" {
		return nil
	}
	pgID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return err
	}
	identity, err := s.queries.GetChannelIdentityByID(ctx, pgID)
	if err != nil {
		return err
	}
	if channelType != "" && strings.TrimSpace(identity.ChannelType) != channelType {
		return ErrInvalidRuleSubject
	}
	return nil
}

func normalizeSourceScope(scope SourceScope) (SourceScope, error) {
	normalized := scope.Normalize()
	if normalized.ThreadID != "" && normalized.ConversationID == "" {
		return SourceScope{}, ErrInvalidSourceScope
	}
	return normalized, nil
}

func normalizeOptionalSourceScope(scope *SourceScope) (SourceScope, error) {
	if scope == nil {
		return SourceScope{}, nil
	}
	return normalizeSourceScope(*scope)
}

func optionalUUID(value string) pgtype.UUID {
	parsed, err := db.ParseUUID(strings.TrimSpace(value))
	if err != nil {
		return pgtype.UUID{}
	}
	return parsed
}

func optionalText(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func sourceScopeFromPg(conversationTypeValue, conversationIDValue, threadIDValue pgtype.Text) *SourceScope {
	scope := SourceScope{
		ConversationType: strings.TrimSpace(conversationTypeValue.String),
		ConversationID:   strings.TrimSpace(conversationIDValue.String),
		ThreadID:         strings.TrimSpace(threadIDValue.String),
	}
	if scope.IsZero() {
		return nil
	}
	return &scope
}

func timeFromPg(value pgtype.Timestamptz) time.Time {
	if value.Valid {
		return value.Time
	}
	return time.Time{}
}

func ruleFromListRow(row sqlc.ListBotACLRulesRow) Rule {
	rule := Rule{
		ID:                          uuid.UUID(row.ID.Bytes).String(),
		BotID:                       uuid.UUID(row.BotID.Bytes).String(),
		Enabled:                     row.Enabled,
		Description:                 strings.TrimSpace(row.Description.String),
		Action:                      row.Action,
		Effect:                      row.Effect,
		SubjectChannelType:          strings.TrimSpace(row.SubjectChannelType.String),
		ChannelType:                 strings.TrimSpace(row.ChannelType.String),
		ChannelSubjectID:            strings.TrimSpace(row.ChannelSubjectID.String),
		ChannelIdentityDisplayName:  strings.TrimSpace(row.ChannelIdentityDisplayName.String),
		ChannelIdentityAvatarURL:    strings.TrimSpace(row.ChannelIdentityAvatarUrl.String),
		SourceConversationName:      strings.TrimSpace(row.SourceConversationName),
		SourceConversationAvatarURL: strings.TrimSpace(row.SourceConversationAvatarUrl),
		CreatedAt:                   timeFromPg(row.CreatedAt),
		UpdatedAt:                   timeFromPg(row.UpdatedAt),
	}
	rule.SourceScope = sourceScopeFromPg(row.SourceConversationType, row.SourceConversationID, row.SourceThreadID)
	if row.ChannelIdentityID.Valid {
		rule.ChannelIdentityID = uuid.UUID(row.ChannelIdentityID.Bytes).String()
	}
	return rule
}

func ruleFromWrite(row sqlc.BotAclRule) Rule {
	rule := Rule{
		ID:                 uuid.UUID(row.ID.Bytes).String(),
		BotID:              uuid.UUID(row.BotID.Bytes).String(),
		Enabled:            row.Enabled,
		Description:        strings.TrimSpace(row.Description.String),
		Action:             row.Action,
		Effect:             row.Effect,
		SubjectChannelType: strings.TrimSpace(row.SubjectChannelType.String),
		SourceScope:        sourceScopeFromPg(row.SourceConversationType, row.SourceConversationID, row.SourceThreadID),
		CreatedAt:          timeFromPg(row.CreatedAt),
		UpdatedAt:          timeFromPg(row.UpdatedAt),
	}
	if row.ChannelIdentityID.Valid {
		rule.ChannelIdentityID = uuid.UUID(row.ChannelIdentityID.Bytes).String()
	}
	return rule
}

func ruleFromUpdateRow(row sqlc.BotAclRule) Rule {
	rule := Rule{
		ID:                 uuid.UUID(row.ID.Bytes).String(),
		BotID:              uuid.UUID(row.BotID.Bytes).String(),
		Enabled:            row.Enabled,
		Description:        strings.TrimSpace(row.Description.String),
		Action:             row.Action,
		Effect:             row.Effect,
		SubjectChannelType: strings.TrimSpace(row.SubjectChannelType.String),
		SourceScope:        sourceScopeFromPg(row.SourceConversationType, row.SourceConversationID, row.SourceThreadID),
		CreatedAt:          timeFromPg(row.CreatedAt),
		UpdatedAt:          timeFromPg(row.UpdatedAt),
	}
	if row.ChannelIdentityID.Valid {
		rule.ChannelIdentityID = uuid.UUID(row.ChannelIdentityID.Bytes).String()
	}
	return rule
}
