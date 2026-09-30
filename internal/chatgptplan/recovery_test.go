package chatgptplan

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
)

func recoverySession(t *testing.T) (*SessionService, *sessionQueries) {
	t.Helper()
	s, q, _ := newSessionFixture(t)
	data := session{ClientID: "oaiapp_test", Subject: "account", Scope: scopes, ExpiresAt: time.Now().Add(time.Hour), Tokens: Tokens{AccessToken: "old-access", RefreshToken: "old-refresh", IDToken: "id", TokenType: "Bearer", Scope: scopes, ExpiresIn: 3600}}
	if err := s.save(context.Background(), q, q.row, &data); err != nil {
		t.Fatal(err)
	}
	return s, q
}

func renewalResponse() *http.Response {
	body, _ := json.Marshal(Tokens{AccessToken: "new-access", RefreshToken: "new-refresh", TokenType: "Bearer", ExpiresIn: 3600})
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body)))}
}

func invalidAccessResponse() *http.Response {
	return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"X-Request-Id": {"request-rejected"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_token","param":"authorization","message":"SECRET"}}`))}
}

func TestRejectedTokenRefreshIsSharedAndStaleRejectionsAreHarmless(t *testing.T) {
	s, q := recoverySession(t)
	ctx := context.Background()
	source := s.TokenSource(q.row.ProviderID.String())
	var refreshes atomic.Int32
	s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		refreshes.Add(1)
		return renewalResponse(), nil
	})
	p := NewProvider("", source, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "Bearer old-access" {
			return invalidAccessResponse(), nil
		}
		if r.Header.Get("Authorization") != "Bearer new-access" {
			t.Error("unexpected credential")
		}
		return streamResponse(`{"models":[{"slug":"test","visibility":"list"}]}`), nil
	})})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := p.ModelCatalog(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes.Load())
	}
	if err := source.RejectAccessToken(ctx, "old-access"); err != nil {
		t.Fatal(err)
	}
	if token, err := source.AccessToken(ctx); err != nil || token != "new-access" {
		t.Fatalf("stale rejection affected current token: %v", err)
	}
	status, err := s.Status(ctx, q.row.ProviderID.String())
	if err != nil || !status.Configured || status.NeedsRecovery {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
}

func TestRejectedTokenRefreshFailureKeepsCorrectRecoveryState(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		want       error
		configured bool
	}{
		{"terminal", `{"error":"invalid_grant"}`, http.StatusBadRequest, ErrNotConnected, false},
		{"temporary", `{"detail":"SECRET"}`, http.StatusServiceUnavailable, ErrUpstream, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, q := recoverySession(t)
			ctx := context.Background()
			s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})
			calls := 0
			p := NewProvider("", s.TokenSource(q.row.ProviderID.String()), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return invalidAccessResponse(), nil })})
			if _, err := p.ModelCatalog(ctx); !errors.Is(err, test.want) {
				t.Fatalf("error = %v", err)
			}
			status, err := s.Status(ctx, q.row.ProviderID.String())
			if err != nil || status.Configured != test.configured || status.NeedsRecovery != test.configured || status.ClientID != "oaiapp_test" {
				t.Fatalf("status = %+v, err = %v", status, err)
			}
			if calls != 1 {
				t.Fatalf("reused rejected token: %d calls", calls)
			}
			data, err := s.open(q.row)
			if err != nil || data.Tokens.AccessToken != "" {
				t.Fatal("rejected access token retained")
			}
			if test.configured {
				if data.Tokens.RefreshToken != "old-refresh" {
					t.Fatal("temporary error discarded renewable session")
				}
				s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return renewalResponse(), nil })
				if token, err := s.AccessToken(ctx, q.row.ProviderID.String()); err != nil || token != "new-access" {
					t.Fatalf("retry did not recover: %v", err)
				}
			}
		})
	}
}

func TestInferenceRetriesOnceWithFreshBodyAndStopsOnRepeatedRejection(t *testing.T) {
	for _, rejectAgain := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovered", true: "rejected again"}[rejectAgain], func(t *testing.T) {
			s, q := recoverySession(t)
			refreshes, calls := 0, 0
			s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { refreshes++; return renewalResponse(), nil })
			p := NewProvider("", s.TokenSource(q.row.ProviderID.String()), &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var wire map[string]any
				if json.NewDecoder(r.Body).Decode(&wire) != nil || wire["store"] != false || wire["stream"] != true || wire["model"] != "test" {
					t.Error("retry lost adapted body")
				}
				if calls == 1 || rejectAgain {
					return invalidAccessResponse(), nil
				}
				if r.Header.Get("Authorization") != "Bearer new-access" {
					t.Error("retry did not use replacement")
				}
				return streamResponse("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\"}}\n\n"), nil
			})})
			_, err := p.DoGenerate(context.Background(), sdk.Request{Model: "test", Messages: []sdk.Message{sdk.UserMessage("Hello")}})
			if rejectAgain && !errors.Is(err, ErrNotConnected) || !rejectAgain && err != nil {
				t.Fatalf("error = %v", err)
			}
			if calls != 2 || refreshes != 1 {
				t.Fatalf("calls=%d refreshes=%d", calls, refreshes)
			}
			status, statusErr := s.Status(context.Background(), q.row.ProviderID.String())
			if statusErr != nil || status.NeedsRecovery != rejectAgain {
				t.Fatalf("status = %+v, err = %v", status, statusErr)
			}
		})
	}
}

func TestMidstreamAuthenticationFailureInvalidatesWithoutReplaying(t *testing.T) {
	s, q := recoverySession(t)
	calls := 0
	p := NewProvider("", s.TokenSource(q.row.ProviderID.String()), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return streamResponse("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"invalid_token\"}}}\n\n"), nil
	})})
	_, err := p.DoGenerate(context.Background(), sdk.Request{Model: "test", Messages: []sdk.Message{sdk.UserMessage("Hi")}})
	if !errors.Is(err, ErrNotConnected) || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
	status, err := s.Status(context.Background(), q.row.ProviderID.String())
	if err != nil || !status.NeedsRecovery {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestPermissionAndQuotaFailuresDoNotInvalidateOrRefresh(t *testing.T) {
	for _, status := range []int{403, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s, q := recoverySession(t)
			p := NewProvider("", s.TokenSource(q.row.ProviderID.String()), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"detail":"private"}`))}, nil
			})})
			if _, err := p.ModelCatalog(context.Background()); err == nil {
				t.Fatal("expected refusal")
			}
			data, err := s.open(q.row)
			if err != nil || data.Tokens.AccessToken != "old-access" || data.Tokens.RefreshToken != "old-refresh" {
				t.Fatal("non-authentication failure changed credentials")
			}
		})
	}
}
