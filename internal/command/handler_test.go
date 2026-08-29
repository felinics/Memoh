package command

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/acl"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/schedule"
	"github.com/felinics/memoh/internal/settings"
)

// --- fake services ---

type fakeRoleResolver struct {
	role string
	err  error
}

func (f *fakeRoleResolver) GetMemberRole(_ context.Context, _, _ string) (string, error) {
	return f.role, f.err
}

type fakeAccessEvaluator struct {
	allow bool
	err   error
}

func (f *fakeAccessEvaluator) Evaluate(_ context.Context, _ acl.EvaluateRequest) (bool, error) {
	return f.allow, f.err
}

type fakeScheduleService struct {
	items []schedule.Schedule
}

type fakeCommandQueries struct {
	latestSessionID  pgtype.UUID
	latestSessionErr error
	messageCount     int64
	latestUsage      int64
	latestUsageErr   error
	cacheRow         dbsqlc.GetSessionCacheStatsRow
	cacheErr         error
	skills           []string
}

func (f *fakeCommandQueries) GetLatestSessionIDByBot(_ context.Context, _ pgtype.UUID) (pgtype.UUID, error) {
	return f.latestSessionID, f.latestSessionErr
}

func (f *fakeCommandQueries) CountMessagesBySession(_ context.Context, _ pgtype.UUID) (int64, error) {
	return f.messageCount, nil
}

func (f *fakeCommandQueries) GetLatestAssistantUsage(_ context.Context, _ pgtype.UUID) (int64, error) {
	if f.latestUsageErr != nil {
		return 0, f.latestUsageErr
	}
	return f.latestUsage, nil
}

func (f *fakeCommandQueries) GetSessionCacheStats(_ context.Context, _ pgtype.UUID) (dbsqlc.GetSessionCacheStatsRow, error) {
	if f.cacheErr != nil {
		return dbsqlc.GetSessionCacheStatsRow{}, f.cacheErr
	}
	return f.cacheRow, nil
}

func (f *fakeCommandQueries) GetSessionUsedSkills(_ context.Context, _ pgtype.UUID) ([]string, error) {
	return f.skills, nil
}

func (*fakeCommandQueries) GetTokenUsageByDayAndType(_ context.Context, _ dbsqlc.GetTokenUsageByDayAndTypeParams) ([]dbsqlc.GetTokenUsageByDayAndTypeRow, error) {
	return nil, nil
}

func (*fakeCommandQueries) GetTokenUsageByModel(_ context.Context, _ dbsqlc.GetTokenUsageByModelParams) ([]dbsqlc.GetTokenUsageByModelRow, error) {
	return nil, nil
}

// newTestHandler creates a Handler with nil services for use in tests.
func newTestHandler(roleResolver MemberRoleResolver) *Handler {
	return NewHandler(nil, roleResolver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}

func newTestHandlerWithQueries(roleResolver MemberRoleResolver, queries CommandQueries) *Handler {
	return NewHandler(nil, roleResolver, nil, nil, nil, nil, nil, nil, nil, nil, nil, queries, nil, nil, nil)
}

func newTestHandlerWithACL(roleResolver MemberRoleResolver, evaluator AccessEvaluator) *Handler {
	return NewHandler(nil, roleResolver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, evaluator, nil, nil)
}

// --- tests ---

func TestIsCommand(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)
	tests := []struct {
		input string
		want  bool
	}{
		{"/help", true},
		{" /schedule list", true},
		{"@BotName /help", true},
		{"@_user_1 /schedule list", true},
		{"<@123456> /mcp list", true},
		{"/help@MemohBot", true},
		{"hello", false},
		{"", false},
		{"/", false},
		{"/ ", false},
		{"/unknown_cmd", false},
		{"check https://example.com/help", false},
		{"@bot hello", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			if got := h.IsCommand(tt.input); got != tt.want {
				t.Errorf("IsCommand(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestExecute_Help(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	result, err := h.Execute(context.Background(), "bot-1", "user-1", "/help")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Available commands") {
		t.Errorf("expected help text, got: %s", result)
	}
	if !strings.Contains(result, "Switch the chat model") {
		t.Errorf("expected top-level model entry, got: %s", result)
	}
}

func TestExecuteResultUsesProvidedInvocation(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	invocation, err := ParseInvocation(InvocationInput{Text: "/help @memoh1bot", BotAliases: []string{"memoh1bot"}})
	if err != nil {
		t.Fatalf("ParseInvocation() error = %v", err)
	}
	result, err := h.ExecuteResult(context.Background(), ExecuteInput{
		Text:       "this deliberately is not reparsable",
		Invocation: &invocation,
	})
	if err != nil {
		t.Fatalf("ExecuteResult() error = %v", err)
	}
	if !strings.Contains(result.Text, "Available commands") {
		t.Fatalf("result = %q, want global help", result.Text)
	}
}

func TestExecute_HelpGroup(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	result, err := h.Execute(context.Background(), "bot-1", "user-1", "/help model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Switch the chat model") {
		t.Errorf("expected group help, got: %s", result)
	}
	if !strings.Contains(result, "Set the chat model") {
		t.Errorf("expected compact action summary, got: %s", result)
	}
	if strings.Contains(result, "(owner)") || strings.Contains(result, "owner only") || strings.Contains(result, "🔒") {
		t.Errorf("help should not expose owner-only decoration, got: %s", result)
	}
}

func TestExecuteResult_HelpGroupUsesShortButtonLabels(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	result, err := h.ExecuteResult(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "user-1",
		Text:              "/help schedule",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Interactive == nil || result.Interactive.Choices == nil {
		t.Fatal("expected interactive choices for group help")
	}
	if !strings.Contains(result.Interactive.Choices.Title, "`/schedule list` — List all schedules") {
		t.Errorf("expected action descriptions in interactive title, got: %s", result.Interactive.Choices.Title)
	}
	if result.Interactive.Choices.Columns != 1 {
		t.Errorf("help buttons should render as one large button per row, got columns=%d", result.Interactive.Choices.Columns)
	}
	for _, item := range result.Interactive.Choices.Choices {
		if strings.HasPrefix(item.Label, "◀ ") {
			continue
		}
		if strings.Contains(item.Label, "—") || strings.Contains(item.Label, "schedule") || strings.Contains(item.Label, "🔒") {
			t.Errorf("button label should stay short, got %q", item.Label)
		}
	}
	for _, item := range result.Interactive.Choices.Choices {
		if strings.Contains(item.Label, "owner") || strings.Contains(item.Label, "🔒") {
			t.Errorf("help button label should not expose permission decoration: %#v", result.Interactive.Choices.Choices)
		}
	}
}

func TestExecute_HelpAction(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	result, err := h.Execute(context.Background(), "bot-1", "user-1", "/help model set")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "/model set <model_id> | <provider_name> <model_name>") {
		t.Errorf("expected action usage, got: %s", result)
	}
	if strings.Contains(result, "Access:") || strings.Contains(result, "owner only") || strings.Contains(result, "(owner)") {
		t.Errorf("action help should not expose owner-only decoration, got: %s", result)
	}
}

func TestExecute_UnknownCommand(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	result, err := h.Execute(context.Background(), "bot-1", "user-1", "/foobar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Unknown command") {
		t.Errorf("expected unknown command message, got: %s", result)
	}
}

func TestExecute_WithMentionPrefix(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	result, err := h.Execute(context.Background(), "bot-1", "user-1", "@BotName /help")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Available commands") {
		t.Errorf("expected help text from mention-prefixed command, got: %s", result)
	}
}

func TestExecute_TelegramBotSuffix(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	result, err := h.Execute(context.Background(), "bot-1", "user-1", "/help@MemohBot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Available commands") {
		t.Errorf("expected help text from telegram-style command, got: %s", result)
	}
}

func TestExecute_UnknownAction(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	result, err := h.Execute(context.Background(), "bot-1", "user-1", "/schedule foobar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Unknown action") {
		t.Errorf("expected unknown action message, got: %s", result)
	}
	if !strings.Contains(result, "/schedule") {
		t.Errorf("expected schedule usage in message, got: %s", result)
	}
}

func TestExecute_WritePermissionDenied(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: ""})
	result, err := h.Execute(context.Background(), "bot-1", "user-1", "/schedule create test desc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Only the bot owner") {
		t.Errorf("expected permission denied, got: %s", result)
	}
}

func TestExecute_WritePermissionAllowedForOwner(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	result, err := h.Execute(context.Background(), "bot-1", "user-1", "/schedule create")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "Permission denied") {
		t.Errorf("owner should not get permission denied, got: %s", result)
	}
	if !strings.Contains(result, "Usage:") {
		t.Errorf("expected usage hint for missing args, got: %s", result)
	}
}

func TestExecute_WritePermissionDeniedForQQAndWeixinWithoutLinkedUser(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: ""})
	for _, channelType := range []string{"qq", "weixin"} {
		t.Run(channelType, func(t *testing.T) {
			t.Parallel()
			result, err := h.ExecuteWithInput(context.Background(), ExecuteInput{
				BotID:             "bot-1",
				ChannelIdentityID: "channel-id-1",
				Text:              "/model set",
				ChannelType:       channelType,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(result, "Only the bot owner") {
				t.Fatalf("%s unlinked write command should be denied, got: %s", channelType, result)
			}
		})
	}
}

func TestExecute_WritePermissionStillDeniedForOtherUnlinkedChannels(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: ""})
	result, err := h.ExecuteWithInput(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "channel-id-1",
		Text:              "/model set",
		ChannelType:       "discord",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Only the bot owner") {
		t.Fatalf("unlinked discord write command should still be denied, got: %s", result)
	}
}

func TestExecute_SettingsDefaultAction(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: ""})
	result, err := h.Execute(context.Background(), "bot-1", "user-1", "/settings")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "Unknown action") {
		t.Errorf("expected settings get attempt, not unknown action, got: %s", result)
	}
}

func TestSettingsResultUsesFocusedActions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		settings       settings.Settings
		mustHaveLabels []string
		aclArgValue    string // the ACL toggle's Args[1] must match this
	}{
		{
			name:           "acl=allow shows ask action",
			settings:       settings.Settings{AclDefaultEffect: "allow"},
			mustHaveLabels: []string{"Reasoning ▸", "Models ▸", "Ask before tools", "Search ▸", "Memory ▸"},
			aclArgValue:    "deny",
		},
		{
			name:           "acl=deny shows allow action",
			settings:       settings.Settings{AclDefaultEffect: "deny"},
			mustHaveLabels: []string{"Reasoning ▸", "Models ▸", "Allow tools", "Search ▸", "Memory ▸"},
			aclArgValue:    "allow",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{}
			result := h.settingsResult(CommandContext{L: i18n.New("en")}, tc.settings)
			if result.Interactive == nil || result.Interactive.Choices == nil {
				t.Fatal("expected settings choices")
			}
			var labels []string
			for _, item := range result.Interactive.Choices.Choices {
				labels = append(labels, item.Label)
			}
			for _, forbidden := range []string{"Reasoning: off", "Effort ▸", "ACL: allow"} {
				for _, label := range labels {
					if label == forbidden {
						t.Fatalf("settings should not expose redundant state button %q; labels=%v", forbidden, labels)
					}
				}
			}
			for _, want := range tc.mustHaveLabels {
				var found bool
				for _, label := range labels {
					if label == want {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("settings labels missing %q: %v", want, labels)
				}
			}
			var aclArgs []string
			for _, item := range result.Interactive.Choices.Choices {
				if item.Action == nil || item.Action.Resource != "settings" || item.Action.Action != "update" {
					continue
				}
				if len(item.Action.Args) >= 2 {
					if item.Action.Args[0] == "--acl_default_effect" {
						aclArgs = item.Action.Args
					}
				}
			}
			if len(aclArgs) != 2 || aclArgs[1] != tc.aclArgValue {
				t.Errorf("acl toggle should dispatch --acl_default_effect %s, got %v", tc.aclArgValue, aclArgs)
			}
		})
	}
}

func TestExecute_MissingArgs(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	tests := []struct {
		cmd      string
		contains string
	}{
		{"/schedule get", "Usage:"},
		{"/schedule create", "Usage:"},
		{"/schedule delete", "Usage:"},
		{"/mcp get", "Usage:"},
		{"/mcp delete", "Usage:"},
		{"/fs read", "isn't available"},
		{"/model set", "Usage:"},
		{"/memory set", "Usage:"},
		{"/search set", "Usage:"},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			t.Parallel()
			result, err := h.Execute(context.Background(), "bot-1", "user-1", tt.cmd)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(result, tt.contains) {
				t.Errorf("expected %q in result, got: %s", tt.contains, result)
			}
		})
	}
}

func TestFormatItems(t *testing.T) {
	t.Parallel()
	result := formatItems([][]kv{
		{{"Name", "foo"}, {"Type", "bar"}},
		{{"Model", "anthropic/claude-opus"}, {"Active", "yes"}},
	})
	// Compact layout: "- label — chip · chip", keys dropped, human words plain.
	if !strings.Contains(result, "- foo — bar") {
		t.Errorf("expected compact '- foo — bar' line, got: %s", result)
	}
	if strings.Contains(result, "`yes`") {
		t.Errorf("boolean should stay plain, got: %s", result)
	}
	// Machine tokens (namespaced slug) render as code spans even as the label.
	if !strings.Contains(result, "- `anthropic/claude-opus` — yes") {
		t.Errorf("expected code-spanned model id label, got: %s", result)
	}
}

func TestFormatRecordsNote(t *testing.T) {
	t.Parallel()
	result := formatRecords([]listRecord{
		{fields: []kv{{"Name", "daily-report"}, {"Enabled", "on"}}, note: "daily at 09:00 · Send the morning summary"},
	})
	// Note inlined with em-dash so plain-text IMs (Weixin / WeChat OA /
	// Local-Web) don't soft-wrap-collapse a 2-space-indented continuation
	// line into the next row's label.
	if !strings.Contains(result, "- `daily-report` — on") {
		t.Errorf("expected label + chip line, got: %s", result)
	}
	if !strings.Contains(result, " — daily at 09:00 · Send the morning summary") {
		t.Errorf("expected inline em-dash note, got: %s", result)
	}
	if strings.Contains(result, "\n  daily at 09:00") {
		t.Errorf("note must not appear on a 2-space-indented continuation line, got: %s", result)
	}
}

func TestFormatItems_Empty(t *testing.T) {
	t.Parallel()
	result := formatItems(nil)
	if result != "" {
		t.Errorf("expected empty string for nil items, got: %q", result)
	}
}

func TestFormatKV(t *testing.T) {
	t.Parallel()
	result := formatKV([]kv{
		{"Name", "test"},
		{"Count", "123"},
		{"Session ID", "9f3ec7a2-1b2c-4d5e"},
	})
	// Short words and numbers render plain.
	if !strings.Contains(result, "- Name: test") || strings.Contains(result, "`test`") {
		t.Errorf("expected plain name, got: %s", result)
	}
	if !strings.Contains(result, "- Count: 123") {
		t.Errorf("expected plain count, got: %s", result)
	}
	// Long opaque identifiers render as code spans.
	if !strings.Contains(result, "- Session ID: `9f3ec7a2-1b2c-4d5e`") {
		t.Errorf("expected code-spanned id, got: %s", result)
	}
}

func TestBareInvocationLandings(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)
	// Groups that previously dumped Usage() help now land on a useful read view.
	want := map[string]string{
		"schedule": "list", "mcp": "list", "memory": "list",
		"search": "list", "email": "outbox", "fs": "list",
	}
	for name, action := range want {
		g, ok := h.registry.groups[name]
		if !ok {
			t.Errorf("group /%s not registered", name)
			continue
		}
		if g.DefaultAction != action {
			t.Errorf("/%s DefaultAction = %q, want %q", name, g.DefaultAction, action)
		}
		if _, ok := g.commands[action]; !ok {
			t.Errorf("/%s default action %q is not a registered sub-command", name, action)
		}
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	if got := truncate("hello world", 5); got != "he..." {
		t.Errorf("truncate: got %q", got)
	}
	if got := truncate("hi", 5); got != "hi" {
		t.Errorf("truncate short: got %q", got)
	}
}

// Verify that the global help includes all resource groups.
func TestGlobalHelp_AllGroups(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)
	help := h.registry.GlobalHelp(i18n.New("en"), "")
	for _, group := range []string{
		"schedule", "mcp", "settings",
		"model", "memory", "search", "usage",
		"email", "skill", "fs", "access",
	} {
		if !strings.Contains(help, "/"+group) {
			t.Errorf("missing /%s in global help", group)
		}
	}
}

func TestGlobalHelpKeepsTelegramGroupCommandsCompact(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)
	help := h.registry.GlobalHelp(i18n.New("en"), "snowluocat_bot")
	for _, commandName := range []string{"help", "start", "new", "link", "compact"} {
		want := "/" + commandName
		if !strings.Contains(help, "- "+want+" —") {
			t.Errorf("compact global help missing list row for %q:\n%s", want, help)
		}
		if strings.Contains(help, "- `"+want+"`") || strings.Contains(help, "- "+want+"@snowluocat_bot") {
			t.Errorf("single-token command %q must remain tap-to-send, not code-spanned", want)
		}
	}
	if strings.Contains(help, "group's actions") || strings.Contains(help, "<group>") {
		t.Errorf("global help leaked internal command-group terminology:\n%s", help)
	}
	if !strings.Contains(help, "for example `/help@snowluocat_bot`") {
		t.Errorf("global help should explain manual group addressing once:\n%s", help)
	}
	if count := strings.Count(help, "snowluocat_bot"); count != 1 {
		t.Errorf("global help repeats bot username %d times, want once:\n%s", count, help)
	}
}

func TestExecuteWithInput_Access(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	result, err := h.ExecuteWithInput(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "channel-id-1",
		UserID:            "user-id-1",
		Text:              "/access",
		ChannelType:       "discord",
		ConversationType:  "thread",
		ConversationID:    "conv-1",
		ThreadID:          "thread-1",
		RouteID:           "route-1",
		SessionID:         "session-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "- Channel Identity: `channel-id-1`") {
		t.Errorf("expected code-spanned channel identity, got: %s", result)
	}
	if !strings.Contains(result, "- Write Commands: yes") || strings.Contains(result, "`yes`") {
		t.Errorf("expected plain 'yes' write access, got: %s", result)
	}
}

func TestExecute_StatusLatest(t *testing.T) {
	t.Parallel()
	sessionUUID := pgtype.UUID{}
	copy(sessionUUID.Bytes[:], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9})
	sessionUUID.Valid = true
	h := newTestHandlerWithQueries(&fakeRoleResolver{role: "owner"}, &fakeCommandQueries{
		latestSessionID: sessionUUID,
		messageCount:    42,
		latestUsage:     1200,
		cacheRow: dbsqlc.GetSessionCacheStatsRow{
			CacheReadTokens:  300,
			TotalInputTokens: 1200,
		},
		skills: []string{"search", "files"},
	})
	result, err := h.Execute(context.Background(), "11111111-1111-1111-1111-111111111111", "user-1", "/status latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Session Status — latest bot session") {
		t.Errorf("expected latest scope in title, got: %s", result)
	}
	if !strings.Contains(result, "- Messages: 42") {
		t.Errorf("expected message count, got: %s", result)
	}
}

func TestExecute_StatusLatestNoRows(t *testing.T) {
	t.Parallel()
	h := newTestHandlerWithQueries(&fakeRoleResolver{role: "owner"}, &fakeCommandQueries{
		latestSessionErr: pgx.ErrNoRows,
	})
	result, err := h.Execute(context.Background(), "11111111-1111-1111-1111-111111111111", "user-1", "/status latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "No session found for this bot.") {
		t.Errorf("expected no session message, got: %s", result)
	}
}

func TestExecute_StatusShowWithoutSession(t *testing.T) {
	t.Parallel()
	h := newTestHandlerWithQueries(&fakeRoleResolver{role: "owner"}, &fakeCommandQueries{})
	result, err := h.Execute(context.Background(), "bot-1", "user-1", "/status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "No active session found for this conversation.") {
		t.Errorf("expected route-aware no session message, got: %s", result)
	}
}

// Verify help usage does not leak permission decorations into user-facing text.
func TestUsage_DoesNotShowOwnerDecoration(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)
	for _, name := range h.registry.order {
		group := h.registry.groups[name]
		usage := group.Usage()
		if strings.Contains(usage, "(owner)") || strings.Contains(usage, "owner only") || strings.Contains(usage, "🔒") {
			t.Errorf("/%s usage leaked owner decoration: %s", name, usage)
		}
	}
}

// Verify new commands with nil services return graceful errors, not panics.
func TestNewCommands_NilServices(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	cmds := []string{
		"/skill list",
		"/fs list",
		"/fs read /test.txt",
	}
	for _, cmd := range cmds {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			result, err := h.Execute(context.Background(), "bot-1", "user-1", cmd)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result == "" {
				t.Error("expected non-empty result")
			}
		})
	}
}

// captureRoleResolver records the arguments passed to GetMemberRole so tests can
// assert that the correct identity is forwarded (regression guard for the bug
// where ChannelIdentityID was swapped with UserID).
type captureRoleResolver struct {
	botID             string
	channelIdentityID string
	role              string
	err               error
}

func (f *captureRoleResolver) GetMemberRole(_ context.Context, botID, channelIdentityID string) (string, error) {
	f.botID = botID
	f.channelIdentityID = channelIdentityID
	return f.role, f.err
}

// TestExecute_CorrectIdentityForwarded guards the critical fix: when input carries
// both a UserID (bound web user) and a ChannelIdentityID (IM identity), the role
// resolver MUST receive the ChannelIdentityID so downstream ManageResolver can
// look it up via bot_channel_admins + user_channel_identity_bindings.
// Passing UserID instead breaks inherited Manage for every non-owner bound user.
func TestExecute_CorrectIdentityForwarded(t *testing.T) {
	t.Parallel()

	t.Run("bound user with manage: channel identity id forwarded", func(t *testing.T) {
		t.Parallel()
		captor := &captureRoleResolver{role: "manager"}
		h := newTestHandler(captor)
		// BotID should be a valid UUID for completeness.
		_, _ = h.ExecuteWithInput(context.Background(), ExecuteInput{
			BotID:             "11111111-1111-1111-1111-111111111111",
			ChannelIdentityID: "33333333-3333-3333-3333-333333333333",
			UserID:            "44444444-4444-4444-4444-444444444444",
			Text:              "/model set",
			ChannelType:       "telegram",
		})
		if captor.channelIdentityID != "33333333-3333-3333-3333-333333333333" {
			t.Errorf("GetMemberRole received channelIdentityID=%q, want %q",
				captor.channelIdentityID, "33333333-3333-3333-3333-333333333333")
		}
	})

	t.Run("unbound user: channel identity id forwarded", func(t *testing.T) {
		t.Parallel()
		captor := &captureRoleResolver{role: ""}
		h := newTestHandler(captor)
		_, _ = h.ExecuteWithInput(context.Background(), ExecuteInput{
			BotID:             "11111111-1111-1111-1111-111111111111",
			ChannelIdentityID: "33333333-3333-3333-3333-333333333333",
			UserID:            "", // unbound
			Text:              "/settings",
			ChannelType:       "discord",
		})
		if captor.channelIdentityID != "33333333-3333-3333-3333-333333333333" {
			t.Errorf("GetMemberRole received channelIdentityID=%q, want %q",
				captor.channelIdentityID, "33333333-3333-3333-3333-333333333333")
		}
	})
}

// suppress unused warnings.
var (
	_ = fakeScheduleService{items: []schedule.Schedule{{ID: "1", Name: "test"}}}
	_ = mcp.Connection{}
	_ = settings.Settings{}
)

// TestLooksLikeInternalError_KeepsRealLeaksHidden pins the markers that MUST
// flag a message as internal (so the user sees a clean retry line instead of
// raw infra).
func TestLooksLikeInternalError_KeepsRealLeaksHidden(t *testing.T) {
	t.Parallel()
	internalCases := []string{
		"failed to dial backend: connection refused",
		"dial tcp 10.0.0.1:5432: connect: connection refused",
		"context deadline exceeded",
		"i/o timeout",
		"no such host: api.example.com",
		"pq: relation \"foo\" does not exist",
		"sql: no rows in result set",
		"x509: certificate signed by unknown authority",
		"panic: nil pointer dereference",
		"runtime error: invalid memory address or nil pointer dereference",
		"goroutine 12 [running]:",
	}
	for _, msg := range internalCases {
		if !looksLikeInternalError(msg) {
			t.Errorf("expected internal-error classification for %q, got false", msg)
		}
	}
}

// TestLooksLikeInternalError_AllowsDomainMessages pins the legitimate-message
// boundary. These are real strings handlers emit; flagging them as internal
// would swap a helpful domain error for a dead "please try again" line.
//
// Documents the trade-offs of the current marker set:
//   - "sqlcoder" (a real model name) used to trip on the bare "sql" marker;
//     the marker is now "sql:" so model IDs with "sql" prefix/substring pass.
//   - URLs in domain messages used to trip on "://"; that marker was removed.
//     Actual URL-bearing transport leaks ("dial tcp…", "no such host") still
//     get caught by the more-specific markers.
//   - "failed to …" as a Go wrap-chain marker still flags legitimate English
//     phrasing like "failed to find a matching schedule" — that's the known
//     trade-off; handlers should phrase domain not-found errors without the
//     "failed to" prefix (or with a more specific verb).
func TestLooksLikeInternalError_AllowsDomainMessages(t *testing.T) {
	t.Parallel()
	domainCases := []string{
		`model "sqlcoder" not found`,
		`ambiguous model: openai/gpt-4 or sqlcoder/sqlcoder-7b`,
		`schedule "daily-recap" not found`,
		`visit https://example.com/docs to configure`,
		`memory provider mem0 returned no matches`,
		"reasoning effort must be one of: low, medium, high",
	}
	for _, msg := range domainCases {
		if looksLikeInternalError(msg) {
			t.Errorf("expected domain-message classification for %q, got true (this leaks the message into a dead retry line)", msg)
		}
	}
}

// TestNormalizeLanguageShorthand guards the "/language zh" shorthand: the bare
// value must be rewritten into the set handler's arg slice. Regression for the
// bug where the rewrite ran after CommandContext.Args was frozen, so /language
// zh fell through to usage text instead of switching the language.
func TestNormalizeLanguageShorthand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in         string
		wantAction string
		wantArgs   []string
	}{
		{"/language zh", "set", []string{"zh"}},
		{"/language en", "set", []string{"en"}},
		{"/language auto", "set", []string{"auto"}},
		{"/language", "", nil},                      // bare → default (show), no rewrite
		{"/language show", "show", nil},             // explicit show untouched
		{"/language set zh", "set", []string{"zh"}}, // already explicit, untouched
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			parsed, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.in, err)
			}
			normalizeLanguageShorthand(canonicalResource(parsed.Resource), &parsed)
			if parsed.Action != tc.wantAction {
				t.Errorf("Action = %q, want %q", parsed.Action, tc.wantAction)
			}
			if strings.Join(parsed.Args, " ") != strings.Join(tc.wantArgs, " ") {
				t.Errorf("Args = %v, want %v", parsed.Args, tc.wantArgs)
			}
		})
	}
}

// outsiderInput builds a read-command invocation from an unbound channel
// identity (no owner/manager role, non-single-bind platform).
func outsiderInput(text string) ExecuteInput {
	return ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "channel-id-1",
		Text:              text,
		ChannelType:       "discord",
		ConversationType:  "direct",
		ConversationID:    "conv-1",
	}
}

// TestExecute_CommandACLGate pins the command-access policy: a caller the chat
// ACL denies cannot operate the bot via slash commands, except the binding
// entry point (/link). Owners and chat-allowed callers pass through.
func TestExecute_CommandACLGate(t *testing.T) {
	t.Parallel()

	t.Run("read command denied for outsider", func(t *testing.T) {
		t.Parallel()
		h := newTestHandlerWithACL(&fakeRoleResolver{role: ""}, &fakeAccessEvaluator{allow: false})
		result, err := h.ExecuteResult(context.Background(), outsiderInput("/access"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result.Text, "don't have access") {
			t.Errorf("expected no-access reply, got: %s", result.Text)
		}
		if strings.Contains(result.Text, "Channel Identity") {
			t.Errorf("denied outsider must not see /access config, got: %s", result.Text)
		}
	})

	t.Run("read command allowed when chat ACL allows", func(t *testing.T) {
		t.Parallel()
		h := newTestHandlerWithACL(&fakeRoleResolver{role: ""}, &fakeAccessEvaluator{allow: true})
		result, err := h.ExecuteResult(context.Background(), outsiderInput("/access"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(result.Text, "don't have access") {
			t.Errorf("chat-allowed caller should reach the command, got: %s", result.Text)
		}
		if !strings.Contains(result.Text, "Channel Identity") {
			t.Errorf("expected /access output for allowed caller, got: %s", result.Text)
		}
	})

	t.Run("owner bypasses chat ACL deny", func(t *testing.T) {
		t.Parallel()
		h := newTestHandlerWithACL(&fakeRoleResolver{role: "owner"}, &fakeAccessEvaluator{allow: false})
		result, err := h.ExecuteResult(context.Background(), outsiderInput("/access"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(result.Text, "don't have access") {
			t.Errorf("owner must always operate the bot, got: %s", result.Text)
		}
		if !strings.Contains(result.Text, "Channel Identity") {
			t.Errorf("expected /access output for owner, got: %s", result.Text)
		}
	})

	t.Run("/link allowed despite chat ACL deny", func(t *testing.T) {
		t.Parallel()
		h := newTestHandlerWithACL(&fakeRoleResolver{role: ""}, &fakeAccessEvaluator{allow: false})
		result, err := h.ExecuteResult(context.Background(), outsiderInput("/link ABC123"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(result.Text, "don't have access") {
			t.Errorf("/link is the binding entry point and must never be gated, got: %s", result.Text)
		}
	})

	t.Run("eval error fails closed", func(t *testing.T) {
		t.Parallel()
		h := newTestHandlerWithACL(&fakeRoleResolver{role: ""}, &fakeAccessEvaluator{err: errors.New("db down")})
		result, err := h.ExecuteResult(context.Background(), outsiderInput("/access"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result.Text, "don't have access") {
			t.Errorf("ACL eval error must deny (fail closed), got: %s", result.Text)
		}
	})
}

// TestCommandAccess pins the boolean policy used to gate route-aware mode
// commands (/new, /stop, /status) that do not flow through Execute.
func TestCommandAccess(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		role      string
		channel   string
		allow     bool
		evalErr   error
		text      string
		wantOK    bool
		wantError bool
	}{
		{name: "link always allowed", role: "", allow: false, text: "/link ABC", wantOK: true},
		{name: "owner allowed despite deny", role: "owner", allow: false, text: "/new", wantOK: true},
		{name: "manager allowed despite deny", role: "manager", allow: false, text: "/stop", wantOK: true},
		{name: "outsider denied", role: "", allow: false, text: "/new", wantOK: false},
		{name: "outsider allowed by chat acl", role: "", allow: true, text: "/status", wantOK: true},
		{name: "qq unbound denied by chat acl", role: "", channel: "qq", allow: false, text: "/new", wantOK: false},
		{name: "weixin unbound denied by chat acl", role: "", channel: "weixin", allow: false, text: "/stop", wantOK: false},
		{name: "eval error propagates", role: "", allow: false, evalErr: errors.New("boom"), text: "/new", wantOK: false, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newTestHandlerWithACL(&fakeRoleResolver{role: tc.role}, &fakeAccessEvaluator{allow: tc.allow, err: tc.evalErr})
			ch := tc.channel
			if ch == "" {
				ch = "discord"
			}
			ok, err := h.CommandAccess(context.Background(), ExecuteInput{
				BotID:             "bot-1",
				ChannelIdentityID: "channel-id-1",
				Text:              tc.text,
				ChannelType:       ch,
				ConversationType:  "direct",
				ConversationID:    "conv-1",
			})
			if tc.wantError && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantError && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.wantOK {
				t.Errorf("CommandAccess(%q) = %v, want %v", tc.text, ok, tc.wantOK)
			}
		})
	}
}

func TestEndsWithTerminalPunct(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"done.", "really?", "stop!", "完成。", "真的？", "等等…", "  trailing.  "} {
		if !endsWithTerminalPunct(s) {
			t.Errorf("endsWithTerminalPunct(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "no punct", "model x", "中文无标点"} {
		if endsWithTerminalPunct(s) {
			t.Errorf("endsWithTerminalPunct(%q) = true, want false", s)
		}
	}
}

// TestFriendlyCommandError_NoDoublePunctZh guards the CJK-aware period logic:
// a zh domain error already ending in the ideographic full stop "。" must not
// gain a trailing ASCII ".". The model not-found path (now carrying a baked-in
// discovery pointer) flows through here in zh sessions.
func TestFriendlyCommandError_NoDoublePunctZh(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)
	zh := i18n.New("zh")
	err := errors.New("找不到模型 \"x\"。用 `/model list` 查看可用模型。")
	got := h.friendlyCommandError(zh, "model", err)
	if strings.Contains(got, "。.") {
		t.Errorf("zh error gained a trailing ASCII period: %q", got)
	}
	if !strings.HasSuffix(got, "。") {
		t.Errorf("zh error should still end with 。, got %q", got)
	}
}
