package execution

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	modeldomain "github.com/memohai/memoh/domains/model"
)

type testUserIDKey struct{}

func withTestUserID(ctx context.Context, userID string) context.Context {
	if strings.TrimSpace(userID) == "" {
		return ctx
	}
	return context.WithValue(ctx, testUserIDKey{}, userID)
}

func testUserIDFromContext(ctx context.Context) string {
	userID, _ := ctx.Value(testUserIDKey{}).(string)
	return strings.TrimSpace(userID)
}

type fakeModelService struct {
	byID    map[string]ModelSnapshot
	enabled []ModelSnapshot
}

func (f *fakeModelService) GetByID(_ context.Context, id string) (ModelSnapshot, error) {
	model, ok := f.byID[id]
	if !ok {
		return ModelSnapshot{}, errors.New("model not found")
	}
	return model, nil
}

func (f *fakeModelService) ListEnabledByType(_ context.Context, _ modeldomain.ModelType) ([]ModelSnapshot, error) {
	return f.enabled, nil
}

type fakeProviderSource struct {
	providers    map[string]Provider
	resolveErr   error
	lookupCalls  int
	resolveCalls int
	userID       string
}

func (f *fakeProviderSource) LookupProvider(_ context.Context, id string) (ProviderDescriptor, error) {
	f.lookupCalls++
	provider, ok := f.providers[id]
	if !ok {
		return ProviderDescriptor{}, errors.New("provider not found")
	}
	return ProviderDescriptor{Name: provider.Name}, nil
}

func (f *fakeProviderSource) ResolveProvider(ctx context.Context, id string) (Provider, error) {
	f.resolveCalls++
	f.userID = testUserIDFromContext(ctx)
	if f.resolveErr != nil {
		return Provider{}, f.resolveErr
	}
	provider, ok := f.providers[id]
	if !ok {
		return Provider{}, errors.New("provider not found")
	}
	return provider, nil
}

func TestResolverBuildsCatalogAndChatExecution(t *testing.T) {
	providerID := "00000000-0000-0000-0000-000000000201"
	modelID := "00000000-0000-0000-0000-000000000301"
	description := "  coding model  "
	contextWindow := 128_000
	model := ModelSnapshot{
		ID:         modelID,
		ModelID:    "worker-model",
		ProviderID: providerID,
		Type:       modeldomain.ModelTypeChat,
		Enable:     true,
		Config: modeldomain.ModelConfig{
			Description:     &description,
			ContextWindow:   &contextWindow,
			Compatibilities: []string{modeldomain.CompatVision, modeldomain.CompatToolCall},
		},
	}
	provider := Provider{
		Name: "provider-a", ClientType: modeldomain.ClientTypeOpenAICompletions,
		Enable: true, BaseURL: "https://api.openai.com/v1", APIKey: "runtime-key",
		PromptCacheTTL: "24h", ChatCompletionsCompat: "strict", SensitiveValues: []string{"stored-key"},
	}
	modelService := &fakeModelService{byID: map[string]ModelSnapshot{modelID: model}, enabled: []ModelSnapshot{model}}
	providerSource := &fakeProviderSource{providers: map[string]Provider{providerID: provider}}
	resolver := NewResolver(modelService, providerSource, WithUserIDContext(withTestUserID))

	catalog, err := resolver.ListEnabledChatModels(context.Background())
	if err != nil {
		t.Fatalf("ListEnabledChatModels() error = %v", err)
	}
	if len(catalog) != 1 || catalog[0].Description != "coding model" || catalog[0].ProviderName != "provider-a" {
		t.Fatalf("catalog = %+v", catalog)
	}
	runtime, err := resolver.ResolveChat(context.Background(), Request{
		ModelUUID: modelID, ExpectedModelID: "worker-model", ExpectedProvider: "provider-a", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("ResolveChat() error = %v", err)
	}
	if runtime.Model == nil || runtime.UUID != modelID || runtime.ModelID != "worker-model" || runtime.ProviderName != "provider-a" {
		t.Fatalf("runtime identity = %+v", runtime)
	}
	if runtime.ContextWindow != contextWindow || !runtime.SupportsImageInput || !runtime.SupportsToolCall || providerSource.userID != "user-1" {
		t.Fatalf("runtime capabilities = %+v credential user = %q", runtime, providerSource.userID)
	}
	if runtime.PromptCacheTTL != "24h" || runtime.ChatCompletionsCompat == "" {
		t.Fatalf("runtime provider config = %+v", runtime)
	}
	if providerSource.lookupCalls != 1 || providerSource.resolveCalls != 1 {
		t.Fatalf("provider calls = lookup %d, resolve %d", providerSource.lookupCalls, providerSource.resolveCalls)
	}
}

func TestResolverClassifiesDisabledConfiguration(t *testing.T) {
	providerID := "00000000-0000-0000-0000-000000000201"
	modelID := "00000000-0000-0000-0000-000000000301"
	model := ModelSnapshot{
		ID: modelID, ModelID: "disabled", ProviderID: providerID,
		Type: modeldomain.ModelTypeChat, Enable: false,
	}
	resolver := NewResolver(
		&fakeModelService{byID: map[string]ModelSnapshot{modelID: model}},
		&fakeProviderSource{},
	)
	_, err := resolver.ResolveChat(context.Background(), Request{ModelUUID: modelID})
	if !errors.Is(err, ErrModelDisabled) || err.Error() != "chat model disabled is disabled or unavailable" {
		t.Fatalf("disabled error = %v", err)
	}
}

func TestResolverSanitizesCredentialErrorsAndImageRuntimeIsOpaque(t *testing.T) {
	providerID := "00000000-0000-0000-0000-000000000201"
	modelID := "00000000-0000-0000-0000-000000000301"
	secret := "secret-1234567890"
	model := ModelSnapshot{
		ID: modelID, ModelID: "image-model", ProviderID: providerID,
		Type: modeldomain.ModelTypeChat, Enable: true,
		Config: modeldomain.ModelConfig{Compatibilities: []string{modeldomain.CompatImageOutput}},
	}
	resolver := NewResolver(
		&fakeModelService{byID: map[string]ModelSnapshot{modelID: model}},
		&fakeProviderSource{resolveErr: SanitizeError(errors.New("credential rejected: "+secret), secret)},
	)
	_, err := resolver.ResolveImage(context.Background(), modelID)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("credential error was not sanitized: %v", err)
	}
	for _, field := range reflect.VisibleFields(reflect.TypeOf(ImageModel{})) {
		if field.IsExported() {
			t.Fatalf("ImageModel exposes field %s; execution must remain opaque", field.Name)
		}
	}
}
