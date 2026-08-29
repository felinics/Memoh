package acl

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
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
		{
			name:          "empty falls back to allow all",
			key:           "",
			wantKey:       PresetAllowAll,
			wantEffect:    EffectAllow,
			wantRuleCount: 0,
		},
		{
			name:          "private only",
			key:           PresetPrivateOnly,
			wantKey:       PresetPrivateOnly,
			wantEffect:    EffectDeny,
			wantRuleCount: 1,
			wantFirstType: "private",
		},
		{
			name:          "group and thread only",
			key:           PresetGroupAndThreadOnly,
			wantKey:       PresetGroupAndThreadOnly,
			wantEffect:    EffectDeny,
			wantRuleCount: 2,
			wantFirstType: "group",
		},
		{
			name:    "invalid preset",
			key:     "nope",
			wantErr: ErrUnknownPreset,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preset, err := ResolvePreset(tt.key)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if preset.Key != tt.wantKey {
				t.Fatalf("expected key %q, got %q", tt.wantKey, preset.Key)
			}
			if preset.DefaultEffect != tt.wantEffect {
				t.Fatalf("expected default effect %q, got %q", tt.wantEffect, preset.DefaultEffect)
			}
			if len(preset.Rules) != tt.wantRuleCount {
				t.Fatalf("expected %d rules, got %d", tt.wantRuleCount, len(preset.Rules))
			}
			if tt.wantFirstType != "" {
				got := preset.Rules[0].SourceScope.ConversationType
				if got != tt.wantFirstType {
					t.Fatalf("expected first conversation type %q, got %q", tt.wantFirstType, got)
				}
			}
		})
	}
}

func TestApplyPreset(t *testing.T) {
	botUUID := pgtype.UUID{Bytes: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Valid: true}

	type createdRule struct {
		effect           string
		conversationType string
	}

	var defaultEffect string
	var createdRules []createdRule

	db := &fakeDBTX{
		execFunc: func(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			if strings.Contains(sql, "acl_default_effect") {
				defaultEffect = args[1].(string)
			}
			return pgconn.CommandTag{}, nil
		},
		queryRowFunc: func(_ context.Context, sql string, args ...any) pgx.Row {
			if strings.Contains(sql, "INSERT INTO bot_acl_rules") {
				createdRules = append(createdRules, createdRule{
					effect:           args[2].(string),
					conversationType: textFromArg(args[8]),
				})
				return &fakeRow{scanFunc: func(_ ...any) error { return nil }}
			}
			return noRule()
		},
	}

	err := ApplyPreset(context.Background(), postgresstore.NewQueries(sqlc.New(db)), botUUID.String(), "", PresetGroupAndThreadOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if defaultEffect != EffectDeny {
		t.Fatalf("expected default effect %q, got %q", EffectDeny, defaultEffect)
	}
	if len(createdRules) != 2 {
		t.Fatalf("expected 2 created rules, got %d", len(createdRules))
	}
	if createdRules[0].conversationType != "group" {
		t.Fatalf("unexpected first rule: %+v", createdRules[0])
	}
	if createdRules[1].conversationType != "thread" {
		t.Fatalf("unexpected second rule: %+v", createdRules[1])
	}
	for _, rule := range createdRules {
		if rule.effect != EffectAllow {
			t.Fatalf("unexpected rule contents: %+v", rule)
		}
	}
}
