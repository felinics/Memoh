package wecom

import (
	"testing"
	"time"
)

func TestCallbackContextCache_PutGet(t *testing.T) {
	cache := newCallbackContextCache(1 * time.Hour)
	cache.Put("config-a", "m1", callbackContext{ReqID: "r1"})
	got, ok := cache.Get("config-a", "m1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.ReqID != "r1" {
		t.Fatalf("unexpected req id: %q", got.ReqID)
	}
}

func TestCallbackContextCache_Expires(t *testing.T) {
	cache := newCallbackContextCache(1 * time.Second)
	cache.Put("config-a", "m1", callbackContext{
		ReqID:     "r1",
		CreatedAt: time.Now().Add(-2 * time.Second),
	})
	if _, ok := cache.Get("config-a", "m1"); ok {
		t.Fatal("expected cache miss due to expiry")
	}
}

func TestCallbackContextCacheIsScopedByConfig(t *testing.T) {
	cache := newCallbackContextCache(time.Hour)
	cache.Put("config-a", "shared-message", callbackContext{ReqID: "request-a"})
	cache.Put("config-b", "shared-message", callbackContext{ReqID: "request-b"})

	first, ok := cache.Get("config-a", "shared-message")
	if !ok || first.ReqID != "request-a" {
		t.Fatalf("config-a callback = %#v, found=%v", first, ok)
	}
	second, ok := cache.Get("config-b", "shared-message")
	if !ok || second.ReqID != "request-b" {
		t.Fatalf("config-b callback = %#v, found=%v", second, ok)
	}
}

func TestWeComClientCacheKeyIsScopedByConfig(t *testing.T) {
	first := wecomClientCacheKey("config-a", "shared-bot")
	if second := wecomClientCacheKey("config-b", "shared-bot"); second == first {
		t.Fatal("different configs must not share a WeCom client cache key")
	}
}
