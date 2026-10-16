package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// probeTestQueries stubs the two rows Test needs; every other method
// nil-panics, keeping the probe path honest about its database footprint.
type probeTestQueries struct {
	dbstore.Queries
	model    sqlc.Model
	provider sqlc.Provider
}

func (q probeTestQueries) GetModelByID(context.Context, pgtype.UUID) (sqlc.Model, error) {
	return q.model, nil
}

func (q probeTestQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return q.provider, nil
}

func newProbeTestService(server *httptest.Server) (*Service, pgtype.UUID) {
	modelID := pgtype.UUID{Bytes: [16]byte{0x10, 0x87}, Valid: true}
	providerID := pgtype.UUID{Bytes: [16]byte{0x10, 0x87, 0x02}, Valid: true}
	return &Service{queries: probeTestQueries{
		model: sqlc.Model{
			ID:         modelID,
			ProviderID: providerID,
			ModelID:    "real-model",
			Type:       string(ModelTypeChat),
		},
		provider: sqlc.Provider{
			ID:         providerID,
			ClientType: string(ClientTypeOpenAICompletions),
			Config:     []byte(`{"api_key":"sk-test","base_url":"` + server.URL + `"}`),
		},
	}}, modelID
}

// #1087: when the provider does not implement the models list (404), the
// model test must fall through to the real-model generation probe instead
// of reporting "Invalid API key" — that probe is the only check able to
// settle such providers.
func TestTestFallsThroughToModelProbeWhenModelsListMissing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models", "/models/real-model":
			w.WriteHeader(http.StatusNotFound)
		case "/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "hi"}}},
			})
		default:
			t.Errorf("unexpected probe request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	service, modelID := newProbeTestService(server)
	resp, err := service.Test(context.Background(), modelID.String())
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if resp.Status != TestStatusOK {
		t.Fatalf("status = %q, want %q (cause: %v)", resp.Status, TestStatusOK, resp.Cause)
	}
}

// An auth failure on the models list is still reported as auth_error and
// must short-circuit before any generation request is attempted.
func TestTestModelsListAuthFailureStaysAuthError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected probe request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	service, modelID := newProbeTestService(server)
	resp, err := service.Test(context.Background(), modelID.String())
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if resp.Status != TestStatusAuthError {
		t.Fatalf("status = %q, want %q (cause: %v)", resp.Status, TestStatusAuthError, resp.Cause)
	}
}

// fakeProbeProvider answers the two probe calls with fixed results; every
// other sdk.Provider method nil-panics.
type fakeProbeProvider struct {
	sdk.Provider
	testErr        error
	modelErr       error
	unsupported    bool
	modelProbeRuns *int
}

func (p fakeProbeProvider) Test(context.Context) error {
	return p.testErr
}

func (p fakeProbeProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	*p.modelProbeRuns++
	if p.modelErr != nil {
		return nil, p.modelErr
	}
	return &sdk.ModelTestResult{Supported: !p.unsupported}, nil
}

// Locks the decision rules of probeChatModel: which provider-check results
// end the test early, and that a failed model check is error whatever its
// kind (#1042) with reachable taken from whether the provider answered.
func TestProbeChatModelOutcome(t *testing.T) {
	t.Parallel()

	apiErr := func(status int, kind sdk.ErrorKind) error {
		return &sdk.APIError{StatusCode: status, Kind: kind}
	}
	refused := fmt.Errorf("request failed: %w", &url.Error{Op: "Get", URL: "http://127.0.0.1:1/models", Err: errors.New("connect: connection refused")})
	cases := []struct {
		name          string
		testErr       error
		modelErr      error
		unsupported   bool
		wantStatus    TestStatus
		wantReachable bool
		wantCause     error
		wantModelRun  bool
	}{
		{name: "passed", wantStatus: TestStatusOK, wantReachable: true, wantModelRun: true},
		{name: "provider 401", testErr: apiErr(http.StatusUnauthorized, sdk.KindAuthentication), wantStatus: TestStatusAuthError, wantReachable: true},
		{name: "provider 403", testErr: apiErr(http.StatusForbidden, sdk.KindPermissionDenied), wantStatus: TestStatusAuthError, wantReachable: true},
		{name: "provider 404 falls through (#1087)", testErr: apiErr(http.StatusNotFound, sdk.KindUnknown), wantStatus: TestStatusOK, wantReachable: true, wantModelRun: true},
		{name: "provider 429 falls through", testErr: apiErr(http.StatusTooManyRequests, sdk.KindRateLimited), wantStatus: TestStatusOK, wantReachable: true, wantModelRun: true},
		{name: "provider 503 falls through", testErr: apiErr(http.StatusServiceUnavailable, sdk.KindServerError), wantStatus: TestStatusOK, wantReachable: true, wantModelRun: true},
		{name: "provider not reached", testErr: refused, wantStatus: TestStatusError, wantReachable: false},
		{name: "model 401 stays error (#1042)", modelErr: apiErr(http.StatusUnauthorized, sdk.KindAuthentication), wantStatus: TestStatusError, wantReachable: true, wantModelRun: true},
		{name: "model 429", modelErr: apiErr(http.StatusTooManyRequests, sdk.KindRateLimited), wantStatus: TestStatusError, wantReachable: true, wantModelRun: true},
		{name: "model not reached", modelErr: refused, wantStatus: TestStatusError, wantReachable: false, wantModelRun: true},
		{name: "model not supported", unsupported: true, wantStatus: TestStatusModelNotSupported, wantReachable: true, wantModelRun: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runs := 0
			provider := fakeProbeProvider{testErr: tc.testErr, modelErr: tc.modelErr, unsupported: tc.unsupported, modelProbeRuns: &runs}
			resp := probeChatModel(context.Background(), provider, "real-model")
			if resp.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (cause: %v)", resp.Status, tc.wantStatus, resp.Cause)
			}
			if resp.Reachable != tc.wantReachable {
				t.Fatalf("reachable = %v, want %v", resp.Reachable, tc.wantReachable)
			}
			if got := runs > 0; got != tc.wantModelRun {
				t.Fatalf("model probe ran = %v, want %v", got, tc.wantModelRun)
			}
			wantCause := tc.modelErr
			if wantCause == nil && !tc.wantModelRun {
				wantCause = tc.testErr
			}
			if wantCause == nil {
				if resp.Cause != nil {
					t.Fatalf("cause = %v, want nil", resp.Cause)
				}
				return
			}
			if !errors.Is(resp.Cause, wantCause) {
				t.Fatalf("cause = %v, want it to wrap %v", resp.Cause, wantCause)
			}
		})
	}
}

// An embedding probe failure is reachable only when the provider answered.
func TestTestEmbeddingModelEmptyVectorIsReachable(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("unexpected probe request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{}}},
		})
	}))
	t.Cleanup(server.Close)

	resp, err := (&Service{}).testEmbeddingModel(t.Context(), string(ClientTypeOpenAICompletions), server.URL, "sk-test", "embed-model", nil)
	if err != nil {
		t.Fatalf("testEmbeddingModel() error = %v", err)
	}
	if resp.Status != TestStatusError {
		t.Fatalf("status = %q, want %q", resp.Status, TestStatusError)
	}
	if !resp.Reachable {
		t.Fatalf("reachable = false, want true (cause: %v)", resp.Cause)
	}
	if !errors.Is(resp.Cause, ErrEmptyEmbeddingVector) {
		t.Fatalf("cause = %v, want %v", resp.Cause, ErrEmptyEmbeddingVector)
	}
}

func TestTestEmbeddingModelReachability(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedURL := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name          string
		baseURL       string
		wantReachable bool
	}{
		{"rejected", server.URL, true},
		{"not reached", closedURL, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp, err := (&Service{}).testEmbeddingModel(context.Background(), string(ClientTypeOpenAICompletions), tc.baseURL, "sk-test", "embed-model", nil)
			if err != nil {
				t.Fatalf("testEmbeddingModel() error = %v", err)
			}
			if resp.Status != TestStatusError {
				t.Fatalf("status = %q, want %q", resp.Status, TestStatusError)
			}
			if resp.Reachable != tc.wantReachable {
				t.Fatalf("reachable = %v, want %v (cause: %v)", resp.Reachable, tc.wantReachable, resp.Cause)
			}
			if resp.Cause == nil {
				t.Fatal("cause = nil, want the probe error")
			}
		})
	}
}
