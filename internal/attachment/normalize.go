package attachment

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/felinics/memoh/internal/media"
)

// MapMediaType maps attachment type strings to media types.
func MapMediaType(rawType string) media.MediaType {
	switch strings.ToLower(strings.TrimSpace(rawType)) {
	case "image", "gif":
		return media.MediaTypeImage
	case "audio", "voice":
		return media.MediaTypeAudio
	case "video":
		return media.MediaTypeVideo
	default:
		return media.MediaTypeFile
	}
}

// NormalizeMime normalizes MIME to lowercase token form.
func NormalizeMime(raw string) string {
	mime := strings.ToLower(strings.TrimSpace(raw))
	if mime == "" {
		return ""
	}
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = strings.TrimSpace(mime[:idx])
	}
	if !strings.Contains(mime, "/") {
		return ""
	}
	return mime
}

// MimeFromDataURL extracts MIME from a data URL.
func MimeFromDataURL(raw string) string {
	value := strings.TrimSpace(raw)
	lower := strings.ToLower(value)
	if !strings.HasPrefix(lower, "data:") {
		return ""
	}
	rest := value[len("data:"):]
	if idx := strings.Index(rest, ";"); idx >= 0 {
		return NormalizeMime(rest[:idx])
	}
	if idx := strings.Index(rest, ","); idx >= 0 {
		return NormalizeMime(rest[:idx])
	}
	return ""
}

// ResolveMime resolves source MIME and sniffed MIME into final MIME.
//
// For image attachments, content sniffing takes precedence over the platform-reported
// MIME because platforms sometimes declare the wrong image subtype (e.g. "image/png"
// for a JPEG file). Using the wrong MIME causes multimodal API rejections (HTTP 400).
func ResolveMime(mediaType media.MediaType, sourceMime, sniffedMime string) string {
	source := NormalizeMime(sourceMime)
	sniffed := NormalizeMime(sniffedMime)
	sourceGeneric := source == "" || source == "application/octet-stream"

	if mediaType == media.MediaTypeImage {
		// Prefer the sniffed MIME for images: it is derived from the actual file
		// bytes and is always more accurate than the platform-declared value.
		if strings.HasPrefix(sniffed, "image/") {
			return sniffed
		}
		if strings.HasPrefix(source, "image/") {
			return source
		}
		if !sourceGeneric {
			return source
		}
		if sniffed != "" {
			return sniffed
		}
		return "application/octet-stream"
	}

	if !sourceGeneric {
		return source
	}
	if sniffed != "" {
		return sniffed
	}
	if source != "" {
		return source
	}
	return "application/octet-stream"
}

// PrepareReaderAndMime reads a small prefix for MIME sniffing and replays it.
func PrepareReaderAndMime(reader io.Reader, mediaType media.MediaType, sourceMime string) (io.Reader, string, error) {
	if reader == nil {
		return nil, "", errors.New("reader is required")
	}
	header := make([]byte, 512)
	n, err := reader.Read(header)
	if err != nil && err != io.EOF {
		return nil, "", fmt.Errorf("read mime sniff bytes: %w", err)
	}
	header = header[:n]
	sniffed := ""
	if len(header) > 0 {
		sniffed = NormalizeMime(http.DetectContentType(header))
	}
	finalMime := ResolveMime(mediaType, sourceMime, sniffed)
	return io.MultiReader(bytes.NewReader(header), reader), finalMime, nil
}

// NormalizeBase64DataURL normalizes raw base64 into a data URL.
func NormalizeBase64DataURL(input, mime string) string {
	value := strings.TrimSpace(input)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		return value
	}
	mime = NormalizeMime(mime)
	if mime == "" {
		mime = "application/octet-stream"
	}
	return "data:" + mime + ";base64," + value
}

// DecodeBase64 decodes both raw base64 and data URL base64 content.
// The returned reader is bounded to maxBytes+1 for caller-side size validation.
func DecodeBase64(input string, maxBytes int64) (io.Reader, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return nil, errors.New("base64 payload is empty")
	}
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		if idx := strings.Index(value, ","); idx >= 0 {
			value = value[idx+1:]
		}
	}
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(value))
	return io.LimitReader(decoder, maxBytes+1), nil
}
