package execution

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewProviderHTTPClientWithoutTimeoutKeepsStreamingFriendlyBehavior(t *testing.T) {
	client := NewProviderHTTPClient(0)
	if client == nil {
		t.Fatal("expected client")
		return
	}
	if client.Timeout != 0 {
		t.Fatalf("expected no client timeout, got %s", client.Timeout)
	}

	uaTransport, ok := client.Transport.(*providerUserAgentRoundTripper)
	if !ok {
		t.Fatalf("expected *providerUserAgentRoundTripper, got %T", client.Transport)
	}

	transport, ok := uaTransport.base.(*http.Transport)
	if !ok {
		t.Fatalf("expected base *http.Transport, got %T", uaTransport.base)
	}
	if transport.TLSHandshakeTimeout < DefaultProviderTLSHandshakeTimeout {
		t.Fatalf("expected TLS handshake timeout >= %s, got %s", DefaultProviderTLSHandshakeTimeout, transport.TLSHandshakeTimeout)
	}
}

func TestNewProviderHTTPClientWithTimeout(t *testing.T) {
	timeout := 45 * time.Second
	client := NewProviderHTTPClient(timeout)
	if client == nil {
		t.Fatal("expected client")
		return
	}
	if client.Timeout != timeout {
		t.Fatalf("expected timeout %s, got %s", timeout, client.Timeout)
	}
}

func TestNewProviderHTTPClientSetsMemohUserAgent(t *testing.T) {
	gotCh := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		gotCh <- req.Header.Get("User-Agent")
	}))
	defer server.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	resp, err := NewProviderHTTPClient(time.Second).Do(req) //nolint:gosec // Test request targets an httptest server URL.
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := <-gotCh; got != DefaultProviderUserAgent() {
		t.Fatalf("expected user agent %q, got %q", DefaultProviderUserAgent(), got)
	}
}

func TestNewProviderHTTPClientPreservesExplicitUserAgent(t *testing.T) {
	const explicitUA = "GitHubCopilotChat/0.38.2"

	gotCh := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		gotCh <- req.Header.Get("User-Agent")
	}))
	defer server.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("User-Agent", explicitUA)

	resp, err := NewProviderHTTPClient(time.Second).Do(req) //nolint:gosec // Test request targets an httptest server URL.
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := <-gotCh; got != explicitUA {
		t.Fatalf("expected explicit user agent %q, got %q", explicitUA, got)
	}
}
