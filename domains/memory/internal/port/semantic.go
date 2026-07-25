package port

import "context"

// SemanticEmbeddingUpsert is the persistence-neutral value consumed by the
// builtin semantic index.
type SemanticEmbeddingUpsert struct {
	TeamID     string
	BotID      string
	NodeID     string
	ModelID    string
	Dimensions int32
	BodyHash   string
	Embedding  []float32
}

type SemanticEmbeddingSearch struct {
	TeamID    string
	BotID     string
	ModelID   string
	Embedding []float32
	Limit     int32
}

type SemanticEmbeddingSeed struct {
	NodeID string
	Score  float64
}

type SemanticEmbeddingKey struct {
	TeamID  string
	BotID   string
	ModelID string
}

type SemanticEmbeddingDelete struct {
	TeamID  string
	BotID   string
	NodeIDs []string
}

// SemanticEmbeddingStore is owned by the builtin Memory consumer. PostgreSQL
// vector types, generated rows, RLS binding, and transactions stay behind it.
type SemanticEmbeddingStore interface {
	UpsertEmbedding(context.Context, SemanticEmbeddingUpsert) error
	SearchEmbeddings(context.Context, SemanticEmbeddingSearch) ([]SemanticEmbeddingSeed, error)
	DeleteEmbeddings(context.Context, SemanticEmbeddingDelete) error
	DeleteBotEmbeddings(context.Context, string, string) error
	CountEmbeddings(context.Context, SemanticEmbeddingKey) (int64, error)
	CheckEmbeddings(context.Context, string) error
}
