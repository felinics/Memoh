package textutil

// EstimatedBytesPerToken is the conservative shared approximation used for
// mixed-language chat history. Two bytes per token avoids badly undercounting
// CJK text while remaining cheap and deterministic.
const EstimatedBytesPerToken = 2

// EstimateTokensFromBytes rounds a byte count up to the shared token estimate.
func EstimateTokensFromBytes(byteCount int) int {
	if byteCount <= 0 {
		return 0
	}
	return (byteCount + EstimatedBytesPerToken - 1) / EstimatedBytesPerToken
}
