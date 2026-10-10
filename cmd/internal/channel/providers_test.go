package channel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel/inbound"
	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/logger"
	"github.com/felinics/memoh/internal/server"
)

func TestNewSessionCreatedByUserIDPrefersCreator(t *testing.T) {
	got := newSessionCreatedByUserID(inbound.NewSessionSpec{
		CreatedByUserID:       "creator-user",
		RuntimeOwnerAccountID: "runtime-owner",
	})
	if got != "creator-user" {
		t.Fatalf("created_by_user_id = %q, want creator-user", got)
	}

	got = newSessionCreatedByUserID(inbound.NewSessionSpec{
		RuntimeOwnerAccountID: "runtime-owner",
	})
	if got != "runtime-owner" {
		t.Fatalf("created_by_user_id fallback = %q, want runtime-owner", got)
	}
}

func TestWebhookTunnelAnswersAnUnknownRouteWithAProblemAndOneRecord(t *testing.T) {
	var logs bytes.Buffer
	e := newWebhookTunnelEcho(logger.New(&logs, "debug", "json"))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nowhere", nil))

	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status = %d content-type = %q: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	var problem server.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != string(apperror.CodeHTTPNotFound) || problem.Fault != "client" || problem.RequestID == "" {
		t.Fatalf("problem = %+v", problem)
	}

	var requests int
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if record["msg"] == "request" {
			requests++
			if record["fault"] != "client" || record["request_id"] != problem.RequestID {
				t.Errorf("record = %v", record)
			}
		}
	}
	if requests != 1 {
		t.Fatalf("request records = %d, want 1: %s", requests, logs.String())
	}
}

// The channel listener binds requests the way the main server does, so a
// webhook body of the wrong shape names its field on either listener.
func TestWebhookTunnelBindsWithTheSharedBinder(t *testing.T) {
	e := newWebhookTunnelEcho(logger.New(&bytes.Buffer{}, "debug", "json"))
	if _, ok := e.Binder.(*httpx.Binder); !ok {
		t.Fatalf("binder = %T, want *httpx.Binder", e.Binder)
	}
}
