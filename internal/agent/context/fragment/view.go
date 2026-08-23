package contextfrag

// Intent names why a context is being built; RenderTarget names what it is
// rendered into. One intent may fan out to several render targets.
type Intent string

const (
	IntentRunConfigPreProvider Intent = "run_config_pre_provider"
	IntentDiscussReply         Intent = "discuss_reply"
)

func (i Intent) ManifestView() ManifestView {
	return ManifestView(i)
}

type RenderTarget string

const RenderSDKMessages RenderTarget = "sdk_messages"

// NormalizeContextRefs fills durable refs and canonical hashes for fragments
// coming from collectors, mirroring what Compile does for legacy inputs.
func NormalizeContextRefs(frags []ContextFrag) []ContextFrag {
	return normalizeContextRefs(frags)
}

// CachePlan records the stable prefix selected for provider prompt caching.
type CachePlan struct {
	StablePrefixHash          string `json:"stable_prefix_hash,omitempty"`
	StableMessageCount        int    `json:"stable_message_count,omitempty"`
	StablePrefixTokenEstimate int    `json:"stable_prefix_token_estimate,omitempty"`
	MidStableMessageCount     int    `json:"mid_stable_message_count,omitempty"`
}
