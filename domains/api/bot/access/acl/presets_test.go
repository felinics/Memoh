package acl

import (
	"context"
	"errors"
	"testing"

	aclpersistence "github.com/memohai/memoh/domains/api/bot/access/acl/persistence"
)

func TestResolvePreset(t *testing.T) {
	tests := []struct {
		name          string
		key           string
		wantKey       string
		wantEffect    string
		wantRuleCount int
		wantFirstType string
		wantErr       error
	}{
		{name: "empty falls back to allow all", wantKey: PresetAllowAll, wantEffect: aclpersistence.EffectAllow},
		{name: "private only", key: PresetPrivateOnly, wantKey: PresetPrivateOnly, wantEffect: aclpersistence.EffectDeny, wantRuleCount: 1, wantFirstType: "private"},
		{name: "group and thread only", key: PresetGroupAndThreadOnly, wantKey: PresetGroupAndThreadOnly, wantEffect: aclpersistence.EffectDeny, wantRuleCount: 2, wantFirstType: "group"},
		{name: "invalid preset", key: "nope", wantErr: ErrUnknownPreset},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preset, err := ResolvePreset(tt.key)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ResolvePreset() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePreset() error = %v", err)
			}
			if preset.Key != tt.wantKey || preset.DefaultEffect != tt.wantEffect || len(preset.Rules) != tt.wantRuleCount {
				t.Fatalf("preset = %#v", preset)
			}
			if tt.wantFirstType != "" && preset.Rules[0].SourceScope.ConversationType != tt.wantFirstType {
				t.Fatalf("first rule = %#v", preset.Rules[0])
			}
		})
	}
}

type presetTransactionFake struct {
	defaultEffect string
	writes        []aclpersistence.RuleWrite
	createErrAt   int
	createErr     error
}

func (f *presetTransactionFake) SetDefaultEffect(_ context.Context, botID, effect string) error {
	if botID == "" {
		return errors.New("empty bot ID")
	}
	f.defaultEffect = effect
	return nil
}

func (f *presetTransactionFake) CreateRule(_ context.Context, input aclpersistence.RuleWrite) (aclpersistence.Rule, error) {
	f.writes = append(f.writes, input)
	if f.createErrAt > 0 && len(f.writes) == f.createErrAt {
		return aclpersistence.Rule{}, f.createErr
	}
	return aclpersistence.Rule{}, nil
}

func TestApplyPresetUsesOneTransaction(t *testing.T) {
	tx := &presetTransactionFake{}
	transactions := 0
	store := &storeFake{transactionFn: func(_ context.Context, fn func(aclpersistence.PresetTransaction) error) error {
		transactions++
		return fn(tx)
	}}
	err := NewService(nil, store, nil, nil).ApplyPreset(t.Context(), " bot-id ", " actor-id ", PresetGroupAndThreadOnly)
	if err != nil {
		t.Fatalf("ApplyPreset() error = %v", err)
	}
	if transactions != 1 {
		t.Fatalf("transactions = %d, want 1", transactions)
	}
	if tx.defaultEffect != aclpersistence.EffectDeny {
		t.Fatalf("default effect = %q, want deny", tx.defaultEffect)
	}
	if len(tx.writes) != 2 {
		t.Fatalf("writes = %#v", tx.writes)
	}
	if tx.writes[0].BotID != "bot-id" || tx.writes[0].CreatedByUserID != "actor-id" {
		t.Fatalf("first write = %#v", tx.writes[0])
	}
	if tx.writes[0].SourceScope.ConversationType != "group" || tx.writes[1].SourceScope.ConversationType != "thread" {
		t.Fatalf("writes = %#v", tx.writes)
	}
}

func TestApplyPresetPropagatesWriteError(t *testing.T) {
	wantErr := errors.New("create rule failed")
	tx := &presetTransactionFake{createErrAt: 1, createErr: wantErr}
	store := &storeFake{transactionFn: func(_ context.Context, fn func(aclpersistence.PresetTransaction) error) error { return fn(tx) }}
	err := NewService(nil, store, nil, nil).ApplyPreset(t.Context(), "bot-id", "actor-id", PresetGroupAndThreadOnly)
	if !errors.Is(err, wantErr) {
		t.Fatalf("ApplyPreset() error = %v, want %v", err, wantErr)
	}
	if len(tx.writes) != 1 {
		t.Fatalf("writes = %d, want stop after first error", len(tx.writes))
	}
}

func TestApplyPresetRejectsUnknownBeforeTransaction(t *testing.T) {
	transactions := 0
	store := &storeFake{transactionFn: func(context.Context, func(aclpersistence.PresetTransaction) error) error { transactions++; return nil }}
	err := NewService(nil, store, nil, nil).ApplyPreset(t.Context(), "bot", "actor", "unknown")
	if !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("ApplyPreset() error = %v", err)
	}
	if transactions != 0 {
		t.Fatalf("transactions = %d, want 0", transactions)
	}
}

func TestPresetSourceChannel(t *testing.T) {
	channel, err := presetSourceChannel(" slack ", aclpersistence.SourceScope{ConversationType: "group", ConversationID: "C123"})
	if err != nil || channel != "slack" {
		t.Fatalf("presetSourceChannel() = (%q, %v)", channel, err)
	}
	_, err = presetSourceChannel("", aclpersistence.SourceScope{ConversationType: "thread", ConversationID: "C123", ThreadID: "T1"})
	if !errors.Is(err, ErrInvalidRuleSubject) {
		t.Fatalf("presetSourceChannel() error = %v", err)
	}
}
