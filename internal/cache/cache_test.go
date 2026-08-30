package cache

import (
	"strings"
	"testing"
	"time"
)

func TestPutGet(t *testing.T) {
	c := New(DefaultConfig())
	key := Key([]byte("req"))
	c.Put(key, Entry{Body: []byte("resp"), ContentType: "application/json"})
	got, ok := c.Get(key)
	if !ok || string(got.Body) != "resp" {
		t.Fatalf("Get = %+v, %v", got, ok)
	}
}

func TestDisabled(t *testing.T) {
	c := New(Config{Enabled: false})
	c.Put("k", Entry{Body: []byte("x")})
	if _, ok := c.Get("k"); ok {
		t.Fatal("disabled cache must not serve entries")
	}
}

func TestTTLExpiry(t *testing.T) {
	c := New(Config{Enabled: true, MaxEntries: 8, TTLSeconds: 1})
	c.Put("k", Entry{Body: []byte("x")})
	time.Sleep(1100 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("entry must expire after TTL")
	}
}

func TestLRUEviction(t *testing.T) {
	c := New(Config{Enabled: true, MaxEntries: 2, TTLSeconds: 3600})
	c.Put("a", Entry{Body: []byte("1")})
	c.Put("b", Entry{Body: []byte("2")})
	c.Get("a") // refresh a
	c.Put("c", Entry{Body: []byte("3")})
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a was refreshed and must survive")
	}
	if _, ok := c.Get("b"); ok {
		t.Fatal("b is LRU and must be evicted")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("c must be present")
	}
}

func TestOversizedSkipped(t *testing.T) {
	c := New(DefaultConfig())
	c.Put("big", Entry{Body: make([]byte, 9<<20)})
	if c.Len() != 0 {
		t.Fatal("oversized entry must be skipped")
	}
}

func TestByteCapEviction(t *testing.T) {
	c := New(Config{Enabled: true, MaxEntries: 100, TTLSeconds: 3600, MaxBytes: 10})
	c.Put("a", Entry{Body: []byte("aaaaaa")}) // 6
	c.Put("b", Entry{Body: []byte("bbbbb")})  // 5 -> 11 > 10, evict a
	if _, ok := c.Get("a"); ok {
		t.Fatal("a must be evicted by the byte cap")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("b must survive")
	}
}

func TestKeyDeterministic(t *testing.T) {
	if Key([]byte("x")) != Key([]byte("x")) {
		t.Fatal("key must be deterministic")
	}
	if Key([]byte("x")) == Key([]byte("y")) {
		t.Fatal("distinct bodies must have distinct keys")
	}
}

func TestOrderPrompt(t *testing.T) {
	body := `{"model":"gpt-5","messages":[{"role":"user","content":"hi"},{"role":"system","content":"be terse"}]}`
	out := OrderPrompt([]byte(body))
	s := string(out)
	si := strings.Index(s, `"system"`)
	ui := strings.Index(s, `"user"`)
	if si == -1 || ui == -1 || si > ui {
		t.Fatalf("system message must be first: %s", s)
	}
}

func TestOrderPromptAlreadyOrdered(t *testing.T) {
	body := `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}]}`
	if out := OrderPrompt([]byte(body)); string(out) != body {
		t.Fatal("already-ordered request must not be churned")
	}
}

func TestOrderPromptBails(t *testing.T) {
	if out := OrderPrompt([]byte(`{"messages":[`)); string(out) != `{"messages":[` {
		t.Fatal("malformed input must pass through")
	}
	if out := OrderPrompt([]byte(`{"x":1}`)); string(out) != `{"x":1}` {
		t.Fatal("no messages: pass through")
	}
}

func TestMissReasons(t *testing.T) {
	c := New(Config{Enabled: true, MaxEntries: 8, TTLSeconds: 1})
	if _, ok, r := c.GetWithReason("k"); ok || r != MissAbsent {
		t.Fatalf("absent miss: got ok=%v reason=%q", ok, r)
	}
	c.Put("k", Entry{Body: []byte("x")})
	if _, ok, r := c.GetWithReason("k"); !ok || r != "" {
		t.Fatalf("hit: got ok=%v reason=%q", ok, r)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, ok, r := c.GetWithReason("k"); ok || r != MissExpired {
		t.Fatalf("expired miss: got ok=%v reason=%q", ok, r)
	}
	d := New(Config{Enabled: false})
	d.Put("k", Entry{Body: []byte("x")})
	if _, ok, r := d.GetWithReason("k"); ok || r != MissDisabled {
		t.Fatalf("disabled miss: got ok=%v reason=%q", ok, r)
	}
}

func TestCanonicalJSON(t *testing.T) {
	a := `{"model":"gpt-5", "messages":[{"role":"user","content":"hi"}], "temperature":0.7}`
	b := `{"temperature":0.7,"model":"gpt-5","messages":[{"content":"hi","role":"user"}]}`
	ca := CanonicalJSON([]byte(a))
	cb := CanonicalJSON([]byte(b))
	if string(ca) != string(cb) {
		t.Fatalf("canonical mismatch:\n%s\n%s", ca, cb)
	}
	if !strings.Contains(string(ca), `"temperature":0.7`) {
		t.Fatalf("sampling param must be preserved: %s", ca)
	}
	if Key(ca) != Key(cb) {
		t.Fatal("canonical forms must key identically")
	}
}

func TestCanonicalJSONNumberPrecision(t *testing.T) {
	in := `{"n":9007199254740993}`
	out := CanonicalJSON([]byte(in))
	if string(out) != in {
		t.Fatalf("large int must survive round-trip exactly: %s", out)
	}
}

func TestCanonicalJSONInvalid(t *testing.T) {
	in := []byte(`{"messages":[`)
	if out := CanonicalJSON(in); string(out) != string(in) {
		t.Fatal("invalid JSON must pass through unchanged")
	}
}

func TestSlidingTTL(t *testing.T) {
	// TTL 1s, sliding on: a hit shortly before expiry must refresh it so
	// the entry survives past the original deadline.
	c := New(Config{Enabled: true, MaxEntries: 8, TTLSeconds: 1, SlidingTTL: true})
	c.Put("k", Entry{Body: []byte("x")})
	time.Sleep(600 * time.Millisecond)
	if _, ok, _ := c.GetWithReason("k"); !ok {
		t.Fatal("entry must be alive at 0.6s")
	}
	time.Sleep(600 * time.Millisecond) // now 1.2s > original 1s TTL
	if _, ok, r := c.GetWithReason("k"); !ok {
		t.Fatalf("sliding hit must refresh expiry (reason=%q)", r)
	}
}

func TestSlidingTTLOff(t *testing.T) {
	c := New(Config{Enabled: true, MaxEntries: 8, TTLSeconds: 1, SlidingTTL: false})
	c.Put("k", Entry{Body: []byte("x")})
	time.Sleep(600 * time.Millisecond)
	if _, ok, _ := c.GetWithReason("k"); !ok {
		t.Fatal("entry must be alive at 0.6s")
	}
	time.Sleep(600 * time.Millisecond)
	if _, ok, r := c.GetWithReason("k"); ok || r != MissExpired {
		t.Fatalf("fixed TTL must expire at 1s: ok=%v reason=%q", ok, r)
	}
}
