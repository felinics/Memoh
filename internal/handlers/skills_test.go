package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/memohai/memoh/internal/accounts"
	"github.com/memohai/memoh/internal/agent/runtime/native"
	"github.com/memohai/memoh/internal/bots"
	"github.com/memohai/memoh/internal/config"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/memohai/memoh/internal/db/postgres/store"
	pluginspkg "github.com/memohai/memoh/internal/plugins"
	skillset "github.com/memohai/memoh/internal/skills"
	"github.com/memohai/memoh/internal/workspace"
	pb "github.com/memohai/memoh/internal/workspace/bridgepb"
)

func TestListSkillsAPIReportsEffectiveShadowedAndSourceMetadata(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeSkillFile(t, path.Join(skillset.ManagedDir(), "alpha", "SKILL.md"), managedSkillRaw("alpha", "Managed Alpha"))
	env.writeSkillFile(t, path.Join("/data/.agents/skills", "alpha", "SKILL.md"), managedSkillRaw("alpha", "Compat Alpha"))
	env.writeSkillFile(t, path.Join("/data/.agents/skills", "beta", "SKILL.md"), managedSkillRaw("beta", "Compat Beta"))

	rec, err := env.callJSON(t, http.MethodGet, "/bots/:bot_id/container/skills", nil, env.handler.ListSkills)
	if err != nil {
		t.Fatalf("ListSkills returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("ListSkills status = %d, want 200", rec.Code)
	}

	var resp SkillsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode skills response: %v", err)
	}
	if len(resp.Skills) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(resp.Skills))
	}

	alphaManaged := mustFindSkillByPath(t, resp.Skills, path.Join(skillset.ManagedDir(), "alpha", "SKILL.md"))
	if !alphaManaged.Managed {
		t.Fatalf("managed alpha should be managed: %+v", alphaManaged)
	}
	if alphaManaged.State != skillset.StateEffective {
		t.Fatalf("managed alpha state = %q, want %q", alphaManaged.State, skillset.StateEffective)
	}
	if alphaManaged.SourceKind != skillset.SourceKindManaged {
		t.Fatalf("managed alpha source_kind = %q, want %q", alphaManaged.SourceKind, skillset.SourceKindManaged)
	}

	alphaCompatPath := path.Join("/data/.agents/skills", "alpha", "SKILL.md")
	alphaCompat := mustFindSkillByPath(t, resp.Skills, alphaCompatPath)
	if alphaCompat.Managed {
		t.Fatalf("compat alpha should not be managed: %+v", alphaCompat)
	}
	if alphaCompat.State != skillset.StateShadowed {
		t.Fatalf("compat alpha state = %q, want %q", alphaCompat.State, skillset.StateShadowed)
	}
	if alphaCompat.ShadowedBy != alphaManaged.SourcePath {
		t.Fatalf("compat alpha shadowed_by = %q, want %q", alphaCompat.ShadowedBy, alphaManaged.SourcePath)
	}
	if alphaCompat.SourceKind != skillset.SourceKindCompat {
		t.Fatalf("compat alpha source_kind = %q, want %q", alphaCompat.SourceKind, skillset.SourceKindCompat)
	}

	betaCompat := mustFindSkillByPath(t, resp.Skills, path.Join("/data/.agents/skills", "beta", "SKILL.md"))
	if betaCompat.State != skillset.StateEffective {
		t.Fatalf("beta compat state = %q, want %q", betaCompat.State, skillset.StateEffective)
	}

	if _, err := os.Stat(env.localPath(skillset.IndexFilePath)); err != nil {
		t.Fatalf("expected derived skill index to be written: %v", err)
	}
}

func TestSkillsActionsAPIAdoptDisableEnableAndDeleteManaged(t *testing.T) {
	env := newSkillsTestEnv(t)
	externalPath := path.Join("/data/.agents/skills", "alpha", "SKILL.md")
	managedPath := path.Join(skillset.ManagedDir(), "alpha", "SKILL.md")
	env.writeSkillFile(t, externalPath, managedSkillRaw("alpha", "Compat Alpha"))

	rec, err := env.callJSON(t, http.MethodPost, "/bots/:bot_id/container/skills/actions", SkillsActionRequest{
		Action:     skillset.ActionAdopt,
		TargetPath: externalPath,
	}, env.handler.ApplySkillAction)
	if err != nil {
		t.Fatalf("adopt returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt status = %d, want 200", rec.Code)
	}
	if _, err := os.Stat(env.localPath(managedPath)); err != nil {
		t.Fatalf("expected managed skill after adopt: %v", err)
	}

	adopted := env.listSkills(t)
	adoptedManaged := mustFindSkillByPath(t, adopted, managedPath)
	if adoptedManaged.State != skillset.StateEffective {
		t.Fatalf("managed adopted skill state = %q, want %q", adoptedManaged.State, skillset.StateEffective)
	}
	adoptedCompat := mustFindSkillByPath(t, adopted, externalPath)
	if adoptedCompat.State != skillset.StateShadowed {
		t.Fatalf("compat adopted skill state = %q, want %q", adoptedCompat.State, skillset.StateShadowed)
	}

	rec, err = env.callJSON(t, http.MethodPost, "/bots/:bot_id/container/skills/actions", SkillsActionRequest{
		Action:     skillset.ActionDisable,
		TargetPath: managedPath,
	}, env.handler.ApplySkillAction)
	if err != nil {
		t.Fatalf("disable returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status = %d, want 200", rec.Code)
	}

	disabled := env.listSkills(t)
	disabledManaged := mustFindSkillByPath(t, disabled, managedPath)
	if disabledManaged.State != skillset.StateDisabled {
		t.Fatalf("managed disabled skill state = %q, want %q", disabledManaged.State, skillset.StateDisabled)
	}
	disabledCompat := mustFindSkillByPath(t, disabled, externalPath)
	if disabledCompat.State != skillset.StateEffective {
		t.Fatalf("compat fallback state = %q, want %q", disabledCompat.State, skillset.StateEffective)
	}

	rec, err = env.callJSON(t, http.MethodPost, "/bots/:bot_id/container/skills/actions", SkillsActionRequest{
		Action:     skillset.ActionEnable,
		TargetPath: managedPath,
	}, env.handler.ApplySkillAction)
	if err != nil {
		t.Fatalf("enable returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("enable status = %d, want 200", rec.Code)
	}

	reenabled := env.listSkills(t)
	if got := mustFindSkillByPath(t, reenabled, managedPath).State; got != skillset.StateEffective {
		t.Fatalf("managed state after enable = %q, want %q", got, skillset.StateEffective)
	}
	if got := mustFindSkillByPath(t, reenabled, externalPath).State; got != skillset.StateShadowed {
		t.Fatalf("compat state after enable = %q, want %q", got, skillset.StateShadowed)
	}

	rec, err = env.callJSON(t, http.MethodDelete, "/bots/:bot_id/container/skills", SkillsDeleteRequest{
		Names: []string{"alpha"},
	}, env.handler.DeleteSkills)
	if err != nil {
		t.Fatalf("DeleteSkills returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("DeleteSkills status = %d, want 200", rec.Code)
	}
	if _, err := os.Stat(env.localPath(managedPath)); !os.IsNotExist(err) {
		t.Fatalf("expected managed skill to be removed, stat err=%v", err)
	}

	deleted := env.listSkills(t)
	if len(deleted) != 1 {
		t.Fatalf("expected only compat skill after delete, got %d items", len(deleted))
	}
	if got := mustFindSkillByPath(t, deleted, externalPath).State; got != skillset.StateEffective {
		t.Fatalf("compat state after delete = %q, want %q", got, skillset.StateEffective)
	}
}

func TestDeleteSkillsAPIRejectsExternalOnlySkill(t *testing.T) {
	env := newSkillsTestEnv(t)
	env.writeSkillFile(t, path.Join("/data/.agents/skills", "alpha", "SKILL.md"), managedSkillRaw("alpha", "Compat Alpha"))

	_, err := env.callJSON(t, http.MethodDelete, "/bots/:bot_id/container/skills", SkillsDeleteRequest{
		Names: []string{"alpha"},
	}, env.handler.DeleteSkills)
	if err == nil {
		t.Fatal("expected deleting external-only skill to fail")
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected echo.HTTPError, got %T", err)
	}
	if httpErr.Code != http.StatusNotFound {
		t.Fatalf("delete external-only status = %d, want 404", httpErr.Code)
	}
}

func TestUpsertSkillsAPIRejectsTraversalName(t *testing.T) {
	env := newSkillsTestEnv(t)

	_, err := env.callJSON(t, http.MethodPost, "/bots/:bot_id/container/skills", SkillsUpsertRequest{
		Skills: []string{"---\nname: ..\ndescription: Escape\n---\n\n# Escape"},
	}, env.handler.UpsertSkills)
	if err == nil {
		t.Fatal("expected upserting traversal skill name to fail")
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected echo.HTTPError, got %T", err)
	}
	if httpErr.Code != http.StatusBadRequest {
		t.Fatalf("upsert traversal status = %d, want 400", httpErr.Code)
	}
}

func TestLoadSkillsUsesEffectiveSetAndPromptReflectsOverrideFallback(t *testing.T) {
	env := newSkillsTestEnv(t)
	managedPath := path.Join(skillset.ManagedDir(), "alpha", "SKILL.md")
	compatPath := path.Join("/data/.agents/skills", "alpha", "SKILL.md")
	env.writeSkillFile(t, managedPath, managedSkillRaw("alpha", "Managed Alpha"))
	env.writeSkillFile(t, compatPath, managedSkillRaw("alpha", "Compat Alpha"))
	env.writeSkillFile(t, path.Join("/data/.agents/skills", "beta", "SKILL.md"), managedSkillRaw("beta", "Compat Beta"))

	loaded, err := env.handler.LoadSkills(context.Background(), env.botID)
	if err != nil {
		t.Fatalf("LoadSkills returned error: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 effective skills, got %d", len(loaded))
	}
	if got := loaded[0].Name + ":" + loaded[0].Description + "|" + loaded[1].Name + ":" + loaded[1].Description; !strings.Contains(got, "alpha:Managed Alpha") {
		t.Fatalf("effective skills should include managed alpha, got %s", got)
	}
	promptBefore := promptFromLoadedSkills(loaded)
	if !strings.Contains(promptBefore, "Managed Alpha") {
		t.Fatalf("prompt should include managed alpha description:\n%s", promptBefore)
	}
	if strings.Contains(promptBefore, "Compat Alpha") {
		t.Fatalf("prompt should not include shadowed compat alpha:\n%s", promptBefore)
	}

	client, err := env.handler.manager.MCPClient(context.Background(), env.botID)
	if err != nil {
		t.Fatalf("get bridge client: %v", err)
	}
	roots, pluginRoots, err := env.handler.skillDiscoveryRoots(context.Background(), env.botID)
	if err != nil {
		t.Fatalf("resolve skill discovery roots: %v", err)
	}
	if err := skillset.ApplyActionWithPluginRoots(context.Background(), client, roots, pluginRoots, skillset.ActionRequest{
		Action:     skillset.ActionDisable,
		TargetPath: managedPath,
	}); err != nil {
		t.Fatalf("disable managed alpha via skillset.ApplyAction: %v", err)
	}

	fallback, err := env.handler.LoadSkills(context.Background(), env.botID)
	if err != nil {
		t.Fatalf("LoadSkills after disable returned error: %v", err)
	}
	if len(fallback) != 2 {
		t.Fatalf("expected 2 effective skills after disable, got %d", len(fallback))
	}
	alphaFallback := mustFindLoadedSkillByName(t, fallback, "alpha")
	if alphaFallback.Description != "Compat Alpha" {
		t.Fatalf("effective alpha description after disable = %q, want %q", alphaFallback.Description, "Compat Alpha")
	}
	promptAfter := promptFromLoadedSkills(fallback)
	if !strings.Contains(promptAfter, "Compat Alpha") {
		t.Fatalf("prompt should include compat alpha after fallback:\n%s", promptAfter)
	}
	if strings.Contains(promptAfter, "Managed Alpha") {
		t.Fatalf("prompt should not include disabled managed alpha after fallback:\n%s", promptAfter)
	}
}

func TestListSkillsAPIUsesConfiguredDiscoveryRoots(t *testing.T) {
	env := newSkillsTestEnvWithMetadata(t, map[string]any{
		"workspace": map[string]any{
			"skill_discovery_roots": []string{"/root/.openclaw/skills"},
		},
	})
	env.writeSkillFile(t, path.Join("/root/.openclaw/skills", "alpha", "SKILL.md"), managedSkillRaw("alpha", "OpenClaw Alpha"))
	env.writeSkillFile(t, path.Join("/data/.agents/skills", "beta", "SKILL.md"), managedSkillRaw("beta", "Ignored Beta"))

	skills := env.listSkills(t)
	if len(skills) != 1 {
		t.Fatalf("expected 1 configured-discovery skill, got %d", len(skills))
	}
	if got := skills[0].SourceRoot; got != "/root/.openclaw/skills" {
		t.Fatalf("source_root = %q, want %q", got, "/root/.openclaw/skills")
	}
	if got := skills[0].Name; got != "alpha" {
		t.Fatalf("skill name = %q, want %q", got, "alpha")
	}
}

func TestListSkillsAPIIncludesEnabledPluginSkillRoots(t *testing.T) {
	env := newSkillsTestEnv(t)
	pluginRoot, err := skillset.PluginSkillsDirForID("github")
	if err != nil {
		t.Fatalf("plugin skills root: %v", err)
	}
	disabledRoot, err := skillset.PluginSkillsDirForID("disabled")
	if err != nil {
		t.Fatalf("disabled plugin skills root: %v", err)
	}
	pluginPath := path.Join(pluginRoot, "review", "SKILL.md")
	env.writeSkillFile(t, pluginPath, managedSkillRaw("review", "Plugin Review"))
	env.writeSkillFile(t, path.Join(disabledRoot, "hidden", "SKILL.md"), managedSkillRaw("hidden", "Hidden Plugin"))
	env.handler.SetPluginService(fakePluginInstallationLister{items: []pluginspkg.Installation{
		{PluginID: "github", Status: pluginspkg.StatusReady, Enabled: true},
		{PluginID: "disabled", Status: pluginspkg.StatusReady, Enabled: false},
		{PluginID: "removed", Status: pluginspkg.StatusUninstalled, Enabled: true},
	}})

	skills := env.listSkills(t)
	if len(skills) != 1 {
		t.Fatalf("expected 1 plugin skill, got %d: %+v", len(skills), skills)
	}
	got := mustFindSkillByPath(t, skills, pluginPath)
	if got.SourceRoot != pluginRoot {
		t.Fatalf("source_root = %q, want %q", got.SourceRoot, pluginRoot)
	}
	if got.SourceKind != skillset.SourceKindPlugin {
		t.Fatalf("source_kind = %q, want %q", got.SourceKind, skillset.SourceKindPlugin)
	}
	if got.State != skillset.StateEffective {
		t.Fatalf("state = %q, want effective", got.State)
	}
}

type skillsTestEnv struct {
	handler  *ContainerdHandler
	dataRoot string
	botID    string
	userID   string
}

type fakePluginInstallationLister struct {
	items []pluginspkg.Installation
	err   error
}

func (f fakePluginInstallationLister) List(context.Context, string) ([]pluginspkg.Installation, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func newSkillsTestEnv(t *testing.T) *skillsTestEnv {
	return newSkillsTestEnvWithMetadata(t, nil)
}

func newSkillsTestEnvWithMetadata(t *testing.T, metadata map[string]any) *skillsTestEnv {
	t.Helper()

	dataRoot, err := newSkillsTestDataRoot()
	if err != nil {
		t.Fatalf("create temp data root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataRoot) })
	userID := "00000000-0000-0000-0000-000000000001"
	botID := "00000000-0000-0000-0000-000000000010"
	startSkillsTestBridgeServer(t, dataRoot, botID)

	cfg := config.WorkspaceConfig{DataRoot: dataRoot}
	var metadataJSON []byte
	if metadata != nil {
		var err error
		metadataJSON, err = json.Marshal(metadata)
		if err != nil {
			t.Fatalf("marshal bot metadata: %v", err)
		}
	} else {
		metadataJSON = []byte(`{}`)
	}
	cfg.DataRoot = dataRoot
	db := &skillsTestDB{userID: userID, botID: botID, metadataJSON: metadataJSON}
	queries := postgresstore.NewQueries(sqlc.New(db))
	accountStore := postgresstore.NewWithQueries(sqlc.New(db))
	manager := workspace.NewManager(slog.Default(), nil, nil, cfg, "", nil, queries)
	handler := NewContainerdHandler(
		slog.Default(),
		manager,
		cfg,
		"",
		bots.NewService(slog.Default(), queries),
		accounts.NewService(slog.Default(), accountStore),
		nil,
	)

	return &skillsTestEnv{
		handler:  handler,
		dataRoot: dataRoot,
		botID:    botID,
		userID:   userID,
	}
}

func (e *skillsTestEnv) callJSON(t *testing.T, method, routePath string, body any, fn func(echo.Context) error) (*httptest.ResponseRecorder, error) {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		bodyReader = strings.NewReader(string(data))
	}

	req := httptest.NewRequestWithContext(context.Background(), method, routePath, bodyReader)
	if body != nil {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	ctx.SetPath(routePath)
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(e.botID)
	ctx.Set("user", &jwt.Token{
		Valid:  true,
		Claims: jwt.MapClaims{"user_id": e.userID, "sub": e.userID},
	})

	return rec, fn(ctx)
}

func (e *skillsTestEnv) listSkills(t *testing.T) []SkillItem {
	t.Helper()
	rec, err := e.callJSON(t, http.MethodGet, "/bots/:bot_id/container/skills", nil, e.handler.ListSkills)
	if err != nil {
		t.Fatalf("ListSkills returned error: %v", err)
	}
	var resp SkillsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode ListSkills response: %v", err)
	}
	return resp.Skills
}

func (e *skillsTestEnv) writeSkillFile(t *testing.T, containerPath, raw string) {
	t.Helper()
	local := e.localPath(containerPath)
	if err := os.MkdirAll(filepath.Dir(local), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(local), err)
	}
	//nolint:gosec // test-only temp workspace path
	if err := os.WriteFile(local, []byte(raw), 0o600); err != nil {
		t.Fatalf("write %s: %v", local, err)
	}
}

func (e *skillsTestEnv) localPath(containerPath string) string {
	clean := path.Clean("/" + strings.TrimSpace(containerPath))
	if clean == "/" {
		return e.dataRoot
	}
	return filepath.Join(e.dataRoot, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
}

func newSkillsTestDataRoot() (string, error) {
	var lastErr error
	for _, dir := range []string{"/tmp", ""} {
		dataRoot, err := os.MkdirTemp(dir, "mh-sk-")
		if err == nil {
			return dataRoot, nil
		}
		lastErr = err
	}
	return "", lastErr
}

type skillsTestDB struct {
	userID       string
	botID        string
	metadataJSON []byte
}

func (*skillsTestDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (*skillsTestDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, nil
}

func (d *skillsTestDB) QueryRow(_ context.Context, sql string, _ ...interface{}) pgx.Row {
	switch {
	case strings.Contains(sql, "FROM users") && strings.Contains(sql, "id = $1"):
		return makeUserRow(mustParseUUID(d.userID), "user")
	case strings.Contains(sql, "FROM bots"):
		return makeBotRow(mustParseUUID(d.botID), mustParseUUID(d.userID), d.metadataJSON)
	default:
		return &skillsTestRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
	}
}

type skillsTestRow struct {
	scanFunc func(dest ...any) error
}

func (r *skillsTestRow) Scan(dest ...any) error {
	return r.scanFunc(dest...)
}

func makeUserRow(userID pgtype.UUID, role string) *skillsTestRow {
	return &skillsTestRow{
		scanFunc: func(dest ...any) error {
			if len(dest) < 14 {
				return pgx.ErrNoRows
			}
			*dest[0].(*pgtype.UUID) = userID
			*dest[1].(*pgtype.Text) = pgtype.Text{String: "owner", Valid: true}
			*dest[2].(*pgtype.Text) = pgtype.Text{}
			*dest[3].(*pgtype.Text) = pgtype.Text{}
			*dest[4].(*string) = role
			*dest[5].(*pgtype.Text) = pgtype.Text{String: "Owner", Valid: true}
			*dest[6].(*pgtype.Text) = pgtype.Text{}
			*dest[7].(*string) = "UTC"
			*dest[8].(*pgtype.Text) = pgtype.Text{}
			*dest[9].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
			*dest[10].(*bool) = true
			*dest[11].(*[]byte) = []byte(`{}`)
			*dest[12].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
			*dest[13].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
			return nil
		},
	}
}

func makeBotRow(botID, ownerUserID pgtype.UUID, metadataJSON []byte) *skillsTestRow {
	return &skillsTestRow{
		scanFunc: func(dest ...any) error {
			if len(dest) < 23 {
				return pgx.ErrNoRows
			}
			*dest[0].(*pgtype.UUID) = botID
			*dest[1].(*pgtype.UUID) = ownerUserID
			*dest[2].(*string) = "test-bot"
			*dest[3].(*pgtype.Text) = pgtype.Text{String: "test-bot", Valid: true}
			*dest[4].(*pgtype.Text) = pgtype.Text{}
			*dest[5].(*pgtype.Text) = pgtype.Text{}
			*dest[6].(*bool) = true
			*dest[7].(*string) = bots.BotStatusReady
			*dest[8].(*string) = "en"
			*dest[9].(*string) = "medium"
			*dest[10].(*pgtype.UUID) = pgtype.UUID{}
			*dest[11].(*pgtype.UUID) = pgtype.UUID{}
			*dest[12].(*pgtype.UUID) = pgtype.UUID{}
			*dest[13].(*bool) = false
			*dest[14].(*int32) = 30
			*dest[15].(*string) = ""
			*dest[16].(*bool) = false
			*dest[17].(*int32) = 100000
			*dest[18].(*pgtype.Int4) = pgtype.Int4{}
			*dest[19].(*pgtype.UUID) = pgtype.UUID{}
			*dest[20].(*[]byte) = metadataJSON
			*dest[21].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
			*dest[22].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
			return nil
		},
	}
}

func mustParseUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		panic(err)
	}
	return u
}

type skillsTestBridgeServer struct {
	pb.UnimplementedContainerServiceServer
	root string
}

func startSkillsTestBridgeServer(t *testing.T, dataRoot, botID string) {
	t.Helper()

	socketPath := filepath.Join(dataRoot, "run", botID, "bridge.sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}

	srv := grpc.NewServer()
	pb.RegisterContainerServiceServer(srv, &skillsTestBridgeServer{root: dataRoot})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(lis)
	}()

	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
		<-done
	})
}

func (s *skillsTestBridgeServer) ListDir(_ context.Context, req *pb.ListDirRequest) (*pb.ListDirResponse, error) {
	_, localPath := s.resolvePath(req.GetPath())
	var resp []*pb.FileEntry
	if req.GetRecursive() {
		err := filepath.WalkDir(localPath, func(current string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, err := filepath.Rel(localPath, current)
			if err != nil || rel == "." {
				return nil
			}
			fileEntry, err := skillsTestFileEntry(filepath.ToSlash(rel), entry)
			if err != nil {
				return err
			}
			resp = append(resp, fileEntry)
			return nil
		})
		if err != nil {
			return nil, toStatusError(err, req.GetPath())
		}
	} else {
		entries, err := os.ReadDir(localPath)
		if err != nil {
			return nil, toStatusError(err, req.GetPath())
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		resp = make([]*pb.FileEntry, 0, len(entries))
		for _, entry := range entries {
			fileEntry, err := skillsTestFileEntry(entry.Name(), entry)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "stat %s: %v", entry.Name(), err)
			}
			resp = append(resp, fileEntry)
		}
	}
	if len(resp) > 1<<31-1 {
		return nil, status.Error(codes.Internal, "too many entries")
	}
	//nolint:gosec // len(resp) is bounds-checked just above
	totalCount := int32(len(resp))
	return &pb.ListDirResponse{
		Entries:    resp,
		TotalCount: totalCount,
	}, nil
}

func skillsTestFileEntry(entryPath string, entry fs.DirEntry) (*pb.FileEntry, error) {
	info, err := entry.Info()
	if err != nil {
		return nil, err
	}
	return &pb.FileEntry{
		Path:    entryPath,
		IsDir:   entry.IsDir(),
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		ModTime: info.ModTime().UTC().Format(time.RFC3339),
	}, nil
}

func (s *skillsTestBridgeServer) ReadRaw(req *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	_, localPath := s.resolvePath(req.GetPath())
	//nolint:gosec // test-only temp workspace path
	data, err := os.ReadFile(localPath)
	if err != nil {
		return toStatusError(err, req.GetPath())
	}
	if len(data) == 0 {
		return nil
	}
	return stream.Send(&pb.DataChunk{Data: data})
}

func (s *skillsTestBridgeServer) WriteRaw(stream pb.ContainerService_WriteRawServer) error {
	var containerPath string
	var data []byte
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if containerPath == "" {
			containerPath = chunk.GetPath()
		}
		data = append(data, chunk.GetData()...)
	}
	if strings.TrimSpace(containerPath) == "" {
		return status.Error(codes.InvalidArgument, "path is required")
	}
	_, localPath := s.resolvePath(containerPath)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o750); err != nil {
		return status.Errorf(codes.Internal, "mkdir parent for %s: %v", containerPath, err)
	}
	//nolint:gosec // test-only temp workspace path
	if err := os.WriteFile(localPath, data, 0o600); err != nil {
		return status.Errorf(codes.Internal, "write %s: %v", containerPath, err)
	}
	return stream.SendAndClose(&pb.WriteRawResponse{BytesWritten: int64(len(data))})
}

func (s *skillsTestBridgeServer) WriteFile(_ context.Context, req *pb.WriteFileRequest) (*pb.WriteFileResponse, error) {
	_, localPath := s.resolvePath(req.GetPath())
	if err := os.MkdirAll(filepath.Dir(localPath), 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir parent for %s: %v", req.GetPath(), err)
	}
	//nolint:gosec // test-only temp workspace path
	if err := os.WriteFile(localPath, req.GetContent(), 0o600); err != nil {
		return nil, status.Errorf(codes.Internal, "write %s: %v", req.GetPath(), err)
	}
	return &pb.WriteFileResponse{}, nil
}

func (s *skillsTestBridgeServer) Stat(_ context.Context, req *pb.StatRequest) (*pb.StatResponse, error) {
	containerPath, localPath := s.resolvePath(req.GetPath())
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, toStatusError(err, req.GetPath())
	}
	return &pb.StatResponse{Entry: &pb.FileEntry{
		Path:    containerPath,
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		ModTime: info.ModTime().UTC().Format(time.RFC3339),
	}}, nil
}

func (s *skillsTestBridgeServer) Mkdir(_ context.Context, req *pb.MkdirRequest) (*pb.MkdirResponse, error) {
	_, localPath := s.resolvePath(req.GetPath())
	if err := os.MkdirAll(localPath, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir %s: %v", req.GetPath(), err)
	}
	return &pb.MkdirResponse{}, nil
}

func (s *skillsTestBridgeServer) DeleteFile(_ context.Context, req *pb.DeleteFileRequest) (*pb.DeleteFileResponse, error) {
	_, localPath := s.resolvePath(req.GetPath())
	if _, err := os.Stat(localPath); err != nil {
		return nil, toStatusError(err, req.GetPath())
	}
	var err error
	if req.GetRecursive() {
		err = os.RemoveAll(localPath)
	} else {
		err = os.Remove(localPath)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "delete %s: %v", req.GetPath(), err)
	}
	return &pb.DeleteFileResponse{}, nil
}

func (s *skillsTestBridgeServer) resolvePath(containerPath string) (string, string) {
	clean := path.Clean("/" + strings.TrimSpace(containerPath))
	if clean == "/" {
		return clean, s.root
	}
	return clean, filepath.Join(s.root, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
}

func toStatusError(err error, containerPath string) error {
	if os.IsNotExist(err) {
		return status.Errorf(codes.NotFound, "path not found: %s", containerPath)
	}
	if os.IsPermission(err) {
		return status.Errorf(codes.PermissionDenied, "permission denied: %s", containerPath)
	}
	return status.Errorf(codes.Internal, "%v", err)
}

func mustFindSkillByPath(t *testing.T, items []SkillItem, sourcePath string) SkillItem {
	t.Helper()
	for _, item := range items {
		if item.SourcePath == sourcePath {
			return item
		}
	}
	t.Fatalf("skill with source path %q not found in %+v", sourcePath, items)
	return SkillItem{}
}

func mustFindLoadedSkillByName(t *testing.T, items []SkillItem, name string) SkillItem {
	t.Helper()
	for _, item := range items {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("loaded skill %q not found in %+v", name, items)
	return SkillItem{}
}

func promptFromLoadedSkills(items []SkillItem) string {
	skills := make([]native.SkillEntry, 0, len(items))
	for _, item := range items {
		skills = append(skills, native.SkillEntry{
			Name:        item.Name,
			Description: item.Description,
			Content:     item.Content,
			Metadata:    item.Metadata,
		})
	}
	return native.GenerateSystemPrompt(native.SystemPromptParams{
		SessionType: "chat",
		Skills:      skills,
		Timezone:    "UTC",
	})
}

func managedSkillRaw(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + description + "\n"
}
