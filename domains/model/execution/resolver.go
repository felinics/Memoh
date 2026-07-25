// Package execution resolves persisted model configuration into runtime-ready
// SDK models without exposing PostgreSQL rows to consumers.
package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	modeldomain "github.com/memohai/memoh/domains/model"
	executionport "github.com/memohai/memoh/domains/model/internal/port/execution"
)

var (
	ErrModelDisabled    = errors.New("model disabled or unavailable")
	ErrModelLookup      = errors.New("model lookup failed")
	ErrProviderDisabled = errors.New("model provider disabled")
)

// ModelSnapshot is the catalog subset execution needs. Defined here so
// execution does not import catalog concrete types.
type ModelSnapshot struct {
	ID         string
	ModelID    string
	ProviderID string
	Type       modeldomain.ModelType
	Enable     bool
	Config     modeldomain.ModelConfig
}

func (m ModelSnapshot) HasCompatibility(c string) bool {
	model := modeldomain.Model{Config: m.Config}
	return model.HasCompatibility(c)
}

// ModelReader is the consumer-owned catalog read port used by Resolver.
type ModelReader interface {
	GetByID(context.Context, string) (ModelSnapshot, error)
	ListEnabledByType(context.Context, modeldomain.ModelType) ([]ModelSnapshot, error)
}

// UserIDContext attaches a user id to ctx for provider credential resolution.
// cmd injects oauthctx.WithUserID; the zero value leaves ctx unchanged.
type UserIDContext func(ctx context.Context, userID string) context.Context

// CatalogModel is the safe subset of model and provider metadata shown to a
// model-selection consumer.
type CatalogModel struct {
	UUID         string
	ModelID      string
	ProviderName string
	Description  string
}

// Request identifies a chat model and optionally pins its previously observed
// identity. Expected values protect persisted subagent sessions from silently
// switching models after configuration changes.
type Request struct {
	ModelUUID        string
	ExpectedModelID  string
	ExpectedProvider string
	UserID           string
}

// ChatModel is a fully resolved chat execution. Credential material is kept in
// the SDK model and never exposed as a database or provider row.
type ChatModel struct {
	Model                 *sdk.Model
	UUID                  string
	ModelID               string
	ProviderName          string
	PromptCacheTTL        string
	ChatCompletionsCompat string
	ContextWindow         int
	SupportsImageInput    bool
	SupportsToolCall      bool
	secrets               []string
}

// SanitizeError redacts credentials used by this runtime while retaining the
// original error for errors.Is/errors.As classification.
func (m ChatModel) SanitizeError(err error) error {
	return SanitizeError(err, m.secrets...)
}

// ImageModel is an opaque image execution capability. Provider credentials and
// transport configuration remain inside Model and cannot be read by tools.
type ImageModel struct {
	config  imageModelConfig
	secrets []string
}

// Resolver turns Model-owned catalog state into runtime execution values.
type Resolver struct {
	models     ModelReader
	providers  executionport.ProviderSource
	withUserID UserIDContext
}

// ResolverOption configures optional Resolver behavior.
type ResolverOption func(*Resolver)

// WithUserIDContext injects the context enrichment used when resolving
// per-user provider credentials (typically oauthctx.WithUserID from cmd).
func WithUserIDContext(fn UserIDContext) ResolverOption {
	return func(r *Resolver) { r.withUserID = fn }
}

func NewResolver(models ModelReader, providers executionport.ProviderSource, opts ...ResolverOption) *Resolver {
	r := &Resolver{models: models, providers: providers}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Resolver) ListEnabledChatModels(ctx context.Context) ([]CatalogModel, error) {
	if r == nil || r.models == nil || r.providers == nil {
		return nil, errors.New("model catalog services not configured")
	}
	modelList, err := r.models.ListEnabledByType(ctx, modeldomain.ModelTypeChat)
	if err != nil {
		return nil, err
	}
	items := make([]CatalogModel, 0, len(modelList))
	for _, model := range modelList {
		provider, err := r.providers.LookupProvider(ctx, model.ProviderID)
		if err != nil {
			return nil, err
		}
		description := ""
		if model.Config.Description != nil {
			description = strings.TrimSpace(*model.Config.Description)
		}
		items = append(items, CatalogModel{
			UUID:         model.ID,
			ModelID:      model.ModelID,
			ProviderName: provider.Name,
			Description:  description,
		})
	}
	return items, nil
}

func (r *Resolver) ResolveChat(ctx context.Context, request Request) (ChatModel, error) {
	if r == nil || r.models == nil || r.providers == nil {
		return ChatModel{}, errors.New("model resolution services not configured")
	}
	modelInfo, err := r.models.GetByID(ctx, strings.TrimSpace(request.ModelUUID))
	if err != nil {
		return ChatModel{}, classifiedWrappedError{err: err, target: ErrModelLookup}
	}
	if modelInfo.Type != modeldomain.ModelTypeChat {
		return ChatModel{}, fmt.Errorf("model %s is not a chat model", modelInfo.ModelID)
	}
	if !modelInfo.Enable || (modelInfo.Config.CatalogAvailable != nil && !*modelInfo.Config.CatalogAvailable) {
		return ChatModel{}, classifiedError{
			message: fmt.Sprintf("chat model %s is disabled or unavailable", modelInfo.ModelID),
			target:  ErrModelDisabled,
		}
	}
	resolveCtx := ctx
	if r.withUserID != nil {
		resolveCtx = r.withUserID(ctx, request.UserID)
	}
	provider, err := r.providers.ResolveProvider(resolveCtx, modelInfo.ProviderID)
	if err != nil {
		return ChatModel{}, err
	}
	if !provider.Enable {
		return ChatModel{}, classifiedError{
			message: fmt.Sprintf("model provider %s is disabled", provider.Name),
			target:  ErrProviderDisabled,
		}
	}
	if expected := strings.TrimSpace(request.ExpectedProvider); expected != "" && provider.Name != expected {
		return ChatModel{}, fmt.Errorf("pinned model provider changed from %q to %q", expected, provider.Name)
	}
	if expected := strings.TrimSpace(request.ExpectedModelID); expected != "" && modelInfo.ModelID != expected {
		return ChatModel{}, fmt.Errorf("pinned model id changed from %q to %q", expected, modelInfo.ModelID)
	}

	chatCompletionsCompat := ResolveChatCompletionsCompat(
		provider.BaseURL,
		provider.ChatCompletionsCompat,
	)
	contextWindow := 0
	if modelInfo.Config.ContextWindow != nil {
		contextWindow = *modelInfo.Config.ContextWindow
	}
	return ChatModel{
		Model: NewSDKChatModel(SDKModelConfig{
			ModelID:               modelInfo.ModelID,
			ClientType:            string(provider.ClientType),
			APIKey:                provider.APIKey,
			CodexAccountID:        provider.CodexAccountID,
			BaseURL:               provider.BaseURL,
			ChatCompletionsCompat: chatCompletionsCompat,
		}),
		UUID:                  modelInfo.ID,
		ModelID:               modelInfo.ModelID,
		ProviderName:          provider.Name,
		PromptCacheTTL:        provider.PromptCacheTTL,
		ChatCompletionsCompat: chatCompletionsCompat,
		ContextWindow:         contextWindow,
		SupportsImageInput:    modelInfo.HasCompatibility(modeldomain.CompatVision),
		SupportsToolCall:      modelInfo.HasCompatibility(modeldomain.CompatToolCall),
		secrets:               compactSecrets(append([]string{provider.APIKey}, provider.SensitiveValues...)...),
	}, nil
}

func (r *Resolver) ResolveImage(ctx context.Context, modelUUID string) (ImageModel, error) {
	if r == nil || r.models == nil || r.providers == nil {
		return ImageModel{}, errors.New("model resolution services not configured")
	}
	modelInfo, err := r.models.GetByID(ctx, strings.TrimSpace(modelUUID))
	if err != nil {
		return ImageModel{}, fmt.Errorf("failed to load image model: %w", err)
	}
	if !modelInfo.Enable {
		return ImageModel{}, classifiedError{
			message: fmt.Sprintf("image model %s is disabled", modelInfo.ModelID),
			target:  ErrModelDisabled,
		}
	}
	if !modelInfo.HasCompatibility(modeldomain.CompatImageOutput) {
		return ImageModel{}, errors.New("configured model does not support image generation")
	}
	provider, err := r.providers.ResolveProvider(ctx, modelInfo.ProviderID)
	if err != nil {
		return ImageModel{}, fmt.Errorf("failed to load model provider: %w", err)
	}
	if !provider.Enable {
		return ImageModel{}, classifiedError{
			message: fmt.Sprintf("image model provider %s is disabled", provider.Name),
			target:  ErrProviderDisabled,
		}
	}
	return ImageModel{
		config: imageModelConfig{
			modelID:        modelInfo.ModelID,
			providerName:   provider.Name,
			clientType:     string(provider.ClientType),
			apiKey:         provider.APIKey,
			codexAccountID: provider.CodexAccountID,
			baseURL:        provider.BaseURL,
			promptCacheTTL: provider.PromptCacheTTL,
		},
		secrets: compactSecrets(append([]string{provider.APIKey}, provider.SensitiveValues...)...),
	}, nil
}

type classifiedError struct {
	message string
	target  error
}

func (e classifiedError) Error() string        { return e.message }
func (e classifiedError) Is(target error) bool { return target == e.target }

type classifiedWrappedError struct {
	err    error
	target error
}

func (e classifiedWrappedError) Error() string        { return e.err.Error() }
func (e classifiedWrappedError) Unwrap() error        { return e.err }
func (e classifiedWrappedError) Is(target error) bool { return target == e.target }
