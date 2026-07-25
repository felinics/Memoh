// Package media defines stable values owned by the Media domain.
package media

import (
	"errors"
	"io"
	"time"
)

type AssetKind uint8

const (
	AssetKindUnspecified AssetKind = iota
	AssetKindImage
	AssetKindAudio
	AssetKindVideo
	AssetKindDocument
	AssetKindOther
)

// AssetRef identifies an asset without exposing its storage location or bytes.
type AssetRef struct {
	ContentHash string
	Kind        AssetKind
	MIME        string
	SizeBytes   uint64
	Name        string
	Role        string
	Ordinal     uint32
	Caption     string
	Duration    time.Duration
	Width       uint32
	Height      uint32
}

// MediaType classifies the kind of media asset.
type MediaType string

const (
	MediaTypeImage MediaType = "image"
	MediaTypeAudio MediaType = "audio"
	MediaTypeVideo MediaType = "video"
	MediaTypeFile  MediaType = "file"
)

// Asset is the domain representation of a persisted media object.
// ContentHash is the content-addressed identifier (SHA-256 hex).
type Asset struct {
	ContentHash string `json:"content_hash"`
	BotID       string `json:"bot_id"`
	Mime        string `json:"mime"`
	SizeBytes   int64  `json:"size_bytes"`
	StorageKey  string `json:"storage_key"`
}

// IngestInput carries the data needed to persist a new media asset.
type IngestInput struct {
	BotID string
	Mime  string
	// Reader provides the raw bytes; caller is responsible for closing.
	Reader io.Reader
	// MaxBytes optionally overrides the default size limit.
	MaxBytes int64
	// OriginalExt preserves the source file extension (e.g. ".md") so it
	// survives even when the MIME type is unknown / generic.
	OriginalExt string
}

const (
	// MaxAssetBytes is the global max accepted payload size.
	MaxAssetBytes int64 = 200 * 1024 * 1024
)

var (
	// ErrAssetNotFound indicates the requested media asset does not exist.
	ErrAssetNotFound = errors.New("media asset not found")
	// ErrProviderUnavailable indicates the storage provider is not configured or reachable.
	ErrProviderUnavailable = errors.New("storage provider unavailable")
	// ErrAssetTooLarge indicates the payload exceeds the configured max asset size.
	ErrAssetTooLarge = errors.New("media asset too large")
	// ErrPathTraversal indicates a storage key attempted directory traversal.
	ErrPathTraversal = errors.New("path traversal is forbidden")
)
