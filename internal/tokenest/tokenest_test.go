package tokenest

import "testing"

func TestFromBytes(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{-1, 0},
		{0, 0},
		{3, 0},
		{4, 1},
		{4000, 1000},
	}
	for _, tc := range cases {
		if got := FromBytes(tc.in); got != tc.want {
			t.Fatalf("FromBytes(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestFromString(t *testing.T) {
	if got := FromString("12345678"); got != 2 {
		t.Fatalf("FromString = %d, want 2", got)
	}
}
