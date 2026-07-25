package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/api/access/acl"
	dbsqlc "github.com/memohai/memoh/domains/api/internal/postgres/sqlc"
)

const (
	botID      = "11111111-1111-1111-1111-111111111111"
	identityID = "22222222-2222-2222-2222-222222222222"
	ruleID     = "33333333-3333-3333-3333-333333333333"
	actorID    = "44444444-4444-4444-4444-444444444444"
)

type queryFake struct {
	queries
	evaluateFn   func(context.Context, dbsqlc.EvaluateBotACLRuleParams) (string, error)
	setDefaultFn func(context.Context, dbsqlc.SetBotACLDefaultEffectParams) error
	createRuleFn func(context.Context, dbsqlc.CreateBotACLRuleParams) (dbsqlc.ApiBotAclRule, error)
	listRulesFn  func(context.Context, pgtype.UUID) ([]dbsqlc.ListBotACLRulesRow, error)
	getManageFn  func(context.Context, dbsqlc.GetBotChannelAdminParams) (dbsqlc.ApiBotChannelAdmin, error)
}

func (f *queryFake) EvaluateBotACLRule(ctx context.Context, input dbsqlc.EvaluateBotACLRuleParams) (string, error) {
	return f.evaluateFn(ctx, input)
}

func (f *queryFake) SetBotACLDefaultEffect(ctx context.Context, input dbsqlc.SetBotACLDefaultEffectParams) error {
	return f.setDefaultFn(ctx, input)
}

func (f *queryFake) CreateBotACLRule(ctx context.Context, input dbsqlc.CreateBotACLRuleParams) (dbsqlc.ApiBotAclRule, error) {
	return f.createRuleFn(ctx, input)
}

func (f *queryFake) ListBotACLRules(ctx context.Context, id pgtype.UUID) ([]dbsqlc.ListBotACLRulesRow, error) {
	return f.listRulesFn(ctx, id)
}

func (f *queryFake) GetBotChannelAdmin(ctx context.Context, input dbsqlc.GetBotChannelAdminParams) (dbsqlc.ApiBotChannelAdmin, error) {
	return f.getManageFn(ctx, input)
}

func TestStoreEvaluateRuleMapsCommand(t *testing.T) {
	var got dbsqlc.EvaluateBotACLRuleParams
	q := &queryFake{evaluateFn: func(_ context.Context, input dbsqlc.EvaluateBotACLRuleParams) (string, error) {
		got = input
		return acl.EffectAllow, nil
	}}
	effect, err := newStore(q, nil, nil).EvaluateRule(t.Context(), acl.Evaluation{
		BotID: botID, Action: acl.ActionChatTrigger, ChannelIdentityID: identityID, ChannelType: "slack",
		SourceScope: acl.SourceScope{ConversationType: "group", ConversationID: "C123", ThreadID: "T1"},
	})
	if err != nil || effect != acl.EffectAllow {
		t.Fatalf("EvaluateRule() = (%q, %v)", effect, err)
	}
	if uuidString(got.BotID) != botID || uuidString(got.ChannelIdentityID) != identityID || got.Action != acl.ActionChatTrigger {
		t.Fatalf("IDs/action = %#v", got)
	}
	if text(got.SubjectChannelType) != "slack" || text(got.SourceConversationType) != "group" || text(got.SourceConversationID) != "C123" || text(got.SourceThreadID) != "T1" {
		t.Fatalf("scope = %#v", got)
	}
}

func TestStoreCreateRuleMapsCommandAndRow(t *testing.T) {
	now := time.Now().UTC()
	var got dbsqlc.CreateBotACLRuleParams
	q := &queryFake{createRuleFn: func(_ context.Context, input dbsqlc.CreateBotACLRuleParams) (dbsqlc.ApiBotAclRule, error) {
		got = input
		return dbsqlc.ApiBotAclRule{
			ID: postgresUUID(ruleID), BotID: input.BotID, Enabled: input.Enabled, Description: input.Description,
			Action: acl.ActionChatTrigger, Effect: input.Effect, ChannelIdentityID: input.ChannelIdentityID,
			SubjectChannelType: input.SubjectChannelType, SourceConversationType: input.SourceConversationType,
			SourceConversationID: input.SourceConversationID, SourceThreadID: input.SourceThreadID,
			CreatedAt: pgtype.Timestamptz{Time: now, Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		}, nil
	}}
	rule, err := newStore(q, nil, nil).CreateRule(t.Context(), acl.RuleWrite{
		BotID: botID, CreatedByUserID: actorID, Enabled: true, Description: " description ", Effect: acl.EffectDeny,
		ChannelIdentityID: identityID, SubjectChannelType: "slack", SourceChannel: "slack",
		SourceScope: acl.SourceScope{ConversationType: "group", ConversationID: "C123"},
	})
	if err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	if uuidString(got.BotID) != botID || uuidString(got.CreatedByUserID) != actorID || uuidString(got.ChannelIdentityID) != identityID {
		t.Fatalf("create IDs = %#v", got)
	}
	if text(got.Description) != "description" || text(got.SourceChannel) != "slack" || text(got.SourceConversationID) != "C123" {
		t.Fatalf("create fields = %#v", got)
	}
	if rule.ID != ruleID || rule.BotID != botID || rule.ChannelIdentityID != identityID || rule.SourceScope == nil || rule.SourceScope.ConversationID != "C123" || !rule.CreatedAt.Equal(now) {
		t.Fatalf("rule = %#v", rule)
	}
}

func TestStoreListRulesMapsOwnerFields(t *testing.T) {
	now := time.Now().UTC()
	q := &queryFake{listRulesFn: func(_ context.Context, id pgtype.UUID) ([]dbsqlc.ListBotACLRulesRow, error) {
		if uuidString(id) != botID {
			t.Fatalf("bot ID = %s", uuidString(id))
		}
		return []dbsqlc.ListBotACLRulesRow{{
			ID: postgresUUID(ruleID), BotID: id, Enabled: true, Action: acl.ActionChatTrigger, Effect: acl.EffectAllow,
			ChannelIdentityID: postgresUUID(identityID), SubjectChannelType: postgresText("slack"),
			SourceConversationType: postgresText("group"), SourceConversationID: postgresText("C123"),
			CreatedAt: pgtype.Timestamptz{Time: now, Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		}}, nil
	}}
	rules, err := newStore(q, nil, nil).ListRules(t.Context(), botID)
	if err != nil || len(rules) != 1 {
		t.Fatalf("ListRules() = (%#v, %v)", rules, err)
	}
	rule := rules[0]
	if rule.ChannelIdentityID != identityID || rule.SubjectChannelType != "slack" ||
		rule.SourceScope == nil || rule.SourceScope.ConversationID != "C123" {
		t.Fatalf("rule = %#v", rule)
	}
}

func TestStoreManageMissingRow(t *testing.T) {
	q := &queryFake{
		getManageFn: func(context.Context, dbsqlc.GetBotChannelAdminParams) (dbsqlc.ApiBotChannelAdmin, error) {
			return dbsqlc.ApiBotChannelAdmin{}, pgx.ErrNoRows
		},
	}
	store := newStore(q, nil, nil)
	_, exists, err := store.GetManageOverride(t.Context(), botID, identityID)
	if err != nil || exists {
		t.Fatalf("GetManageOverride() = (%v, %v)", exists, err)
	}
}

type transactionBeginnerFake struct {
	tx    pgx.Tx
	err   error
	calls int
}

func (b *transactionBeginnerFake) Begin(context.Context) (pgx.Tx, error) {
	b.calls++
	return b.tx, b.err
}

type transactionFake struct {
	pgx.Tx
	commits   int
	rollbacks int
	commitErr error
}

func (tx *transactionFake) Commit(context.Context) error   { tx.commits++; return tx.commitErr }
func (tx *transactionFake) Rollback(context.Context) error { tx.rollbacks++; return nil }

func TestStoreRunPresetTransactionUsesTransactionQueries(t *testing.T) {
	setCalls, createCalls := 0, 0
	txQueries := &queryFake{
		setDefaultFn: func(_ context.Context, input dbsqlc.SetBotACLDefaultEffectParams) error {
			setCalls++
			if uuidString(input.ID) != botID || input.AclDefaultEffect != acl.EffectDeny {
				t.Fatalf("default input = %#v", input)
			}
			return nil
		},
		createRuleFn: func(_ context.Context, input dbsqlc.CreateBotACLRuleParams) (dbsqlc.ApiBotAclRule, error) {
			createCalls++
			return dbsqlc.ApiBotAclRule{ID: postgresUUID(ruleID), BotID: input.BotID, Effect: input.Effect}, nil
		},
	}
	pgxTx := &transactionFake{}
	beginner := &transactionBeginnerFake{tx: pgxTx}
	store := newStore(&queryFake{}, beginner, func(got pgx.Tx) queries {
		if got != pgxTx {
			t.Fatalf("bound transaction = %T, want test transaction", got)
		}
		return txQueries
	})
	err := store.RunPresetTransaction(t.Context(), func(tx acl.PresetTransaction) error {
		if err := tx.SetDefaultEffect(t.Context(), botID, acl.EffectDeny); err != nil {
			return err
		}
		_, err := tx.CreateRule(t.Context(), acl.RuleWrite{BotID: botID, Effect: acl.EffectAllow})
		return err
	})
	if err != nil {
		t.Fatalf("RunPresetTransaction() error = %v", err)
	}
	if beginner.calls != 1 || pgxTx.commits != 1 || pgxTx.rollbacks != 1 || setCalls != 1 || createCalls != 1 {
		t.Fatalf("calls = begin %d, commit %d, rollback %d, set %d, create %d", beginner.calls, pgxTx.commits, pgxTx.rollbacks, setCalls, createCalls)
	}
}

func TestStoreRunPresetTransactionRequiresSupportedRunner(t *testing.T) {
	store := newStore(&queryFake{}, nil, nil)
	err := store.RunPresetTransaction(t.Context(), func(acl.PresetTransaction) error { return nil })
	if !errors.Is(err, acl.ErrTransactionsRequired) {
		t.Fatalf("RunPresetTransaction() error = %v, want ErrTransactionsRequired", err)
	}
}

func TestStoreRunPresetTransactionRollsBackCallbackError(t *testing.T) {
	wantErr := errors.New("preset failed")
	pgxTx := &transactionFake{}
	store := newStore(&queryFake{}, &transactionBeginnerFake{tx: pgxTx}, func(pgx.Tx) queries { return &queryFake{} })
	err := store.RunPresetTransaction(t.Context(), func(acl.PresetTransaction) error { return wantErr })
	if !errors.Is(err, wantErr) || pgxTx.commits != 0 || pgxTx.rollbacks != 1 {
		t.Fatalf("RunPresetTransaction() = %v, commit/rollback = %d/%d", err, pgxTx.commits, pgxTx.rollbacks)
	}
}

func postgresUUID(value string) pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.MustParse(value), Valid: true}
}

func postgresText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}
