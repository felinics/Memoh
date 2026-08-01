package textutil

import "testing"

func TestEstimateTokensFromBytes(t *testing.T) {
	t.Parallel()

	for bytes, want := range map[int]int{-1: 0, 0: 0, 1: 1, 2: 1, 3: 2, 400: 200} {
		if got := EstimateTokensFromBytes(bytes); got != want {
			t.Errorf("EstimateTokensFromBytes(%d) = %d, want %d", bytes, got, want)
		}
	}
}
