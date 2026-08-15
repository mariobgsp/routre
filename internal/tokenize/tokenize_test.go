package tokenize

import "testing"

func TestEstimateBasics(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcdefgh", 2},
		{"\n", 1},     // 0 newline-tokens rounds up to 1
		{"ab\ncd", 2}, // 4 ascii chars -> 1, 1 newline -> 0.5 -> ceil 2
	}
	for _, c := range cases {
		if got := Estimate(c.in); got != c.want {
			t.Errorf("Estimate(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestEstimateNonASCII(t *testing.T) {
	// CJK: ~1 token per rune
	got := Estimate("你好世界")
	if got != 4 {
		t.Errorf("Estimate(CJK) = %d, want 4", got)
	}
}

func TestEstimateMonotonic(t *testing.T) {
	short, long := Estimate("short text"), Estimate("short text "+string(make([]byte, 4000)))
	if long <= short {
		t.Errorf("longer input must estimate more tokens: %d vs %d", long, short)
	}
}
