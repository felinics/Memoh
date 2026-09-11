package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workspacedeps"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

const (
	depsTestBotID   = "11111111-1111-1111-1111-111111111111"
	depsTestOwnerID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	depsTestOtherID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

// depsAuthQueries answers the bot lookup and reports no user grants, so only
// the owner and admins pass the manage check.
type depsAuthQueries struct {
	dbstore.Queries
	bot sqlc.GetBotByIDRow
}

func (q depsAuthQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (depsAuthQueries) ListBotUserGrantsForUser(context.Context, sqlc.ListBotUserGrantsForUserParams) ([]sqlc.ListBotUserGrantsForUserRow, error) {
	return nil, nil
}

// fakeWorkspaceDependencyService records the arguments of every call and
// replays canned results.
type fakeWorkspaceDependencyService struct {
	deps map[string]catalog.Dependency

	list       workspacedeps.ListResult
	listErr    error
	preflight  workspacedeps.PreflightResult
	operation  workspacedeps.OperationResult
	opErr      error
	beforeRun  func(context.Context)
	opLogs     [][2]string
	preview    workspacedeps.ScriptPreview
	previewErr error

	calls     []string
	targetIDs []string
	versions  []string
	depIDs    [][]string
	actions   []catalog.Action
}

func (f *fakeWorkspaceDependencyService) Refresh(ctx context.Context, botID, targetID string) (workspacedeps.ListResult, error) {
	return f.List(ctx, botID, targetID)
}

func (*fakeWorkspaceDependencyService) Icon(context.Context, string) ([]byte, error) {
	return nil, workspacedeps.ErrDependencyNotFound
}

func (f *fakeWorkspaceDependencyService) record(name, targetID string) {
	f.calls = append(f.calls, name)
	f.targetIDs = append(f.targetIDs, targetID)
}

func (f *fakeWorkspaceDependencyService) Dependency(_ context.Context, depID string) (catalog.Dependency, error) {
	dep, ok := f.deps[depID]
	if !ok {
		return dep, workspacedeps.ErrDependencyNotFound
	}
	return dep, nil
}

// Catalog returns the fixture dependencies by id, the order the real catalog
// keeps.
func (f *fakeWorkspaceDependencyService) Catalog(_ context.Context, _ bool) (workspacedeps.CatalogView, error) {
	f.record("catalog", "")
	ids := make([]string, 0, len(f.deps))
	for id := range f.deps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	deps := make([]catalog.Dependency, 0, len(ids))
	for _, id := range ids {
		deps = append(deps, f.deps[id])
	}
	return workspacedeps.CatalogView{Items: deps}, nil
}

func (f *fakeWorkspaceDependencyService) List(_ context.Context, _, targetID string) (workspacedeps.ListResult, error) {
	f.record("list", targetID)
	return f.list, f.listErr
}

func (f *fakeWorkspaceDependencyService) Preflight(_ context.Context, _, targetID string, depIDs []string) (workspacedeps.PreflightResult, error) {
	f.record("preflight", targetID)
	f.depIDs = append(f.depIDs, depIDs)
	return f.preflight, nil
}

func (f *fakeWorkspaceDependencyService) run(ctx context.Context, name, targetID string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error) {
	f.record(name, targetID)
	if f.beforeRun != nil {
		f.beforeRun(ctx)
	}
	for _, line := range f.opLogs {
		sink.Log(line[0], line[1])
	}
	if err := ctx.Err(); err != nil {
		return workspacedeps.OperationResult{}, err
	}
	return f.operation, f.opErr
}

func (f *fakeWorkspaceDependencyService) Install(ctx context.Context, _, targetID, _, version string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error) {
	f.versions = append(f.versions, version)
	return f.run(ctx, "install", targetID, sink)
}

func (f *fakeWorkspaceDependencyService) Update(ctx context.Context, _, targetID, _, version string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error) {
	f.versions = append(f.versions, version)
	return f.run(ctx, "update", targetID, sink)
}

func (f *fakeWorkspaceDependencyService) Reinstall(ctx context.Context, _, targetID, _, version string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error) {
	f.versions = append(f.versions, version)
	return f.run(ctx, "reinstall", targetID, sink)
}

func (f *fakeWorkspaceDependencyService) Remove(ctx context.Context, _, targetID, _ string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error) {
	return f.run(ctx, "remove", targetID, sink)
}

func (f *fakeWorkspaceDependencyService) Rollback(_ context.Context, _, targetID, _ string) (workspacedeps.OperationResult, error) {
	f.record("rollback", targetID)
	return f.operation, f.opErr
}

func (f *fakeWorkspaceDependencyService) CheckUpdates(_ context.Context, _, targetID string) (workspacedeps.ListResult, error) {
	f.record("check_updates", targetID)
	return f.list, f.listErr
}

func (f *fakeWorkspaceDependencyService) ScriptPreviewDetails(_ context.Context, _, targetID, depID string, action catalog.Action) (workspacedeps.ScriptPreview, error) {
	if _, ok := f.deps[depID]; !ok {
		return workspacedeps.ScriptPreview{}, workspacedeps.ErrDependencyNotFound
	}
	if !workspacedeps.ActionSupported(f.deps[depID], action) {
		return workspacedeps.ScriptPreview{}, workspacedeps.ErrActionUnsupported
	}
	f.record("script", targetID)
	f.actions = append(f.actions, action)
	return f.preview, f.previewErr
}

func depsTestCatalog() map[string]catalog.Dependency {
	return map[string]catalog.Dependency{
		"codex": {
			ID: "codex", Name: "Codex", Description: "OpenAI Codex CLI", Icon: "openai",
			Category: catalog.CategoryAgent, Source: catalog.SourceManaged, Provides: []string{"codex"},
			Platforms: []catalog.Platform{
				{OS: "linux", Arch: []string{"amd64", "arm64"}, Libc: "glibc"},
				{OS: "darwin", Arch: []string{"arm64"}},
			},
			Scripts: catalog.Scripts{Install: "install.sh", Update: "update.sh", Remove: "remove.sh", CheckUpdate: "check-update.sh"},
		},
		// node ships with the image and has no scripts: nothing can be
		// installed over it or removed from it.
		"node": {
			ID: "node", Name: "Node.js", Category: catalog.CategoryRuntime, Source: catalog.SourceImage,
			Provides: []string{"node", "npm", "npx"},
		},
		// python ships with the image but can take a managed overlay.
		"python": {
			ID: "python", Name: "Python", Category: catalog.CategoryRuntime, Source: catalog.SourceImage,
			Provides: []string{"python3", "pip3"},
			Scripts:  catalog.Scripts{Install: "install.sh", Remove: "remove.sh"},
		},
		// ripgrep is pinned: installs always produce 14.1.0.
		"ripgrep": {
			ID: "ripgrep", Name: "ripgrep", Category: catalog.CategoryTool, Source: catalog.SourceManaged,
			Provides: []string{"rg"},
			Version:  catalog.VersionSpec{Pin: "14.1.0"},
			Scripts:  catalog.Scripts{Install: "install.sh", Remove: "remove.sh"},
		},
	}
}

func newDepsTestHandler(role string, svc workspaceDependencyService) *ContainerdHandler {
	botRow := testBotRow(depsTestBotID, map[string]any{})
	botRow.OwnerUserID = testUUID(depsTestOwnerID)
	return &ContainerdHandler{
		logger:         slog.New(slog.DiscardHandler),
		botService:     bots.NewService(nil, depsAuthQueries{bot: botRow}),
		accountService: accounts.NewService(nil, testAdminAccountStore{role: role}),
		workspaceDeps:  svc,
	}
}

type depsCall struct {
	method         string
	target         string
	depID          string
	body           any
	userID         string
	omitRevision   bool
	requestContext context.Context
}

func (call depsCall) invoke(t *testing.T, fn func(echo.Context) error) (*httptest.ResponseRecorder, error) {
	t.Helper()
	if !call.omitRevision && call.depID != "" && (call.method == http.MethodPost || call.method == http.MethodDelete) && !strings.Contains(call.target, "/rollback") {
		switch body := call.body.(type) {
		case nil:
			call.body = WorkspaceDependencyInstallRequest{DefinitionRevision: strings.Repeat("a", 64)}
		case WorkspaceDependencyInstallRequest:
			if body.DefinitionRevision == "" {
				body.DefinitionRevision = strings.Repeat("a", 64)
				call.body = body
			}
		}
	}
	var body *strings.Reader
	if call.body != nil {
		data, err := json.Marshal(call.body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		body = strings.NewReader(string(data))
	} else {
		body = strings.NewReader("")
	}
	requestContext := call.requestContext
	if requestContext == nil {
		requestContext = context.Background()
	}
	req := httptest.NewRequestWithContext(requestContext, call.method, call.target, body)
	if call.body != nil {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	names := []string{"bot_id"}
	values := []string{depsTestBotID}
	if call.depID != "" {
		names = append(names, "dep_id")
		values = append(values, call.depID)
	}
	ctx.SetParamNames(names...)
	ctx.SetParamValues(values...)
	userID := call.userID
	if userID == "" {
		userID = depsTestOwnerID
	}
	ctx.Set("user", &jwt.Token{Valid: true, Claims: jwt.MapClaims{"user_id": userID, "sub": userID}})
	return rec, fn(ctx)
}

func requireAppErrorCode(t *testing.T, err error, want apperror.Code) {
	t.Helper()
	if got := apperror.CodeOf(err); got != want {
		t.Fatalf("error = %v (code %q), want code %q", err, got, want)
	}
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %T: %v\n%s", out, err, rec.Body.String())
	}
	return out
}

// sseFrames decodes every data frame of an SSE body as a generic object;
// comment frames (heartbeats) are skipped.
func sseFrames(t *testing.T, body string) []map[string]any {
	t.Helper()
	var frames []map[string]any
	for _, chunk := range strings.Split(body, "\n\n") {
		for _, line := range strings.Split(chunk, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var frame map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
				t.Fatalf("decode frame %q: %v", line, err)
			}
			frames = append(frames, frame)
		}
	}
	return frames
}

func TestListWorkspaceDependenciesMapsEntries(t *testing.T) {
	deps := depsTestCatalog()
	checked := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	svc := &fakeWorkspaceDependencyService{
		deps: deps,
		list: workspacedeps.ListResult{
			Workspace: workspacedeps.WorkspaceRunning,
			Platform:  workspacedeps.Platform{OS: "linux", Arch: "arm64", Libc: "glibc"},
			DataRoot:  "/data",
			Entries: []workspacedeps.Entry{
				{
					Dependency: deps["codex"],
					Installation: &workspacedeps.Installation{
						Status: workspacedeps.StatusInstalled, LastCheckedAt: &checked, LastError: "",
					},
					Observed: workspacedeps.Observed{
						Present: true, Source: workspacedeps.SourceManaged, Version: "0.147.0",
						State: &workspacedeps.State{Version: "0.147.0", PreviousVersion: "0.140.0"},
					},
					Status: workspacedeps.StatusInstalled, InstalledVersion: "0.147.0",
					LatestVersion: "0.151.0", UpdateAvailable: true, PlatformSupported: true,
					Actions: []catalog.Action{catalog.ActionUpdate, catalog.ActionReinstall, catalog.ActionRemove, workspacedeps.ActionRollback},
				},
				{
					Dependency:        deps["node"],
					Observed:          workspacedeps.Observed{Present: true, Source: workspacedeps.SourceToolkit, Version: "22.1.0", Command: "/opt/memoh/toolkit/bin/node"},
					Installation:      &workspacedeps.Installation{Status: workspacedeps.StatusInstalled},
					Status:            workspacedeps.StatusInstalled,
					InstalledVersion:  "22.1.0",
					ImageVersion:      "22.1.0",
					PlatformSupported: true,
				},
				{
					// Neither recorded nor discovered: status must be omitted.
					Dependency:        deps["ripgrep"],
					PlatformSupported: false,
					Actions:           []catalog.Action{catalog.ActionInstall},
				},
				{
					// A managed overlay over the image copy.
					Dependency:        deps["python"],
					Observed:          workspacedeps.Observed{Present: true, Source: workspacedeps.SourceManaged, Version: "3.13.2", Command: "/data/.memoh/deps/python/current/bin/python3"},
					Installation:      &workspacedeps.Installation{Status: workspacedeps.StatusInstalled},
					Status:            workspacedeps.StatusInstalled,
					InstalledVersion:  "3.13.2",
					ImageVersion:      "3.12.3",
					Overlay:           true,
					PlatformSupported: true,
					Actions:           []catalog.Action{catalog.ActionUpdate, catalog.ActionReinstall, catalog.ActionRemove},
				},
			},
		},
	}
	h := newDepsTestHandler("admin", svc)
	rec, err := depsCall{method: http.MethodGet, target: "/bots/x/dependencies"}.invoke(t, h.ListWorkspaceDependencies)
	if err != nil {
		t.Fatalf("ListWorkspaceDependencies: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	raw := decodeJSON[map[string]any](t, rec)
	if raw["workspace_state"] != "running" {
		t.Errorf("workspace_state = %v", raw["workspace_state"])
	}
	platform, _ := raw["platform"].(map[string]any)
	if platform["os"] != "linux" || platform["arch"] != "arm64" || platform["libc"] != "glibc" {
		t.Errorf("platform = %v", raw["platform"])
	}
	items, _ := raw["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("items = %v", raw["items"])
	}
	codex := items[0].(map[string]any)
	if codex["id"] != "codex" || codex["category"] != "agent" || codex["source"] != "managed" || codex["icon"] != "openai" {
		t.Errorf("codex identity = %v", codex)
	}
	if codex["status"] != "installed" || codex["installed_version"] != "0.147.0" || codex["latest_version"] != "0.151.0" || codex["update_available"] != true {
		t.Errorf("codex versions = %v", codex)
	}
	for _, gone := range []string{"required_version", "needs_alignment", "image_version", "overlay"} {
		if _, ok := codex[gone]; ok {
			t.Errorf("codex must not carry %s: %v", gone, codex)
		}
	}
	if codex["previous_version"] != "0.140.0" {
		t.Errorf("codex previous_version = %v", codex["previous_version"])
	}
	if codex["install_path"] != "/data/.memoh/deps/codex" {
		t.Errorf("codex install_path = %v", codex["install_path"])
	}
	if codex["last_checked_at"] == nil {
		t.Errorf("codex last_checked_at missing: %v", codex)
	}
	if actions, _ := codex["actions"].([]any); len(actions) != 4 || actions[3] != "rollback" {
		t.Errorf("codex actions = %v", codex["actions"])
	}
	if _, ok := codex["last_error"]; ok {
		t.Errorf("empty last_error must be omitted: %v", codex)
	}
	node := items[1].(map[string]any)
	if node["source"] != "image" || node["install_path"] != "/opt/memoh/toolkit/bin/node" || node["image_version"] != "22.1.0" {
		t.Errorf("node = %v", node)
	}
	if _, ok := node["overlay"]; ok {
		t.Errorf("image copy in effect must not report an overlay: %v", node)
	}
	if actions, _ := node["actions"].([]any); actions == nil || len(actions) != 0 {
		t.Errorf("image dependency actions = %v, want []", node["actions"])
	}
	python := items[3].(map[string]any)
	if python["source"] != "image" || python["overlay"] != true || python["installed_version"] != "3.13.2" || python["image_version"] != "3.12.3" {
		t.Errorf("python overlay = %v", python)
	}
	if python["install_path"] != "/data/.memoh/deps/python" {
		t.Errorf("python install_path = %v, want the managed home while the overlay is in effect", python["install_path"])
	}
	ripgrep := items[2].(map[string]any)
	if _, ok := ripgrep["status"]; ok {
		t.Errorf("zero status must be omitted: %v", ripgrep)
	}
	if ripgrep["platform_supported"] != false || ripgrep["platform_reason"] != "unsupported_platform" {
		t.Errorf("ripgrep platform = %v", ripgrep)
	}
	if ripgrep["install_path"] != "/data/.memoh/deps/ripgrep" {
		t.Errorf("ripgrep install_path = %v", ripgrep["install_path"])
	}
	if _, ok := ripgrep["previous_version"]; ok {
		t.Errorf("previous_version must be omitted without state: %v", ripgrep)
	}
}

func TestListWorkspaceDependenciesOmitsPlatformWhenUnknown(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog(), list: workspacedeps.ListResult{Workspace: workspacedeps.WorkspaceNotRunning}}
	h := newDepsTestHandler("admin", svc)
	rec, err := depsCall{method: http.MethodGet, target: "/bots/x/dependencies"}.invoke(t, h.ListWorkspaceDependencies)
	if err != nil {
		t.Fatalf("ListWorkspaceDependencies: %v", err)
	}
	raw := decodeJSON[map[string]any](t, rec)
	if raw["workspace_state"] != "not_running" {
		t.Errorf("workspace_state = %v", raw["workspace_state"])
	}
	if _, ok := raw["platform"]; ok {
		t.Errorf("platform must be omitted when unknown: %v", raw)
	}
	if items, ok := raw["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %v, want []", raw["items"])
	}
}

// TestListWorkspaceDependenciesReportsDiscoveryError covers the degraded
// list: a running workspace whose discovery failed still answers 200 with the
// records and names the problem in discovery_error, which is omitted when
// discovery worked.
func TestListWorkspaceDependenciesReportsDiscoveryError(t *testing.T) {
	deps := depsTestCatalog()
	svc := &fakeWorkspaceDependencyService{
		deps: deps,
		list: workspacedeps.ListResult{
			Workspace:      workspacedeps.WorkspaceRunning,
			DataRoot:       "/data",
			DiscoveryError: "workspacedeps: discovery script exited 137 before finishing",
			Entries: []workspacedeps.Entry{{
				Dependency:        deps["codex"],
				Installation:      &workspacedeps.Installation{Status: workspacedeps.StatusFailed, LastError: "operation interrupted"},
				Status:            workspacedeps.StatusFailed,
				PlatformSupported: true,
			}},
		},
	}
	h := newDepsTestHandler("admin", svc)
	rec, err := depsCall{method: http.MethodGet, target: "/bots/x/dependencies"}.invoke(t, h.ListWorkspaceDependencies)
	if err != nil {
		t.Fatalf("ListWorkspaceDependencies: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 despite the discovery failure", rec.Code)
	}
	raw := decodeJSON[map[string]any](t, rec)
	if raw["workspace_state"] != "running" || raw["discovery_error"] != string(apperror.CodeWorkspaceDependencyDiscoveryFailed) {
		t.Errorf("response = %v", raw)
	}
	if _, ok := raw["platform"]; ok {
		t.Errorf("platform must be omitted when unknown: %v", raw)
	}
	items, _ := raw["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", raw["items"])
	}
	codex := items[0].(map[string]any)
	if codex["status"] != "failed" || codex["last_error_code"] != string(apperror.CodeWorkspaceDependencyOperationFailed) {
		t.Errorf("codex = %v", codex)
	}
	if actions, _ := codex["actions"].([]any); actions == nil || len(actions) != 0 {
		t.Errorf("actions = %v, want [] without facts", codex["actions"])
	}

	svc.list.DiscoveryError = ""
	rec, err = depsCall{method: http.MethodGet, target: "/bots/x/dependencies"}.invoke(t, h.ListWorkspaceDependencies)
	if err != nil {
		t.Fatalf("ListWorkspaceDependencies: %v", err)
	}
	if raw := decodeJSON[map[string]any](t, rec); raw["discovery_error"] != nil {
		t.Errorf("discovery_error must be omitted when discovery worked: %v", raw)
	}
}

func TestListWorkspaceDependenciesPassesWorkspaceTarget(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog()}
	h := newDepsTestHandler("admin", svc)
	if _, err := (depsCall{method: http.MethodGet, target: "/bots/x/dependencies?workspace_target_id=remote-1"}).invoke(t, h.ListWorkspaceDependencies); err != nil {
		t.Fatalf("ListWorkspaceDependencies: %v", err)
	}
	if _, err := (depsCall{method: http.MethodGet, target: "/bots/x/dependencies"}).invoke(t, h.ListWorkspaceDependencies); err != nil {
		t.Fatalf("ListWorkspaceDependencies (default target): %v", err)
	}
	if len(svc.targetIDs) != 2 || svc.targetIDs[0] != "remote-1" || svc.targetIDs[1] != workspacedeps.TargetNative {
		t.Fatalf("target ids = %v", svc.targetIDs)
	}
}

func TestListWorkspaceDependenciesMapsServiceErrors(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog(), listErr: workspacedeps.ErrRemoteOffline}
	h := newDepsTestHandler("admin", svc)
	_, err := depsCall{method: http.MethodGet, target: "/bots/x/dependencies"}.invoke(t, h.ListWorkspaceDependencies)
	requireAppErrorCode(t, err, apperror.CodeWorkspaceDependencyRemoteOffline)

	svc.listErr = errors.New("boom")
	_, err = depsCall{method: http.MethodGet, target: "/bots/x/dependencies"}.invoke(t, h.ListWorkspaceDependencies)
	requireAppErrorCode(t, err, apperror.CodeWorkspaceDependencyOperationFailed)
}

func TestWorkspaceDependencyRoutesRequireManagePermission(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog()}
	h := newDepsTestHandler("user", svc)
	_, err := depsCall{method: http.MethodGet, target: "/bots/x/dependencies", userID: depsTestOtherID}.invoke(t, h.ListWorkspaceDependencies)
	requireForbidden(t, err)
	_, err = depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/update", depID: "codex", userID: depsTestOtherID}.invoke(t, h.UpdateWorkspaceDependency)
	requireForbidden(t, err)
	if len(svc.calls) != 0 {
		t.Fatalf("service must not be called without permission: %v", svc.calls)
	}
}

func TestWorkspaceDependencyRoutesNeedService(t *testing.T) {
	h := newDepsTestHandler("admin", nil)
	_, err := depsCall{method: http.MethodGet, target: "/bots/x/dependencies"}.invoke(t, h.ListWorkspaceDependencies)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusServiceUnavailable {
		t.Fatalf("error = %v, want HTTP 503", err)
	}
}

func TestPreflightWorkspaceDependenciesStates(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{
		deps: depsTestCatalog(),
		preflight: workspacedeps.PreflightResult{
			Workspace: workspacedeps.WorkspaceRunning,
			Items: []workspacedeps.PreflightItem{
				{DependencyID: "codex", Name: "Codex", Satisfied: true, InstalledVersion: "0.151.0"},
				{DependencyID: "claude-code", Name: "Claude Code", Reason: workspacedeps.PreflightReasonMissing},
				{DependencyID: "mac-only", Name: "Mac Only", Reason: workspacedeps.PreflightReasonPlatformUnsupported},
				{DependencyID: "nope", Reason: workspacedeps.PreflightReasonUnknownDependency},
			},
		},
	}
	h := newDepsTestHandler("admin", svc)
	body := WorkspaceDependencyPreflightRequest{DependencyIDs: []string{" codex ", "claude-code", "", "mac-only", "nope"}, WorkspaceTargetID: "remote-2"}
	rec, err := depsCall{method: http.MethodPost, target: "/bots/x/dependencies/preflight", body: body}.invoke(t, h.PreflightWorkspaceDependencies)
	if err != nil {
		t.Fatalf("PreflightWorkspaceDependencies: %v", err)
	}
	resp := decodeJSON[WorkspaceDependencyPreflightResponse](t, rec)
	if resp.WorkspaceState != "running" || len(resp.Items) != 4 {
		t.Fatalf("response = %+v", resp)
	}
	want := map[string]string{"codex": "satisfied", "claude-code": "missing", "mac-only": "platform_unsupported", "nope": "unknown_dependency"}
	for _, item := range resp.Items {
		if item.State != want[item.DependencyID] {
			t.Errorf("%s state = %q, want %q", item.DependencyID, item.State, want[item.DependencyID])
		}
	}
	if resp.Items[0].Name != "Codex" || resp.Items[0].InstalledVersion != "0.151.0" {
		t.Errorf("satisfied item = %+v", resp.Items[0])
	}
	if strings.Contains(rec.Body.String(), "required_version") {
		t.Errorf("preflight response still carries required_version: %s", rec.Body.String())
	}
	if len(svc.depIDs) != 1 || strings.Join(svc.depIDs[0], ",") != "codex,claude-code,mac-only,nope" {
		t.Errorf("dependency ids passed = %v", svc.depIDs)
	}
	if svc.targetIDs[0] != "remote-2" {
		t.Errorf("target from body = %q", svc.targetIDs[0])
	}
}

func TestPreflightWorkspaceDependenciesWorkspaceNotRunning(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog(), preflight: workspacedeps.PreflightResult{Workspace: workspacedeps.WorkspaceMissing}}
	h := newDepsTestHandler("admin", svc)
	rec, err := depsCall{method: http.MethodPost, target: "/bots/x/dependencies/preflight", body: WorkspaceDependencyPreflightRequest{DependencyIDs: []string{"codex"}}}.invoke(t, h.PreflightWorkspaceDependencies)
	if err != nil {
		t.Fatalf("PreflightWorkspaceDependencies: %v", err)
	}
	raw := decodeJSON[map[string]any](t, rec)
	if raw["workspace_state"] != "missing" {
		t.Errorf("workspace_state = %v", raw["workspace_state"])
	}
	if items, ok := raw["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %v, want []", raw["items"])
	}
}

func TestPreflightWorkspaceDependenciesRejectsEmptyRequest(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog()}
	h := newDepsTestHandler("admin", svc)
	_, err := depsCall{method: http.MethodPost, target: "/bots/x/dependencies/preflight", body: WorkspaceDependencyPreflightRequest{DependencyIDs: []string{" "}}}.invoke(t, h.PreflightWorkspaceDependencies)
	requireAppErrorCode(t, err, apperror.CodeWorkspaceDependencyRequestInvalid)
	if len(svc.calls) != 0 {
		t.Fatalf("service called for an invalid request: %v", svc.calls)
	}
}

func TestUpdateWorkspaceDependencyStreamsEvents(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{
		deps:   depsTestCatalog(),
		opLogs: [][2]string{{"stderr", "updating codex"}, {"stdout", ""}, {"stdout", "done"}},
		operation: workspacedeps.OperationResult{
			DependencyID: "codex", Action: catalog.ActionUpdate, Version: "0.151.0",
			Entrypoints: map[string]string{"codex": "/data/.memoh/deps/codex/current/bin/codex"},
		},
	}
	h := newDepsTestHandler("admin", svc)
	body := WorkspaceDependencyInstallRequest{Version: " 0.151.0 "}
	rec, err := depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/update?workspace_target_id=remote-3", depID: "codex", body: body}.invoke(t, h.UpdateWorkspaceDependency)
	if err != nil {
		t.Fatalf("UpdateWorkspaceDependency: %v", err)
	}
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get(echo.HeaderContentType), "text/event-stream") {
		t.Fatalf("status = %d, content-type = %q", rec.Code, rec.Header().Get(echo.HeaderContentType))
	}
	frames := sseFrames(t, rec.Body.String())
	if len(frames) != 5 {
		t.Fatalf("frames = %v", frames)
	}
	// started echoes the requested version; the service receives it trimmed.
	if frames[0]["type"] != "started" || frames[0]["dependency_id"] != "codex" || frames[0]["version"] != "0.151.0" {
		t.Errorf("started = %v", frames[0])
	}
	if len(svc.versions) != 1 || svc.versions[0] != "0.151.0" {
		t.Errorf("versions passed = %q", svc.versions)
	}
	if frames[1]["type"] != "log" || frames[1]["stream"] != "stderr" || frames[1]["data"] != "updating codex" {
		t.Errorf("log 1 = %v", frames[1])
	}
	if data, ok := frames[2]["data"]; !ok || data != "" {
		t.Errorf("empty log line must keep its data field: %v", frames[2])
	}
	if frames[4]["type"] != "done" || frames[4]["version"] != "0.151.0" {
		t.Errorf("done = %v", frames[4])
	}
	if entrypoints, _ := frames[4]["entrypoints"].(map[string]any); entrypoints["codex"] != "/data/.memoh/deps/codex/current/bin/codex" {
		t.Errorf("done entrypoints = %v", frames[4]["entrypoints"])
	}
	if len(svc.calls) != 2 || svc.calls[0] != "script" || svc.calls[1] != "update" || svc.targetIDs[1] != "remote-3" {
		t.Errorf("service call = %v %v", svc.calls, svc.targetIDs)
	}

	// No body means latest: started carries no version and the service gets
	// an empty one. A body naming no version is the same.
	for _, call := range []depsCall{
		{method: http.MethodPost, target: "/bots/x/dependencies/codex/update", depID: "codex"},
		{method: http.MethodPost, target: "/bots/x/dependencies/codex/reinstall", depID: "codex", body: WorkspaceDependencyInstallRequest{}},
	} {
		rec, err := call.invoke(t, h.UpdateWorkspaceDependency)
		if err != nil {
			t.Fatalf("%s: %v", call.target, err)
		}
		started := sseFrames(t, rec.Body.String())[0]
		if _, ok := started["version"]; ok || started["type"] != "started" {
			t.Errorf("%s started = %v, want no version", call.target, started)
		}
	}
	if len(svc.versions) != 3 || svc.versions[1] != "" || svc.versions[2] != "" {
		t.Errorf("versions passed = %q", svc.versions)
	}

	// A malformed body is rejected before the stream opens.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/bots/x/dependencies/codex/update", strings.NewReader("{"))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec = httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	ctx.SetParamNames("bot_id", "dep_id")
	ctx.SetParamValues(depsTestBotID, "codex")
	ctx.Set("user", &jwt.Token{Valid: true, Claims: jwt.MapClaims{"user_id": depsTestOwnerID, "sub": depsTestOwnerID}})
	requireAppErrorCode(t, h.UpdateWorkspaceDependency(ctx), apperror.CodeWorkspaceDependencyRequestInvalid)
	if len(svc.calls) != 6 {
		t.Errorf("service called for a malformed body: %v", svc.calls)
	}
}

func TestWorkspaceDependencyStreamReportsErrorsAsFrames(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog(), opLogs: [][2]string{{"stderr", "starting"}}, opErr: workspacedeps.ErrBusy}
	h := newDepsTestHandler("admin", svc)
	rec, err := depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/update", depID: "codex"}.invoke(t, h.UpdateWorkspaceDependency)
	if err != nil {
		t.Fatalf("UpdateWorkspaceDependency: %v", err)
	}
	frames := sseFrames(t, rec.Body.String())
	if len(frames) != 3 || frames[2]["type"] != "error" {
		t.Fatalf("frames = %v", frames)
	}
	if frames[2]["code"] != string(apperror.CodeWorkspaceDependencyBusy) {
		t.Errorf("error code = %v", frames[2]["code"])
	}
	if msg, _ := frames[2]["message"].(string); msg == "" || strings.Contains(msg, "already in progress\n") {
		t.Errorf("error message = %q", msg)
	}
	if _, ok := frames[2]["args"].(map[string]any); !ok {
		t.Errorf("error args must be an object: %v", frames[2])
	}

	// Structured errors must not leak private diagnostics. Execution logs
	// remain separate log events.
	svc.opErr = &workspacedeps.ExitError{Code: 1, StderrTail: "npm ERR! 404"}
	rec, err = depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/reinstall", depID: "codex"}.invoke(t, h.ReinstallWorkspaceDependency)
	if err != nil {
		t.Fatalf("ReinstallWorkspaceDependency: %v", err)
	}
	frames = sseFrames(t, rec.Body.String())
	last := frames[len(frames)-1]
	if last["type"] != "error" || last["code"] != string(apperror.CodeWorkspaceDependencyOperationFailed) {
		t.Fatalf("error frame = %v", last)
	}
	if msg, _ := last["message"].(string); msg == "" || strings.Contains(msg, "npm ERR! 404") {
		t.Errorf("message leaked private script diagnostics: %q", msg)
	}
	if svc.calls[len(svc.calls)-1] != "reinstall" {
		t.Errorf("calls = %v", svc.calls)
	}
}

// TestWorkspaceDependencyStreamLogsRefusalsBelowWarn pins the log level of
// the two outcomes that are not faults: a busy verdict (operations never
// queue) and the client going away. A script failure stays a warning.
func TestWorkspaceDependencyStreamLogsRefusalsBelowWarn(t *testing.T) {
	var logs strings.Builder
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog(), opErr: workspacedeps.ErrBusy}
	h := newDepsTestHandler("admin", svc)
	h.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if _, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/update", depID: "codex"}).invoke(t, h.UpdateWorkspaceDependency); err != nil {
		t.Fatalf("UpdateWorkspaceDependency: %v", err)
	}
	out := logs.String()
	if strings.Contains(out, "level=WARN") || strings.Contains(out, "level=ERROR") {
		t.Errorf("busy verdict logged above INFO:\n%s", out)
	}
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "another operation is in progress") {
		t.Errorf("busy verdict not logged at INFO:\n%s", out)
	}

	logs.Reset()
	svc.opErr = &workspacedeps.ExitError{Code: 1, StderrTail: "boom"}
	if _, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/update", depID: "codex"}).invoke(t, h.UpdateWorkspaceDependency); err != nil {
		t.Fatalf("UpdateWorkspaceDependency: %v", err)
	}
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "operation failed") {
		t.Errorf("script failure not logged at WARN:\n%s", out)
	}
}

func TestWorkspaceDependencyStreamValidatesBeforeOpening(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog()}
	h := newDepsTestHandler("admin", svc)
	_, err := depsCall{method: http.MethodPost, target: "/bots/x/dependencies/nope/update", depID: "nope"}.invoke(t, h.UpdateWorkspaceDependency)
	requireAppErrorCode(t, err, apperror.CodeWorkspaceDependencyNotFound)
	_, err = depsCall{method: http.MethodPost, target: "/bots/x/dependencies/node/reinstall", depID: "node"}.invoke(t, h.ReinstallWorkspaceDependency)
	requireAppErrorCode(t, err, apperror.CodeWorkspaceDependencyActionUnsupported)
	if len(svc.calls) != 0 {
		t.Fatalf("service called for a rejected request: %v", svc.calls)
	}
	// An image dependency with scripts takes an overlay update like any other.
	if _, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/python/update", depID: "python"}).invoke(t, h.UpdateWorkspaceDependency); err != nil {
		t.Fatalf("python update: %v", err)
	}
	if len(svc.calls) != 2 || svc.calls[0] != "script" || svc.calls[1] != "update" {
		t.Fatalf("calls = %v, want the python overlay update", svc.calls)
	}
}

func TestRollbackWorkspaceDependency(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{
		deps: depsTestCatalog(),
		operation: workspacedeps.OperationResult{
			DependencyID: "codex", Action: workspacedeps.ActionRollback, Version: "0.147.0",
			Entrypoints:  map[string]string{"codex": "/data/.memoh/deps/codex/current/bin/codex"},
			Installation: workspacedeps.Installation{Status: workspacedeps.StatusInstalled},
		},
	}
	h := newDepsTestHandler("admin", svc)
	rec, err := depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/rollback", depID: "codex"}.invoke(t, h.RollbackWorkspaceDependency)
	if err != nil {
		t.Fatalf("RollbackWorkspaceDependency: %v", err)
	}
	resp := decodeJSON[WorkspaceDependencyOperationResponse](t, rec)
	if resp.DependencyID != "codex" || resp.Action != "rollback" || resp.Version != "0.147.0" || resp.Status != "installed" {
		t.Errorf("response = %+v", resp)
	}

	svc.opErr = workspacedeps.ErrRollbackUnavailable
	_, err = depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/rollback", depID: "codex"}.invoke(t, h.RollbackWorkspaceDependency)
	requireAppErrorCode(t, err, apperror.CodeWorkspaceDependencyRollbackUnavailable)
	if definition, _ := apperror.Lookup(apperror.CodeWorkspaceDependencyRollbackUnavailable); definition.HTTPStatus != http.StatusConflict {
		t.Errorf("rollback_unavailable status = %d, want 409", definition.HTTPStatus)
	}
}

func TestCheckWorkspaceDependencyUpdatesReturnsList(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog(), list: workspacedeps.ListResult{Workspace: workspacedeps.WorkspaceRunning}}
	h := newDepsTestHandler("admin", svc)
	rec, err := depsCall{method: http.MethodPost, target: "/bots/x/dependencies/check-updates"}.invoke(t, h.CheckWorkspaceDependencyUpdates)
	if err != nil {
		t.Fatalf("CheckWorkspaceDependencyUpdates: %v", err)
	}
	resp := decodeJSON[WorkspaceDependencyListResponse](t, rec)
	if resp.WorkspaceState != "running" || len(svc.calls) != 1 || svc.calls[0] != "check_updates" {
		t.Errorf("response = %+v, calls = %v", resp, svc.calls)
	}
}

func TestGetWorkspaceDependencyScript(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{
		deps: depsTestCatalog(),
		preview: workspacedeps.ScriptPreview{
			DependencyID: "codex", Action: catalog.ActionUpdate, Digest: "sha256:abc", Exec: "exec sh -s", TimeoutSeconds: 1200,
			Env:    []workspacedeps.ScriptEnvEntry{{Key: "MEMOH_DEP_HOME", Value: "/data/.memoh/deps/codex"}, {Key: "NPM_TOKEN", Secret: true}},
			Script: "#!/bin/sh\n",
		},
	}
	h := newDepsTestHandler("admin", svc)
	rec, err := depsCall{method: http.MethodGet, target: "/bots/x/dependencies/codex/script?action=update", depID: "codex"}.invoke(t, h.GetWorkspaceDependencyScript)
	if err != nil {
		t.Fatalf("GetWorkspaceDependencyScript: %v", err)
	}
	resp := decodeJSON[WorkspaceDependencyScriptResponse](t, rec)
	if resp.Action != "update" || resp.Digest != "sha256:abc" || resp.Exec != "exec sh -s" || resp.TimeoutSeconds != 1200 || resp.Script != "#!/bin/sh\n" {
		t.Errorf("response = %+v", resp)
	}
	if len(resp.Env) != 2 || resp.Env[0].Key != "MEMOH_DEP_HOME" || !resp.Env[1].Secret || resp.Env[1].Value != "" {
		t.Errorf("env = %+v", resp.Env)
	}
	if svc.actions[0] != catalog.ActionUpdate {
		t.Errorf("action passed = %q", svc.actions[0])
	}

	// Default action is install; unknown actions are rejected before the
	// service is asked.
	if _, err := (depsCall{method: http.MethodGet, target: "/bots/x/dependencies/codex/script", depID: "codex"}).invoke(t, h.GetWorkspaceDependencyScript); err != nil {
		t.Fatalf("default action: %v", err)
	}
	if svc.actions[1] != catalog.ActionInstall {
		t.Errorf("default action = %q", svc.actions[1])
	}
	_, err = depsCall{method: http.MethodGet, target: "/bots/x/dependencies/codex/script?action=check_update", depID: "codex"}.invoke(t, h.GetWorkspaceDependencyScript)
	requireAppErrorCode(t, err, apperror.CodeWorkspaceDependencyRequestInvalid)
	if len(svc.actions) != 2 {
		t.Errorf("service asked for an invalid action: %v", svc.actions)
	}

	svc.previewErr = workspacedeps.ErrActionUnsupported
	_, err = depsCall{method: http.MethodGet, target: "/bots/x/dependencies/codex/script?action=rollback", depID: "codex"}.invoke(t, h.GetWorkspaceDependencyScript)
	requireAppErrorCode(t, err, apperror.CodeWorkspaceDependencyActionUnsupported)
}

func TestWorkspaceDependencyStreamHeartbeatIsComment(t *testing.T) {
	rec := httptest.NewRecorder()
	ticks := make(chan time.Time)
	flushed := make(chan struct{}, 2)
	flusher := dependencySignalFlusher{flushed: flushed}
	stream := startWorkspaceDependencyStream(rec, flusher, ticks, func() {})
	ticks <- time.Time{}
	<-flushed
	stream.send(workspaceDependencyLogEvent{Type: "log", Stream: "stdout", Data: "x"})
	stream.close()
	body := rec.Body.String()
	if !strings.Contains(body, ": ping\n\n") {
		t.Fatalf("heartbeat missing: %q", body)
	}
	if frames := sseFrames(t, body); len(frames) != 1 || frames[0]["data"] != "x" {
		t.Fatalf("frames = %v", frames)
	}
}

type dependencySignalFlusher struct{ flushed chan struct{} }

func (f dependencySignalFlusher) Flush() { f.flushed <- struct{}{} }

type dependencyBlockedWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *dependencyBlockedWriter) Write(_ []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return 0, io.ErrClosedPipe
}
func (*dependencyBlockedWriter) Flush() {}

func TestWorkspaceDependencySlowClientNeverBlocksOperation(t *testing.T) {
	writer := &dependencyBlockedWriter{started: make(chan struct{}), release: make(chan struct{})}
	stream := startWorkspaceDependencyStream(writer, writer, nil, func() {})
	stream.send(workspaceDependencyStartedEvent{Type: "started"})
	<-writer.started
	// The writer is held by an explicit signal while the producer exhausts
	// the queue. Every send must return without waiting for that signal.
	for i := 0; i < workspaceDependencyLogBuffer*4; i++ {
		stream.send(workspaceDependencyLogEvent{Type: "log", Data: "output"})
	}
	stream.send(workspaceDependencyDoneEvent{Type: "done", Version: "1.0.0"})
	if len(stream.events) != workspaceDependencyLogBuffer {
		t.Fatalf("unexpected queue size: %d", len(stream.events))
	}
	close(writer.release)
	stream.close()
}

func TestWorkspaceDependencyErrorMapping(t *testing.T) {
	cases := map[apperror.Code]error{
		apperror.CodeWorkspaceDependencyNotFound:            workspacedeps.ErrDependencyNotFound,
		apperror.CodeWorkspaceDependencyActionUnsupported:   workspacedeps.ErrActionUnsupported,
		apperror.CodeWorkspaceDependencyPlatformUnsupported: workspacedeps.ErrPlatformUnsupported,
		apperror.CodeWorkspaceDependencyBusy:                workspacedeps.ErrBusy,
		apperror.CodeWorkspaceDependencyWorkspaceNotRunning: workspacedeps.ErrWorkspaceNotRunning,
		apperror.CodeWorkspaceDependencyWorkspaceMissing:    workspacedeps.ErrWorkspaceMissing,
		apperror.CodeWorkspaceDependencyRemoteOffline:       workspacedeps.ErrRemoteOffline,
		apperror.CodeWorkspaceDependencyRollbackUnavailable: workspacedeps.ErrRollbackUnavailable,
		apperror.CodeWorkspaceDependencyOperationFailed:     errors.New("unexpected"),
	}
	for want, sentinel := range cases {
		wrapped := errors.Join(errors.New("context"), sentinel)
		if got := apperror.CodeOf(workspaceDependencyError(wrapped)); got != want {
			t.Errorf("%v -> %q, want %q", sentinel, got, want)
		}
	}
	if workspaceDependencyError(nil) != nil {
		t.Error("nil must map to nil")
	}
	already := apperror.New(apperror.CodeWorkspaceUnreachable, nil)
	if got := workspaceDependencyError(already); !errors.Is(got, already) || apperror.CodeOf(got) != apperror.CodeWorkspaceUnreachable {
		t.Errorf("existing app error mapped to %v, want it untouched", got)
	}
}

func TestWorkspaceDependencyMutationPreparesRevisionWithoutClientPreview(t *testing.T) {
	for _, action := range []string{"update", "reinstall"} {
		t.Run(action, func(t *testing.T) {
			revision := strings.Repeat("b", 64)
			svc := &fakeWorkspaceDependencyService{
				deps:      depsTestCatalog(),
				preview:   workspacedeps.ScriptPreview{Revision: revision},
				operation: workspacedeps.OperationResult{DefinitionRevision: revision},
			}
			h := newDepsTestHandler("admin", svc)
			handlers := map[string]func(echo.Context) error{
				"update":    h.UpdateWorkspaceDependency,
				"reinstall": h.ReinstallWorkspaceDependency,
			}
			rec, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/" + action, depID: "codex", omitRevision: true}).invoke(t, handlers[action])
			if err != nil {
				t.Fatal(err)
			}
			frames := sseFrames(t, rec.Body.String())
			if len(frames) != 2 || frames[0]["type"] != "started" || frames[1]["type"] != "done" {
				t.Fatalf("operation did not run: %v", frames)
			}
			for _, frame := range frames {
				if frame["definition_revision"] != revision {
					t.Fatalf("revision was not pinned: %v", frames)
				}
			}
			if len(svc.calls) != 2 || svc.calls[0] != "script" || svc.calls[1] != action {
				t.Fatalf("expected server preparation followed by operation: %v", svc.calls)
			}
		})
	}
}

func TestWorkspaceDependencyMutationRejectsMalformedRevision(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog()}
	h := newDepsTestHandler("admin", svc)
	_, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/update", depID: "codex", body: WorkspaceDependencyInstallRequest{DefinitionRevision: "invalid"}}).invoke(t, h.UpdateWorkspaceDependency)
	requireAppErrorCode(t, err, apperror.CodeWorkspaceDependencyRequestInvalid)
	if len(svc.calls) != 0 {
		t.Fatal("malformed revision reached operation")
	}
}

func TestWorkspaceDependencyItemIncludesSanitizedErrorDetail(t *testing.T) {
	item := workspaceDependencyItem(workspacedeps.Entry{
		Dependency:   catalog.Dependency{ID: "codex"},
		Installation: &workspacedeps.Installation{Status: workspacedeps.StatusFailed, LastError: "download failed: https://user:pass@mirror.test/release?token=private"},
	}, "/data")
	if item.LastErrorCode == "" || !strings.Contains(item.LastError, "download failed") || strings.Contains(item.LastError, "user:pass") || strings.Contains(item.LastError, "private") {
		t.Fatalf("error detail=%+v", item)
	}
}

func TestWorkspaceDependencyDisconnectOnlyEndsObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog()}
	svc.beforeRun = func(opCtx context.Context) {
		cancel()
		if opCtx.Err() != nil {
			t.Fatalf("disconnect canceled admitted operation: %v", opCtx.Err())
		}
	}
	h := newDepsTestHandler("admin", svc)
	rec, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/update", depID: "codex", requestContext: ctx}).invoke(t, h.UpdateWorkspaceDependency)
	if err != nil {
		t.Fatal(err)
	}
	frames := sseFrames(t, rec.Body.String())
	if frames[len(frames)-1]["type"] != "done" {
		t.Fatalf("completed operation was reported as failed: %v", frames)
	}
}

type dependencyDelayedWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	buffer  bytes.Buffer
}

func (w *dependencyDelayedWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started); <-w.release })
	return w.buffer.Write(p)
}
func (*dependencyDelayedWriter) Flush()           {}
func (w *dependencyDelayedWriter) String() string { return w.buffer.String() }

func TestWorkspaceDependencyBacklogRetainsTerminalEvent(t *testing.T) {
	writer := &dependencyDelayedWriter{started: make(chan struct{}), release: make(chan struct{})}
	stream := startWorkspaceDependencyStream(writer, writer, nil, func() {})
	stream.send(workspaceDependencyStartedEvent{Type: "started"})
	<-writer.started
	for i := 0; i < workspaceDependencyLogBuffer*4; i++ {
		stream.send(workspaceDependencyLogEvent{Type: "log", Data: "output"})
	}
	stream.send(workspaceDependencyDoneEvent{Type: "done", Version: "1.0.0"})
	close(writer.release)
	stream.close()
	frames := sseFrames(t, writer.String())
	if final := frames[len(frames)-1]; final["type"] != "done" || final["version"] != "1.0.0" {
		t.Fatalf("terminal event lost in backlog: %v", final)
	}
}
