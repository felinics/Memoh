package heartbeat

import "testing"

func TestNormalizeHeartbeatIntervalDefault(t *testing.T) {
	t.Parallel()

	if got := normalizeHeartbeatInterval(0); got != 1440 {
		t.Fatalf("normalizeHeartbeatInterval(0) = %d, want 1440", got)
	}
	if got := normalizeHeartbeatInterval(-5); got != 1440 {
		t.Fatalf("normalizeHeartbeatInterval(-5) = %d, want 1440", got)
	}
	if got := normalizeHeartbeatInterval(60); got != 60 {
		t.Fatalf("normalizeHeartbeatInterval(60) = %d, want 60", got)
	}
}
