package attachment

import (
	"encoding/json"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/media"
)

// Bundle is the internal attachment normalization shape shared across
// messaging, channel ingress, and gateway preparation.
// It is intentionally not exposed as a user-facing contract.
type Bundle struct {
	Type           string
	Base64         string
	Path           string
	URL            string
	PlatformKey    string
	SourcePlatform string
	ContentHash    string
	Name           string
	Mime           string
	Size           int64
	DurationMs     int64
	Width          int
	Height         int
	ThumbnailURL   string
	Caption        string
	Metadata       map[string]any
}

const (
	MetadataKeyBotID      = "bot_id"
	MetadataKeyStorageKey = "storage_key"
	MetadataKeyName       = "name"
	MetadataKeySourcePath = "source_path"
	MetadataKeySourceURL  = "source_url"

	containerMediaSubdir       = ".memoh/media"
	legacyContainerMediaSubdir = "media"
)

// Normalize canonicalizes transport fields and fills lightweight derived data
// such as MIME, name, and inferred type.
func (b Bundle) Normalize() Bundle {
	b.Type = strings.ToLower(strings.TrimSpace(b.Type))
	b.Base64 = strings.TrimSpace(b.Base64)
	b.Path = strings.TrimSpace(b.Path)
	b.URL = strings.TrimSpace(b.URL)
	b.PlatformKey = strings.TrimSpace(b.PlatformKey)
	b.SourcePlatform = strings.TrimSpace(b.SourcePlatform)
	b.ContentHash = strings.TrimSpace(b.ContentHash)
	b.Name = strings.TrimSpace(b.Name)
	b.Mime = NormalizeMime(b.Mime)
	b.ThumbnailURL = strings.TrimSpace(b.ThumbnailURL)
	b.Caption = strings.TrimSpace(b.Caption)

	if b.Base64 == "" && IsDataURL(b.URL) {
		b.Base64 = b.URL
		b.URL = ""
	}
	if b.Path == "" && isLocalPath(b.URL) {
		b.Path = b.URL
		b.URL = ""
	}
	if b.Base64 != "" {
		b.Base64 = NormalizeBase64DataURL(b.Base64, b.Mime)
		if b.Mime == "" {
			b.Mime = MimeFromDataURL(b.Base64)
		}
	}
	if b.Name == "" {
		b.Name = InferNameFromRef(firstNonEmpty(b.Path, b.URL))
	}
	if b.Type == "" {
		switch {
		case b.Mime != "":
			b.Type = InferTypeFromMime(b.Mime)
		case b.Base64 != "":
			if mime := MimeFromDataURL(b.Base64); mime != "" {
				b.Type = InferTypeFromMime(mime)
			} else {
				b.Type = "file"
			}
		default:
			b.Type = InferTypeFromExt(firstNonEmpty(b.Path, b.URL, b.Name))
		}
	}
	if b.Type == "" {
		b.Type = "file"
	}
	return b
}

// BundleFromMap builds a bundle from a generic JSON-like object.
func BundleFromMap(item map[string]any) Bundle {
	if item == nil {
		return Bundle{}
	}
	return bundleFieldsFromMap(item).Normalize()
}

// bundleFieldsFromMap extracts all known fields from a map into a Bundle without
// calling Normalize. Used as a shared primitive by BundleFromMap and
// parseToolInputBundle to avoid duplicating field extraction logic.
func bundleFieldsFromMap(m map[string]any) Bundle {
	bundle := Bundle{
		Type:           stringValue(m["type"]),
		Base64:         stringValue(m["base64"]),
		Path:           stringValue(m["path"]),
		URL:            stringValue(m["url"]),
		PlatformKey:    stringValue(m["platform_key"]),
		SourcePlatform: stringValue(m["source_platform"]),
		ContentHash:    stringValue(m["content_hash"]),
		Name:           stringValue(m["name"]),
		Mime:           stringValue(m["mime"]),
		Size:           int64Value(m["size"]),
		DurationMs:     int64Value(m["duration_ms"]),
		Width:          int(int64Value(m["width"])),
		Height:         int(int64Value(m["height"])),
		ThumbnailURL:   stringValue(m["thumbnail_url"]),
		Caption:        stringValue(m["caption"]),
	}
	if metadata, ok := m["metadata"].(map[string]any); ok {
		bundle.Metadata = cloneMetadata(metadata)
	}
	return bundle
}

// ToMap serializes the internal bundle back into a generic JSON-like object.
func (b Bundle) ToMap() map[string]any {
	b = b.Normalize()
	item := map[string]any{}
	if b.Type != "" {
		item["type"] = b.Type
	}
	if b.Base64 != "" {
		item["base64"] = b.Base64
	}
	if b.Path != "" {
		item["path"] = b.Path
	}
	if b.URL != "" {
		item["url"] = b.URL
	}
	if b.PlatformKey != "" {
		item["platform_key"] = b.PlatformKey
	}
	if b.SourcePlatform != "" {
		item["source_platform"] = b.SourcePlatform
	}
	if b.ContentHash != "" {
		item["content_hash"] = b.ContentHash
	}
	if b.Name != "" {
		item["name"] = b.Name
	}
	if b.Mime != "" {
		item["mime"] = b.Mime
	}
	if b.Size > 0 {
		item["size"] = b.Size
	}
	if b.DurationMs > 0 {
		item["duration_ms"] = b.DurationMs
	}
	if b.Width > 0 {
		item["width"] = b.Width
	}
	if b.Height > 0 {
		item["height"] = b.Height
	}
	if b.ThumbnailURL != "" {
		item["thumbnail_url"] = b.ThumbnailURL
	}
	if b.Caption != "" {
		item["caption"] = b.Caption
	}
	if len(b.Metadata) > 0 {
		item["metadata"] = cloneMetadata(b.Metadata)
	}
	return item
}

// MergeIntoMap applies bundle-owned fields into an existing JSON-like object.
// Unknown fields are preserved.
func (b Bundle) MergeIntoMap(item map[string]any) map[string]any {
	if item == nil {
		item = make(map[string]any)
	}
	b = b.Normalize()
	for _, key := range []string{
		"base64",
		"path",
		"url",
		"platform_key",
		"source_platform",
		"content_hash",
		"name",
		"mime",
		"size",
		"duration_ms",
		"width",
		"height",
		"thumbnail_url",
		"caption",
		"metadata",
		"type",
	} {
		delete(item, key)
	}
	for key, value := range b.ToMap() {
		item[key] = value
	}
	return item
}

// WithAsset rewrites the bundle into a persisted asset reference while
// preserving useful source metadata for later resolution and rendering.
// Callers must guarantee b is already normalized (produced by BundleFromXxx or Normalize()).
func (b Bundle) WithAsset(botID string, asset media.Asset) Bundle {
	sourcePath := b.Path
	sourceURL := b.URL
	if b.Name == "" {
		b.Name = InferNameFromRef(firstNonEmpty(sourcePath, sourceURL))
	}

	meta := cloneMetadata(b.Metadata)
	if meta == nil {
		meta = make(map[string]any)
	}
	if strings.TrimSpace(botID) != "" {
		meta[MetadataKeyBotID] = strings.TrimSpace(botID)
	}
	if strings.TrimSpace(asset.StorageKey) != "" {
		meta[MetadataKeyStorageKey] = strings.TrimSpace(asset.StorageKey)
	}
	if strings.TrimSpace(b.Name) != "" {
		meta[MetadataKeyName] = strings.TrimSpace(b.Name)
	}
	if strings.TrimSpace(sourcePath) != "" {
		meta[MetadataKeySourcePath] = strings.TrimSpace(sourcePath)
	}
	if strings.TrimSpace(sourceURL) != "" && !IsDataURL(sourceURL) && !isLocalPath(sourceURL) {
		meta[MetadataKeySourceURL] = strings.TrimSpace(sourceURL)
	}

	b.ContentHash = strings.TrimSpace(asset.ContentHash)
	b.Path = ""
	b.URL = ""
	b.Base64 = ""
	b.PlatformKey = ""
	b.Metadata = meta
	if b.Mime == "" {
		b.Mime = NormalizeMime(asset.Mime)
	}
	if b.Size == 0 && asset.SizeBytes > 0 {
		b.Size = asset.SizeBytes
	}
	return b.Normalize()
}

// WithAssetAccess rewrites the bundle into a persisted asset reference and keeps
// a consumer-accessible path/URL when the caller needs one.
func (b Bundle) WithAssetAccess(botID string, asset media.Asset, accessRef string) Bundle {
	b = b.WithAsset(botID, asset)
	if accessRef = strings.TrimSpace(accessRef); accessRef != "" {
		if IsHTTPURL(accessRef) {
			b.URL = accessRef
		} else {
			b.Path = accessRef
		}
	}
	return b.Normalize()
}

// ParseToolInputBundles normalizes the `send.attachments` argument shape into
// internal bundles while preserving existing path resolution rules.
func ParseToolInputBundles(raw any) ([]Bundle, bool) {
	switch v := raw.(type) {
	case nil:
		return nil, true
	case string:
		bundle, ok := parseToolInputBundle(v)
		if !ok {
			return []Bundle{}, true
		}
		return []Bundle{bundle}, true
	case map[string]any:
		bundle, ok := parseToolInputBundle(v)
		if !ok {
			return []Bundle{}, true
		}
		return []Bundle{bundle}, true
	case []string:
		result := make([]Bundle, 0, len(v))
		for _, item := range v {
			bundle, ok := parseToolInputBundle(item)
			if ok {
				result = append(result, bundle)
			}
		}
		return result, true
	case []any:
		result := make([]Bundle, 0, len(v))
		for _, item := range v {
			bundle, ok := parseToolInputBundle(item)
			if ok {
				result = append(result, bundle)
			}
		}
		return result, true
	default:
		return nil, false
	}
}

func parseToolInputBundle(raw any) (Bundle, bool) {
	switch v := raw.(type) {
	case string:
		return normalizeToolBundle(Bundle{URL: v}), strings.TrimSpace(v) != ""
	case map[string]any:
		bundle := bundleFieldsFromMap(v)
		if bundle.Base64 == "" && bundle.Path == "" && bundle.URL == "" &&
			bundle.PlatformKey == "" && bundle.ContentHash == "" {
			return Bundle{}, false
		}
		return normalizeToolBundle(bundle), true
	default:
		return Bundle{}, false
	}
}

func normalizeToolBundle(bundle Bundle) Bundle {
	if path := strings.TrimSpace(bundle.Path); path != "" {
		bundle.Path = normalizeToolInputRef(path)
		bundle.URL = ""
	}
	if bundle.Path == "" {
		rawURL := strings.TrimSpace(bundle.URL)
		switch {
		case IsDataURL(rawURL):
			bundle.Base64 = rawURL
			bundle.URL = ""
		case IsHTTPURL(rawURL):
			bundle.URL = rawURL
		case rawURL != "":
			bundle.Path = normalizeToolInputRef(rawURL)
			bundle.URL = ""
		}
	}
	return bundle.Normalize()
}

func normalizeToolInputRef(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || IsHTTPURL(raw) || IsDataURL(raw) || isLocalPath(raw) {
		return raw
	}
	return DataMountPath(raw)
}

// IsDataURL reports whether the reference is a data URL.
func IsDataURL(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "data:")
}

// IsHTTPURL reports whether the reference is an http(s) URL.
func IsHTTPURL(raw string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://")
}

// IsDataPath reports whether the reference points to the container data mount.
func IsDataPath(raw string) bool {
	_, ok := DataSubpath(raw)
	return ok
}

// ExtractStorageKey derives the media storage key from a consumer-facing media
// path. The legacy /data/media root remains readable during the transition.
func ExtractStorageKey(accessPath string) string {
	raw := strings.TrimSpace(accessPath)
	for _, root := range mediaAccessRoots() {
		marker := strings.TrimRight(root, "/") + "/"
		if idx := strings.Index(raw, marker); idx >= 0 {
			return raw[idx+len(marker):]
		}
	}
	return ""
}

// IsMediaAccessPath reports whether raw is under the current or legacy media
// root. Both roots are managed media during the transition.
func IsMediaAccessPath(raw string) bool {
	cleaned := path.Clean("/" + strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"))
	for _, root := range mediaAccessRoots() {
		if cleaned == root || strings.HasPrefix(cleaned, strings.TrimRight(root, "/")+"/") {
			return true
		}
	}
	return false
}

// DataMountPath joins a relative path under the canonical container data mount.
func DataMountPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return config.DefaultDataMount
	}
	return strings.TrimRight(config.DefaultDataMount, "/") + "/" + strings.TrimLeft(raw, "/")
}

// MediaAccessPath returns the consumer-facing media access path for a storage key.
func MediaAccessPath(storageKey string) string {
	return mediaAccessPath(containerMediaSubdir, storageKey)
}

func mediaAccessRoots() [2]string {
	return [2]string{
		mediaAccessPath(containerMediaSubdir, ""),
		mediaAccessPath(legacyContainerMediaSubdir, ""),
	}
}

func mediaAccessPath(subdir, storageKey string) string {
	storageKey = strings.TrimSpace(storageKey)
	if storageKey == "" {
		return path.Join(config.DefaultDataMount, subdir)
	}
	return path.Join(config.DefaultDataMount, subdir, storageKey)
}

// DataSubpath strips the canonical /data/ prefix and returns the container-relative path.
func DataSubpath(containerPath string) (string, bool) {
	raw := strings.TrimSpace(containerPath)
	prefix := strings.TrimRight(config.DefaultDataMount, "/") + "/"
	if !strings.HasPrefix(raw, prefix) {
		return "", false
	}
	subPath := raw[len(prefix):]
	if strings.TrimSpace(subPath) == "" {
		return "", false
	}
	return subPath, true
}

// InferTypeFromMime infers attachment type from MIME.
func InferTypeFromMime(mime string) string {
	mime = NormalizeMime(mime)
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	default:
		return "file"
	}
}

// InferTypeFromExt infers attachment type from file extension.
func InferTypeFromExt(raw string) string {
	ext := strings.ToLower(filepath.Ext(InferNameFromRef(raw)))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg":
		return "image"
	case ".mp3", ".wav", ".ogg", ".flac", ".aac":
		return "audio"
	case ".mp4", ".webm", ".avi", ".mov":
		return "video"
	default:
		return "file"
	}
}

// InferNameFromRef extracts a filename from a path or URL-like reference.
func InferNameFromRef(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || IsDataURL(raw) {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed != nil && parsed.Path != "" {
		if base := filepath.Base(parsed.Path); base != "." && base != "/" {
			return strings.TrimSpace(base)
		}
	}
	base := filepath.Base(raw)
	if base == "." || base == "/" {
		return ""
	}
	return strings.TrimSpace(base)
}

func isLocalPath(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), "/")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func stringValue(raw any) string {
	value, _ := raw.(string)
	return strings.TrimSpace(value)
}

func int64Value(raw any) int64 {
	switch value := raw.(type) {
	case int:
		return int64(value)
	case int8:
		return int64(value)
	case int16:
		return int64(value)
	case int32:
		return int64(value)
	case int64:
		return value
	case float32:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		cloned := make(map[string]any, len(metadata))
		for key, value := range metadata {
			cloned[key] = value
		}
		return cloned
	}
	var cloned map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil
	}
	return cloned
}

// MetadataString returns a trimmed string metadata value.
func MetadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
