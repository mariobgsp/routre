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
