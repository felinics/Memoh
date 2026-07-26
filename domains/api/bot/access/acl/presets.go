package acl

import (
	"context"
	"errors"
	"strings"

	aclpersistence "github.com/memohai/memoh/domains/api/bot/access/acl/persistence"
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
	Rules         []aclpersistence.CreateRuleRequest
}

func DefaultPresetKey() string { return PresetAllowAll }

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
		return Preset{Key: PresetAllowAll, DefaultEffect: aclpersistence.EffectAllow}, nil
	case PresetPrivateOnly:
		return Preset{
			Key: PresetPrivateOnly, DefaultEffect: aclpersistence.EffectDeny,
			Rules: []aclpersistence.CreateRuleRequest{{Enabled: true, Effect: aclpersistence.EffectAllow, SourceScope: &aclpersistence.SourceScope{ConversationType: "private"}}},
		}, nil
	case PresetGroupOnly:
		return Preset{
			Key: PresetGroupOnly, DefaultEffect: aclpersistence.EffectDeny,
			Rules: []aclpersistence.CreateRuleRequest{{Enabled: true, Effect: aclpersistence.EffectAllow, SourceScope: &aclpersistence.SourceScope{ConversationType: "group"}}},
		}, nil
	case PresetGroupAndThreadOnly:
		return Preset{
			Key: PresetGroupAndThreadOnly, DefaultEffect: aclpersistence.EffectDeny,
			Rules: []aclpersistence.CreateRuleRequest{
				{Enabled: true, Effect: aclpersistence.EffectAllow, SourceScope: &aclpersistence.SourceScope{ConversationType: "group"}},
				{Enabled: true, Effect: aclpersistence.EffectAllow, SourceScope: &aclpersistence.SourceScope{ConversationType: "thread"}},
			},
		}, nil
	case PresetDenyAll:
		return Preset{Key: PresetDenyAll, DefaultEffect: aclpersistence.EffectDeny}, nil
	default:
		return Preset{}, ErrUnknownPreset
	}
}

// ValidatePreset validates a preset before a caller creates related state.
// ApplyPreset repeats this inexpensive validation to keep its own contract
// complete when called directly.
func (*Service) ValidatePreset(raw string) error {
	_, err := ResolvePreset(raw)
	return err
}

// ApplyPreset applies a complete preset atomically. Validation happens before
// opening the transaction; any write error is returned unchanged so the
// adapter can roll back the default effect and every rule together.
func (s *Service) ApplyPreset(ctx context.Context, botID, createdByUserID, rawPreset string) error {
	if err := s.configured(); err != nil {
		return err
	}
	preset, err := ResolvePreset(rawPreset)
	if err != nil {
		return err
	}

	botID = strings.TrimSpace(botID)
	createdByUserID = strings.TrimSpace(createdByUserID)
	writes := make([]aclpersistence.RuleWrite, 0, len(preset.Rules))
	for _, rule := range preset.Rules {
		write, err := presetRuleWrite(botID, createdByUserID, rule)
		if err != nil {
			return err
		}
		writes = append(writes, write)
	}

	return s.store.RunPresetTransaction(ctx, func(tx aclpersistence.PresetTransaction) error {
		if err := tx.SetDefaultEffect(ctx, botID, preset.DefaultEffect); err != nil {
			return err
		}
		for _, write := range writes {
			if _, err := tx.CreateRule(ctx, write); err != nil {
				return err
			}
		}
		return nil
	})
}

func presetRuleWrite(botID, createdByUserID string, rule aclpersistence.CreateRuleRequest) (aclpersistence.RuleWrite, error) {
	if err := validateEffect(rule.Effect); err != nil {
		return aclpersistence.RuleWrite{}, err
	}
	sourceScope, err := normalizeOptionalSourceScope(rule.SourceScope)
	if err != nil {
		return aclpersistence.RuleWrite{}, err
	}
	sourceChannel, err := presetSourceChannel(rule.SubjectChannelType, sourceScope)
	if err != nil {
		return aclpersistence.RuleWrite{}, err
	}
	return aclpersistence.RuleWrite{
		BotID: botID, CreatedByUserID: createdByUserID, Enabled: rule.Enabled,
		Description: strings.TrimSpace(rule.Description), Effect: strings.TrimSpace(rule.Effect),
		ChannelIdentityID:  strings.TrimSpace(rule.ChannelIdentityID),
		SubjectChannelType: strings.TrimSpace(rule.SubjectChannelType),
		SourceChannel:      sourceChannel, SourceScope: sourceScope,
	}, nil
}

func presetSourceChannel(subjectChannelType string, sourceScope aclpersistence.SourceScope) (string, error) {
	if sourceScope.IsZero() || (sourceScope.ConversationID == "" && sourceScope.ThreadID == "") {
		return "", nil
	}
	subjectChannelType = strings.TrimSpace(subjectChannelType)
	if subjectChannelType == "" {
		return "", ErrInvalidRuleSubject
	}
	return subjectChannelType, nil
}
