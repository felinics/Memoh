// Package acl implements ACL-owned PostgreSQL persistence.
package acl

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	aclpersistence "github.com/memohai/memoh/domains/api/bot/access/acl/persistence"
	dbsqlc "github.com/memohai/memoh/domains/api/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type queries interface {
	EvaluateBotACLRule(context.Context, dbsqlc.EvaluateBotACLRuleParams) (string, error)
	GetBotACLDefaultEffect(context.Context, pgtype.UUID) (string, error)
	SetBotACLDefaultEffect(context.Context, dbsqlc.SetBotACLDefaultEffectParams) error
	ListBotACLRules(context.Context, pgtype.UUID) ([]dbsqlc.ListBotACLRulesRow, error)
	CreateBotACLRule(context.Context, dbsqlc.CreateBotACLRuleParams) (dbsqlc.ApiBotAclRule, error)
	UpdateBotACLRule(context.Context, dbsqlc.UpdateBotACLRuleParams) (dbsqlc.ApiBotAclRule, error)
	DeleteBotACLRuleByID(context.Context, pgtype.UUID) error
	GetBotChannelAdmin(context.Context, dbsqlc.GetBotChannelAdminParams) (dbsqlc.ApiBotChannelAdmin, error)
	ListBotChannelAdmins(context.Context, pgtype.UUID) ([]dbsqlc.ListBotChannelAdminsRow, error)
	UpsertBotChannelAdmin(context.Context, dbsqlc.UpsertBotChannelAdminParams) (dbsqlc.ApiBotChannelAdmin, error)
	DeleteBotChannelAdmin(context.Context, dbsqlc.DeleteBotChannelAdminParams) error
}

type Store struct {
	queries      queries
	transactions transactionBeginner
	bindQueries  func(pgx.Tx) queries
}

type presetTransaction struct {
	queries queries
}

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

var (
	_ aclpersistence.Store             = (*Store)(nil)
	_ aclpersistence.PresetTransaction = (*presetTransaction)(nil)
)

func NewStore(pool *pgxpool.Pool) *Store {
	if pool == nil {
		return newStore(nil, nil, nil)
	}
	generated := dbsqlc.New(pool)
	return newStore(generated, pool, func(tx pgx.Tx) queries { return generated.WithTx(tx) })
}

func newStore(queries queries, transactions transactionBeginner, bindQueries func(pgx.Tx) queries) *Store {
	return &Store{queries: queries, transactions: transactions, bindQueries: bindQueries}
}

func (s *Store) EvaluateRule(ctx context.Context, input aclpersistence.Evaluation) (string, error) {
	botID, err := db.ParseUUID(input.BotID)
	if err != nil {
		return "", err
	}
	return s.queries.EvaluateBotACLRule(ctx, dbsqlc.EvaluateBotACLRuleParams{
		BotID: botID, Action: input.Action,
		ChannelIdentityID:      optionalUUID(input.ChannelIdentityID),
		SubjectChannelType:     optionalText(input.ChannelType),
		SourceConversationType: optionalText(input.SourceScope.ConversationType),
		SourceConversationID:   optionalText(input.SourceScope.ConversationID),
		SourceThreadID:         optionalText(input.SourceScope.ThreadID),
	})
}

func (s *Store) GetDefaultEffect(ctx context.Context, botID string) (string, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return "", err
	}
	return s.queries.GetBotACLDefaultEffect(ctx, id)
}

func (s *Store) SetDefaultEffect(ctx context.Context, botID, effect string) error {
	return setDefaultEffect(ctx, s.queries, botID, effect)
}

func (s *Store) ListRules(ctx context.Context, botID string) ([]aclpersistence.Rule, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotACLRules(ctx, id)
	if err != nil {
		return nil, err
	}
	items := make([]aclpersistence.Rule, 0, len(rows))
	for _, row := range rows {
		items = append(items, ruleFromListRow(row))
	}
	return items, nil
}

func (s *Store) CreateRule(ctx context.Context, input aclpersistence.RuleWrite) (aclpersistence.Rule, error) {
	return createRule(ctx, s.queries, input)
}

func (s *Store) UpdateRule(ctx context.Context, input aclpersistence.RuleWrite) (aclpersistence.Rule, error) {
	id, err := db.ParseUUID(input.ID)
	if err != nil {
		return aclpersistence.Rule{}, err
	}
	row, err := s.queries.UpdateBotACLRule(ctx, dbsqlc.UpdateBotACLRuleParams{
		ID: id, Enabled: input.Enabled, Effect: input.Effect,
		Description: optionalText(input.Description), ChannelIdentityID: optionalUUID(input.ChannelIdentityID),
		SubjectChannelType: optionalText(input.SubjectChannelType), SourceChannel: optionalText(input.SourceChannel),
		SourceConversationType: optionalText(input.SourceScope.ConversationType),
		SourceConversationID:   optionalText(input.SourceScope.ConversationID), SourceThreadID: optionalText(input.SourceScope.ThreadID),
	})
	if err != nil {
		return aclpersistence.Rule{}, err
	}
	return ruleFromWrite(row), nil
}

func (s *Store) DeleteRule(ctx context.Context, ruleID string) error {
	id, err := db.ParseUUID(ruleID)
	if err != nil {
		return err
	}
	return s.queries.DeleteBotACLRuleByID(ctx, id)
}

func (s *Store) GetManageOverride(ctx context.Context, botID, identityID string) (aclpersistence.ManageOverride, bool, error) {
	bot, identity, err := parsePair(botID, identityID)
	if err != nil {
		return aclpersistence.ManageOverride{}, false, err
	}
	row, err := s.queries.GetBotChannelAdmin(ctx, dbsqlc.GetBotChannelAdminParams{BotID: bot, ChannelIdentityID: identity})
	if errors.Is(err, pgx.ErrNoRows) {
		return aclpersistence.ManageOverride{}, false, nil
	}
	if err != nil {
		return aclpersistence.ManageOverride{}, false, err
	}
	return manageOverride(row), true, nil
}

func (s *Store) ListManageOverrides(ctx context.Context, botID string) ([]aclpersistence.ManageOverride, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotChannelAdmins(ctx, id)
	if err != nil {
		return nil, err
	}
	items := make([]aclpersistence.ManageOverride, 0, len(rows))
	for _, row := range rows {
		items = append(items, aclpersistence.ManageOverride{
			ID: db.UUIDString(row.ID), BotID: db.UUIDString(row.BotID), ChannelIdentityID: db.UUIDString(row.ChannelIdentityID),
			Granted: row.Granted, CreatedAt: timestamp(row.CreatedAt),
		})
	}
	return items, nil
}

func (s *Store) UpsertManageOverride(ctx context.Context, input aclpersistence.ManageOverrideWrite) (aclpersistence.ManageOverride, error) {
	botID, identityID, err := parsePair(input.BotID, input.ChannelIdentityID)
	if err != nil {
		return aclpersistence.ManageOverride{}, err
	}
	row, err := s.queries.UpsertBotChannelAdmin(ctx, dbsqlc.UpsertBotChannelAdminParams{
		BotID: botID, ChannelIdentityID: identityID, Granted: input.Granted,
		CreatedByUserID: optionalUUID(input.CreatedByUserID),
	})
	if err != nil {
		return aclpersistence.ManageOverride{}, err
	}
	return manageOverride(row), nil
}

func (s *Store) DeleteManageOverride(ctx context.Context, botID, identityID string) error {
	bot, identity, err := parsePair(botID, identityID)
	if err != nil {
		return err
	}
	return s.queries.DeleteBotChannelAdmin(ctx, dbsqlc.DeleteBotChannelAdminParams{BotID: bot, ChannelIdentityID: identity})
}

func (s *Store) RunPresetTransaction(ctx context.Context, fn func(aclpersistence.PresetTransaction) error) error {
	if fn == nil {
		return errors.New("acl preset transaction callback is required")
	}
	if s == nil || s.transactions == nil || s.bindQueries == nil {
		return aclpersistence.ErrTransactionsRequired
	}
	tx, err := s.transactions.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(BindPresetTransaction(s.bindQueries(tx))); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// BindPresetTransaction binds ACL's preset writes to transaction-scoped
// PostgreSQL queries without exposing the generated query handle.
func BindPresetTransaction(queries queries) aclpersistence.PresetTransaction {
	return &presetTransaction{queries: queries}
}

func (tx *presetTransaction) SetDefaultEffect(ctx context.Context, botID, effect string) error {
	return setDefaultEffect(ctx, tx.queries, botID, effect)
}

func (tx *presetTransaction) CreateRule(ctx context.Context, input aclpersistence.RuleWrite) (aclpersistence.Rule, error) {
	return createRule(ctx, tx.queries, input)
}

func setDefaultEffect(ctx context.Context, q queries, botID, effect string) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return q.SetBotACLDefaultEffect(ctx, dbsqlc.SetBotACLDefaultEffectParams{ID: id, AclDefaultEffect: effect})
}

func createRule(ctx context.Context, q queries, input aclpersistence.RuleWrite) (aclpersistence.Rule, error) {
	botID, err := db.ParseUUID(input.BotID)
	if err != nil {
		return aclpersistence.Rule{}, err
	}
	row, err := q.CreateBotACLRule(ctx, dbsqlc.CreateBotACLRuleParams{
		BotID: botID, Enabled: input.Enabled, Effect: input.Effect,
		CreatedByUserID: optionalUUID(input.CreatedByUserID), Description: optionalText(input.Description),
		ChannelIdentityID: optionalUUID(input.ChannelIdentityID), SubjectChannelType: optionalText(input.SubjectChannelType),
		SourceChannel: optionalText(input.SourceChannel), SourceConversationType: optionalText(input.SourceScope.ConversationType),
		SourceConversationID: optionalText(input.SourceScope.ConversationID), SourceThreadID: optionalText(input.SourceScope.ThreadID),
	})
	if err != nil {
		return aclpersistence.Rule{}, err
	}
	return ruleFromWrite(row), nil
}

func parsePair(first, second string) (pgtype.UUID, pgtype.UUID, error) {
	firstID, err := db.ParseUUID(first)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	secondID, err := db.ParseUUID(second)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	return firstID, secondID, nil
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
	return pgtype.Text{String: value, Valid: value != ""}
}

func text(value pgtype.Text) string { return strings.TrimSpace(value.String) }

func timestamp(value pgtype.Timestamptz) time.Time {
	if value.Valid {
		return value.Time
	}
	return time.Time{}
}

func sourceScope(conversationType, conversationID, threadID pgtype.Text) *aclpersistence.SourceScope {
	scope := aclpersistence.SourceScope{ConversationType: text(conversationType), ConversationID: text(conversationID), ThreadID: text(threadID)}
	if scope.IsZero() {
		return nil
	}
	return &scope
}

func ruleFromListRow(row dbsqlc.ListBotACLRulesRow) aclpersistence.Rule {
	return aclpersistence.Rule{
		ID: db.UUIDString(row.ID), BotID: db.UUIDString(row.BotID), Enabled: row.Enabled, Description: text(row.Description),
		Action: row.Action, Effect: row.Effect, ChannelIdentityID: db.UUIDString(row.ChannelIdentityID),
		SubjectChannelType: text(row.SubjectChannelType), SourceScope: sourceScope(row.SourceConversationType, row.SourceConversationID, row.SourceThreadID),
		CreatedAt: timestamp(row.CreatedAt), UpdatedAt: timestamp(row.UpdatedAt),
	}
}

func ruleFromWrite(row dbsqlc.ApiBotAclRule) aclpersistence.Rule {
	return aclpersistence.Rule{
		ID: db.UUIDString(row.ID), BotID: db.UUIDString(row.BotID), Enabled: row.Enabled, Description: text(row.Description),
		Action: row.Action, Effect: row.Effect, ChannelIdentityID: db.UUIDString(row.ChannelIdentityID),
		SubjectChannelType: text(row.SubjectChannelType), SourceScope: sourceScope(row.SourceConversationType, row.SourceConversationID, row.SourceThreadID),
		CreatedAt: timestamp(row.CreatedAt), UpdatedAt: timestamp(row.UpdatedAt),
	}
}

func manageOverride(row dbsqlc.ApiBotChannelAdmin) aclpersistence.ManageOverride {
	return aclpersistence.ManageOverride{
		ID: db.UUIDString(row.ID), BotID: db.UUIDString(row.BotID), ChannelIdentityID: db.UUIDString(row.ChannelIdentityID),
		Granted: row.Granted, CreatedAt: timestamp(row.CreatedAt),
	}
}
