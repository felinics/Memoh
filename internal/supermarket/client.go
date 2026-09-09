package supermarket

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrInvalidBaseURL = errors.New("configured Supermarket URL is invalid")
	ErrCrossOrigin    = errors.New("supermarket request left the configured origin")
	ErrRedirectLimit  = errors.New("supermarket redirect limit exceeded")
)

// Client owns the outbound HTTP boundary to one configured Supermarket origin.
type Client struct {
	base       *url.URL
	httpClient *http.Client
	initErr    error
}

func NewClient(rawBaseURL string, httpClient *http.Client) *Client {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawBaseURL), "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" ||
		base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return &Client{initErr: ErrInvalidBaseURL}
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{base: base, httpClient: httpClient}
}

// Get sends a same-origin GET for an API path rooted at the configured base URL.
func (c *Client) Get(ctx context.Context, requestPath, accept string) (*http.Response, error) {
	return c.GetWithHeaders(ctx, requestPath, accept, nil)
}

// GetWithHeaders sends a same-origin GET with caller-supplied headers.
func (c *Client) GetWithHeaders(
	ctx context.Context,
	requestPath, accept string,
	headers http.Header,
) (*http.Response, error) {
	if c == nil || c.initErr != nil || c.base == nil {
		return nil, ErrInvalidBaseURL
	}
	requestURL, err := c.resolve(requestPath)
	if err != nil {
		return nil, ErrCrossOrigin
	}
	return c.get(ctx, requestURL, accept, headers)
}

// GetArtifact resolves a descriptor URL and rejects requests outside the
// configured Supermarket origin.
func (c *Client) GetArtifact(ctx context.Context, rawURL, accept string) (*http.Response, error) {
	if c == nil || c.initErr != nil || c.base == nil {
		return nil, ErrInvalidBaseURL
	}
	reference, err := url.Parse(rawURL)
	if err != nil {
		return nil, ErrCrossOrigin
	}
	var requestURL *url.URL
	if reference.IsAbs() {
		requestURL = reference
	} else {
		requestURL, err = c.resolve(rawURL)
	}
	if err != nil || !sameOrigin(requestURL, c.base) {
		return nil, ErrCrossOrigin
	}
	return c.get(ctx, requestURL, accept, nil)
}

func (c *Client) resolve(requestPath string) (*url.URL, error) {
	reference, err := url.Parse(requestPath)
	if err != nil || reference.IsAbs() || reference.Host != "" || reference.User != nil || reference.Fragment != "" {
		return nil, ErrCrossOrigin
	}
	joined, err := url.JoinPath(c.base.String(), reference.EscapedPath())
	if err != nil {
		return nil, err
	}
	requestURL, err := url.Parse(joined)
	if err != nil {
		return nil, err
	}
	requestURL.RawQuery = reference.RawQuery
	if !sameOrigin(requestURL, c.base) {
		return nil, ErrCrossOrigin
	}
	return requestURL, nil
}

func (c *Client) get(
	ctx context.Context,
	requestURL *url.URL,
	accept string,
	headers http.Header,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	client := *c.httpClient
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return ErrRedirectLimit
		}
		if !sameOrigin(next.URL, c.base) {
			return ErrCrossOrigin
		}
		return nil
	}
	resp, err := client.Do(req) //nolint:gosec // Client pins requests and redirects to configured Supermarket origin.
	if err != nil {
		return nil, err
	}
	if resp.Request == nil || !sameOrigin(resp.Request.URL, c.base) {
		_ = resp.Body.Close()
		return nil, ErrCrossOrigin
	}
	return resp, nil
}

func sameOrigin(candidate, base *url.URL) bool {
	return candidate != nil && base != nil && candidate.User == nil &&
		candidate.Scheme == base.Scheme && strings.EqualFold(candidate.Host, base.Host)
}
