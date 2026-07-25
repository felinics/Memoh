package weixin

import (
	"testing"
	"time"
)

func TestContextTokenCache_PutGet(t *testing.T) {
	cache := newContextTokenCache(1 * time.Hour)

	cache.Put("user1", "token1")
	got, ok := cache.Get("user1")
	if !ok {
		t.Fatal("expected to find token")
	}
	if got != "token1" {
		t.Errorf("token = %q, want %q", got, "token1")
	}
}

func TestContextTokenCache_Miss(t *testing.T) {
	cache := newContextTokenCache(1 * time.Hour)

	_, ok := cache.Get("nonexistent")
	if ok {
		t.Error("expected miss for nonexistent key")
	}
}

func TestContextTokenCache_EmptyKey(t *testing.T) {
	cache := newContextTokenCache(1 * time.Hour)

	cache.Put("", "token1")
	_, ok := cache.Get("")
	if ok {
		t.Error("expected miss for empty key")
	}
}

func TestContextTokenCache_EmptyToken(t *testing.T) {
	cache := newContextTokenCache(1 * time.Hour)

	cache.Put("user1", "")
	_, ok := cache.Get("user1")
	if ok {
		t.Error("expected miss for empty token")
	}
}

func TestContextTokenCache_Overwrite(t *testing.T) {
	cache := newContextTokenCache(1 * time.Hour)

	cache.Put("user1", "token1")
	cache.Put("user1", "token2")
	got, ok := cache.Get("user1")
	if !ok {
		t.Fatal("expected to find token")
	}
	if got != "token2" {
		t.Errorf("token = %q, want %q", got, "token2")
	}
}

func TestContextTokenCache_Expiry(t *testing.T) {
	cache := newContextTokenCache(1 * time.Millisecond)

	cache.Put("user1", "token1")
	time.Sleep(5 * time.Millisecond)
	_, ok := cache.Get("user1")
	if ok {
		t.Error("expected miss after expiry")
	}
}
