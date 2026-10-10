package command

import (
	"context"
	"strings"
	"testing"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestContextRegistered(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)
	g, ok := h.registry.groups["context"]
	if !ok || g.DefaultAction != "show" {
		t.Fatalf("/context not registered with show default")
	}
}

func TestRenderProgressBar(t *testing.T) {
	t.Parallel()
	if got := renderProgressBar(0.5, 10); got != strings.Repeat("█", 5)+strings.Repeat("░", 5) {
		t.Errorf("bar 0.5 = %q", got)
	}
	if got := renderProgressBar(2, 4); got != strings.Repeat("█", 4) {
		t.Errorf("bar clamp high = %q", got)
	}
	if got := renderProgressBar(-1, 4); got != strings.Repeat("░", 4) {
		t.Errorf("bar clamp low = %q", got)
	}
}

func TestRenderContextUsageNoWindow(t *testing.T) {
	t.Parallel()
	h := newTestHandlerWithQueries(&fakeRoleResolver{role: "owner"}, &fakeCommandQueries{
		messageCount: 7, latestUsage: 1500,
	})
	out, err := h.renderContextUsage(CommandContext{Ctx: context.Background(), BotID: "b"}, "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "**Context**") {
		t.Errorf("missing bold title: %s", out)
	}
	if !strings.Contains(out, "Messages: 7") {
		t.Errorf("missing message count: %s", out)
	}
	// No model service wired => no window => the "N tokens used" fallback path.
	if !strings.Contains(out, "1.5K tokens used") {
		t.Errorf("missing used tokens: %s", out)
	}
}

// A runtime session shows the runtime's own measurement against the window of
// the same observation, never a turn total or another model's window.
func TestRenderRuntimeContextUsage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		context string
		want    []string
		reject  []string
	}{
		{"known with window", `{"used_tokens":1585,"context_window":200000,"source":"acp_usage_update"}`, []string{"1%", "1.6K / 200.0K used"}, []string{"Not available"}},
		{"known without window", `{"used_tokens":1585}`, []string{"1.6K tokens used"}, []string{"%", "Not available"}},
		{"unknown", ``, []string{"Not available"}, []string{"tokens used", "%"}},
		{"stale", `{"used_tokens":180000,"context_window":200000,"stale":"compact"}`, []string{"Not available"}, []string{"180", "%"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			row := dbsqlc.GetLatestContextUsageRow{SessionRuntimeType: "acp_agent", MessageRuntimeType: "acp_agent", Usage: []byte(`{"inputTokens":3080000}`)}
			if tc.context != "" {
				row.ContextUsage = []byte(tc.context)
			}
			h := newTestHandlerWithQueries(&fakeRoleResolver{role: "owner"}, &fakeCommandQueries{messageCount: 3, contextRow: &row})
			cc := CommandContext{Ctx: context.Background(), BotID: "b"}
			out, err := h.renderContextUsage(cc, "11111111-1111-1111-1111-111111111111")
			if err != nil {
				t.Fatal(err)
			}
			status, err := h.renderSessionStatus(cc, "11111111-1111-1111-1111-111111111111", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("/context missing %q: %s", want, out)
				}
			}
			for _, reject := range tc.reject {
				if strings.Contains(out, reject) {
					t.Errorf("/context contains %q: %s", reject, out)
				}
			}
			if strings.Contains(status, "3.1M") || (tc.name == "unknown" && !strings.Contains(status, "Context: Not available")) {
				t.Errorf("/status context: %s", status)
			}
		})
	}
}
