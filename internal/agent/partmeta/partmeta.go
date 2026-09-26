// Package partmeta is Memoh's own namespace on sdk.ProviderMetadata. The SDK
// types provider metadata as namespace → name → string so that a provider's
// opaque tokens survive every wire format; Memoh annotates tool-call parts
// with its own records (a pending approval, a user-input request, the
// execution location) and keeps them in the "memoh" namespace, one JSON
// document per key.
//
// bot_history_messages has always stored these annotations as nested objects
// at the top level of providerMetadata, beside the provider namespaces. Fold
// and Unfold convert between that stored shape and the typed one, so the
// database format does not change with the SDK type.
package partmeta

import (
	"encoding/json"

	sdk "github.com/felinics/twilight/sdk"
)

// Namespace holds Memoh's annotations inside sdk.ProviderMetadata.
const Namespace = "memoh"

// Keys of the annotations Memoh writes. They are also the top-level keys of
// the stored shape, which is why Fold always claims them for Namespace even
// when their values happen to be all strings.
const (
	KeyApproval          = "approval"
	KeyUserInput         = "user_input"
	KeyExecutionLocation = "execution_location"
)

var ownKeys = map[string]bool{
	KeyApproval:          true,
	KeyUserInput:         true,
	KeyExecutionLocation: true,
}

// Set returns meta with value recorded under key. meta is not modified; a
// value that cannot be encoded leaves meta unchanged.
func Set(meta sdk.ProviderMetadata, key string, value any) sdk.ProviderMetadata {
	raw, err := json.Marshal(value)
	if err != nil {
		return meta
	}
	return meta.Merge(sdk.NewProviderMetadata(Namespace, map[string]string{key: string(raw)}))
}

// Has reports whether key is recorded.
func Has(meta sdk.ProviderMetadata, key string) bool {
	_, ok := lookup(meta, key)
	return ok
}

// Value decodes the annotation under key.
func Value(meta sdk.ProviderMetadata, key string) (any, bool) {
	raw, ok := lookup(meta, key)
	if !ok {
		return nil, false
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return raw, true
	}
	return value, true
}

// Object decodes the annotation under key as a JSON object.
func Object(meta sdk.ProviderMetadata, key string) (map[string]any, bool) {
	value, ok := Value(meta, key)
	if !ok {
		return nil, false
	}
	obj, ok := value.(map[string]any)
	return obj, ok
}

// lookup finds the encoded annotation. A namespace named after the key is the
// legacy untyped shape carried in memory by a caller that folded it itself;
// it is read the same way.
func lookup(meta sdk.ProviderMetadata, key string) (string, bool) {
	if meta == nil {
		return "", false
	}
	if raw, ok := meta[Namespace][key]; ok {
		return raw, true
	}
	if values, ok := meta[key]; ok && len(values) > 0 {
		raw, err := json.Marshal(values)
		if err != nil {
			return "", false
		}
		return string(raw), true
	}
	return "", false
}

// Fold types the stored shape: an object under a key Memoh does not own is a
// provider namespace whose non-string values are encoded the way a provider
// encodes them (sdk.StringValues); everything else is a Memoh annotation and
// is encoded under Namespace.
func Fold(stored map[string]any) sdk.ProviderMetadata {
	if len(stored) == 0 {
		return nil
	}
	out := sdk.ProviderMetadata{}
	// A stored object may already carry the memoh namespace (a row written
	// in the SDK shape). Its entries are taken first so an annotation stored
	// at the top level always wins, whatever order the map iterates in.
	if obj, ok := stored[Namespace].(map[string]any); ok {
		if values := sdk.StringValues(obj); len(values) > 0 {
			out[Namespace] = values
		}
	}
	for key, value := range stored {
		if key == Namespace {
			continue
		}
		if obj, ok := value.(map[string]any); ok && !ownKeys[key] {
			if values := sdk.StringValues(obj); len(values) > 0 {
				out[key] = values
			}
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			continue
		}
		if out[Namespace] == nil {
			out[Namespace] = map[string]string{}
		}
		out[Namespace][key] = string(raw)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Unfold produces the stored shape: Memoh annotations decode back to their
// documents at the top level and provider namespaces stay as they are.
func Unfold(meta sdk.ProviderMetadata) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := make(map[string]any, len(meta))
	for namespace, values := range meta {
		if namespace == Namespace {
			for key, raw := range values {
				var value any
				if err := json.Unmarshal([]byte(raw), &value); err != nil {
					out[key] = raw
					continue
				}
				out[key] = value
			}
			continue
		}
		obj := make(map[string]any, len(values))
		for key, value := range values {
			obj[key] = value
		}
		out[namespace] = obj
	}
	return out
}

// Delete returns meta without the annotation under key. meta is not modified;
// a namespace named after the key (the legacy untyped shape) is removed too.
func Delete(meta sdk.ProviderMetadata, key string) sdk.ProviderMetadata {
	if !Has(meta, key) {
		return meta
	}
	out := meta.Clone()
	if values, ok := out[Namespace]; ok {
		delete(values, key)
		if len(values) == 0 {
			delete(out, Namespace)
		}
	}
	delete(out, key)
	if len(out) == 0 {
		return nil
	}
	return out
}
