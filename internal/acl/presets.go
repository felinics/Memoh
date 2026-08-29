package acl

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	PresetAllowAll           = "allow_all"
	PresetPrivateOnly        = "private_only"
	PresetGroupOnly          = "group_only"
	PresetGroupAndThreadOnly = "group_and_thread_only"
	PresetDenyAll            = "deny_all"
)

var ErrUnknownPreset = errors.New("unknown acl preset")

type Preset struct {
	Key           string
	DefaultEffect string
	Rules         []CreateRuleRequest
}

func DefaultPresetKey() string {
	return PresetAllowAll
}

func NormalizePresetKey(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return DefaultPresetKey()
	}
	return value
}

func ResolvePreset(raw string) (Preset, error) {
	switch NormalizePresetKey(raw) {
	case PresetAllowAll:
		return Preset{
			Key:           PresetAllowAll,
			DefaultEffect: EffectAllow,
		}, nil
	case PresetPrivateOnly:
		return Preset{
			Key:           PresetPrivateOnly,
			DefaultEffect: EffectDeny,
			Rules: []CreateRuleRequest{
				{
					Enabled:     true,
					Effect:      EffectAllow,
					SourceScope: &SourceScope{ConversationType: "private"},
				},
			},
		}, nil
	case PresetGroupOnly:
		return Preset{
			Key:           PresetGroupOnly,
			DefaultEffect: EffectDeny,
			Rules: []CreateRuleRequest{
				{
					Enabled:     true,
					Effect:      EffectAllow,
					SourceScope: &SourceScope{ConversationType: "group"},
				},
			},
		}, nil
	case PresetGroupAndThreadOnly:
		return Preset{
			Key:           PresetGroupAndThreadOnly,
			DefaultEffect: EffectDeny,
			Rules: []CreateRuleRequest{
				{
					Enabled:     true,
					Effect:      EffectAllow,
					SourceScope: &SourceScope{ConversationType: "group"},
				},
				{
					Enabled:     true,
					Effect:      EffectAllow,
					SourceScope: &SourceScope{ConversationType: "thread"},
				},
			},
		}, nil
	case PresetDenyAll:
		return Preset{
			Key:           PresetDenyAll,
			DefaultEffect: EffectDeny,
		}, nil
	default:
		return Preset{}, ErrUnknownPreset
	}
}

func ApplyPreset(ctx context.Context, queries dbstore.Queries, botID, createdByUserID, rawPreset string) error {
	if queries == nil {
		return errors.New("acl queries not configured")
	}

	preset, err := ResolvePreset(rawPreset)
	if err != nil {
		return err
	}

	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}

	if err := queries.SetBotACLDefaultEffect(ctx, sqlc.SetBotACLDefaultEffectParams{
		ID:               pgBotID,
		AclDefaultEffect: preset.DefaultEffect,
	}); err != nil {
		return err
	}

	for _, rule := range preset.Rules {
		if err := applyPresetRule(ctx, queries, pgBotID, createdByUserID, rule); err != nil {
			return err
		}
	}

	return nil
}

func applyPresetRule(ctx context.Context, queries dbstore.Queries, botID pgtype.UUID, createdByUserID string, rule CreateRuleRequest) error {
	if err := validateEffect(rule.Effect); err != nil {
		return err
	}

	sourceScope, err := normalizeOptionalSourceScope(rule.SourceScope)
	if err != nil {
		return err
	}

	sourceChannel, err := presetSourceChannel(rule.SubjectChannelType, sourceScope)
	if err != nil {
		return err
	}

	_, err = queries.CreateBotACLRule(ctx, sqlc.CreateBotACLRuleParams{
		BotID:                  botID,
		Enabled:                rule.Enabled,
		Description:            optionalText(rule.Description),
		Effect:                 rule.Effect,
		ChannelIdentityID:      optionalUUID(rule.ChannelIdentityID),
		SubjectChannelType:     optionalText(rule.SubjectChannelType),
		SourceChannel:          optionalText(sourceChannel),
		SourceConversationType: optionalText(sourceScope.ConversationType),
		SourceConversationID:   optionalText(sourceScope.ConversationID),
		SourceThreadID:         optionalText(sourceScope.ThreadID),
		CreatedByUserID:        optionalUUID(createdByUserID),
	})
	return err
}

func presetSourceChannel(subjectChannelType string, sourceScope SourceScope) (string, error) {
	if sourceScope.IsZero() {
		return "", nil
	}
	if sourceScope.ConversationID == "" && sourceScope.ThreadID == "" {
		return "", nil
	}

	subjectChannelType = strings.TrimSpace(subjectChannelType)
	if subjectChannelType == "" {
		return "", ErrInvalidRuleSubject
	}
	return subjectChannelType, nil
}
