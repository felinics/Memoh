package qq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestQQAccessTokenAcceptsStringExpiresIn(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/getAppAccessToken" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "token-1",
			"expires_in":   "7200",
		})
	}))
	defer server.Close()

	client := &qqClient{
		appID:        "1024",
		clientSecret: "secret",
		httpClient:   server.Client(),
		tokenURL:     server.URL + "/app/getAppAccessToken",
		msgSeq:       make(map[string]int),
	}

	token, err := client.accessToken(context.Background())
	if err != nil {
		t.Fatalf("access token: %v", err)
	}
	if token != "token-1" {
		t.Fatalf("unexpected token: %q", token)
	}
	if remaining := time.Until(client.expiresAt); remaining < 7100*time.Second || remaining > 7200*time.Second {
		t.Fatalf("unexpected token ttl: %s", remaining)
	}
}

func TestDiscoverSelfRequiresAccessToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/getAppAccessToken" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "token-1",
			"expires_in":   7200,
		})
	}))
	defer server.Close()

	adapter := NewQQAdapter(nil)
	adapter.httpClient = server.Client()
	adapter.tokenURL = server.URL + "/app/getAppAccessToken"
	identity, externalID, err := adapter.DiscoverSelf(context.Background(), map[string]any{
		"appId":        "1024",
		"clientSecret": "secret",
	})
	if err != nil {
		t.Fatalf("DiscoverSelf error = %v", err)
	}
	if externalID != "1024" {
		t.Fatalf("external id = %q, want 1024", externalID)
	}
	if identity["app_id"] != "1024" {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestDiscoverSelfRejectsTokenError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid secret"}`))
	}))
	defer server.Close()

	adapter := NewQQAdapter(nil)
	adapter.httpClient = server.Client()
	adapter.tokenURL = server.URL + "/app/getAppAccessToken"
	_, _, err := adapter.DiscoverSelf(context.Background(), map[string]any{
		"appId":        "1024",
		"clientSecret": "bad",
	})
	if err == nil {
		t.Fatal("expected DiscoverSelf to fail")
	}
}

func TestQQSendStreamShardPostsReplacePayload(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token-1", "expires_in": 7200})
		case "/v2/users/user-openid/stream_messages":
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sm-1", "remain_msg_len": 4096})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &qqClient{
		appID:        "1024",
		clientSecret: "secret",
		httpClient:   server.Client(),
		tokenURL:     server.URL + "/app/getAppAccessToken",
		apiBaseURL:   server.URL,
		msgSeq:       make(map[string]int),
	}

	resp, err := client.sendStreamShard(context.Background(), "user-openid", "msg-1", qqStreamShardRequest{
		StreamMsgID: "sm-1",
		Index:       3,
		InputState:  qqStreamInputGenerating,
		ContentRaw:  "累计文本",
	})
	if err != nil {
		t.Fatalf("send stream shard: %v", err)
	}
	if resp.ID != "sm-1" || resp.RemainMsgLen != 4096 {
		t.Fatalf("unexpected response: %+v", resp)
	}

	want := map[string]any{
		"input_mode":    "replace",
		"input_state":   float64(qqStreamInputGenerating),
		"index":         float64(3),
		"content_type":  "markdown",
		"content_raw":   "累计文本",
		"stream_msg_id": "sm-1",
		"msg_id":        "msg-1",
		"msg_seq":       float64(1),
	}
	for key, wantValue := range want {
		if captured[key] != wantValue {
			t.Fatalf("body[%s] = %v, want %v (full body: %v)", key, captured[key], wantValue, captured)
		}
	}
}

func TestQQRetriesOnceAfterUnauthorized(t *testing.T) {
	t.Parallel()

	var tokenCalls, apiCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			tokenCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token", "expires_in": 7200})
		case "/gateway":
			if apiCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"url": "wss://gateway"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &qqClient{
		appID:        "1024",
		clientSecret: "secret",
		httpClient:   server.Client(),
		apiBaseURL:   server.URL,
		tokenURL:     server.URL + "/app/getAppAccessToken",
		msgSeq:       make(map[string]int),
	}
	gateway, err := client.gatewayURL(context.Background())
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	if gateway != "wss://gateway" || apiCalls.Load() != 2 || tokenCalls.Load() != 2 {
		t.Fatalf("gateway=%q api=%d token=%d, want one refresh and one retry", gateway, apiCalls.Load(), tokenCalls.Load())
	}
}

func TestQQDoesNotRetryOtherStatuses(t *testing.T) {
	t.Parallel()

	var apiCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token", "expires_in": 7200})
			return
		}
		apiCalls.Add(1)
		// The body mentions 401 to prove the decision reads the status code.
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("status=401"))
	}))
	defer server.Close()

	client := &qqClient{
		appID:        "1024",
		clientSecret: "secret",
		httpClient:   server.Client(),
		apiBaseURL:   server.URL,
		tokenURL:     server.URL + "/app/getAppAccessToken",
		msgSeq:       make(map[string]int),
	}
	if _, err := client.gatewayURL(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if calls := apiCalls.Load(); calls != 1 {
		t.Fatalf("api calls = %d, want 1", calls)
	}
}
