package acl

import (
	"context"
	"errors"
	"testing"
	"time"

	aclpersistence "github.com/memohai/memoh/domains/api/bot/access/acl/persistence"
)

type storeFake struct {
	evaluateFn     func(context.Context, aclpersistence.Evaluation) (string, error)
	defaultFn      func(context.Context, string) (string, error)
	setDefaultFn   func(context.Context, string, string) error
	listRulesFn    func(context.Context, string) ([]aclpersistence.Rule, error)
	createRuleFn   func(context.Context, aclpersistence.RuleWrite) (aclpersistence.Rule, error)
	updateRuleFn   func(context.Context, aclpersistence.RuleWrite) (aclpersistence.Rule, error)
	deleteRuleFn   func(context.Context, string) error
	getManageFn    func(context.Context, string, string) (aclpersistence.ManageOverride, bool, error)
	listManageFn   func(context.Context, string) ([]aclpersistence.ManageOverride, error)
	upsertManageFn func(context.Context, aclpersistence.ManageOverrideWrite) (aclpersistence.ManageOverride, error)
	deleteManageFn func(context.Context, string, string) error
	transactionFn  func(context.Context, func(aclpersistence.PresetTransaction) error) error
}

type identityReaderFake struct {
	listFn func(context.Context, []string) ([]aclpersistence.ChannelIdentity, error)
}

func (f *identityReaderFake) ListChannelIdentities(ctx context.Context, ids []string) ([]aclpersistence.ChannelIdentity, error) {
	if f == nil || f.listFn == nil {
		return nil, nil
	}
	return f.listFn(ctx, ids)
}

type observedReaderFake struct {
	listFn func(context.Context, aclpersistence.ObservedConversationQuery) ([]aclpersistence.ObservedConversationCandidate, error)
}

func (f *observedReaderFake) ListObservedConversations(ctx context.Context, input aclpersistence.ObservedConversationQuery) ([]aclpersistence.ObservedConversationCandidate, error) {
	if f == nil || f.listFn == nil {
		return nil, nil
	}
	return f.listFn(ctx, input)
}

func (f *storeFake) EvaluateRule(ctx context.Context, input aclpersistence.Evaluation) (string, error) {
	if f.evaluateFn != nil {
		return f.evaluateFn(ctx, input)
	}
	return "", nil
}

func (f *storeFake) GetDefaultEffect(ctx context.Context, botID string) (string, error) {
	if f.defaultFn != nil {
		return f.defaultFn(ctx, botID)
	}
	return "", nil
}

func (f *storeFake) SetDefaultEffect(ctx context.Context, botID, effect string) error {
	if f.setDefaultFn != nil {
		return f.setDefaultFn(ctx, botID, effect)
	}
	return nil
}

func (f *storeFake) ListRules(ctx context.Context, botID string) ([]aclpersistence.Rule, error) {
	if f.listRulesFn != nil {
		return f.listRulesFn(ctx, botID)
	}
	return nil, nil
}

func (f *storeFake) CreateRule(ctx context.Context, input aclpersistence.RuleWrite) (aclpersistence.Rule, error) {
	if f.createRuleFn != nil {
		return f.createRuleFn(ctx, input)
	}
	return aclpersistence.Rule{}, nil
}

func (f *storeFake) UpdateRule(ctx context.Context, input aclpersistence.RuleWrite) (aclpersistence.Rule, error) {
	if f.updateRuleFn != nil {
		return f.updateRuleFn(ctx, input)
	}
	return aclpersistence.Rule{}, nil
}

func (f *storeFake) DeleteRule(ctx context.Context, ruleID string) error {
	if f.deleteRuleFn != nil {
		return f.deleteRuleFn(ctx, ruleID)
	}
	return nil
}

func (f *storeFake) GetManageOverride(ctx context.Context, botID, identityID string) (aclpersistence.ManageOverride, bool, error) {
	if f.getManageFn != nil {
		return f.getManageFn(ctx, botID, identityID)
	}
	return aclpersistence.ManageOverride{}, false, nil
}

func (f *storeFake) ListManageOverrides(ctx context.Context, botID string) ([]aclpersistence.ManageOverride, error) {
	if f.listManageFn != nil {
		return f.listManageFn(ctx, botID)
	}
	return nil, nil
}

func (f *storeFake) UpsertManageOverride(ctx context.Context, input aclpersistence.ManageOverrideWrite) (aclpersistence.ManageOverride, error) {
	if f.upsertManageFn != nil {
		return f.upsertManageFn(ctx, input)
	}
	return aclpersistence.ManageOverride{}, nil
}

func (f *storeFake) DeleteManageOverride(ctx context.Context, botID, identityID string) error {
	if f.deleteManageFn != nil {
		return f.deleteManageFn(ctx, botID, identityID)
	}
	return nil
}

func (f *storeFake) RunPresetTransaction(ctx context.Context, fn func(aclpersistence.PresetTransaction) error) error {
	if f.transactionFn != nil {
		return f.transactionFn(ctx, fn)
	}
	return errors.New("unexpected preset transaction")
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name    string
		effect  string
		allowed bool
	}{
		{name: "allow", effect: aclpersistence.EffectAllow, allowed: true},
		{name: "deny", effect: aclpersistence.EffectDeny, allowed: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got aclpersistence.Evaluation
			store := &storeFake{evaluateFn: func(_ context.Context, input aclpersistence.Evaluation) (string, error) {
				got = input
				return tt.effect, nil
			}}
			allowed, err := NewService(nil, store, nil, nil).Evaluate(t.Context(), aclpersistence.EvaluateRequest{
				BotID: " bot-id ", ChannelIdentityID: " identity-id ", ChannelType: " slack ",
				SourceScope: aclpersistence.SourceScope{ConversationType: "group", ConversationID: " C123 "},
			})
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if allowed != tt.allowed {
				t.Fatalf("Evaluate() = %v, want %v", allowed, tt.allowed)
			}
			if got.BotID != "bot-id" || got.Action != aclpersistence.ActionChatTrigger || got.ChannelIdentityID != "identity-id" || got.ChannelType != "slack" {
				t.Fatalf("evaluation = %#v", got)
			}
			if got.SourceScope.ConversationType != "group" || got.SourceScope.ConversationID != "C123" {
				t.Fatalf("source scope = %#v", got.SourceScope)
			}
		})
	}
}

func TestEvaluateRejectsInvalidScopeBeforeConfiguration(t *testing.T) {
	_, err := NewService(nil, nil, nil, nil).Evaluate(t.Context(), aclpersistence.EvaluateRequest{
		SourceScope: aclpersistence.SourceScope{ThreadID: "thread-1"},
	})
	if !errors.Is(err, ErrInvalidSourceScope) {
		t.Fatalf("Evaluate() error = %v, want ErrInvalidSourceScope", err)
	}
}

func TestValidateTarget(t *testing.T) {
	identities := &identityReaderFake{listFn: func(_ context.Context, ids []string) ([]aclpersistence.ChannelIdentity, error) {
		if ids[0] == "missing" {
			return nil, nil
		}
		return []aclpersistence.ChannelIdentity{{ID: ids[0], Channel: "telegram"}}, nil
	}}
	service := NewService(nil, &storeFake{}, identities, nil)
	tests := []struct {
		name, identityID, channelType string
		wantErr                       bool
	}{
		{name: "all targets"},
		{name: "platform only", channelType: "telegram"},
		{name: "identity only", identityID: "identity"},
		{name: "matching platform", identityID: "identity", channelType: "telegram"},
		{name: "different platform", identityID: "identity", channelType: "discord", wantErr: true},
		{name: "missing identity", identityID: "missing", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.validateTarget(t.Context(), tt.identityID, tt.channelType)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateTarget() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCreateRuleBuildsPersistenceCommand(t *testing.T) {
	var got aclpersistence.RuleWrite
	store := &storeFake{createRuleFn: func(_ context.Context, input aclpersistence.RuleWrite) (aclpersistence.Rule, error) {
		got = input
		return aclpersistence.Rule{ID: "rule-id", SubjectChannelType: input.SubjectChannelType, SourceScope: &input.SourceScope}, nil
	}}
	service := NewService(nil, store, nil, nil)
	rule, err := service.CreateRule(t.Context(), " bot-id ", " actor-id ", aclpersistence.CreateRuleRequest{
		Enabled: true, Description: " group rule ", Effect: aclpersistence.EffectDeny, SubjectChannelType: "slack",
		SourceScope: &aclpersistence.SourceScope{ConversationType: "group", ConversationID: "C123"},
	})
	if err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	if got.BotID != "bot-id" || got.CreatedByUserID != "actor-id" || got.Description != "group rule" {
		t.Fatalf("write identity fields = %#v", got)
	}
	if got.SourceChannel != "slack" || got.SourceScope.ConversationType != "group" || got.SourceScope.ConversationID != "C123" {
		t.Fatalf("write scope = %#v", got)
	}
	if rule.ID != "rule-id" {
		t.Fatalf("rule = %#v", rule)
	}
}

func TestCreateRuleResolvesSourceChannelFromIdentity(t *testing.T) {
	findCalls := 0
	var got aclpersistence.RuleWrite
	identities := &identityReaderFake{
		listFn: func(_ context.Context, ids []string) ([]aclpersistence.ChannelIdentity, error) {
			findCalls++
			return []aclpersistence.ChannelIdentity{{ID: ids[0], Channel: "telegram"}}, nil
		},
	}
	store := &storeFake{
		createRuleFn: func(_ context.Context, input aclpersistence.RuleWrite) (aclpersistence.Rule, error) {
			got = input
			return aclpersistence.Rule{}, nil
		},
	}
	_, err := NewService(nil, store, identities, nil).CreateRule(t.Context(), "bot", "actor", aclpersistence.CreateRuleRequest{
		Enabled: true, Effect: aclpersistence.EffectAllow, ChannelIdentityID: "identity",
		SourceScope: &aclpersistence.SourceScope{ConversationType: "group", ConversationID: "group-1"},
	})
	if err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	if got.SourceChannel != "telegram" {
		t.Fatalf("SourceChannel = %q, want telegram", got.SourceChannel)
	}
	if findCalls != 1 {
		t.Fatalf("ListChannelIdentities calls = %d, want 1", findCalls)
	}
}

func TestRuleCRUDAndDefaultEffectDelegate(t *testing.T) {
	store := &storeFake{
		defaultFn: func(_ context.Context, botID string) (string, error) {
			if botID != "bot" {
				t.Fatalf("GetDefaultEffect bot = %q", botID)
			}
			return aclpersistence.EffectDeny, nil
		},
		setDefaultFn: func(_ context.Context, botID, effect string) error {
			if botID != "bot" || effect != aclpersistence.EffectAllow {
				t.Fatalf("SetDefaultEffect = (%q, %q)", botID, effect)
			}
			return nil
		},
		listRulesFn: func(_ context.Context, botID string) ([]aclpersistence.Rule, error) {
			return []aclpersistence.Rule{{ID: botID + "-rule"}}, nil
		},
		updateRuleFn: func(_ context.Context, input aclpersistence.RuleWrite) (aclpersistence.Rule, error) {
			return aclpersistence.Rule{ID: input.ID, Effect: input.Effect}, nil
		},
		deleteRuleFn: func(_ context.Context, id string) error {
			if id != "rule" {
				t.Fatalf("DeleteRule id = %q", id)
			}
			return nil
		},
	}
	service := NewService(nil, store, nil, nil)
	effect, err := service.GetDefaultEffect(t.Context(), " bot ")
	if err != nil || effect != aclpersistence.EffectDeny {
		t.Fatalf("GetDefaultEffect() = (%q, %v)", effect, err)
	}
	if err := service.SetDefaultEffect(t.Context(), " bot ", " allow "); err != nil {
		t.Fatalf("SetDefaultEffect() error = %v", err)
	}
	if err := service.SetDefaultEffect(t.Context(), "bot", "invalid"); !errors.Is(err, ErrInvalidEffect) {
		t.Fatalf("invalid effect error = %v", err)
	}
	rules, err := service.ListRules(t.Context(), " bot ")
	if err != nil || len(rules) != 1 || rules[0].ID != "bot-rule" {
		t.Fatalf("ListRules() = (%#v, %v)", rules, err)
	}
	updated, err := service.UpdateRule(t.Context(), " rule ", aclpersistence.UpdateRuleRequest{Enabled: true, Effect: aclpersistence.EffectAllow})
	if err != nil || updated.ID != "rule" {
		t.Fatalf("UpdateRule() = (%#v, %v)", updated, err)
	}
	if err := service.DeleteRule(t.Context(), " rule "); err != nil {
		t.Fatalf("DeleteRule() error = %v", err)
	}
}

func TestManageOverrides(t *testing.T) {
	now := time.Now().UTC()
	store := &storeFake{
		getManageFn: func(context.Context, string, string) (aclpersistence.ManageOverride, bool, error) {
			return aclpersistence.ManageOverride{Granted: false}, true, nil
		},
		upsertManageFn: func(_ context.Context, input aclpersistence.ManageOverrideWrite) (aclpersistence.ManageOverride, error) {
			return aclpersistence.ManageOverride{BotID: input.BotID, ChannelIdentityID: input.ChannelIdentityID, Granted: input.Granted, CreatedAt: now}, nil
		},
	}
	identities := &identityReaderFake{listFn: func(context.Context, []string) ([]aclpersistence.ChannelIdentity, error) {
		return []aclpersistence.ChannelIdentity{{ID: "identity"}}, nil
	}}
	service := NewService(nil, store, identities, nil)
	granted, exists, err := service.GetManageOverride(t.Context(), "bot", "identity")
	if err != nil || !exists || granted {
		t.Fatalf("GetManageOverride() = (%v, %v, %v)", granted, exists, err)
	}
	override, err := service.SetManageOverride(t.Context(), " bot ", " identity ", true, " actor ")
	if err != nil || !override.Granted || override.BotID != "bot" {
		t.Fatalf("SetManageOverride() = (%#v, %v)", override, err)
	}
}

func TestSetManageOverrideRejectsMissingIdentity(t *testing.T) {
	identities := &identityReaderFake{listFn: func(context.Context, []string) ([]aclpersistence.ChannelIdentity, error) {
		return []aclpersistence.ChannelIdentity{}, nil
	}}
	_, err := NewService(nil, &storeFake{}, identities, nil).SetManageOverride(
		t.Context(), "bot", "missing", true, "actor",
	)
	if !errors.Is(err, ErrInvalidRuleSubject) {
		t.Fatalf("SetManageOverride() error = %v, want ErrInvalidRuleSubject", err)
	}
}

func TestListManageOverridesEnrichesInOneBatch(t *testing.T) {
	store := &storeFake{listManageFn: func(context.Context, string) ([]aclpersistence.ManageOverride, error) {
		return []aclpersistence.ManageOverride{
			{ID: "override-1", ChannelIdentityID: "identity-1"},
			{ID: "override-2", ChannelIdentityID: "identity-2"},
			{ID: "override-3", ChannelIdentityID: "identity-1"},
		}, nil
	}}
	calls := 0
	identities := &identityReaderFake{listFn: func(_ context.Context, ids []string) ([]aclpersistence.ChannelIdentity, error) {
		calls++
		if len(ids) != 2 || ids[0] != "identity-1" || ids[1] != "identity-2" {
			t.Fatalf("identity IDs = %#v", ids)
		}
		return []aclpersistence.ChannelIdentity{
			{ID: "identity-1", Channel: "slack", ChannelSubjectID: "U1", DisplayName: "Alice", AvatarURL: "alice.png"},
			{ID: "identity-2", Channel: "discord", ChannelSubjectID: "U2", DisplayName: "Bob", AvatarURL: "bob.png"},
		}, nil
	}}
	items, err := NewService(nil, store, identities, nil).ListManageOverrides(t.Context(), "bot")
	if err != nil {
		t.Fatalf("ListManageOverrides() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("ListChannelIdentities calls = %d, want 1", calls)
	}
	if items[0].ChannelType != "slack" || items[0].ChannelSubjectID != "U1" ||
		items[0].ChannelIdentityDisplayName != "Alice" || items[0].ChannelIdentityAvatarURL != "alice.png" {
		t.Fatalf("first override = %#v", items[0])
	}
	if items[1].ChannelType != "discord" || items[2].ChannelType != "slack" {
		t.Fatalf("overrides = %#v", items)
	}
}

func TestListManageOverridesEmptySkipsIdentityReader(t *testing.T) {
	store := &storeFake{listManageFn: func(context.Context, string) ([]aclpersistence.ManageOverride, error) {
		return []aclpersistence.ManageOverride{}, nil
	}}
	identities := &identityReaderFake{listFn: func(context.Context, []string) ([]aclpersistence.ChannelIdentity, error) {
		t.Fatal("ListChannelIdentities must not be called")
		return nil, nil
	}}
	items, err := NewService(nil, store, identities, nil).ListManageOverrides(t.Context(), "bot")
	if err != nil || len(items) != 0 {
		t.Fatalf("ListManageOverrides() = (%#v, %v)", items, err)
	}
}

func TestListObservedConversations(t *testing.T) {
	var got aclpersistence.ObservedConversationQuery
	observed := &observedReaderFake{listFn: func(_ context.Context, input aclpersistence.ObservedConversationQuery) ([]aclpersistence.ObservedConversationCandidate, error) {
		got = input
		return []aclpersistence.ObservedConversationCandidate{{RouteID: "route", ConversationID: "chat"}}, nil
	}}
	service := NewService(nil, &storeFake{}, nil, observed)
	items, err := service.ListObservedConversationsByChannelIdentity(t.Context(), " bot ", " identity ")
	if err != nil || len(items) != 1 || got.BotID != "bot" || got.ChannelIdentityID != "identity" {
		t.Fatalf("identity conversations = (%#v, %#v, %v)", items, got, err)
	}
	_, err = service.ListObservedConversationsByChannelType(t.Context(), "bot", " slack ")
	if err != nil || got.ChannelType != "slack" {
		t.Fatalf("type query = (%#v, %v)", got, err)
	}
	if _, err := service.ListObservedConversationsByChannelType(t.Context(), "bot", " "); err == nil {
		t.Fatal("empty channel type error = nil")
	}
}

func TestValidateEffect(t *testing.T) {
	for _, effect := range []string{aclpersistence.EffectAllow, aclpersistence.EffectDeny} {
		if err := validateEffect(effect); err != nil {
			t.Fatalf("validateEffect(%q) = %v", effect, err)
		}
	}
	if err := validateEffect("unknown"); !errors.Is(err, ErrInvalidEffect) {
		t.Fatalf("validateEffect(unknown) = %v", err)
	}
}
