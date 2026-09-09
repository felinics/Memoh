package supermarket

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientPinsRequestsToConfiguredOrigin(t *testing.T) {
	var requested string
	client := NewClient("https://supermarket.example", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requested = req.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("{}")),
			Request:    req,
			Header:     make(http.Header),
		}, nil
	})})
	resp, err := client.Get(context.Background(), "/api/packages?q=docs", "application/json")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	_ = resp.Body.Close()
	if requested != "https://supermarket.example/api/packages?q=docs" {
		t.Fatalf("requested URL = %q", requested)
	}
	artifactResp, err := client.GetArtifact(context.Background(), "https://attacker.example/artifact", "application/gzip")
	if artifactResp != nil {
		_ = artifactResp.Body.Close()
	}
	if !errors.Is(err, ErrCrossOrigin) {
		t.Fatalf("cross-origin Artifact error = %v, want ErrCrossOrigin", err)
	}
}

func TestClientPreservesConfiguredBasePath(t *testing.T) {
	requested := make([]string, 0, 2)
	client := NewClient("https://gateway.example/memoh/supermarket", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requested = append(requested, req.URL.String())
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("{}")),
				Request:    req,
				Header:     make(http.Header),
			}, nil
		}),
	})
	apiResp, err := client.Get(context.Background(), "/api/packages?q=docs", "application/json")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	_ = apiResp.Body.Close()
	artifactResp, err := client.GetArtifact(context.Background(), "/api/artifacts/skill/digest", "application/gzip")
	if err != nil {
		t.Fatalf("GetArtifact() error = %v", err)
	}
	_ = artifactResp.Body.Close()
	want := []string{
		"https://gateway.example/memoh/supermarket/api/packages?q=docs",
		"https://gateway.example/memoh/supermarket/api/artifacts/skill/digest",
	}
	if len(requested) != len(want) || requested[0] != want[0] || requested[1] != want[1] {
		t.Fatalf("requested URLs = %#v, want %#v", requested, want)
	}
}

func TestClientForwardsHeadersAndRejectsCrossOriginRedirect(t *testing.T) {
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(redirectTarget.Close)

	var conditionalHeader string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conditionalHeader = req.Header.Get("If-None-Match")
		http.Redirect(w, req, redirectTarget.URL+"/artifact", http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	client := NewClient(origin.URL, origin.Client())
	headers := make(http.Header)
	headers.Set("If-None-Match", `"digest"`)
	resp, err := client.GetWithHeaders(context.Background(), "/api/artifacts/icon/digest", "image/png", headers)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrCrossOrigin) {
		t.Fatalf("cross-origin redirect error = %v, want ErrCrossOrigin", err)
	}
	if conditionalHeader != `"digest"` {
		t.Fatalf("If-None-Match = %q, want forwarded value", conditionalHeader)
	}
}

func TestClientRejectsInvalidBaseURL(t *testing.T) {
	for _, rawURL := range []string{"file:///tmp/supermarket", "https://supermarket.example?tenant=memoh"} {
		client := NewClient(rawURL, nil)
		resp, err := client.Get(context.Background(), "/api/packages", "application/json")
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, ErrInvalidBaseURL) {
			t.Fatalf("base URL %q error = %v, want ErrInvalidBaseURL", rawURL, err)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
