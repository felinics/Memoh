package models

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"

	opencodego "github.com/felinics/twilight/provider/opencode/go"
	"github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/reasoning"
)

type modelSessionKey struct{}

// WithModelSession scopes provider session metadata to the owning conversation.
// Only providers that require it forward this value to the remote endpoint.
func WithModelSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, modelSessionKey{}, strings.TrimSpace(sessionID))
}

var openCodeGoRoutes = opencodego.New()

// ResolveModelClientType uses Twilight's routing table for multi-protocol
// providers, so prompt caching and media handling follow the actual wire protocol.
// Reasoning policy keeps the provider identity: sharing a protocol does not mean
// Go models accept Claude's adaptive flag or OpenAI's effort normalization.
func ResolveModelClientType(clientType, modelID string) string {
	if clientType != string(ClientTypeOpenCodeGo) {
		return clientType
	}
	// Twilight routes models outside its exception table to Completions and only
	// fails on an invalid route override, which Memoh never configures.
	protocol, _ := openCodeGoRoutes.ProtocolForModel(modelID)
	return string(protocol)
}

// newOpenCodeGoModel sends requests through Twilight's Go provider rather than a
// protocol adapter of Memoh's own, because the provider also adapts requests to
// how Go's routes behave. Claude-specific thinking configuration therefore does
// not apply; their controls come from the Go model catalog.
func newOpenCodeGoModel(cfg SDKModelConfig) *sdk.Model {
	// Memoh reads the wire protocol from the provider name for reasoning, prompt
	// caching, and media handling.
	name := ResolveModelClientType(cfg.ClientType, cfg.ModelID)
	provider := newOpenCodeGoProvider(cfg.BaseURL, cfg.APIKey, cfg.HTTPClient, name)
	provider.reasoning = cfg
	return &sdk.Model{
		ID:       cfg.ModelID,
		Provider: provider,
		Type:     sdk.ModelTypeChat,
	}
}

// Go accepts its catalog's tiers verbatim, including max. Models with explicit
// switches express off through thinking.type; Luna and Hy use effort "none".
func openCodeGoEffortParam(cfg SDKModelConfig) (string, bool) {
	rc := cfg.ReasoningConfig
	if rc == nil || cfg.ReasoningDialect == reasoning.DialectToggle || cfg.ReasoningDialect == reasoning.DialectBudget {
		return "", false
	}
	// Older Messages rows have no declared controls. Do not infer Claude's wire.
	if cfg.ReasoningDialect == "" && ResolveModelClientType(cfg.ClientType, cfg.ModelID) == string(ClientTypeAnthropicMessages) {
		return "", false
	}
	if rc.Active && rc.Effort != "" && rc.Effort != reasoning.EffortEnabled {
		return rc.Effort, true
	}
	if rc.Disabled && cfg.ReasoningOffSupport != reasoning.OffSupportAccepted && rc.OffEffort != "" {
		return rc.OffEffort, true
	}
	return "", false
}

func applyOpenCodeGoThinking(req *sdk.Request, cfg SDKModelConfig) {
	rc := cfg.ReasoningConfig
	if rc == nil || cfg.ReasoningOffSupport != reasoning.OffSupportAccepted {
		return
	}
	var options json.RawMessage
	switch {
	case rc.Disabled:
		options = json.RawMessage(`{"thinking":{"type":"disabled"}}`)
	case rc.Active && cfg.ReasoningDialect == reasoning.DialectBudget:
		// Reuse Memoh's established budget allowances; these are not native
		// effort tiers. Bounds come from the model catalog, not its name.
		budget := legacyAnthropicBudgetFor(rc.Effort)
		if cfg.ThinkingBudgetMin != nil {
			budget = max(budget, *cfg.ThinkingBudgetMin)
		}
		if cfg.ThinkingBudgetMax != nil {
			budget = min(budget, *cfg.ThinkingBudgetMax)
		}
		options = json.RawMessage(fmt.Sprintf(`{"thinking":{"type":"enabled","budget_tokens":%d}}`, budget))
	case rc.Active:
		options = json.RawMessage(`{"thinking":{"type":"enabled"}}`)
	default:
		return
	}
	// Memoh owns this namespace; Twilight forwards it to the selected protocol.
	// Clone the outer map because a request may share options with another turn.
	req.ProviderOptions = maps.Clone(req.ProviderOptions)
	if req.ProviderOptions == nil {
		req.ProviderOptions = make(map[string]json.RawMessage)
	}
	req.ProviderOptions[string(ClientTypeOpenCodeGo)] = options
}

func newOpenCodeGoProvider(baseURL, apiKey string, httpClient *http.Client, name string) *openCodeGoSessionProvider {
	opts := []opencodego.Option{
		opencodego.WithAPIKey(apiKey),
		opencodego.WithHTTPClient(httpClient),
		// The agent's HTTP clients do not add Memoh's User-Agent.
		opencodego.WithHeaders(map[string]string{"User-Agent": DefaultProviderUserAgent()}),
	}
	if baseURL != "" {
		opts = append(opts, opencodego.WithBaseURL(baseURL))
	}
	// Standalone jobs (memory extraction and probes) have no conversation owner.
	// Give each constructed model/job its own stable ID across tool steps.
	return &openCodeGoSessionProvider{Provider: opencodego.New(opts...), name: name, jobID: uuid.NewString()}
}

type openCodeGoSessionProvider struct {
	sdk.Provider
	name      string
	jobID     string
	reasoning SDKModelConfig
}

func (p *openCodeGoSessionProvider) Name() string { return p.name }

func (p *openCodeGoSessionProvider) sessionContext(ctx context.Context) context.Context {
	sessionID, _ := ctx.Value(modelSessionKey{}).(string)
	if sessionID == "" {
		sessionID = p.jobID
	}
	return sdk.WithRequestHeaders(ctx, map[string]string{opencodego.SessionHeader: sessionID})
}

func (p *openCodeGoSessionProvider) DoGenerate(ctx context.Context, req sdk.Request) (sdk.ModelResult, error) {
	return p.Provider.DoGenerate(p.sessionContext(ctx), p.requestWithReasoning(req))
}

func (p *openCodeGoSessionProvider) DoStream(ctx context.Context, req sdk.Request) (<-chan sdk.StreamPart, error) {
	return p.Provider.DoStream(p.sessionContext(ctx), p.requestWithReasoning(req))
}

// The runtime sees the transport name for cache and media handling, and its
// generic effort policy can normalize max or omit Messages effort. Apply the
// Go catalog decision at the provider boundary, with the full model config.
func (p *openCodeGoSessionProvider) requestWithReasoning(req sdk.Request) sdk.Request {
	if p.reasoning.ReasoningConfig != nil {
		req.ReasoningEffort = nil
		ApplyReasoningToRequest(&req, p.reasoning)
	}
	return req
}

func (p *openCodeGoSessionProvider) TestModel(ctx context.Context, modelID string) (*sdk.ModelTestResult, error) {
	return p.Provider.TestModel(p.sessionContext(ctx), modelID)
}
