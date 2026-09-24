package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func TestDiscoverIconsTheme(t *testing.T) {
	page, _ := url.Parse("https://example.com/docs/page")
	source := `<head><base href="/assets/"><link rel="icon" href="fallback.ico"><link rel="icon" media="(prefers-color-scheme: light)" href="light.svg"><link rel="icon" media="(prefers-color-scheme: dark)" href="dark.svg"></head>`
	got := discoverIcons(strings.NewReader(source), page)
	if got.Light != "https://example.com/assets/light.svg" || got.Dark != "https://example.com/assets/dark.svg" {
		t.Fatalf("%+v", got)
	}
	if got := discoverIcons(strings.NewReader(`<link rel="icon" href="javascript:alert(1)">`), page); got.Dark != "https://example.com/favicon.ico" {
		t.Fatal(got)
	}
	got = discoverIcons(strings.NewReader(`<link rel="icon" href="a.ico"><link rel="icon" type="image/svg+xml" href="adaptive.svg">`), page)
	if got.Light != "https://example.com/docs/adaptive.svg" || got.Dark != got.Light {
		t.Fatalf("%+v", got)
	}
}

func TestIconIPRestrictions(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "0.0.0.0", "192.0.0.8", "64:ff9b::a00:1", "::ffff:10.0.0.1"} {
		if publicIconIP(net.ParseIP(raw)) {
			t.Fatal(raw)
		}
	}
	// 198.18.0.0/15 is where fake-IP proxies map public hosts.
	for _, raw := range []string{"8.8.8.8", "198.18.0.21"} {
		if !publicIconIP(net.ParseIP(raw)) {
			t.Fatal(raw)
		}
	}
}

func TestDiscoverGitHubScriptTheme(t *testing.T) {
	page, _ := url.Parse("https://github.com/")
	source := `<link rel="icon" type="image/svg+xml" href="https://github.githubassets.com/favicons/favicon.svg" data-base-href="https://github.githubassets.com/favicons/favicon">`
	got := discoverIcons(strings.NewReader(source), page)
	if got.Light != "https://github.githubassets.com/favicons/favicon.svg" || got.Dark != "https://github.githubassets.com/favicons/favicon-dark.svg" {
		t.Fatalf("%+v", got)
	}
}

func TestIconURLRestrictions(t *testing.T) {
	for _, raw := range []string{"http://localhost/a", "http://127.0.0.1/a", "http://[::1]/a", "https://user:pass@example.com/a", "javascript:alert(1)", "https://example.com:8443/", "http://example.com:6379/"} {
		u, _ := url.Parse(raw)
		if validIconURL(u) {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{"https://example.com/", "https://example.com:443/", "http://example.com:80/"} {
		u, _ := url.Parse(raw)
		if !validIconURL(u) {
			t.Fatal(raw)
		}
	}
}

func TestSiteIconOrigin(t *testing.T) {
	got, ok := siteIconOrigin("https://Example.com/private/path?q=1#frag")
	if !ok || got != "https://example.com/" {
		t.Fatalf("%q %v", got, ok)
	}
	if _, ok := siteIconOrigin("http://10.0.0.1/"); ok {
		t.Fatal("private origin accepted")
	}
}

// The public-IP dialer cannot reach an httptest server, so the fetch is faked
// to check the handler contract: one fetch per origin across pages and
// concurrent callers, and cached misses that expire sooner than hits.
func TestSiteIconFetchesEachOriginOnce(t *testing.T) {
	var fetches atomic.Int32
	release := make(chan struct{})
	h := NewSiteIconHandler()
	now := time.Unix(0, 0)
	h.now = func() time.Time { return now }
	h.fetch = func(_ context.Context, origin string) (SiteIconResponse, bool) {
		fetches.Add(1)
		<-release
		if origin == "https://missing.example/" {
			return SiteIconResponse{}, false
		}
		return SiteIconResponse{Light: origin + "light.svg", Dark: origin + "dark.svg"}, true
	}
	get := func(target string) SiteIconResponse {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/site-icon?url="+url.QueryEscape(target), nil)
		if err := h.Get(echo.New().NewContext(req, rec)); err != nil {
			t.Error(err)
		}
		var got SiteIconResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return got
	}

	var wg sync.WaitGroup
	results := make([]SiteIconResponse, 8)
	for i := range results {
		wg.Add(1)
		go func() { defer wg.Done(); results[i] = get(fmt.Sprintf("https://example.com/page/%d", i)) }()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	want := SiteIconResponse{Light: "https://example.com/light.svg", Dark: "https://example.com/dark.svg"}
	for _, got := range results {
		if got != want {
			t.Fatalf("%+v", got)
		}
	}
	if got := get("https://example.com/another"); got != want || fetches.Load() != 1 {
		t.Fatalf("fetches = %d, got %+v", fetches.Load(), got)
	}

	if got := get("https://missing.example/"); got != (SiteIconResponse{}) {
		t.Fatalf("%+v", got)
	}
	get("https://missing.example/x")
	if fetches.Load() != 2 {
		t.Fatalf("miss not cached: fetches = %d", fetches.Load())
	}
	now = now.Add(siteIconFailureCacheTTL)
	get("https://missing.example/")
	get("https://example.com/")
	if fetches.Load() != 3 {
		t.Fatalf("miss should expire before hit: fetches = %d", fetches.Load())
	}
}

func TestSiteIconCacheIsBounded(t *testing.T) {
	h := NewSiteIconHandler()
	for i := range siteIconCacheMaxEntries + 10 {
		h.store(fmt.Sprintf("https://site%d.example/", i), SiteIconResponse{}, time.Hour)
	}
	if len(h.cache) > siteIconCacheMaxEntries {
		t.Fatalf("cache grew to %d", len(h.cache))
	}
}
