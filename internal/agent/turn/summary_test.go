package turn

import (
	"slices"
	"strings"
	"testing"
)

func TestPlaceSummaryPutsTheSummaryAfterTheLastKeptItemInsideTheSpan(t *testing.T) {
	for _, tc := range []struct {
		name, items, want string
	}{
		{name: "prefix", items: "ccck", want: "Sk"},
		{name: "nothing kept inside", items: "kccck", want: "kSk"},
		{name: "anchor inside", items: "ccAcck", want: "ASk"},
		{name: "anchors straddled", items: "cAcBck", want: "ABSk"},
		{name: "kept after the span", items: "cckAk", want: "SkAk"},
		{name: "nothing covered", items: "kAk", want: "kAk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := strings.Split(tc.items, "")
			got, placed := PlaceSummary(items, "S", func(item string) bool { return item == "c" })
			if strings.Join(got, "") != tc.want || placed != strings.Contains(tc.items, "c") {
				t.Fatalf("PlaceSummary(%s) = %s placed=%v, want %s", tc.items, strings.Join(got, ""), placed, tc.want)
			}
			if !placed && !slices.Equal(got, items) {
				t.Fatal("nothing covered must leave the items as they are")
			}
		})
	}
}
