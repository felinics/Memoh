package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/workspacedeps"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

type dependencyRepairCall struct {
	botID, depID, version, revision, actor string
	action                                 catalog.Action
}

type fakeDependencyRepairService struct {
	*fakeWorkspaceDependencyService
	prepared        workspacedeps.PreparedInstall
	authorization   workspacedeps.RepairAuthorization
	desired         workspacedeps.DesiredInstallation
	err             error
	requests        []dependencyRepairCall
	beforeAuthorize func(context.Context)
}

func (f *fakeDependencyRepairService) PrepareInstall(_ context.Context, botID, depID string, action catalog.Action, version string) (workspacedeps.PreparedInstall, error) {
	f.record("prepare")
	f.requests = append(f.requests, dependencyRepairCall{botID: botID, depID: depID, version: version, action: action})
	return f.prepared, f.err
}

func (f *fakeDependencyRepairService) PrepareRepairAuthorization(_ context.Context, botID, depID, version, revision string) (workspacedeps.RepairAuthorization, error) {
	f.record("prepare_repair")
	f.requests = append(f.requests, dependencyRepairCall{botID: botID, depID: depID, version: version, revision: revision})
	return f.authorization, f.err
}

func (f *fakeDependencyRepairService) AuthorizeRepair(ctx context.Context, botID, depID, version, revision, actor string) (workspacedeps.DesiredInstallation, error) {
	f.record("authorize_repair")
	f.requests = append(f.requests, dependencyRepairCall{botID: botID, depID: depID, version: version, revision: revision, actor: actor})
	if f.beforeAuthorize != nil {
		f.beforeAuthorize(ctx)
	}
	return f.desired, f.err
}

func (f *fakeDependencyRepairService) RetryRepair(_ context.Context, botID, depID string) (workspacedeps.DesiredInstallation, error) {
	f.record("retry_repair")
	f.requests = append(f.requests, dependencyRepairCall{botID: botID, depID: depID})
	return f.desired, f.err
}

func dependencyReadHandler(svc workspaceDependencyService) *ContainerdHandler {
	h := newDepsTestHandler("user", svc)
	bot := testBotRow(depsTestBotID, map[string]any{})
	bot.OwnerUserID = testUUID(depsTestOwnerID)
	h.botService = bots.NewService(nil, depsAuthQueries{
		bot:    bot,
		grants: []sqlc.ListBotUserGrantsForUserRow{{Permissions: []byte(`["workspace_read"]`)}},
	})
	return h
}

func TestWorkspaceDependencyReadPermissionDoesNotGrantExecution(t *testing.T) {
	svc := &fakeDependencyRepairService{fakeWorkspaceDependencyService: &fakeWorkspaceDependencyService{deps: depsTestCatalog()}}
	h := dependencyReadHandler(svc)
	for _, route := range []struct {
		name, method, target string
		body                 any
		handler              func(echo.Context) error
	}{
		{"list", http.MethodGet, "/bots/x/dependencies", nil, h.ListWorkspaceDependencies},
		{"preflight", http.MethodPost, "/bots/x/dependencies/preflight", WorkspaceDependencyPreflightRequest{DependencyIDs: []string{"codex"}}, h.PreflightWorkspaceDependencies},
	} {
		t.Run(route.name, func(t *testing.T) {
			rec, err := (depsCall{method: route.method, target: route.target, body: route.body, userID: depsTestOtherID}).invoke(t, route.handler)
			if err != nil || rec.Code != http.StatusOK {
				t.Fatalf("read permission rejected: status=%d, error=%v", rec.Code, err)
			}
		})
	}
	if !reflect.DeepEqual(svc.calls, []string{"list", "preflight"}) {
		t.Fatalf("read endpoints performed unexpected work: %v", svc.calls)
	}
	svc.calls = nil
	for _, route := range []struct {
		name    string
		body    any
		handler func(echo.Context) error
	}{
		{"prepare", WorkspaceDependencyPrepareRequest{Action: "install", DefinitionRevision: strings.Repeat("a", 64)}, h.PrepareWorkspaceDependency},
		{"repair/prepare", WorkspaceDependencyRepairRequest{DefinitionRevision: strings.Repeat("a", 64)}, h.PrepareWorkspaceDependencyRepair},
		{"repair/authorize", WorkspaceDependencyRepairRequest{Version: "0.151.0", DefinitionRevision: strings.Repeat("a", 64)}, h.AuthorizeWorkspaceDependencyRepair},
		{"repair/retry", nil, h.RetryWorkspaceDependencyRepair},
		{"install", nil, h.InstallWorkspaceDependency},
		{"update", nil, h.UpdateWorkspaceDependency},
		{"reinstall", nil, h.ReinstallWorkspaceDependency},
		{"check-updates", nil, h.CheckWorkspaceDependencyUpdates},
	} {
		t.Run(route.name, func(t *testing.T) {
			_, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/" + route.name, depID: "codex", body: route.body, userID: depsTestOtherID}).invoke(t, route.handler)
			requireForbidden(t, err)
		})
	}
	if len(svc.calls) != 0 {
		t.Fatalf("read permission authorized execution: %v", svc.calls)
	}
}

func TestWorkspaceDependencyPrepareReturnsTargetWithoutInstalling(t *testing.T) {
	revision := strings.Repeat("d", 64)
	svc := &fakeDependencyRepairService{
		fakeWorkspaceDependencyService: &fakeWorkspaceDependencyService{deps: depsTestCatalog()},
		prepared:                       workspacedeps.PreparedInstall{DependencyID: "codex", Action: catalog.ActionUpdate, Version: "0.151.0", DefinitionRevision: revision, SourceURL: "https://registry.example/dependencies/codex", RegistryID: "memoh", ManifestDigest: strings.Repeat("e", 64)},
	}
	h := newDepsTestHandler("user", svc)
	rec, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/prepare", depID: "codex", body: WorkspaceDependencyPrepareRequest{Action: "update", DefinitionRevision: revision}}).invoke(t, h.PrepareWorkspaceDependency)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeJSON[WorkspaceDependencyPreparedResponse](t, rec)
	if response.Version != "0.151.0" || response.DefinitionRevision != revision || response.ManifestDigest != svc.prepared.ManifestDigest || response.SourceURL != svc.prepared.SourceURL || response.Action != "update" {
		t.Fatalf("prepared target lost provenance: %+v", response)
	}
	if !reflect.DeepEqual(svc.calls, []string{"prepare"}) || len(svc.requests) != 1 || svc.requests[0].version != "" || svc.requests[0].action != catalog.ActionUpdate || svc.requests[0].botID != depsTestBotID {
		t.Fatalf("prepare did not remain a metadata operation: calls=%v, requests=%+v", svc.calls, svc.requests)
	}
}

func TestWorkspaceDependencyMutationRejectsUnconfirmedTarget(t *testing.T) {
	for _, action := range []string{"install", "update", "reinstall"} {
		for name, body := range map[string]any{
			"no body":         nil,
			"no revision":     WorkspaceDependencyInstallRequest{Version: "0.151.0"},
			"no version":      WorkspaceDependencyInstallRequest{DefinitionRevision: strings.Repeat("a", 64)},
			"latest alias":    WorkspaceDependencyInstallRequest{Version: "latest", DefinitionRevision: strings.Repeat("a", 64)},
			"release channel": WorkspaceDependencyInstallRequest{Version: "nightly", DefinitionRevision: strings.Repeat("a", 64)},
			"range":           WorkspaceDependencyInstallRequest{Version: "^0.151.0", DefinitionRevision: strings.Repeat("a", 64)},
		} {
			t.Run(action+"/"+name, func(t *testing.T) {
				svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog()}
				h := newDepsTestHandler("admin", svc)
				routes := map[string]func(echo.Context) error{"install": h.InstallWorkspaceDependency, "update": h.UpdateWorkspaceDependency, "reinstall": h.ReinstallWorkspaceDependency}
				rec, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/" + action, depID: "codex", body: body, omitRevision: true}).invoke(t, routes[action])
				requireAppErrorCode(t, err, apperror.CodeWorkspaceDependencyRequestInvalid)
				if len(svc.calls) != 0 || rec.Body.Len() != 0 || strings.HasPrefix(rec.Header().Get(echo.HeaderContentType), "text/event-stream") {
					t.Fatalf("unconfirmed target reached execution: calls=%v, body=%s", svc.calls, rec.Body.String())
				}
			})
		}
	}
}

func TestWorkspaceDependencyRepairPreparationDoesNotAuthorize(t *testing.T) {
	revision := strings.Repeat("a", 64)
	svc := &fakeDependencyRepairService{
		fakeWorkspaceDependencyService: &fakeWorkspaceDependencyService{deps: depsTestCatalog()},
		authorization:                  workspacedeps.RepairAuthorization{DependencyID: "codex", Version: "0.151.0", DefinitionRevision: revision, RegistryID: "memoh", SourceURL: "https://registry.example/codex", ManifestDigest: strings.Repeat("b", 64)},
	}
	h := newDepsTestHandler("user", svc)
	rec, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/repair/prepare", depID: "codex", body: WorkspaceDependencyRepairRequest{DefinitionRevision: " " + revision + " "}}).invoke(t, h.PrepareWorkspaceDependencyRepair)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeJSON[WorkspaceDependencyPreparedResponse](t, rec)
	if response.Version != "0.151.0" || response.Action != "reinstall" || response.DefinitionRevision != revision || response.SourceURL != svc.authorization.SourceURL {
		t.Fatalf("incorrect repair confirmation: %+v", response)
	}
	if !reflect.DeepEqual(svc.calls, []string{"prepare_repair"}) || len(svc.requests) != 1 || svc.requests[0].revision != revision || svc.requests[0].version != "" {
		t.Fatalf("prepare authorized an installation: calls=%v, requests=%+v", svc.calls, svc.requests)
	}
}

func TestWorkspaceDependencyRepairAuthorizationUsesConfirmedActorAndSurvivesDisconnect(t *testing.T) {
	revision := strings.Repeat("a", 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := &fakeDependencyRepairService{fakeWorkspaceDependencyService: &fakeWorkspaceDependencyService{deps: depsTestCatalog()}, desired: dependencyDesiredFixture()}
	svc.beforeAuthorize = func(opCtx context.Context) {
		cancel()
		if opCtx.Err() != nil {
			t.Fatalf("disconnect canceled authorized repair: %v", opCtx.Err())
		}
	}
	h := newDepsTestHandler("user", svc)
	rec, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/repair/authorize", depID: "codex", body: WorkspaceDependencyRepairRequest{Version: " 0.151.0 ", DefinitionRevision: " " + revision + " "}, requestContext: ctx}).invoke(t, h.AuthorizeWorkspaceDependencyRepair)
	if err != nil {
		t.Fatal(err)
	}
	want := dependencyRepairCall{botID: depsTestBotID, depID: "codex", version: "0.151.0", revision: revision, actor: depsTestOwnerID}
	if !reflect.DeepEqual(svc.calls, []string{"authorize_repair"}) || len(svc.requests) != 1 || svc.requests[0] != want {
		t.Fatalf("wrong authorization: calls=%v, requests=%+v", svc.calls, svc.requests)
	}
	assertPublicDependencyDesired(t, rec.Body.Bytes(), svc.desired)
}

func TestWorkspaceDependencyRepairRejectsInvalidConfirmationBeforeService(t *testing.T) {
	for _, body := range []WorkspaceDependencyRepairRequest{
		{Version: "0.151.0"},
		{Version: "0.151.0", DefinitionRevision: "invalid"},
		{DefinitionRevision: strings.Repeat("a", 64)},
		{Version: "latest", DefinitionRevision: strings.Repeat("a", 64)},
		{Version: ">=0.151.0", DefinitionRevision: strings.Repeat("a", 64)},
	} {
		t.Run(body.Version+"/"+body.DefinitionRevision, func(t *testing.T) {
			svc := &fakeDependencyRepairService{fakeWorkspaceDependencyService: &fakeWorkspaceDependencyService{deps: depsTestCatalog()}}
			h := newDepsTestHandler("admin", svc)
			_, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/repair/authorize", depID: "codex", body: body}).invoke(t, h.AuthorizeWorkspaceDependencyRepair)
			requireAppErrorCode(t, err, apperror.CodeWorkspaceDependencyRequestInvalid)
			if len(svc.calls) != 0 {
				t.Fatalf("invalid confirmation reached service: %v", svc.calls)
			}
		})
	}
}

func TestWorkspaceDependencyRepairRetryUsesExistingTarget(t *testing.T) {
	svc := &fakeDependencyRepairService{fakeWorkspaceDependencyService: &fakeWorkspaceDependencyService{deps: depsTestCatalog()}, desired: dependencyDesiredFixture()}
	svc.desired.RepairStatus = workspacedeps.RepairQueued
	h := newDepsTestHandler("user", svc)
	rec, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/repair/retry", depID: "codex", omitRevision: true}).invoke(t, h.RetryWorkspaceDependencyRepair)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(svc.calls, []string{"retry_repair"}) || len(svc.requests) != 1 || svc.requests[0] != (dependencyRepairCall{botID: depsTestBotID, depID: "codex"}) {
		t.Fatalf("retry changed the authorized target: %+v", svc.requests)
	}
	assertPublicDependencyDesired(t, rec.Body.Bytes(), svc.desired)
}

func TestWorkspaceDependencyRepairFailureUsesStableCodeWithoutPrivateDetail(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code apperror.Code
	}{
		{workspacedeps.ErrRepairAuthorizationRequired, apperror.CodeWorkspaceDependencyRepairAuthorizationRequired},
		{workspacedeps.ErrDesiredTargetChanged, apperror.CodeWorkspaceDependencyTargetChanged},
		{workspacedeps.ErrRepairManualRequired, apperror.CodeWorkspaceDependencyRepairManualRequired},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			svc := &fakeDependencyRepairService{fakeWorkspaceDependencyService: &fakeWorkspaceDependencyService{deps: depsTestCatalog()}, err: errors.Join(tc.err, errors.New("SECRET /private/deps auth-token"))}
			h := newDepsTestHandler("user", svc)
			_, err := (depsCall{method: http.MethodPost, target: "/bots/x/dependencies/codex/repair/retry", depID: "codex", omitRevision: true}).invoke(t, h.RetryWorkspaceDependencyRepair)
			requireAppErrorCode(t, err, tc.code)
			problem, ok := apperror.PublicFrom(err, "test-request")
			if !ok {
				t.Fatal("repair error has no public representation")
			}
			public, marshalErr := json.Marshal(problem)
			if marshalErr != nil || strings.Contains(string(public), "SECRET") || strings.Contains(string(public), "/private/deps") {
				t.Fatalf("private repair diagnostic leaked: %s, %v", public, marshalErr)
			}
		})
	}
}

func dependencyDesiredFixture() workspacedeps.DesiredInstallation {
	next := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	return workspacedeps.DesiredInstallation{
		Version: "0.151.0", Revision: "desired-revision", DefinitionRevision: strings.Repeat("a", 64), SourceURL: "https://registry.example/codex", RegistryID: "memoh", ManifestDigest: strings.Repeat("b", 64),
		AutoRepairAuthorizedAt: next.Add(-time.Hour), RepairStatus: workspacedeps.RepairBackoff, RepairOperationID: depsTestOperationID, RepairAttempts: 2, RepairNextAttemptAt: &next, RepairLastErrorCode: "dependency.operation_failed",
		AuthorizedByActor: "PRIVATE_ACTOR", AuthorizedByOperationID: "PRIVATE_AUDIT_OPERATION", StoreRoot: "/private/store", PayloadPath: "/private/payload", InstallationID: "PRIVATE_INSTALLATION", Entrypoints: map[string]string{"codex": "/private/entrypoint"},
	}
}

func assertPublicDependencyDesired(t *testing.T, body []byte, desired workspacedeps.DesiredInstallation) {
	t.Helper()
	var response WorkspaceDependencyDesired
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if response.Version != desired.Version || response.Revision != desired.Revision || response.RepairStatus != string(desired.RepairStatus) || response.RepairOperationID != desired.RepairOperationID || response.RepairAttempts != desired.RepairAttempts || response.RepairNextAttemptAt == nil || !response.RepairNextAttemptAt.Equal(*desired.RepairNextAttemptAt) {
		t.Fatalf("recovery progress lost: %+v", response)
	}
	for _, private := range []string{desired.AuthorizedByActor, desired.AuthorizedByOperationID, desired.StoreRoot, desired.PayloadPath, desired.InstallationID, desired.Entrypoints["codex"]} {
		if strings.Contains(string(body), private) {
			t.Fatalf("desired response exposed private value %q: %s", private, body)
		}
	}
}

func TestWorkspaceDependencyListExposesRecoveryWithoutPrivatePaths(t *testing.T) {
	desired := dependencyDesiredFixture()
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog(), list: workspacedeps.ListResult{
		Workspace: workspacedeps.WorkspaceRunning,
		Entries:   []workspacedeps.Entry{{Dependency: depsTestCatalog()["codex"], Desired: &desired, Installation: &workspacedeps.Installation{OperationID: "active-operation", LastOperationID: depsTestOperationID}}},
	}}
	h := dependencyReadHandler(svc)
	rec, err := (depsCall{method: http.MethodGet, target: "/bots/x/dependencies", userID: depsTestOtherID}).invoke(t, h.ListWorkspaceDependencies)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Items []struct {
			Desired         json.RawMessage `json:"desired"`
			OperationID     string          `json:"operation_id"`
			LastOperationID string          `json:"last_operation_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || len(response.Items) != 1 {
		t.Fatalf("list response: %s, %v", rec.Body.String(), err)
	}
	item := response.Items[0]
	if item.OperationID != "active-operation" || item.LastOperationID != depsTestOperationID {
		t.Fatalf("active and completed operations were conflated: %+v", item)
	}
	assertPublicDependencyDesired(t, item.Desired, desired)
	if !reflect.DeepEqual(svc.calls, []string{"list"}) {
		t.Fatalf("reading recovery status triggered execution: %v", svc.calls)
	}
}
