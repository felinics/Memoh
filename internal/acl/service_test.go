package acl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

// ---- fake DB infrastructure ----

type fakeDBTX struct {
	queryRowFunc func(ctx context.Context, sql string, args ...any) pgx.Row
	queryFunc    func(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	execFunc     func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (f *fakeDBTX) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if f.execFunc != nil {
		return f.execFunc(ctx, sql, args...)
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakeDBTX) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if f.queryFunc != nil {
		return f.queryFunc(ctx, sql, args...)
	}
	return &fakeRows{}, nil
}

func (f *fakeDBTX) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if f.queryRowFunc != nil {
		return f.queryRowFunc(ctx, sql, args...)
	}
	return &fakeRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
}

type fakeRow struct {
	scanFunc func(dest ...any) error
}

func (r *fakeRow) Scan(dest ...any) error {
	if r.scanFunc == nil {
		return pgx.ErrNoRows
	}
	return r.scanFunc(dest...)
}

type fakeRows struct {
	rows    []func(dest ...any) error
	idx     int
	lastErr error
}

func (*fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                 { return r.lastErr }
func (*fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (*fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	if r.idx == 0 || r.idx > len(r.rows) {
		return errors.New("scan called without next")
	}
	scan := r.rows[r.idx-1]
	if scan == nil {
		return nil
	}
	return scan(dest...)
}
func (*fakeRows) Values() ([]any, error) { return nil, nil }
func (*fakeRows) RawValues() [][]byte    { return nil }
func (*fakeRows) Conn() *pgx.Conn        { return nil }

// ---- helpers ----

func makeStringRow(value string) *fakeRow {
	return &fakeRow{
		scanFunc: func(dest ...any) error {
			*dest[0].(*string) = value
			return nil
		},
	}
}

func textFromArg(value any) string {
	switch v := value.(type) {
	case pgtype.Text:
		return strings.TrimSpace(v.String)
	case *pgtype.Text:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(v.String)
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

// matchedRule returns a fakeRow that scans the given effect string.
func matchedRule(effect string) *fakeRow {
	return makeStringRow(effect)
}

// noRule returns a fakeRow that returns pgx.ErrNoRows (no matching rule).
func noRule() *fakeRow {
	return &fakeRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
}

// ---- Evaluate tests ----

func TestEvaluate(t *testing.T) {
	botUUID := pgtype.UUID{Bytes: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Valid: true}

	tests := []struct {
		name            string
		matchedOverride string // "" means no matching override; query returns the default effect
		defaultEffect   string
		wantAllowed     bool
	}{
		{
			name:            "whitelist override allows",
			matchedOverride: EffectAllow,
			defaultEffect:   EffectDeny,
			wantAllowed:     true,
		},
		{
			name:            "blacklist override denies",
			matchedOverride: EffectDeny,
			defaultEffect:   EffectAllow,
			wantAllowed:     false,
		},
		{
			name:          "no matching override - default allow",
			defaultEffect: EffectAllow,
			wantAllowed:   true,
		},
		{
			name:          "no matching override - default deny",
			defaultEffect: EffectDeny,
			wantAllowed:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var evaluateQueries int
			db := &fakeDBTX{
				queryRowFunc: func(_ context.Context, sql string, _ ...any) pgx.Row {
					switch {
					case strings.Contains(sql, "COALESCE((") && strings.Contains(sql, "r.effect <> b.acl_default_effect"):
						evaluateQueries++
						if strings.Contains(sql, "ORDER BY") {
							t.Fatalf("evaluate query must not depend on rule ordering: %s", sql)
						}
						if tt.matchedOverride != "" {
							return matchedRule(tt.matchedOverride)
						}
						return makeStringRow(tt.defaultEffect)
					case strings.Contains(sql, "acl_default_effect"):
						return makeStringRow(tt.defaultEffect)
					default:
						return noRule()
					}
				},
			}
			queries := postgresstore.NewQueries(sqlc.New(db))
			service := NewService(nil, queries)

			allowed, err := service.Evaluate(context.Background(), EvaluateRequest{
				BotID:             botUUID.String(),
				ChannelIdentityID: "55555555-5555-5555-5555-555555555555",
				ChannelType:       "telegram",
				SourceScope: SourceScope{
					ConversationType: "group",
					ConversationID:   "group-1",
				},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if allowed != tt.wantAllowed {
				t.Fatalf("expected allowed=%v, got %v", tt.wantAllowed, allowed)
			}
			if evaluateQueries != 1 {
				t.Fatalf("expected one evaluate query, got %d", evaluateQueries)
			}
		})
	}
}

func TestEvaluatePassesGroupScopeToQuery(t *testing.T) {
	botUUID := pgtype.UUID{Bytes: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Valid: true}
	identityUUID := "55555555-5555-5555-5555-555555555555"
	var captured []any
	db := &fakeDBTX{
		queryRowFunc: func(_ context.Context, sql string, args ...any) pgx.Row {
			if strings.Contains(sql, "COALESCE((") && strings.Contains(sql, "source_conversation_id") {
				captured = append([]any(nil), args...)
				return makeStringRow(EffectAllow)
			}
			return noRule()
		},
	}
	service := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))
	_, err := service.Evaluate(context.Background(), EvaluateRequest{
		BotID:             botUUID.String(),
		ChannelIdentityID: identityUUID,
		ChannelType:       "slack",
		SourceScope: SourceScope{
			ConversationType: "group",
			ConversationID:   "C123",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(captured) != 7 {
		t.Fatalf("expected 7 evaluate args, got %d: %#v", len(captured), captured)
	}
	if captured[0] != ActionChatTrigger {
		t.Fatalf("unexpected action arg: %#v", captured[0])
	}
	if got := textFromArg(captured[1]); got != "slack" {
		t.Fatalf("subject channel arg = %q, want slack", got)
	}
	if got := captured[2].(pgtype.UUID); !got.Valid || uuid.UUID(got.Bytes).String() != identityUUID {
		t.Fatalf("channel identity arg = %#v, want %s", captured[2], identityUUID)
	}
	if got := textFromArg(captured[3]); got != "group" {
		t.Fatalf("conversation type arg = %q, want group", got)
	}
	if got := textFromArg(captured[4]); got != "C123" {
		t.Fatalf("conversation id arg = %q, want C123", got)
	}
	if got := textFromArg(captured[5]); got != "" {
		t.Fatalf("thread id arg = %q, want empty", got)
	}
	if got := captured[6].(pgtype.UUID); !got.Valid || uuid.UUID(got.Bytes).String() != botUUID.String() {
		t.Fatalf("bot id arg = %#v, want %s", captured[6], botUUID.String())
	}
}

func TestEvaluateRejectsInvalidScope(t *testing.T) {
	service := NewService(nil, nil)
	_, err := service.Evaluate(context.Background(), EvaluateRequest{
		BotID: "11111111-1111-1111-1111-111111111111",
		SourceScope: SourceScope{
			ThreadID: "thread-1",
			// missing ConversationID - invalid
		},
	})
	if !errors.Is(err, ErrInvalidSourceScope) {
		t.Fatalf("expected ErrInvalidSourceScope, got %v", err)
	}
}

func TestValidateTarget(t *testing.T) {
	identityUUID := pgtype.UUID{Bytes: uuid.MustParse("55555555-5555-5555-5555-555555555555"), Valid: true}
	db := &fakeDBTX{
		queryRowFunc: func(_ context.Context, sql string, _ ...any) pgx.Row {
			if !strings.Contains(sql, "FROM channel_identities") {
				return noRule()
			}
			return &fakeRow{scanFunc: func(dest ...any) error {
				*dest[0].(*pgtype.UUID) = identityUUID
				*dest[1].(*string) = "telegram"
				*dest[2].(*string) = "alice"
				*dest[3].(*pgtype.Text) = pgtype.Text{String: "Alice", Valid: true}
				*dest[4].(*pgtype.Text) = pgtype.Text{}
				*dest[5].(*[]byte) = []byte("{}")
				*dest[6].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
				*dest[7].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
				return nil
			}}
		},
	}
	service := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))

	tests := []struct {
		name               string
		channelIdentityID  string
		subjectChannelType string
		wantErr            bool
	}{
		{"all platforms and users", "", "", false},
		{"platform only", "", "telegram", false},
		{"user only", identityUUID.String(), "", false},
		{"user under matching platform", identityUUID.String(), "telegram", false},
		{"user under different platform", identityUUID.String(), "discord", true},
		{"invalid identity id", "not-a-uuid", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.validateTarget(context.Background(), tt.channelIdentityID, tt.subjectChannelType)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateTarget() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestCreateGroupRuleResolvesSourceChannelFromSubjectChannel(t *testing.T) {
	botUUID := "11111111-1111-1111-1111-111111111111"
	actorUUID := "22222222-2222-2222-2222-222222222222"
	ruleUUID := pgtype.UUID{Bytes: uuid.MustParse("33333333-3333-3333-3333-333333333333"), Valid: true}
	var captured []any
	db := &fakeDBTX{
		queryRowFunc: func(_ context.Context, sql string, args ...any) pgx.Row {
			if strings.Contains(sql, "INSERT INTO bot_acl_rules") {
				captured = append([]any(nil), args...)
				return &fakeRow{scanFunc: func(dest ...any) error {
					*dest[0].(*pgtype.UUID) = ruleUUID
					*dest[1].(*pgtype.UUID) = args[0].(pgtype.UUID)
					*dest[2].(*string) = ActionChatTrigger
					*dest[3].(*string) = args[2].(string)
					*dest[4].(*pgtype.UUID) = pgtype.UUID{}
					*dest[5].(*pgtype.Text) = args[7].(pgtype.Text)
					*dest[6].(*pgtype.Text) = args[8].(pgtype.Text)
					*dest[7].(*pgtype.Text) = args[9].(pgtype.Text)
					*dest[8].(*pgtype.Text) = args[10].(pgtype.Text)
					*dest[9].(*pgtype.UUID) = args[3].(pgtype.UUID)
					*dest[10].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
					*dest[11].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
					*dest[12].(*bool) = args[1].(bool)
					*dest[13].(*pgtype.Text) = args[4].(pgtype.Text)
					*dest[14].(*pgtype.Text) = args[6].(pgtype.Text)
					return nil
				}}
			}
			return noRule()
		},
	}
	service := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))
	rule, err := service.CreateRule(context.Background(), botUUID, actorUUID, CreateRuleRequest{
		Enabled:            true,
		Effect:             EffectDeny,
		SubjectChannelType: "slack",
		SourceScope: &SourceScope{
			ConversationType: "group",
			ConversationID:   "C123",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(captured) != 11 {
		t.Fatalf("expected 11 create args, got %d: %#v", len(captured), captured)
	}
	if got := captured[5].(pgtype.UUID); got.Valid {
		t.Fatalf("channel identity arg should be empty for group rule: %#v", got)
	}
	if got := textFromArg(captured[6]); got != "slack" {
		t.Fatalf("subject channel arg = %q, want slack", got)
	}
	if got := textFromArg(captured[7]); got != "slack" {
		t.Fatalf("source channel arg = %q, want slack", got)
	}
	if got := textFromArg(captured[8]); got != "group" {
		t.Fatalf("source conversation type arg = %q, want group", got)
	}
	if got := textFromArg(captured[9]); got != "C123" {
		t.Fatalf("source conversation id arg = %q, want C123", got)
	}
	if rule.SubjectChannelType != "slack" || rule.SourceScope == nil || rule.SourceScope.ConversationID != "C123" {
		t.Fatalf("unexpected returned rule: %+v", rule)
	}
}

func TestValidateEffect(t *testing.T) {
	if err := validateEffect(EffectAllow); err != nil {
		t.Fatalf("allow should be valid: %v", err)
	}
	if err := validateEffect(EffectDeny); err != nil {
		t.Fatalf("deny should be valid: %v", err)
	}
	if err := validateEffect("unknown"); err == nil {
		t.Fatal("expected error for unknown effect")
	}
}

func TestSetDefaultEffect(t *testing.T) {
	botUUID := pgtype.UUID{Bytes: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Valid: true}
	var capturedEffect string
	db := &fakeDBTX{
		execFunc: func(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			if strings.Contains(sql, "acl_default_effect") {
				capturedEffect = args[1].(string)
			}
			return pgconn.CommandTag{}, nil
		},
	}
	service := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))
	if err := service.SetDefaultEffect(context.Background(), botUUID.String(), EffectAllow); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedEffect != EffectAllow {
		t.Fatalf("expected effect %q, got %q", EffectAllow, capturedEffect)
	}
	if err := service.SetDefaultEffect(context.Background(), botUUID.String(), "invalid"); !errors.Is(err, ErrInvalidEffect) {
		t.Fatalf("expected ErrInvalidEffect, got %v", err)
	}
}

func TestListObservedConversationsByChannelIdentity(t *testing.T) {
	botUUID := pgtype.UUID{Bytes: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Valid: true}
	channelIdentityUUID := pgtype.UUID{Bytes: uuid.MustParse("55555555-5555-5555-5555-555555555555"), Valid: true}
	routeUUID := pgtype.UUID{Bytes: uuid.MustParse("66666666-6666-6666-6666-666666666666"), Valid: true}
	now := time.Now().UTC()

	db := &fakeDBTX{
		queryFunc: func(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
			if !strings.Contains(sql, "observed_routes") && !strings.Contains(sql, "bot_sessions") {
				return &fakeRows{}, nil
			}
			return &fakeRows{
				rows: []func(dest ...any) error{
					func(dest ...any) error {
						*dest[0].(*pgtype.UUID) = routeUUID
						*dest[1].(*string) = "feishu"
						*dest[2].(*string) = "group"
						*dest[3].(*string) = "chat-1"
						*dest[4].(*string) = "thread-1"
						*dest[5].(*string) = "Team Chat"
						*dest[6].(*string) = "https://example.com/team.png"
						*dest[7].(*pgtype.Timestamptz) = pgtype.Timestamptz{Time: now, Valid: true}
						return nil
					},
				},
			}, nil
		},
	}

	service := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))
	items, err := service.ListObservedConversationsByChannelIdentity(context.Background(), botUUID.String(), channelIdentityUUID.String())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].RouteID != routeUUID.String() {
		t.Fatalf("unexpected route id: %s", items[0].RouteID)
	}
	if items[0].ConversationID != "chat-1" || items[0].ThreadID != "thread-1" {
		t.Fatalf("unexpected conversation scope: %+v", items[0])
	}
	if items[0].ConversationAvatarURL != "https://example.com/team.png" {
		t.Fatalf("unexpected conversation avatar: %s", items[0].ConversationAvatarURL)
	}
}

func TestTextFromArg(t *testing.T) {
	if got := textFromArg(pgtype.Text{String: "  hello  ", Valid: true}); got != "hello" {
		t.Fatalf("unexpected: %q", got)
	}
	if got := textFromArg("world"); got != "world" {
		t.Fatalf("unexpected: %q", got)
	}
}
