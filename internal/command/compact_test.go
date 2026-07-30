package command

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/memohai/memoh/internal/agent/context/compaction"
	"github.com/memohai/memoh/internal/i18n"
)

func TestCompactRunErrorKeepsDiagnosticsOutOfChat(t *testing.T) {
	t.Parallel()

	h := &Handler{}
	cc := CommandContext{L: i18n.New("en")}

	tooSmall := h.compactRunError(cc, fmt.Errorf("compaction: %w: window=512 output_reserve=51 fixed_prompt=180", compaction.ErrSummaryWindowTooSmall))
	if tooSmall == "" || strings.Contains(tooSmall, "window=") {
		t.Fatalf("window-too-small message leaked diagnostics: %q", tooSmall)
	}

	generic := h.compactRunError(cc, errors.New("dial tcp 10.0.0.1:443: i/o timeout"))
	if generic == "" || strings.Contains(generic, "dial tcp") {
		t.Fatalf("generic run failure leaked diagnostics: %q", generic)
	}

	if tooSmall == generic {
		t.Fatal("window-too-small must surface its own actionable message, not the generic failure")
	}
}
