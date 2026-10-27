package contextlimit

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTierLimitStringEmptyUnchanged(t *testing.T) {
	t.Parallel()

	got := GatewayResultTier.LimitString("", "test_tool")
	if got != "" {
		t.Fatalf("LimitString(empty) = %q, want empty", got)
	}
}

func TestTierLimitStringSingleByteUnderCeilingUnchanged(t *testing.T) {
	t.Parallel()

	got := GatewayResultTier.LimitString("a", "test_tool")
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
