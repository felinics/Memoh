package http

import "testing"

func TestResolveListenAddrUsesConfigured(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")

	got := ResolveListenAddr(":8080")
	if got.String() != ":8080" {
		t.Fatalf("ResolveListenAddr() = %q, want :8080", got)
	}
}

func TestResolveListenAddrPrefersHTTPAddrEnv(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:9090")

	got := ResolveListenAddr(":8080")
	if got.String() != "127.0.0.1:9090" {
		t.Fatalf("ResolveListenAddr() = %q, want 127.0.0.1:9090", got)
	}
}
