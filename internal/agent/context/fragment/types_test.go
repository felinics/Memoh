package contextfrag

import "testing"

func TestDefaultToolExchangePolicy(t *testing.T) {
	t.Parallel()

	got := DefaultToolExchangePolicy()
	if got.MinMessages != 10 {
		t.Fatalf("MinMessages = %d, want 10", got.MinMessages)
	}

	other := DefaultToolExchangePolicy()
	other.MinMessages = 99
	if got.MinMessages == other.MinMessages {
		t.Fatal("DefaultToolExchangePolicy must return a fresh pointer per call, not a shared one")
	}
}
