package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
)

func TestWorkspaceManagementRejectsRemovedDeviceFields(t *testing.T) {
	for _, value := range []string{`"remote-computer"`, `"native"`, `""`, `null`} {
		for _, body := range []any{&AppInstallRequest{}, &AppUpdateRequest{}, &WorkspaceDependencyPreflightRequest{}, &WorkspaceDependencyInstallRequest{}} {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(`{"workspace_target_id":`+value+`}`))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			c := echo.New().NewContext(req, httptest.NewRecorder())
			if err := bindWorkspaceManagementRequest(c, body); err == nil {
				t.Fatalf("accepted removed target %s for %T", value, body)
			}
		}
	}
}

func TestDependencyManagementRejectsDeviceBeforeServiceCalls(t *testing.T) {
	svc := &fakeWorkspaceDependencyService{deps: depsTestCatalog()}
	h := newDepsTestHandler("admin", svc)
	for _, value := range []string{"remote-computer", "native", ""} {
		for _, route := range []struct {
			method, path string
			handler      echo.HandlerFunc
		}{
			{http.MethodGet, "/bots/x/dependencies", h.ListWorkspaceDependencies},
			{http.MethodPost, "/bots/x/dependencies/check-updates", h.CheckWorkspaceDependencyUpdates},
			{http.MethodPost, "/bots/x/dependencies/codex/update", h.UpdateWorkspaceDependency},
		} {
			_, err := (depsCall{method: route.method, target: route.path + "?workspace_target_id=" + value, depID: "codex"}).invoke(t, route.handler)
			requireAppErrorCode(t, err, apperror.CodeWorkspaceDependencyRequestInvalid)
		}
	}
	if len(svc.calls) != 0 {
		t.Fatalf("rejected requests reached service: %v", svc.calls)
	}
}

func TestAppManagementRejectsDeviceBeforeAuthorizationOrService(t *testing.T) {
	h := &AppsHandler{}
	for _, value := range []string{"remote-computer", "native", ""} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bots/x/apps?workspace_target_id="+value, nil)
		c := echo.New().NewContext(req, httptest.NewRecorder())
		_, err := h.authorize(c)
		requireAppErrorCode(t, err, apperror.CodeAppRequestInvalid)
	}
}
