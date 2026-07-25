package builtin

import (
	"context"
	"errors"
	"strings"
	"testing"

	team "github.com/memohai/memoh/domains/iam/team"
	memport "github.com/memohai/memoh/domains/memory/internal/port"
	memreg "github.com/memohai/memoh/domains/memory/registry"
)

type embeddingModelResolverFake struct {
	spec       EmbeddingModelSpec
	resolveErr error
	enabled    bool
	enabledErr error
	resolved   []string
	checked    []string
}

type semanticEmbeddingStoreFake struct {
	deleted     memport.SemanticEmbeddingDelete
	deletedTeam string
	deletedBot  string
	counted     memport.SemanticEmbeddingKey
	checkedTeam string
	count       int64
	err         error
}

func (f *semanticEmbeddingStoreFake) UpsertEmbedding(context.Context, memport.SemanticEmbeddingUpsert) error {
	return f.err
}

func (f *semanticEmbeddingStoreFake) SearchEmbeddings(context.Context, memport.SemanticEmbeddingSearch) ([]memport.SemanticEmbeddingSeed, error) {
	return nil, f.err
}

func (f *semanticEmbeddingStoreFake) DeleteEmbeddings(_ context.Context, value memport.SemanticEmbeddingDelete) error {
	f.deleted = value
	return f.err
}

func (f *semanticEmbeddingStoreFake) DeleteBotEmbeddings(_ context.Context, teamID, botID string) error {
	f.deletedTeam, f.deletedBot = teamID, botID
	return f.err
}

func (f *semanticEmbeddingStoreFake) CountEmbeddings(_ context.Context, value memport.SemanticEmbeddingKey) (int64, error) {
	f.counted = value
	return f.count, f.err
}

func (f *semanticEmbeddingStoreFake) CheckEmbeddings(_ context.Context, teamID string) error {
	f.checkedTeam = teamID
	return f.err
}

func (f *embeddingModelResolverFake) ResolveEmbeddingModel(_ context.Context, ref string) (EmbeddingModelSpec, error) {
	f.resolved = append(f.resolved, ref)
	return f.spec, f.resolveErr
}

func (f *embeddingModelResolverFake) EmbeddingModelEnabled(_ context.Context, ref string) (bool, error) {
	f.checked = append(f.checked, ref)
	return f.enabled, f.enabledErr
}

func TestCheckedPGVectorInt32(t *testing.T) {
	t.Parallel()
	if got, err := checkedPgvectorInt32("limit", 42); err != nil || got != 42 {
		t.Fatalf("checkedPgvectorInt32(42) = %d, %v", got, err)
	}
	if _, err := checkedPgvectorInt32("limit", -1); err == nil {
		t.Fatal("checkedPgvectorInt32(-1) succeeded")
	}
	if _, err := checkedPgvectorInt32("limit", int(maxPgvectorInt32)+1); err == nil {
		t.Fatal("checkedPgvectorInt32(max+1) succeeded")
	}
}

func TestPGVectorTeamResolverDefaultsToSingleton(t *testing.T) {
	t.Parallel()
	index := &pgvectorIndex{resolveTeam: memreg.FixedTeamIDResolver(team.DefaultTeamID)}
	got, err := index.teamID(context.Background())
	if err != nil {
		t.Fatalf("teamID() error = %v", err)
	}
	if got != team.DefaultTeamID {
		t.Fatalf("teamID() = %s, want %s", got, team.DefaultTeamID)
	}
}

func TestPGVectorTeamResolverFailsClosed(t *testing.T) {
	t.Parallel()
	index := &pgvectorIndex{resolveTeam: func(context.Context) (string, error) {
		return "", errors.New("team missing")
	}}
	if _, err := index.teamID(context.Background()); err == nil {
		t.Fatal("teamID() without team succeeded")
	}
	index.resolveTeam = memreg.FixedTeamIDResolver("not-a-uuid")
	if _, err := index.teamID(context.Background()); err == nil {
		t.Fatal("teamID() with invalid team succeeded")
	}
}

func TestPGVectorIndexUsesPersistenceNeutralStore(t *testing.T) {
	t.Parallel()
	store := &semanticEmbeddingStoreFake{count: 7}
	index := &pgvectorIndex{
		store: store, modelID: "22222222-2222-4222-8222-222222222222",
		resolveTeam: memreg.FixedTeamIDResolver("11111111-1111-4111-8111-111111111111"),
	}
	if err := index.DeleteNodes(t.Context(), "33333333-3333-4333-8333-333333333333", []string{" node-a ", "", "node-b"}); err != nil {
		t.Fatalf("DeleteNodes() error = %v", err)
	}
	if store.deleted.TeamID != "11111111-1111-4111-8111-111111111111" || store.deleted.BotID != "33333333-3333-4333-8333-333333333333" {
		t.Fatalf("DeleteNodes identity = %+v", store.deleted)
	}
	if got := strings.Join(store.deleted.NodeIDs, ","); got != "node-a,node-b" {
		t.Fatalf("DeleteNodes node ids = %q", got)
	}
	count, err := index.Count(t.Context(), "33333333-3333-4333-8333-333333333333")
	if err != nil || count != 7 {
		t.Fatalf("Count() = %d, %v", count, err)
	}
	if store.counted.ModelID != index.modelID {
		t.Fatalf("Count model id = %q, want %q", store.counted.ModelID, index.modelID)
	}
	if err := index.DeleteBot(t.Context(), "33333333-3333-4333-8333-333333333333"); err != nil {
		t.Fatalf("DeleteBot() error = %v", err)
	}
	if store.deletedTeam == "" || store.deletedBot != "33333333-3333-4333-8333-333333333333" {
		t.Fatalf("DeleteBot identity = (%q, %q)", store.deletedTeam, store.deletedBot)
	}
	if err := index.Health(t.Context()); err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if store.checkedTeam != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("Health team = %q", store.checkedTeam)
	}
}

func TestResolveEmbeddingModel(t *testing.T) {
	t.Parallel()
	valid := EmbeddingModelSpec{
		ID:         " 11111111-1111-1111-1111-111111111111 ",
		ModelID:    " text-embedding-3-small ",
		Type:       " embedding ",
		Enabled:    true,
		Dimensions: 1536,
		ProviderID: " provider-id ",
		ClientType: " openai-completions ",
		BaseURL:    " https://example.test/v1 ",
		APIKey:     " secret-token ",
	}
	tests := []struct {
		name    string
		mutate  func(*EmbeddingModelSpec)
		wantErr string
	}{
		{name: "valid"},
		{name: "invalid id", mutate: func(spec *EmbeddingModelSpec) { spec.ID = "not-a-uuid" }, wantErr: "invalid embedding model id"},
		{name: "wrong type", mutate: func(spec *EmbeddingModelSpec) { spec.Type = "chat" }, wantErr: "is not an embedding model"},
		{name: "disabled", mutate: func(spec *EmbeddingModelSpec) { spec.Enabled = false }, wantErr: "is disabled"},
		{name: "missing provider", mutate: func(spec *EmbeddingModelSpec) { spec.ProviderID = " " }, wantErr: "has no provider"},
		{name: "missing dimensions", mutate: func(spec *EmbeddingModelSpec) { spec.Dimensions = 0 }, wantErr: "missing dimensions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := valid
			if tt.mutate != nil {
				tt.mutate(&spec)
			}
			resolver := &embeddingModelResolverFake{spec: spec}
			got, err := resolveEmbeddingModel(t.Context(), resolver, " model-ref ")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveEmbeddingModel() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveEmbeddingModel() error = %v", err)
			}
			if got.ID != "11111111-1111-1111-1111-111111111111" || got.ModelID != "text-embedding-3-small" {
				t.Fatalf("resolveEmbeddingModel() identity = (%q, %q)", got.ID, got.ModelID)
			}
			if got.ClientType != "openai-completions" || got.BaseURL != "https://example.test/v1" || got.APIKey != "secret-token" {
				t.Fatalf("resolveEmbeddingModel() provider = (%q, %q, %q)", got.ClientType, got.BaseURL, got.APIKey)
			}
			if len(resolver.resolved) != 1 || resolver.resolved[0] != "model-ref" {
				t.Fatalf("ResolveEmbeddingModel refs = %v, want [model-ref]", resolver.resolved)
			}
		})
	}
}

func TestResolveEmbeddingModelPreservesResolverError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("model lookup failed")
	_, err := resolveEmbeddingModel(t.Context(), &embeddingModelResolverFake{resolveErr: wantErr}, "model-ref")
	if !errors.Is(err, wantErr) {
		t.Fatalf("resolveEmbeddingModel() error = %v, want wrapping %v", err, wantErr)
	}
}

func TestPGVectorEmbeddingEnabledDistinguishesDisabledAndLookupError(t *testing.T) {
	t.Parallel()

	t.Run("disabled", func(t *testing.T) {
		resolver := &embeddingModelResolverFake{}
		index := &pgvectorIndex{modelResolver: resolver, modelRef: "model-ref"}
		err := index.ensureEmbeddingEnabled(t.Context())
		if err == nil || !strings.Contains(err.Error(), "is disabled") {
			t.Fatalf("ensureEmbeddingEnabled() error = %v, want disabled", err)
		}
	})

	t.Run("lookup error", func(t *testing.T) {
		wantErr := errors.New("model not found")
		resolver := &embeddingModelResolverFake{enabledErr: wantErr}
		index := &pgvectorIndex{modelResolver: resolver, modelRef: "model-ref"}
		err := index.ensureEmbeddingEnabled(t.Context())
		if !errors.Is(err, wantErr) {
			t.Fatalf("ensureEmbeddingEnabled() error = %v, want wrapping %v", err, wantErr)
		}
	})
}
