package toolexec_test

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/toolexec"
)

// A tool that returned nothing produced nil for hooks, stream events and rows
// before the typed outputs; the zero ToolOutput keeps that value instead of
// turning into an empty string.
func TestOutputValueZeroIsNil(t *testing.T) {
	t.Parallel()
	if got := toolexec.OutputValue(sdk.ToolOutput{}); got != nil {
		t.Fatalf("zero output = %#v, want nil", got)
	}
	if got := toolexec.OutputValue(sdk.TextOutput("x")); got != "x" {
		t.Fatalf("text output = %#v", got)
	}
	doc, err := sdk.JSONOutput(map[string]any{"ok": true})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := toolexec.OutputValue(doc).(map[string]any); !ok || got["ok"] != true {
		t.Fatalf("document output = %#v", toolexec.OutputValue(doc))
	}
}
