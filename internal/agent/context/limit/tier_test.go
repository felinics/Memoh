package contextlimit

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"
)

func TestTierLimitStringEmptyUnchanged(t *testing.T) {
	t.Parallel()

	got := PruneTier.LimitString("", "test_tool")
	if got != "" {
		t.Fatalf("LimitString(empty) = %q, want empty", got)
	}
}

func TestTierLimitStringSingleByteUnderCeilingUnchanged(t *testing.T) {
	t.Parallel()

	got := PruneTier.LimitString("a", "test_tool")
	if got != "a" {
		t.Fatalf("LimitString(1 byte) = %q, want unchanged", got)
	}
}

func TestTierLimitStringPreservesHeadAndTailAcrossMultiByteRunes(t *testing.T) {
	t.Parallel()

	unit := "汉😀"
	large := "HEAD" + strings.Repeat(unit, (GatewayArgsTier.MaxBytes/len(unit))+20) + "TAILX"

	got := GatewayArgsTier.LimitString(large, "test_tool")

	if !utf8.ValidString(got) {
		t.Fatalf("limited text is not valid UTF-8:\n%s", got)
	}
	for _, want := range []string{GatewayArgsTier.Marker, "HEAD", "TAIL"} {
		if !strings.Contains(got, want) {
			t.Fatalf("limited text missing %q:\n%s", want, got)
		}
	}
}

func TestTierLimitStringBoundsLineCountUnderByteCeiling(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("x\n", 3000)

	got := GatewayResultTier.LimitString(large, "test_tool")

	if n := countLines(got); n > GatewayResultTier.MaxLines {
		t.Fatalf("limited text lines = %d, want <= %d\n%s", n, GatewayResultTier.MaxLines, got)
	}
	if !strings.Contains(got, GatewayResultTier.Marker) {
		t.Fatalf("limited text missing marker:\n%s", got)
	}
}

func TestTruncateStepToolResultUnderThresholdUnchanged(t *testing.T) {
	t.Parallel()

	msg := sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "lookup", Result: "ok"})

	got, ok := TruncateStepToolResult(msg, 512)
	if ok {
		t.Fatalf("expected ok = false for under-threshold content, got %#v", got)
	}
	if !reflect.DeepEqual(got, msg) {
		t.Fatalf("expected message unchanged, got %#v, want %#v", got, msg)
	}
}

func TestTruncateStepToolResultOverThresholdReplacesWithSummary(t *testing.T) {
	t.Parallel()

	msg := sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "call-2", ToolName: "lookup", Result: strings.Repeat("x", 1000)})

	got, ok := TruncateStepToolResult(msg, 512)
	if !ok {
		t.Fatal("expected ok = true for over-threshold content")
	}
	if len(got.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(got.Content))
	}
	part, isResult := got.Content[0].(sdk.ToolResultPart)
	if !isResult {
		t.Fatalf("expected ToolResultPart, got %T", got.Content[0])
	}
	if part.ToolCallID != "call-2" || part.ToolName != "lookup" {
		t.Fatalf("expected ToolCallID/ToolName preserved, got %+v", part)
	}
	text, _ := part.Result.(string)
	if !strings.Contains(text, "[tool result pruned: ") {
		t.Fatalf("expected pruned summary text, got %q", text)
	}
}

func TestTruncateStepToolResultNonPositiveThresholdDefaultsTo512(t *testing.T) {
	t.Parallel()

	msg := sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "call-3", ToolName: "lookup", Result: strings.Repeat("x", 100)})

	if _, ok := TruncateStepToolResult(msg, 0); ok {
		t.Fatal("expected default threshold 512 to keep 100-byte content unchanged when thresholdBytes=0")
	}
	if _, ok := TruncateStepToolResult(msg, -7); ok {
		t.Fatal("expected default threshold 512 to keep 100-byte content unchanged when thresholdBytes<0")
	}
}
