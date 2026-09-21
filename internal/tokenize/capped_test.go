package tokenize

import (
	"strings"
	"testing"
	"time"
)

// TestCountTokensSizeCap pins the exact/estimate switch at ExactLimit and
// proves a 1 MiB body never pays for a BPE count.
func TestCountTokensSizeCap(t *testing.T) {
	atCap := strings.Repeat("a", ExactLimit)
	if got, want := CountCapped(atCap), int64(Count(atCap, KindOpenAI)); got != want {
		t.Errorf("at the cap: CountCapped=%d, want exact %d", got, want)
	}

	over := strings.Repeat("a", ExactLimit+1)
	if got, want := CountCapped(over), int64(Estimate(over)); got != want {
		t.Errorf("above the cap: CountCapped=%d, want Estimate %d", got, want)
	}

	oneMB := strings.Repeat("a", 1<<20)
	if got, want := CountCapped(oneMB), int64(Estimate(oneMB)); got != want {
		t.Fatalf("1 MiB: CountCapped=%d, want Estimate %d (a BPE count ran)", got, want)
	}
	start := time.Now()
	CountCapped(oneMB)
	// Estimate is an O(n) rune scan (~1-2 ms/MiB); a BPE count is ~700 ms.
	// The bound is loose enough to survive -race instrumentation and still
	// fails by two orders of magnitude if an exact count sneaks back in.
	if d := time.Since(start); d > 25*time.Millisecond {
		t.Errorf("CountCapped(1 MiB) took %v, want <25ms", d)
	}
}

// TestClampCountOverCounts proves the max_tokens sizing estimate is
// pessimistic. Under-counting would inflate max_tokens and make the upstream
// reject the whole request for exceeding its window.
func TestClampCountOverCounts(t *testing.T) {
	atCap := strings.Repeat("a", ExactLimit)
	if got, want := ClampCount(atCap), int64(Count(atCap, KindOpenAI)); got != want {
		t.Errorf("at the cap: ClampCount=%d, want exact %d", got, want)
	}

	// The real clamp input is a json.Marshal result, which escapes newlines.
	big := strings.Repeat(`{"role":"tool","content":"x"}`, 20_000) // ~580 KiB
	got := ClampCount(big)
	if want := int64(len(big)+2)/3 + 1; got != want {
		t.Errorf("above the cap: ClampCount=%d, want ceil(bytes/3)+1 = %d", got, want)
	}
	if est := int64(Estimate(big)); got < est {
		t.Errorf("ClampCount=%d under-counts Estimate=%d", got, est)
	}
}

// TestCountCappedNeverExactAboveLimit is the local half of the request-path
// guard: the capped helpers must not reach the BPE counter above ExactLimit.
func TestCountCappedNeverExactAboveLimit(t *testing.T) {
	var seen []int
	SetCountObserver(func(n int) { seen = append(seen, n) })
	defer SetCountObserver(nil)

	big := strings.Repeat("a", ExactLimit+4096)
	CountCapped(big)
	ClampCount(big)
	if len(seen) != 0 {
		t.Fatalf("exact Count ran above the cap: saw lengths %v (cap %d)", seen, ExactLimit)
	}

	CountCapped(strings.Repeat("a", 128))
	if len(seen) != 1 || seen[0] != 128 {
		t.Fatalf("below the cap the observer must see the exact count, got %v", seen)
	}
}
