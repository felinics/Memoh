package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/botworkspace"
)

type removalWorkspace struct {
	*createBotStreamWorkspace
	row botworkspace.Workspace
}

func (w *removalWorkspace) RequestAbsent(_ context.Context, _ string, preserve bool) (botworkspace.Workspace, error) {
	w.row = botworkspace.Workspace{
		Desired: botworkspace.DesiredAbsent, DesiredGeneration: 2, PreserveData: preserve,
		Observed: botworkspace.ObservedRunning, ObservedGeneration: 1,
	}
	return w.row, nil
}

func (w *removalWorkspace) Get(context.Context, string) (botworkspace.Workspace, error) {
	return w.row, nil
}

func TestDeleteContainerAnswersAtOnceAndGetReportsTheRemoval(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000105"
	botID := "00000000-0000-0000-0000-000000000205"
	ws := &removalWorkspace{createBotStreamWorkspace: &createBotStreamWorkspace{}}
	handler := newRestoreContainerHandler(t, ownerID, botID, &restoreWorkspaceManager{}, ws)
	call := func(method, target string, fn echo.HandlerFunc) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx := testAuthContext(echo.New(), httptest.NewRequest(method, target, nil), rec, ownerID)
		ctx.SetParamNames("bot_id")
		ctx.SetParamValues(botID)
		if err := fn(ctx); err != nil {
			t.Fatalf("%s %s: %v", method, target, err)
		}
		return rec
	}
	getRemoval := func() *WorkspaceRemovalResponse {
		t.Helper()
		var body GetContainerResponse
		if err := json.Unmarshal(call(http.MethodGet, "/bots/"+botID+"/container", handler.GetContainer).Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Removal
	}

	if rec := call(http.MethodDelete, "/bots/"+botID+"/container?preserve_data=true", handler.DeleteContainer); rec.Code != http.StatusAccepted {
		t.Fatalf("DELETE = %d, want 202 without waiting for the removal", rec.Code)
	}
	if removal := getRemoval(); removal == nil || removal.State != botworkspace.RemovalRemoving || !removal.PreserveData {
		t.Fatalf("GET during the removal = %+v", removal)
	}

	ws.row.Observed, ws.row.ObservedGeneration = botworkspace.ObservedFailed, 2
	if removal := getRemoval(); removal == nil || removal.State != botworkspace.RemovalFailed {
		t.Fatalf("GET after the removal failed = %+v", removal)
	}
}
