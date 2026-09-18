package models

import (
	"context"
	"strings"

	opencodego "github.com/felinics/twilight/provider/opencode/go"
	"github.com/felinics/twilight/sdk"
	"github.com/google/uuid"
)

const openCodeGoBaseURL = "https://opencode.ai/zen/go/v1"

type modelSessionKey struct{}

// WithModelSession scopes provider session metadata to the owning conversation.
// Only providers that require it forward this value to the remote endpoint.
func WithModelSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, modelSessionKey{}, strings.TrimSpace(sessionID))
}

// ResolveModelClientType uses Twilight's routing catalog for multi-protocol
// providers, so reasoning and prompt caching follow the actual wire protocol.
func ResolveModelClientType(clientType, modelID string) string {
	if clientType == string(ClientTypeOpenCodeGo) {
		if protocol, err := opencodego.New().ProtocolForModel(modelID); err == nil {
			return string(protocol)
		}
	}
	return clientType
}

func newOpenCodeGoModel(cfg SDKModelConfig) *sdk.Model {
	if cfg.BaseURL == "" {
		cfg.BaseURL = openCodeGoBaseURL
	}
	provider := opencodego.New(
		opencodego.WithAPIKey(cfg.APIKey),
		opencodego.WithBaseURL(cfg.BaseURL),
		opencodego.WithHTTPClient(cfg.HTTPClient),
	)
	model := provider.ChatModel(cfg.ModelID)
	if protocol, err := provider.ProtocolForModel(cfg.ModelID); err == nil {
		// Keep Memoh's per-model thinking configuration when constructing the
		// protocol delegate. Twilight owns the model-to-protocol mapping.
		cfg.ClientType = string(protocol)
		model = NewSDKChatModel(cfg)
	}
	// Unknown routes retain Twilight's explicit error; never guess a protocol.
	model.Provider = withOpenCodeGoSession(model.Provider)
	return model
}

type openCodeGoSessionProvider struct {
	sdk.Provider
	jobID string
}

func withOpenCodeGoSession(provider sdk.Provider) sdk.Provider {
	// Standalone jobs (memory extraction and probes) have no conversation owner.
	// Give each constructed model/job its own stable ID across SDK tool steps.
	return &openCodeGoSessionProvider{Provider: provider, jobID: uuid.NewString()}
}

func (p *openCodeGoSessionProvider) sessionContext(ctx context.Context) context.Context {
	sessionID, _ := ctx.Value(modelSessionKey{}).(string)
	if sessionID == "" {
		sessionID = p.jobID
	}
	return sdk.WithRequestHeaders(ctx, map[string]string{
		opencodego.SessionHeader: sessionID,
		"User-Agent":             DefaultProviderUserAgent(),
	})
}

func (p *openCodeGoSessionProvider) DoGenerate(ctx context.Context, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
	return p.Provider.DoGenerate(p.sessionContext(ctx), params)
}

func (p *openCodeGoSessionProvider) DoStream(ctx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
	return p.Provider.DoStream(p.sessionContext(ctx), params)
}

func (p *openCodeGoSessionProvider) TestModel(ctx context.Context, modelID string) (*sdk.ModelTestResult, error) {
	return p.Provider.TestModel(p.sessionContext(ctx), modelID)
}
