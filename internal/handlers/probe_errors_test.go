package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
)

func TestProbeErrorCodes(t *testing.T) {
	t.Parallel()

	apiErr := func(kind sdk.ErrorKind) error {
		return fmt.Errorf("openai: test request failed: %w", &sdk.APIError{StatusCode: http.StatusBadRequest, Kind: kind})
	}
	refused := fmt.Errorf("request failed: %w", &url.Error{Op: "Get", URL: "http://127.0.0.1:1/models", Err: errors.New("connect: connection refused")})
	cases := []struct {
		name string
		err  error
		// credentialsRejected is whether the verdict is auth_error.
		credentialsRejected bool
		want                apperror.Code
	}{
		{"authentication", apiErr(sdk.KindAuthentication), true, apperror.CodeAgentProviderAuthFailed},
		{"authentication, verdict not auth_error (#1042)", apiErr(sdk.KindAuthentication), false, apperror.CodeAgentProviderRequestRejected},
		{"permission denied", apiErr(sdk.KindPermissionDenied), true, apperror.CodeAgentProviderPermissionDenied},
		{"permission denied, verdict not auth_error", apiErr(sdk.KindPermissionDenied), false, apperror.CodeAgentProviderPermissionDenied},
		{"quota exhausted", apiErr(sdk.KindQuotaExhausted), false, apperror.CodeAgentProviderQuotaExhausted},
		{"rate limited", apiErr(sdk.KindRateLimited), false, apperror.CodeAgentProviderRateLimited},
		{"server error", apiErr(sdk.KindServerError), false, apperror.CodeAgentProviderOverloaded},
		{"unknown kind", apiErr(sdk.KindUnknown), false, apperror.CodeAgentProviderRequestRejected},
		{"kind added by a later SDK", apiErr(sdk.ErrorKind("content_filtered")), false, apperror.CodeAgentProviderRequestRejected},
		{"no response", refused, false, apperror.CodeAgentProviderUnreachable},
		{"ended context", context.DeadlineExceeded, false, ""},
		{"anything else", errors.New("embedding provider returned no vector values"), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := probeError(tc.err, tc.credentialsRejected)
			if code := apperror.CodeOf(got); code != tc.want {
				t.Fatalf("code = %q, want %q", code, tc.want)
			}
			if tc.want == "" && !errors.Is(got, tc.err) {
				t.Fatalf("probeError() = %v, want the input unchanged", got)
			}
		})
	}
}

// probeQueries stubs the rows the two test endpoints read; every other
// method nil-panics.
type probeQueries struct {
	dbstore.Queries
	model    sqlc.Model
	provider sqlc.Provider
}

func (q probeQueries) GetModelByID(context.Context, pgtype.UUID) (sqlc.Model, error) {
	return q.model, nil
}

func (q probeQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return q.provider, nil
}

func newProbeQueries(baseURL string) probeQueries {
	providerID := pgtype.UUID{Bytes: [16]byte{0x0a, 0x08}, Valid: true}
	return probeQueries{
		model: sqlc.Model{
			ID:         pgtype.UUID{Bytes: [16]byte{0x0a, 0x08, 0x01}, Valid: true},
			ProviderID: providerID,
			ModelID:    "real-model",
			Type:       string(models.ModelTypeChat),
		},
		provider: sqlc.Provider{
			ID:         providerID,
			ClientType: string(models.ClientTypeOpenAICompletions),
			Config:     []byte(`{"api_key":"sk-test","base_url":"` + baseURL + `"}`),
		},
	}
}

// serveProbe posts to target on the routes register adds and returns the
// status and the raw response body.
func serveProbe(t *testing.T, register func(*echo.Echo), target string) (int, string) {
	t.Helper()
	e := echo.New()
	register(e)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, nil))
	return rec.Code, rec.Body.String()
}

// decodeProbeBody decodes a test response and fails when it has a field the
// test response contract does not define.
func decodeProbeBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	for key := range fields {
		if !slices.Contains([]string{"status", "reachable", "latency_ms", "code"}, key) {
			t.Fatalf("response field %q in %s is not part of the contract", key, body)
		}
	}
	return fields
}

func closedLocalURL(t *testing.T) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + addr
}

// A provider that refuses the connection is answered with the unreachable
// code and nothing of the transport error; the cause goes to the log.
func TestProviderTestAnswersUnreachableWithoutErrorText(t *testing.T) {
	t.Parallel()

	baseURL := closedLocalURL(t)
	queries := newProbeQueries(baseURL)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := NewProvidersHandler(logger, providers.NewService(logger, queries, ""), nil)

	status, body := serveProbe(t, h.Register, "/providers/"+queries.provider.ID.String()+"/test")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, body)
	}
	fields := decodeProbeBody(t, body)
	if fields["code"] != string(apperror.CodeAgentProviderUnreachable) || fields["status"] != string(providers.TestStatusError) || fields["reachable"] != false {
		t.Fatalf("body = %s, want status error, reachable false, code %s", body, apperror.CodeAgentProviderUnreachable)
	}
	host := strings.TrimPrefix(baseURL, "http://")
	for _, leaked := range []string{host, "refused", "dial", "request failed", "/models"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("body %s contains %q", body, leaked)
		}
	}
	if !strings.Contains(logs.String(), `"reason":"agent.provider_unreachable"`) || !strings.Contains(logs.String(), "refused") {
		t.Fatalf("log %s does not record the cause", logs.String())
	}
}

// An upstream 401 whose body echoes secrets reaches the client only as a
// code, on both test endpoints and on both steps of the model test. The body
// is not logged either; only the SDK error text is. A 401 from the model
// check is an error, not auth_error (#1042), and its code does not blame the
// credentials either.
func TestProbeResponsesCarryNoUpstreamText(t *testing.T) {
	t.Parallel()

	const (
		upstreamMessage = "Incorrect API key provided: sk-live-ab12****yz89"
		echoedInput     = "USER-INPUT-ECHOED-BY-UPSTREAM"
	)
	cases := []struct {
		name       string
		rejectPath string
		endpoint   func(logger *slog.Logger, queries probeQueries) (func(*echo.Echo), string)
		wantStatus string
		wantCode   apperror.Code
	}{
		{
			name:       "provider test",
			rejectPath: "/models",
			endpoint: func(logger *slog.Logger, queries probeQueries) (func(*echo.Echo), string) {
				h := NewProvidersHandler(logger, providers.NewService(logger, queries, ""), nil)
				return h.Register, "/providers/" + queries.provider.ID.String() + "/test"
			},
			wantStatus: string(providers.TestStatusAuthError),
			wantCode:   apperror.CodeAgentProviderAuthFailed,
		},
		{
			name:       "model test, provider check",
			rejectPath: "/models",
			endpoint: func(logger *slog.Logger, queries probeQueries) (func(*echo.Echo), string) {
				h := NewModelsHandler(logger, models.NewService(logger, queries), nil)
				return h.Register, "/models/" + queries.model.ID.String() + "/test"
			},
			wantStatus: string(models.TestStatusAuthError),
			wantCode:   apperror.CodeAgentProviderAuthFailed,
		},
		{
			name:       "model test, model check",
			rejectPath: "/models/real-model",
			endpoint: func(logger *slog.Logger, queries probeQueries) (func(*echo.Echo), string) {
				h := NewModelsHandler(logger, models.NewService(logger, queries), nil)
				return h.Register, "/models/" + queries.model.ID.String() + "/test"
			},
			wantStatus: string(models.TestStatusError),
			wantCode:   apperror.CodeAgentProviderRequestRejected,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.rejectPath {
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "real-model"}}})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{"message": upstreamMessage, "type": "invalid_request_error", "code": "invalid_api_key"},
					"input": echoedInput,
				})
			}))
			t.Cleanup(upstream.Close)

			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			register, target := tc.endpoint(logger, newProbeQueries(upstream.URL))
			status, body := serveProbe(t, register, target)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", status, body)
			}
			fields := decodeProbeBody(t, body)
			if fields["code"] != string(tc.wantCode) || fields["status"] != tc.wantStatus || fields["reachable"] != true {
				t.Fatalf("body = %s, want status %s, reachable true, code %s", body, tc.wantStatus, tc.wantCode)
			}
			for _, leaked := range []string{"sk-live", "Incorrect API key", "invalid_api_key", echoedInput} {
				if strings.Contains(body, leaked) {
					t.Fatalf("body %s contains %q", body, leaked)
				}
			}
			if strings.Contains(logs.String(), echoedInput) {
				t.Fatalf("log %s contains the upstream body", logs.String())
			}
			if !strings.Contains(logs.String(), `"reason":"`+string(tc.wantCode)+`"`) {
				t.Fatalf("log %s does not record the cause", logs.String())
			}
		})
	}
}
