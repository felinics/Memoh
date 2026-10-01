package media

import "io"

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
	RawMD5      string `json:"raw_md5,omitempty"`
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
