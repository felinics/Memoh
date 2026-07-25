package compaction

import (
	"context"
	"errors"
	"time"
)

var (
	ErrArtifactNotFound    = errors.New("compaction artifact not found")
	ErrPersistenceConflict = errors.New("compaction persistence conflict")
)

// CandidateRecord is the persistence-neutral source row used for selection.
type CandidateRecord struct {
	ID                      string
	BotID                   string
	SessionID               string
	SenderChannelIdentityID string
	SenderUserID            string
	SenderDisplayName       string
	SenderAvatarURL         string
	Platform                string
	ExternalMessageID       string
	SourceReplyToMessageID  string
	Role                    string
	Content                 []byte
	Metadata                []byte
	Usage                   []byte
	CompactID               string
	EventID                 string
	DisplayText             string
	CreatedAt               time.Time
	CompactionEpoch         int64
	ConversationType        string
	ConversationName        string
	ReplyTarget             string
}

type AssetRecord struct {
	MessageID   string
	Role        string
	Ordinal     int
	ContentHash string
	Name        string
	Metadata    []byte
}

type LogRecord struct {
	ID           string
	BotID        string
	SessionID    string
	Status       string
	Summary      string
	MessageCount int
	ErrorMessage string
	Usage        []byte
	ModelID      string
	StartedAt    time.Time
	CompletedAt  time.Time
}

type ArtifactRecord struct {
	ID              string
	BotID           string
	SessionID       string
	Status          string
	Summary         string
	MessageCount    int
	ErrorMessage    string
	Usage           []byte
	ModelID         string
	ArtifactVersion int
	Coverage        []byte
	AnchorStartMs   int64
	AnchorEndMs     int64
	ArtifactLevel   int
	ParentIDs       []string
	SupersededBy    string
	SupersededAt    time.Time
	StartedAt       time.Time
	CompletedAt     time.Time
}

type CreateLogInput struct {
	BotID         string
	SessionID     string
	ExpectedEpoch int64
}

type ClaimCandidatesInput struct {
	LogID              string
	MessageIDs         []string
	ExpectedCompactIDs []string
}

type CompleteLogInput struct {
	ID            string
	Status        string
	Summary       string
	MessageCount  int
	ErrorMessage  string
	Usage         []byte
	ModelID       string
	Coverage      []byte
	AnchorStartMs int64
	AnchorEndMs   int64
}

type ListLogsInput struct {
	BotID  string
	Limit  int
	Offset int
}

type ArtifactParentsInput struct {
	SuccessorID string
	BotID       string
	SessionID   string
}

// CompactionStore owns the persistence needed to claim and finalize attempts.
type CompactionStore interface {
	ListCandidates(context.Context, string) ([]CandidateRecord, error)
	CreateLog(context.Context, CreateLogInput) (string, error)
	ClaimCandidates(context.Context, ClaimCandidatesInput) (int64, error)
	ListAssets(context.Context, []string) ([]AssetRecord, error)
	CompleteLog(context.Context, CompleteLogInput) error
	CountLogs(context.Context, string) (int64, error)
	ListLogs(context.Context, ListLogsInput) ([]LogRecord, error)
	DeleteLogs(context.Context, string) error
}

// ArtifactStore owns compaction artifact lineage reads.
type ArtifactStore interface {
	GetArtifact(context.Context, string) (ArtifactRecord, error)
	ListArtifactsBySession(context.Context, string) ([]ArtifactRecord, error)
	ListParentIDs(context.Context, ArtifactParentsInput) ([]string, error)
}
