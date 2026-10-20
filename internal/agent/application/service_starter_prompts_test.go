package application

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
)

const (
	starterBotID   = "11111111-1111-1111-1111-111111111111"
	starterOwnerID = "22222222-2222-2222-2222-222222222222"
	starterUserID  = "44444444-4444-4444-4444-444444444444"
)

type starterPromptQueries struct {
	dbstore.Queries
	bot           sqlc.GetBotByIDRow
	sessions      []sqlc.ListSessionsByBotAndCreatedByUserPagedRow
	installations []sqlc.BotAppInstallation
	sessionParams *sqlc.ListSessionsByBotAndCreatedByUserPagedParams
}

func (q *starterPromptQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q *starterPromptQueries) ListSessionsByBotAndCreatedByUserPaged(_ context.Context, arg sqlc.ListSessionsByBotAndCreatedByUserPagedParams) ([]sqlc.ListSessionsByBotAndCreatedByUserPagedRow, error) {
	q.sessionParams = &arg
	return q.sessions, nil
}

// GetModelByID fails the way an unreachable database does, so a configured
// title model cannot be resolved and generation fails.
func (*starterPromptQueries) GetModelByID(context.Context, pgtype.UUID) (sqlc.Model, error) {
	return sqlc.Model{}, errors.New("database unavailable")
}

func (q *starterPromptQueries) ListBotAppInstallations(context.Context, pgtype.UUID) ([]sqlc.BotAppInstallation, error) {
	return q.installations, nil
}

func newStarterPromptService(t *testing.T, titleModelID string) (*Service, *starterPromptQueries) {
	t.Helper()
	botID, err := db.ParseUUID(starterBotID)
	if err != nil {
		t.Fatal(err)
	}
	ownerID, err := db.ParseUUID(starterOwnerID)
	if err != nil {
		t.Fatal(err)
	}
	queries := &starterPromptQueries{bot: sqlc.GetBotByIDRow{
		ID: botID, OwnerUserID: ownerID, Name: "game",
		DisplayName: pgtype.Text{String: "游戏小帮手", Valid: true},
	}}
	svc := &Service{
		queries: queries,
		accountService: accounts.NewService(nil, titleModelAccountStore{account: dbstore.AccountRecord{
			ID: starterOwnerID, TitleModelID: titleModelID,
		}}),
		logger: slog.New(slog.DiscardHandler),
	}
	return svc, queries
}

func starterRequest(knownHash string) StarterPromptsRequest {
	return StarterPromptsRequest{BotID: starterBotID, UserID: starterUserID, Locale: "zh-CN", KnownHash: knownHash}
}

func TestStarterPromptsReportsUnchangedForKnownHash(t *testing.T) {
	t.Parallel()
	svc, _ := newStarterPromptService(t, "")
	material, _, err := svc.collectStarterPromptMaterial(context.Background(), starterRequest(""))
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := buildStarterPromptsPrompt(material)
	if err != nil {
		t.Fatal(err)
	}
	known := starterPromptsHash(prompt)

	got, err := svc.StarterPrompts(context.Background(), starterRequest(known))
	if err != nil {
		t.Fatalf("StarterPrompts() error = %v", err)
	}
	if !got.Unchanged || got.Hash != known || got.Items == nil {
		t.Fatalf("StarterPrompts() = %+v, want unchanged with hash %q and an empty item list", got, known)
	}
}

func TestStarterPromptsWithoutTitleModelReportsWhy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		userID string
		want   StarterPromptsReason
	}{
		{name: "owner", userID: starterOwnerID, want: StarterPromptsTitleModelMissing},
		{name: "member", userID: starterUserID, want: StarterPromptsOwnerTitleModelMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, _ := newStarterPromptService(t, "")
			req := starterRequest("stale")
			req.UserID = tc.userID

			got, err := svc.StarterPrompts(context.Background(), req)
			if err != nil {
				t.Fatalf("StarterPrompts() error = %v", err)
			}
			if got.Reason != tc.want || got.Unchanged || got.Hash != "" || got.Items == nil || len(got.Items) != 0 {
				t.Fatalf("StarterPrompts() = %+v, want reason %q, no hash and an empty item list", got, tc.want)
			}
		})
	}
}

func TestStarterPromptsRefreshWithoutTitleModelReportsWhy(t *testing.T) {
	t.Parallel()
	svc, _ := newStarterPromptService(t, "")
	req := starterRequest("")
	req.Refresh = true

	got, err := svc.StarterPrompts(context.Background(), req)
	if err != nil {
		t.Fatalf("StarterPrompts() error = %v", err)
	}
	if got.Reason != StarterPromptsOwnerTitleModelMissing || len(got.Items) != 0 {
		t.Fatalf("StarterPrompts() with Refresh = %+v, want reason %q", got, StarterPromptsOwnerTitleModelMissing)
	}
}

func TestStarterPromptsGenerationFailureCarriesNoReason(t *testing.T) {
	t.Parallel()
	svc, queries := newStarterPromptService(t, "33333333-3333-3333-3333-333333333333")
	svc.modelsService = models.NewService(slog.New(slog.DiscardHandler), queries)
	req := starterRequest("")
	req.UserID = starterOwnerID

	got, err := svc.StarterPrompts(context.Background(), req)
	if err != nil {
		t.Fatalf("StarterPrompts() error = %v", err)
	}
	if got.Reason != "" || got.Unchanged || got.Hash != "" || got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("StarterPrompts() = %+v, want an empty set without a reason", got)
	}
}

func TestStarterPromptMaterialReadsOnlyTheUsersRecentSessions(t *testing.T) {
	t.Parallel()
	svc, queries := newStarterPromptService(t, "")
	queries.sessions = []sqlc.ListSessionsByBotAndCreatedByUserPagedRow{{Title: "塞尔达神庙攻略"}, {Title: "  "}}

	material, ownerID, err := svc.collectStarterPromptMaterial(context.Background(), starterRequest(""))
	if err != nil {
		t.Fatal(err)
	}
	if ownerID != starterOwnerID {
		t.Fatalf("owner = %q, want %q", ownerID, starterOwnerID)
	}
	params := queries.sessionParams
	if params == nil || params.CreatedByUserID.String() != starterUserID || !params.UseVisibility || params.Visibility != "user" {
		t.Fatalf("session query = %+v, want the requesting user's user-visible sessions", params)
	}
	if len(material.RecentTopics) != 1 || material.RecentTopics[0] != "塞尔达神庙攻略" {
		t.Fatalf("recent topics = %q, want the one non-empty title", material.RecentTopics)
	}
}

func TestStarterPromptsHashFollowsMaterial(t *testing.T) {
	t.Parallel()
	hashOf := func(mutate func(*starterPromptQueries, *StarterPromptsRequest)) string {
		svc, queries := newStarterPromptService(t, "")
		req := starterRequest("")
		mutate(queries, &req)
		material, _, err := svc.collectStarterPromptMaterial(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		prompt, err := buildStarterPromptsPrompt(material)
		if err != nil {
			t.Fatal(err)
		}
		return starterPromptsHash(prompt)
	}

	base := hashOf(func(*starterPromptQueries, *StarterPromptsRequest) {})
	if again := hashOf(func(*starterPromptQueries, *StarterPromptsRequest) {}); again != base {
		t.Fatalf("hash is not stable: %q then %q", base, again)
	}
	changes := map[string]func(*starterPromptQueries, *StarterPromptsRequest){
		"new session title": func(q *starterPromptQueries, _ *StarterPromptsRequest) {
			q.sessions = []sqlc.ListSessionsByBotAndCreatedByUserPagedRow{{Title: "新游戏发售"}}
		},
		"installed app": func(q *starterPromptQueries, _ *StarterPromptsRequest) {
			q.installations = []sqlc.BotAppInstallation{{AppID: "web-search", Status: "installed"}}
		},
		"locale": func(_ *starterPromptQueries, r *StarterPromptsRequest) { r.Locale = "en" },
		"display name": func(q *starterPromptQueries, _ *StarterPromptsRequest) {
			q.bot.DisplayName = pgtype.Text{String: "攻略助手", Valid: true}
		},
	}
	for name, mutate := range changes {
		if got := hashOf(mutate); got == base {
			t.Errorf("%s: hash unchanged, want a new hash", name)
		}
	}
	if got := hashOf(func(q *starterPromptQueries, _ *StarterPromptsRequest) {
		q.installations = []sqlc.BotAppInstallation{{AppID: "web-search", Status: "failed"}}
	}); got != base {
		t.Errorf("a failed installation changed the hash; only usable Apps are material")
	}
}

func TestStarterPromptsRefreshGeneratesEvenForTheKnownHash(t *testing.T) {
	t.Parallel()
	svc, _ := newStarterPromptService(t, "")
	material, _, err := svc.collectStarterPromptMaterial(context.Background(), starterRequest(""))
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := buildStarterPromptsPrompt(material)
	if err != nil {
		t.Fatal(err)
	}
	req := starterRequest(starterPromptsHash(prompt))
	req.Refresh = true

	got, err := svc.StarterPrompts(context.Background(), req)
	if err != nil {
		t.Fatalf("StarterPrompts() error = %v", err)
	}
	if got.Unchanged {
		t.Fatalf("StarterPrompts() with Refresh = %+v, want a generation attempt instead of unchanged", got)
	}
}

func TestStarterPromptAvoidList(t *testing.T) {
	t.Parallel()
	prompts := []string{" a ", "", "   "}
	for i := range 20 {
		prompts = append(prompts, strings.Repeat("x", i+1))
	}
	avoid := starterPromptAvoidList(prompts)
	if len(avoid) != starterPromptAvoidLimit || avoid[0] != "a" {
		t.Fatalf("starterPromptAvoidList() = %q, want %d trimmed non-empty prompts", avoid, starterPromptAvoidLimit)
	}
	if section := starterPromptsAvoidSection(nil); section != "" {
		t.Fatalf("starterPromptsAvoidSection(nil) = %q, want empty", section)
	}
	if section := starterPromptsAvoidSection([]string{"Plan a \"route\""}); !strings.Contains(section, `"Plan a \"route\""`) {
		t.Fatalf("starterPromptsAvoidSection() = %q, want the prompts JSON-encoded", section)
	}
}

func TestStarterPromptAppFromUsesReleaseName(t *testing.T) {
	t.Parallel()
	app, ok := starterPromptAppFrom(sqlc.BotAppInstallation{
		AppID: "node", Status: "installed",
		Release: []byte(`{"name":"Node.js","description":"Run JavaScript in the workspace"}`),
	})
	if !ok || app.Name != "Node.js" || app.Description != "Run JavaScript in the workspace" {
		t.Fatalf("starterPromptAppFrom() = %+v, %v", app, ok)
	}
	app, ok = starterPromptAppFrom(sqlc.BotAppInstallation{AppID: "node", Status: "updating", Release: []byte("{")})
	if !ok || app.Name != "node" {
		t.Fatalf("starterPromptAppFrom() with an unreadable release = %+v, %v, want the App id", app, ok)
	}
}

func TestNormalizeStarterPromptLocale(t *testing.T) {
	t.Parallel()
	cases := []struct{ input, want string }{
		{"zh", "zh"}, {"zh-CN", "zh-CN"}, {"\tja ", "ja"}, {"", "en"}, {"zh\nignore", "en"}, {"<script>", "en"},
	}
	for _, tc := range cases {
		if got := normalizeStarterPromptLocale(tc.input); got != tc.want {
			t.Errorf("normalizeStarterPromptLocale(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestParseStarterPrompts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want int
	}{
		{"bare array", `[{"title":"a","prompt":"b"}]`, 1},
		{"code fence", "```json\n[{\"title\":\"a\",\"prompt\":\"b\"},{\"title\":\"c\",\"prompt\":\"d\"}]\n```", 2},
		{"drops incomplete items", `[{"title":"a","prompt":""},{"title":" ","prompt":"b"},{"title":"c","prompt":"d"}]`, 1},
		{"keeps at most three", `[{"title":"1","prompt":"1"},{"title":"2","prompt":"2"},{"title":"3","prompt":"3"},{"title":"4","prompt":"4"}]`, 3},
		{"not json", "Here are some ideas!", 0},
		{"broken json", `[{"title":"a",`, 0},
	}
	for _, tc := range cases {
		if got := parseStarterPrompts(tc.text); len(got) != tc.want {
			t.Errorf("%s: parseStarterPrompts() = %+v, want %d items", tc.name, got, tc.want)
		}
	}
}

func TestCompleteStarterPromptsParsesModelOutput(t *testing.T) {
	t.Parallel()
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r","object":"chat.completion","created":0,"model":"m",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"[{\"title\":\"查攻略\",\"prompt\":\"帮我找一份神庙攻略\"}]"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	t.Cleanup(server.Close)

	svc := &Service{logger: slog.New(slog.DiscardHandler)}
	provider := sqlc.Provider{ClientType: "openai-completions", Config: []byte(`{"base_url":"` + server.URL + `","api_key":"k"}`)}
	items := svc.completeStarterPrompts(context.Background(), svc.logger, "user-1", models.GetResponse{ModelID: "m"}, provider, "PROMPT-MARKER")
	if len(items) != 1 || items[0].Title != "查攻略" || items[0].Prompt != "帮我找一份神庙攻略" {
		t.Fatalf("completeStarterPrompts() = %+v", items)
	}
	if !strings.Contains(gotBody, "PROMPT-MARKER") {
		t.Fatalf("request body did not carry the prompt: %s", gotBody)
	}
}
