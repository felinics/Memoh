package vector

import (
	"context"
	"testing"

	memport "github.com/memohai/memoh/domains/memory/internal/port"
)

func TestStoreRejectsInvalidIdentityBeforeOpeningTransaction(t *testing.T) {
	t.Parallel()
	store := NewStore(nil)
	if err := store.UpsertEmbedding(t.Context(), memport.SemanticEmbeddingUpsert{
		TeamID: "team", BotID: "bad-bot", ModelID: "bad-model",
	}); err == nil {
		t.Fatal("UpsertEmbedding() error = nil")
	}
	if _, err := store.SearchEmbeddings(t.Context(), memport.SemanticEmbeddingSearch{
		TeamID: "team", BotID: "bad-bot", ModelID: "bad-model",
	}); err == nil {
		t.Fatal("SearchEmbeddings() error = nil")
	}
	if err := store.DeleteBotEmbeddings(context.Background(), "bad-team", "bad-bot"); err == nil {
		t.Fatal("DeleteBotEmbeddings() error = nil")
	}
}

func TestStoreFailsClosedWhenLegacyStoreIsUnavailable(t *testing.T) {
	t.Parallel()
	store := NewStore(nil)
	if err := store.CheckEmbeddings(t.Context(), "11111111-1111-4111-8111-111111111111"); err == nil {
		t.Fatal("CheckEmbeddings() error = nil")
	}
}
