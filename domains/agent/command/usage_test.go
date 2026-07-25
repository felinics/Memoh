package command

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestUsageSummaryFormatting(t *testing.T) {
	t.Parallel()
	h := newTestHandlerWithQueries(&fakeRoleResolver{role: "owner"}, &fakeCommandQueries{
		usageByDay: []UsageByDay{{
			SessionType:  "chat",
			Day:          time.Date(2026, time.July, 2, 0, 0, 0, 0, time.UTC),
			InputTokens:  1500,
			OutputTokens: 250,
		}},
	})

	result, err := h.Execute(context.Background(), "11111111-1111-1111-1111-111111111111", "user-1", "/usage summary")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result, "- Jul 02 · 1.5K in · 250 out") {
		t.Fatalf("Execute() result = %q", result)
	}
}

func TestUsageByModelFormatting(t *testing.T) {
	t.Parallel()
	h := newTestHandlerWithQueries(&fakeRoleResolver{role: "owner"}, &fakeCommandQueries{
		usageByModel: []UsageByModel{{
			ModelName:    "Model",
			ProviderName: "Provider",
			InputTokens:  2000,
			OutputTokens: 300,
		}},
	})

	result, err := h.Execute(context.Background(), "11111111-1111-1111-1111-111111111111", "user-1", "/usage by-model")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result, "- Model (Provider) — 2.0K in · 300 out") {
		t.Fatalf("Execute() result = %q", result)
	}
}
