package contextfrag

import (
	"encoding/json"

	sdk "github.com/memohai/twilight-ai/sdk"
)

// EstimateBytesPerToken is the byte-per-token heuristic shared by every
// context ledger (selection budgets, compaction triggers, manifest
// accounting), and the single swap point for a real tokenizer.
const EstimateBytesPerToken = 4

// EstimateImageTokens is the flat per-image estimate. Real image cost is
// resolution-dependent and provider-capped at roughly 1.1–1.6K tokens, so a
// ceiling-magnitude flat figure keeps image-heavy history visible to budget
// pressure without counting base64 payload bytes as text.
const EstimateImageTokens = 1500

// TokensFromBytes converts a byte count to the shared token estimate.
func TokensFromBytes(n int) int {
	if n <= 0 {
		return 0
	}
	return n / EstimateBytesPerToken
}

// EstimateSDKMessageTokens estimates tokens for one SDK message additively
// across all parts: text and reasoning count their raw bytes, tool calls and
// tool results count their serialized payload, and each image counts the
// flat EstimateImageTokens instead of its base64 payload.
func EstimateSDKMessageTokens(msg sdk.Message) int {
	bytes, images := sdkMessageEstimate(msg)
	return TokensFromBytes(bytes) + images*EstimateImageTokens
}

func sdkMessageEstimate(msg sdk.Message) (bytes, images int) {
	for _, part := range msg.Content {
		switch p := part.(type) {
		case sdk.TextPart:
			bytes += len(p.Text)
		case sdk.ReasoningPart:
			bytes += len(p.Text)
		case sdk.ImagePart:
			images++
		default:
			if data, err := json.Marshal(part); err == nil {
				bytes += len(data)
			}
		}
	}
	return bytes, images
}

// EstimateFragTokens computes the token estimate from the fragment's parts,
// ignoring any preset TokenEstimate.
func EstimateFragTokens(frag ContextFrag) int {
	bytes, images := fragEstimate(frag)
	return TokensFromBytes(bytes) + images*EstimateImageTokens
}

func fragEstimate(frag ContextFrag) (bytes, images int) {
	for _, part := range frag.Parts {
		switch part.Type {
		case PartText:
			bytes += len(part.Text)
		case PartSDKMessage:
			if msg := partMessage(part); msg != nil {
				msgBytes, msgImages := sdkMessageEstimate(*msg)
				bytes += msgBytes
				images += msgImages
			}
		case PartImage:
			images++
		}
	}
	return bytes, images
}

// ResolveFragTokens returns the fragment's authoritative token estimate:
// the collector-provided TokenEstimate when set (which may carry real
// provider usage), otherwise the computed part estimate.
func ResolveFragTokens(frag ContextFrag) int {
	if frag.TokenEstimate > 0 {
		return frag.TokenEstimate
	}
	return EstimateFragTokens(frag)
}

// ToolDefAccountingFor measures one tool definition as the provider will
// receive it (name, description, parameter schema). A definition that fails
// to serialize falls back to its visible prose size.
func ToolDefAccountingFor(provider string, tool sdk.Tool) ToolDefAccounting {
	size := len(tool.Name) + len(tool.Description)
	if data, err := json.Marshal(tool); err == nil {
		size = len(data)
	}
	return ToolDefAccounting{
		Provider:      provider,
		Name:          tool.Name,
		Bytes:         size,
		TokenEstimate: TokensFromBytes(size),
	}
}
