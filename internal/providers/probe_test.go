package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/models"
)

func probeTestProvider(t *testing.T, handler http.HandlerFunc) TestResponse {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	sdkProvider := models.NewSDKProvider(server.URL, "sk-test", "", models.ClientTypeOpenAIResponses, 5*time.Second, server.Client())
	return TestSDKCredentials(context.Background(), sdkProvider)
}

func TestTestSDKProviderOK(t *testing.T) {
	resp := probeTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/models/__ping__":
			http.Error(w, `{"error":{"message":"model not found"}}`, http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/responses":
			http.Error(w, `{"error":{"message":"invalid model"}}`, http.StatusBadRequest)
		default:
			http.Error(w, "unexpected request "+r.Method+" "+r.URL.Path, http.StatusTeapot)
		}
	})
	if resp.Status != TestStatusOK || !resp.Reachable {
		t.Fatalf("response = %#v", resp)
	}
}

func TestTestSDKProviderAuthError(t *testing.T) {
	resp := probeTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	})
	if resp.Status != TestStatusAuthError || !resp.Reachable {
		t.Fatalf("response = %#v", resp)
	}
	if resp.Message == "" {
		t.Fatal("message is empty")
	}
}

func TestTestSDKProviderAuthErrorOnlyOnGeneration(t *testing.T) {
	resp := probeTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/models/__ping__":
			http.Error(w, `{"error":{"message":"model not found"}}`, http.StatusNotFound)
		default:
			http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
		}
	})
	if resp.Status != TestStatusAuthError || !resp.Reachable {
		t.Fatalf("response = %#v", resp)
	}
}

func TestTestSDKProviderUnreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	serverURL := server.URL
	client := server.Client()
	server.Close()
	sdkProvider := models.NewSDKProvider(serverURL, "sk-test", "", models.ClientTypeOpenAIResponses, 5*time.Second, client)
	resp := TestSDKCredentials(context.Background(), sdkProvider)
	if resp.Status != TestStatusError || resp.Reachable {
		t.Fatalf("response = %#v", resp)
	}
}
