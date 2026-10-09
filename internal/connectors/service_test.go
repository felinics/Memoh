package connectors

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	connectsdk "github.com/felinics/connect-it/sdk/go"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/config"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/errs"
)

func TestNormalizeAlias(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"GitHub":            "github",
		" Google Drive ":    "google-drive",
		"中文":                "connector",
		"many---separators": "many-separators",
		"UPPER_and spaces":  "upper-and-spaces",
	}
	for input, want := range tests {
		if got := normalizeAlias(input); got != want {
			t.Errorf("normalizeAlias(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAllocateAliasAvoidsCollisionsAndLengthOverflow(t *testing.T) {
	t.Parallel()

	used := map[string]bool{
		"github":   true,
		"github-2": true,
	}
	if got := allocateAlias("GitHub", used); got != "github-3" {
		t.Fatalf("allocateAlias collision = %q, want %q", got, "github-3")
	}

	got := allocateAlias(strings.Repeat("a", 64), map[string]bool{
		strings.Repeat("a", 32): true,
	})
	if got != strings.Repeat("a", 30)+"-2" {
		t.Fatalf("allocateAlias long value = %q", got)
	}
	if len(got) > 32 {
		t.Fatalf("allocateAlias length = %d, want at most 32", len(got))
	}
}

func TestUpstreamErrorPreservesClassification(t *testing.T) {
	t.Parallel()

	apiErr := &connectsdk.APIError{
		StatusCode: http.StatusConflict,
		Code:       "conflict",
		Message:    "private upstream detail",
	}
	err := upstreamError(fmt.Errorf("PRIVATE wrapper: %w", apiErr))
	if !errors.Is(err, ErrUpstreamUnavailable) {
		t.Fatalf("upstream error does not match ErrUpstreamUnavailable: %v", err)
	}
	var gotAPIError *connectsdk.APIError
	if !errors.As(err, &gotAPIError) || gotAPIError.Code != apiErr.Code || gotAPIError.StatusCode != apiErr.StatusCode {
		t.Fatalf("upstream error did not retain APIError classification: %v", err)
	}
	if gotAPIError == apiErr || gotAPIError.Message != "" || apiErr.Message != "private upstream detail" {
		t.Fatalf("upstream error must retain only a sanitized copy: %v", gotAPIError)
	}
	for _, leaked := range []string{"PRIVATE", apiErr.Message} {
		if strings.Contains(errs.Text(err), leaked) || strings.Contains(err.Error(), leaked) {
			t.Fatalf("upstream diagnostic leaked: %v", err)
		}
	}
}

func TestUpstreamErrorDiscardsUnrecognizedCode(t *testing.T) {
	t.Parallel()
	const private = "PRIVATE test-only-client-secret-56"
	err := upstreamError(&connectsdk.APIError{StatusCode: http.StatusBadGateway, Code: private, Message: private})
	var apiErr *connectsdk.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadGateway || apiErr.Code != "upstream_error" {
		t.Fatalf("unexpected sanitized classification: %v", err)
	}
	if strings.Contains(errs.Text(err), private) || strings.Contains(err.Error(), private) {
		t.Fatalf("upstream diagnostic leaked: %v", err)
	}
}

type failedBindingQueries struct {
	dbstore.Queries
	err error
}

func (q failedBindingQueries) ListConnectorsByBotID(context.Context, pgtype.UUID) ([]dbsqlc.Connector, error) {
	return nil, q.err
}

func TestBindingRollbackDiscardsUpstreamDiagnostics(t *testing.T) {
	t.Parallel()
	const private = "PRIVATE test-only-client-secret-56"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/connections/conn-56" {
			t.Errorf("unexpected rollback request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(private))
	}))
	defer upstream.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	storageErr := errors.New("binding store unavailable")
	svc, err := NewService(logger, config.Config{ConnectIt: config.ConnectItConfig{
		BaseURL: upstream.URL, APIToken: "test-only-token",
	}}, failedBindingQueries{err: storageErr})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.createBindingOrRollback(context.Background(), "00000000-0000-0000-0000-000000000056", "conn-56", "github")
	if !errors.Is(err, storageErr) {
		t.Fatalf("lost binding failure: %v", err)
	}
	if strings.Contains(logs.String(), private) {
		t.Fatalf("rollback leaked upstream diagnostics: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "HTTP 502") || !strings.Contains(logs.String(), "conn-56") {
		t.Fatalf("rollback lost safe diagnostics: %s", logs.String())
	}
}

// The alias is the durable tool namespace clients need in order to reason
// about which connection a tool call reaches, so every projection must carry
// it — including the "unavailable" shape built when Connect-It has already
// dropped the connection.
func TestConnectorFromCarriesStoredAlias(t *testing.T) {
	t.Parallel()

	item := dbsqlc.Connector{ConnectionID: "conn-1", Alias: "github-2", Enabled: true}
	got := connectorFrom(item, connectsdk.Connection{
		ConnectorType: "github",
		AuthMethod:    "oauth",
		Status:        "active",
	})
	if got.Alias != "github-2" {
		t.Fatalf("alias not projected into the API model: %+v", got)
	}
	if got.ConnectionID != "conn-1" || !got.Enabled || got.Status != "active" {
		t.Fatalf("unexpected projection: %+v", got)
	}
}
