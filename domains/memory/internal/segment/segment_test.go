package segment

import (
	"reflect"
	"testing"
)

func TestTokens(t *testing.T) {
	t.Parallel()

	// CJK expectations assert the meaningful words are present (rather than the
	// exact slice) because gse's HMM may emit slightly varying sub-tokens across
	// dictionary versions; what matters for lexical recall is that a whole
	// sentence is no longer a single giant token.
	tests := []struct {
		name        string
		query       string
		wantSome    []string // every entry must appear in Tokens(query)
		wantNotFlat bool     // true => must produce >1 token (regression: was 1 for CJK)
	}{
		{
			name:        "chinese short phrase splits into words",
			query:       "用户使用中文交流",
			wantSome:    []string{"用户", "使用", "中文", "交流"},
			wantNotFlat: true,
		},
		{
			name:        "chinese full sentence no longer one token",
			query:       "我平时用什么语言交流的",
			wantSome:    []string{"语言", "交流"},
			wantNotFlat: true,
		},
		{
			name:        "chinese question splits",
			query:       "你还记得我用什么语言交流吗？",
			wantSome:    []string{"记得", "语言", "交流"},
			wantNotFlat: true,
		},
		{
			name:     "mixed cjk and latin keeps ascii whole",
			query:    "用户建议尝试写入memory条目",
			wantSome: []string{"用户", "建议", "memory", "条目"},
		},
		{
			name:     "pure english matches strings.Fields",
			query:    "The quick brown fox",
			wantSome: []string{"the", "quick", "brown", "fox"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Tokens(tt.query)
			if len(got) == 0 {
				t.Fatalf("Tokens(%q) returned no tokens", tt.query)
			}
			if tt.wantNotFlat && len(got) <= 1 {
				t.Fatalf("Tokens(%q) = %v, expected >1 token (CJK must be segmented, not whole-string)", tt.query, got)
			}
			for _, w := range tt.wantSome {
				if !contains(got, w) {
					t.Errorf("Tokens(%q) = %v, missing expected token %q", tt.query, got, w)
				}
			}
		})
	}
}

func TestTokensEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("empty string", func(t *testing.T) {
		t.Parallel()
		if got := Tokens(""); len(got) != 0 {
			t.Fatalf(`Tokens("") = %v, want empty`, got)
		}
	})
	t.Run("only punctuation", func(t *testing.T) {
		t.Parallel()
		if got := Tokens("？？？ , , ."); len(got) != 0 {
			t.Fatalf(`Tokens("？？？ , , .") = %v, want empty`, got)
		}
	})
	t.Run("ascii single word", func(t *testing.T) {
		t.Parallel()
		if got := Tokens("memory"); !reflect.DeepEqual(got, []string{"memory"}) {
			t.Fatalf(`Tokens("memory") = %v, want ["memory"]`, got)
		}
	})
}

func TestLexicalScore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     string
		body      string
		wantExact float64 // set when an exact value is asserted; else use wantPos/wantRange
		wantRange [2]float64
		wantPos   bool // true => score must be > 0
		wantNeg   bool // true => score must be 0
	}{
		{
			name:      "empty query is neutral match",
			query:     "",
			body:      "anything",
			wantExact: 1,
		},
		{
			name:      "exact substring fast path",
			query:     "工具能力",
			body:      "用户对工具能力感兴趣",
			wantExact: 1,
		},
		{
			name:  "english token partial match",
			query: "the quick fox",
			body:  "the brown fox",
			// "the" and "fox" hit, "quick" misses => 2/3
			wantExact: 2.0 / 3.0,
		},
		{
			name:    "chinese sentence query now scores above zero",
			query:   "我平时用什么语言交流的",
			body:    "用户使用中文交流",
			wantPos: true, // regression: legacy strings.Fields gave 0 here
		},
		{
			name:    "chinese question scores above zero",
			query:   "你还记得我用什么语言交流吗？",
			body:    "用户使用中文交流",
			wantPos: true,
		},
		{
			name:    "chinese query no overlap scores zero",
			query:   "我喜欢喝茶",
			body:    "用户对工具能力感兴趣",
			wantNeg: true,
		},
		{
			name:      "no query tokens scores zero",
			query:     "？？？",
			body:      "用户使用中文交流",
			wantExact: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := LexicalScore(tt.query, tt.body)
			switch {
			case tt.wantExact > 0 || (tt.name == "no query tokens scores zero" && tt.wantExact == 0):
				if got != tt.wantExact {
					t.Fatalf("LexicalScore(%q,%q) = %v, want %v", tt.query, tt.body, got, tt.wantExact)
				}
			case tt.wantPos:
				if got <= 0 {
					t.Fatalf("LexicalScore(%q,%q) = %v, want > 0", tt.query, tt.body, got)
				}
			case tt.wantNeg:
				if got != 0 {
					t.Fatalf("LexicalScore(%q,%q) = %v, want 0", tt.query, tt.body, got)
				}
			default:
				_ = tt.wantRange
			}
		})
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
