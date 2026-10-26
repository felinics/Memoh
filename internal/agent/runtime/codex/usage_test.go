package codex

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
)

func TestAccountUsage(t *testing.T) {
	var response protocol.GetAccountRateLimitsResponse
	if err := json.Unmarshal([]byte(`{"rateLimits":{
		"primary":{"usedPercent":52,"windowDurationMins":300,"resetsAt":1790000000},
		"secondary":{"usedPercent":12,"windowDurationMins":10080}}}`), &response); err != nil {
		t.Fatal(err)
	}
	usage := accountUsage(response)
	if usage.LimitReached || len(usage.Windows) != 2 {
		t.Fatalf("usage = %+v", usage)
	}
	primary, secondary := usage.Windows[0], usage.Windows[1]
	if primary.UsedPercent != 52 || primary.WindowMinutes != 300 || primary.ResetsAt == nil || !primary.ResetsAt.Equal(time.Unix(1790000000, 0)) {
		t.Fatalf("primary = %+v", primary)
	}
	if secondary.UsedPercent != 12 || secondary.WindowMinutes != 10080 || secondary.ResetsAt != nil {
		t.Fatalf("secondary = %+v", secondary)
	}

	if err := json.Unmarshal([]byte(`{"rateLimits":{"rateLimitReachedType":"rate_limit_reached"}}`), &response); err != nil {
		t.Fatal(err)
	}
	if !accountUsage(response).LimitReached {
		t.Fatal("a reached limit must be reported")
	}
}
