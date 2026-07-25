package common

import (
	"context"
	"net/http"
	"testing"
)

func TestParseHTTPProxyConfigExtractsCredentialsFromURL(t *testing.T) {
	t.Parallel()

	cfg, err := ParseHTTPProxyConfig(map[string]any{
		"httpProxyUrl": "http://proxy-user:" + "proxy-pass" + "@sztu.cc:3128",
	})
	if err != nil {
		t.Fatalf("ParseHTTPProxyConfig() error = %v", err)
	}
	if cfg.URL != "http://proxy-user:"+"proxy-pass"+"@sztu.cc:3128" {
		t.Fatalf("unexpected proxy URL: %q", cfg.URL)
	}
}

func TestNewHTTPClientExplicitProxyOverridesEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env-proxy:18080")

	client, err := NewHTTPClient(0, HTTPProxyConfig{
		URL: "http://memoh:" + "secret" + "@config-proxy:3128",
	})
	if err != nil {
		t.Fatalf("NewHTTPClient() error = %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.weixin.qq.com/cgi-bin/stable_token", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("transport.Proxy() error = %v", err)
	}
	if proxyURL == nil {
		t.Fatal("expected explicit proxy URL")
		return
	}
	if proxyURL.Host != "config-proxy:3128" {
		t.Fatalf("unexpected proxy host: %q", proxyURL.Host)
	}
	if proxyURL.User == nil || proxyURL.User.Username() != "memoh" {
		t.Fatalf("unexpected proxy user: %#v", proxyURL.User)
	}
	password, ok := proxyURL.User.Password()
	if !ok || password != "secret" {
		t.Fatalf("unexpected proxy password: %q", password)
	}
}

func TestNewHTTPClientFallsBackToEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env-proxy:18080")

	client, err := NewHTTPClient(0, HTTPProxyConfig{})
	if err != nil {
		t.Fatalf("NewHTTPClient() error = %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.telegram.org/bot123/getMe", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("transport.Proxy() error = %v", err)
	}
	if proxyURL == nil || proxyURL.Host != "env-proxy:18080" {
		t.Fatalf("expected env proxy, got %#v", proxyURL)
	}
}
