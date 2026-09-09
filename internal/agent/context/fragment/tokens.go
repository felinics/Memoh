package contextfrag

import (
	"encoding/json"

	sdk "github.com/felinics/twilight/sdk"
)

// EstimateBytesPerToken is the byte-per-token heuristic shared by every
// context ledger (selection budgets, compaction triggers, manifest
// accounting), and the single swap point for a real tokenizer.
const EstimateBytesPerToken = 4

const (
	// ProviderBudgetEstimator identifies the conservative estimator used only
	// for provider-envelope decisions.
	ProviderBudgetEstimator = "bytes_ceil_div4_margin_v1"
	// ProviderBudgetSafetyFactorPercent adds 25 percent headroom after byte
	// ceiling. The midpoint of the accepted 15–30 percent range covers common
	// multilingual and JSON density without changing legacy ledger estimates.
	ProviderBudgetSafetyFactorPercent = 125
)

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

// ProviderBudgetTokensFromBytes converts bytes for provider-envelope decisions,
// including selection under a provider budget. Compaction, cache metrics, and
// other ledger consumers keep the legacy floor-based TokensFromBytes contract.
func ProviderBudgetTokensFromBytes(n int) int {
	if n <= 0 {
		return 0
	}
	ceiling := n / EstimateBytesPerToken
	if n%EstimateBytesPerToken != 0 {
		ceiling++
	}
	return ceiling * ProviderBudgetSafetyFactorPercent / 100
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

// ResolveProviderBudgetFragTokens keeps an authoritative fragment estimate when
// it is larger, otherwise it uses the conservative provider byte estimate.
func ResolveProviderBudgetFragTokens(frag ContextFrag) int {
	bytes, images := fragEstimate(frag)
	estimated := ProviderBudgetTokensFromBytes(bytes) + images*EstimateImageTokens
	if frag.TokenEstimate > estimated {
		return frag.TokenEstimate
	}
	return estimated
}

// ProviderEnvelopeTokens prices one provider payload for envelope decisions
// with the estimator that selection applies per fragment, so a single-message
// fragment costs the same whether it is being selected or has already been
// frozen into a prefix.
func ProviderEnvelopeTokens(system string, messages []sdk.Message, tools []sdk.Tool) int {
	total := ProviderBudgetTokensFromBytes(len(system))
	for _, message := range messages {
		bytes, images := sdkMessageEstimate(message)
		total += ProviderBudgetTokensFromBytes(bytes) + images*EstimateImageTokens
	}
	for _, tool := range tools {
		total += ProviderToolDefTokens(ToolDefAccountingFor("", tool))
	}
	return total
}

// ProviderToolDefTokens prices one tool definition for envelope decisions.
func ProviderToolDefTokens(def ToolDefAccounting) int {
	return max(def.TokenEstimate, ProviderBudgetTokensFromBytes(def.Bytes))
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
