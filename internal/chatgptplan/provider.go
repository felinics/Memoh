// Package chatgptplan implements the public Sign in with ChatGPT subscription
// protocol. It does not use Codex's private endpoint or account claims.
package chatgptplan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	responses "github.com/felinics/twilight/provider/openai/responses"
	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/openaicatalog"
)

const APIBaseURL = "https://api.openai.com/v1"

var (
	ErrNotConnected          = errors.New("chatgpt subscription is not connected")
	ErrInvalidAuthorization  = errors.New("invalid chatgpt authorization")
	ErrOwner                 = errors.New("chatgpt authorization belongs to another user")
	ErrEncryptionUnavailable = errors.New("chatgpt credential encryption is unavailable")
	ErrQuota                 = errors.New("chatgpt subscription usage limit reached")
	ErrUpstream              = errors.New("chatgpt service unavailable")
	ErrInterrupted           = errors.New("chatgpt response did not complete")
	ErrNotEligible           = errors.New("chatgpt subscription is unavailable for this account")
	ErrCapability            = errors.New("chatgpt subscription capability is unsupported")
	ErrPermission            = errors.New("chatgpt subscription permission is missing")
	ErrRefreshInvalid        = errors.New("chatgpt refresh token is unusable")
)

// TokenSource serializes renewal and invalidates only the rejected token.
type TokenSource interface {
	AccessToken(context.Context) (string, error)
	RejectAccessToken(context.Context, string) error
}

type Provider struct {
	inner  sdk.Provider
	client *http.Client
}

func NewProvider(token string, source TokenSource, baseClient *http.Client) *Provider {
	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	c := *baseClient
	rt := c.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	c.Transport = &transport{base: rt, token: token, source: source}
	// Never forward credentials to a configurable provider URL or a redirect.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Provider{inner: responses.New(responses.WithBaseURL(APIBaseURL), responses.WithHTTPClient(&c)), client: &c}
}
func (*Provider) Name() string { return "openai-chatgpt" }
func (p *Provider) ChatModel(id string) *sdk.Model {
	return &sdk.Model{ID: id, Provider: p, Type: sdk.ModelTypeChat}
}

func (p *Provider) ListModels(ctx context.Context) ([]sdk.Model, error) {
	catalog, err := p.ModelCatalog(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]sdk.Model, 0, len(catalog))
	for _, m := range catalog {
		models = append(models, sdk.Model{ID: m.Slug, DisplayName: m.DisplayName, Provider: p, Type: sdk.ModelTypeChat})
	}
	return models, nil
}

// ModelCatalog retains the capabilities omitted by sdk.Model.
func (p *Provider) ModelCatalog(ctx context.Context) ([]openaicatalog.Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, APIBaseURL+"/models?client_version="+openaicatalog.ClientVersion, nil)
	if err != nil {
		return nil, err
	}
	// #nosec G704 -- The URL is fixed to api.openai.com, the transport enforces it, and redirects are disabled.
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode)
	}
	var wire struct {
		Models []openaicatalog.Model `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&wire); err != nil {
		return nil, ErrUpstream
	}
	models := make([]openaicatalog.Model, 0, len(wire.Models))
	for _, m := range wire.Models {
		if m.Visibility == "list" && m.Slug != "" {
			models = append(models, m)
		}
	}
	return models, nil
}

func (p *Provider) Test(ctx context.Context) *sdk.ProviderTestResult {
	_, err := p.ListModels(ctx)
	if err != nil {
		return &sdk.ProviderTestResult{Status: sdk.ProviderStatusUnhealthy, Message: err.Error(), Error: err}
	}
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK, Message: "connected"}
}

func (p *Provider) TestModel(ctx context.Context, id string) (*sdk.ModelTestResult, error) {
	_, err := p.DoGenerate(ctx, sdk.Request{Model: id, Messages: []sdk.Message{{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "Reply OK."}}}}})
	if err != nil {
		return nil, err
	}
	return &sdk.ModelTestResult{Supported: true, Message: "supported"}, nil
}

func (p *Provider) DoGenerate(ctx context.Context, req sdk.Request) (sdk.ModelResult, error) {
	ch, err := p.DoStream(ctx, req)
	if err != nil {
		return sdk.ModelResult{}, err
	}
	return sdk.CollectStream(ctx, ch)
}

func (p *Provider) DoStream(ctx context.Context, req sdk.Request) (<-chan sdk.StreamPart, error) {
	state := &streamState{}
	ch, err := p.inner.DoStream(context.WithValue(ctx, streamStateKey{}, state), req)
	if err != nil {
		return nil, err
	}
	out := make(chan sdk.StreamPart)
	go func() {
		defer close(out)
		failed := false
		for part := range ch {
			switch part.(type) {
			case *sdk.ErrorPart:
				e := state.failure()
				if e == nil {
					e = ErrUpstream
				}
				if !failed && errors.Is(e, ErrNotConnected) && state.reject != nil {
					if rejectErr := state.reject(ctx); rejectErr != nil {
						e = errors.Join(e, rejectErr)
					}
				}
				failed = true
				part = &sdk.ErrorPart{Error: e}
			case *sdk.FinishStepPart, *sdk.FinishPart:
				if !state.success() || failed {
					if !failed {
						failed = true
						e := state.failure()
						if e == nil {
							e = ErrInterrupted
						}
						select {
						case out <- &sdk.ErrorPart{Error: e}:
						case <-ctx.Done():
							return
						}
					}
					continue
				}
			}
			select {
			case out <- part:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

type (
	streamStateKey struct{}
	streamState    struct {
		mu        sync.Mutex
		completed bool
		err       error
		requestID string
		reject    func(context.Context) error
	}
)

func (s *streamState) failure() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *streamState) success() bool  { s.mu.Lock(); defer s.mu.Unlock(); return s.completed }
func (s *streamState) fail(err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *streamState) observe(data []byte) {
	var event struct {
		wireError
		Type     string    `json:"type"`
		Error    wireError `json:"error"`
		Response struct {
			Error wireError `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &event) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch event.Type {
	case "response.completed":
		s.completed = true
	case "response.incomplete":
		s.err = ErrInterrupted
	case "response.failed", "error":
		failure := event.wireError
		if failure.Code == "" {
			failure = event.Error
		}
		if failure.Code == "" {
			failure = event.Response.Error
		}
		s.err = &UpstreamError{
			Status: http.StatusOK, Code: failure.Code, Param: failure.Param, RequestID: s.requestID,
			Shape: event.Type, kind: responseError(failure.Code, ErrUpstream),
		}
	}
}

type transport struct {
	base   http.RoundTripper
	token  string
	source TokenSource
}

func (t *transport) RoundTrip(req *http.Request) (result *http.Response, err error) {
	state, _ := req.Context().Value(streamStateKey{}).(*streamState)
	defer func() {
		if err != nil {
			state.fail(err)
		}
	}()
	if req.URL.Scheme != "https" || req.URL.Host != "api.openai.com" || (req.URL.Path != "/v1/responses" && req.URL.Path != "/v1/models") {
		return nil, ErrInvalidAuthorization
	}
	if req.Body != nil {
		defer func() { _ = req.Body.Close() }()
	}
	var body []byte
	if req.Method == http.MethodPost {
		var wire map[string]any
		if json.NewDecoder(io.LimitReader(req.Body, 16<<20)).Decode(&wire) != nil {
			return nil, ErrInvalidAuthorization
		}
		adaptRequest(wire)
		body, err = json.Marshal(wire)
		if err != nil {
			return nil, err
		}
	}
	// Authentication can be retried only before a stream opens, at most once.
	// Each attempt gets the current token and a fresh copy of the adapted body.
	for attempt := 0; ; attempt++ {
		token := t.token
		if t.source != nil {
			token, err = t.source.AccessToken(req.Context())
			if err != nil {
				return nil, err
			}
		}
		if token == "" {
			return nil, ErrNotConnected
		}
		r := req.Clone(req.Context())
		r.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			r.GetBody = nil
		}
		resp, callErr := t.base.RoundTrip(r)
		if callErr != nil {
			return nil, ErrUpstream
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			failure := readUpstreamError(resp)
			_ = resp.Body.Close()
			if errors.Is(failure, ErrNotConnected) && t.source != nil {
				if err := t.source.RejectAccessToken(req.Context(), token); err != nil {
					return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
				}
				if attempt == 0 {
					continue
				}
			}
			return nil, failure
		}
		if state != nil && r.URL.Path == "/v1/responses" && resp.StatusCode == http.StatusOK {
			state.requestID = resp.Header.Get("X-Request-Id")
			if t.source != nil {
				state.reject = func(ctx context.Context) error { return t.source.RejectAccessToken(ctx, token) }
			}
			resp.Body = &eventBody{ReadCloser: resp.Body, state: state}
		}
		return resp, nil
	}
}

func responseError(code string, fallback error) error {
	switch code {
	case "subscription_sharing_usage_limit_exceeded", "rate_limit_exceeded", "insufficient_quota", "usage_limit_reached", "token_budget_exceeded":
		return ErrQuota
	case "subscription_sharing_user_not_eligible":
		return ErrNotEligible
	case "subscription_sharing_unsupported_capability":
		return ErrCapability
	case "chatpass_v2_scope_not_authorized", "chatpass_v2_invalid_authorization_context", "subscription_sharing_route_not_supported":
		return ErrPermission
	case "subscription_sharing_invalid_user", "invalid_token", "invalid_api_key", "authentication_error":
		return ErrNotConnected
	case "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
		return ErrUpstream
	default:
		return fallback
	}
}

func statusError(status int) error {
	switch status {
	case http.StatusForbidden:
		return ErrPermission
	case http.StatusUnauthorized:
		return ErrNotConnected
	case http.StatusTooManyRequests:
		return ErrQuota
	default:
		return ErrUpstream
	}
}

func adaptRequest(w map[string]any) {
	w["stream"] = true
	w["store"] = false
	for _, k := range strings.Fields("background conversation max_output_tokens max_tool_calls metadata moderation multi_agent prompt prompt_cache_retention safety_identifier temperature top_logprobs top_p truncation user previous_response_id") {
		delete(w, k)
	}
	if input, ok := w["input"].([]any); ok {
		for _, v := range input {
			if item, ok := v.(map[string]any); ok {
				if item["role"] == "system" {
					item["role"] = "developer"
				}
				if item["type"] == "function_call" {
					item["namespace"] = "memoh"
				}
			}
		}
	}
	if list, ok := w["tools"].([]any); ok && len(list) > 0 {
		w["tools"] = []any{map[string]any{"type": "namespace", "name": "memoh", "description": "Memoh agent tools", "tools": list}}
	}
	if choice, ok := w["tool_choice"].(map[string]any); ok && choice["type"] == "function" {
		choice["namespace"] = "memoh"
	}
}

// Inspect SSE while preserving the SDK's parser and encrypted reasoning metadata.
// Only response.completed makes an inference successful, including Generate.
type eventBody struct {
	io.ReadCloser
	state   *streamState
	pending []byte
}

func (b *eventBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.pending = append(b.pending, p[:n]...)
	for {
		i := bytes.IndexByte(b.pending, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(b.pending[:i])
		if bytes.HasPrefix(line, []byte("data:")) {
			b.state.observe(bytes.TrimSpace(line[5:]))
		}
		b.pending = b.pending[i+1:]
	}
	if len(b.pending) > 16<<20 {
		return n, fmt.Errorf("%w: oversized event", ErrUpstream)
	}
	if err == io.EOF && !b.state.success() && b.state.failure() == nil {
		b.state.fail(ErrInterrupted)
	}
	return n, err
}
