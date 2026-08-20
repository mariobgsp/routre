package tokenize

import "testing"

func TestCountKnownTokens(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"hello", 1},       // "hello" is a single cl100k token
		{"world", 1},       // "world" is a single cl100k token
		{"hello world", 2}, // "hello" + " world"
		{"a", 1},
	}
	for _, c := range cases {
		if got := Count(c.text, KindOpenAI); got != c.want {
			t.Errorf("Count(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}

func TestCountMonotonicBounded(t *testing.T) {
	// A token count must never exceed the byte length of the input and must
	// never be negative.
	for _, s := range []string{
		"the quick brown fox jumps over the lazy dog",
		"hello world this is a longer sentence with punctuation!",
		"12345 67890",
		"日本語のテキストです",
		"<tool_result>\nline1\nline2\n</tool_result>",
	} {
		n := Count(s, KindOpenAI)
		if n < 0 || n > len(s) {
			t.Fatalf("Count(%q) = %d, out of range [0,%d]", s, n, len(s))
		}
	}
}

func TestCountCachedDeterministic(t *testing.T) {
	text := "the quick brown fox jumps over the lazy dog"
	a := Count(text, KindOpenAI)
	b := Count(text, KindOpenAI) // should hit the cache
	if a != b {
		t.Fatalf("Count not deterministic: %d vs %d", a, b)
	}
	if a == 0 {
		t.Fatal("expected a non-zero count")
	}
}

func TestCountEmpty(t *testing.T) {
	if got := Count("", KindAnthropic); got != 0 {
		t.Fatalf("Count(\"\") = %d, want 0", got)
	}
}
