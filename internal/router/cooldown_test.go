package router

import (
	"testing"
	"time"
)

func TestReportFailureOverloaded_NoEscalation(t *testing.T) {
	cur := time.Unix(1700000000, 0)
	tiers := []TierInput{
		{Name: "t", Providers: []ProviderInput{
			{Name: "p", Kind: "anthropic", BaseURL: "x", APIKeyEnv: "x", Models: []string{"m"}},
		}},
	}
	r := New(tiers, CooldownPolicy{Base: 2 * time.Second, Max: 5 * time.Minute, MaxHits: 30})
	r.now = func() time.Time { return cur }
	ps := r.provs[0]
	for i := 0; i < 3; i++ {
		cur = cur.Add(time.Duration(i+1) * time.Second)
		r.ReportFailureWithBackoff(ps, ErrOverloaded, time.Second)
		got := r.CooldownRemaining(ps)
		if got > 2*time.Second {
			t.Errorf("overloaded hit %d: cooldown=%s, want <=2s (no escalation)", i+1, got)
		}
	}
}

func TestReportFailureServer_StillEscalates(t *testing.T) {
	cur := time.Unix(1700000000, 0)
	tiers := []TierInput{
		{Name: "t", Providers: []ProviderInput{
			{Name: "p", Kind: "anthropic", BaseURL: "x", APIKeyEnv: "x", Models: []string{"m"}},
		}},
	}
	r := New(tiers, CooldownPolicy{Base: 2 * time.Second, Max: 5 * time.Minute, MaxHits: 30})
	r.now = func() time.Time { return cur }
	ps := r.provs[0]
	for i := 0; i < 9; i++ {
		cur = cur.Add(time.Duration(i+1) * time.Second)
		r.ReportFailureWithBackoff(ps, ErrServer, 0)
	}
	if got := r.CooldownRemaining(ps); got < 4*time.Minute {
		t.Errorf("after 9 server hits, cooldown=%s, want >=4min (still escalating)", got)
	}
}
