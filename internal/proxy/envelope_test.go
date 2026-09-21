package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mariobgsp/routre/internal/cache"
	"github.com/mariobgsp/routre/internal/rtk"
)

// ---- frozen pre-envelope key derivation (golden reference) ----

func legacyCanonicalJSON(in []byte) []byte {
	if !json.Valid(in) {
		return in
	}
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return in
	}
	out, err := json.Marshal(v)
	if err != nil {
		return in
	}
	return out
}

func legacyCacheKey(processed []byte) string {
	if bytes.Contains(processed, []byte(`"stream"`)) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(processed, &m); err == nil {
			if _, ok := m["stream"]; ok {
				delete(m, "stream")
				if b, err := json.Marshal(m); err == nil {
					return cache.Key(b)
				}
			}
		}
	}
	return cache.Key(processed)
}

func legacyKey(processed []byte, canonicalKeys bool) string {
	if canonicalKeys {
		return legacyCacheKey(legacyCanonicalJSON(processed))
	}
	return legacyCacheKey(processed)
}

// TestEnvelopeCacheKeysUnchanged pins the envelope's key derivation to the
// pre-change one for every body whose canonical bytes are unaffected by the
// escaping change (no < > &). Together with the v2 prefix this is the whole
// cache-migration story: nothing else moves.
func TestEnvelopeCacheKeysUnchanged(t *testing.T) {
	bodies := []string{
		`{"model":"m","messages":[{"role":"user","content":"hello"}]}`,
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
		`{"messages":[{"role":"user","content":"x"}],"model":"m"}`,
		`{"model":"m","temperature":0.7,"top_p":1,"messages":[]}`,
		`[1,2,{"a":3}]`,
		`"just a string"`,
		`42`,
	}
	for _, canonicalKeys := range []bool{true, false} {
		for _, b := range bodies {
			env := newEnvelope([]byte(b))
			env.finish(canonicalKeys)
			want := legacyKey([]byte(b), canonicalKeys)
			if got := strings.TrimPrefix(env.key, cacheKeyVersion); got != want {
				t.Errorf("canonicalKeys=%v body=%s\n got %s\nwant %s", canonicalKeys, b, got, want)
			}
			if !strings.HasPrefix(env.key, cacheKeyVersion) {
				t.Errorf("key %q is missing the %q version prefix", env.key, cacheKeyVersion)
			}
		}
	}
}

// TestEnvelopeKeyChangesForHTMLEscapedBodies documents the one deliberate key
// change: a body with literal < > & used to be canonicalised with json.Marshal
// (escaping, and growing it). The escape-free canonical must produce a
// different key exactly once.
func TestEnvelopeKeyChangesForHTMLEscapedBodies(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"<T> && a->b"}]}`)
	env := newEnvelope(body)
	env.finish(true)
	if got, legacy := strings.TrimPrefix(env.key, cacheKeyVersion), legacyKey(body, true); got == legacy {
		t.Fatal("expected the escape-free canonical to change the key for a body with < > &")
	}
}

// TestEnvelopeCanonicalDoesNotGrowJSStyleBody is the measured regression the
// envelope was redesigned for: a JS client sends < > & literally, and a
// json.Marshal canonical round-trip grows such a body by ~50%. The canonical
// form (key input) and the bytes sent upstream must both stay <= the raw body.
func TestEnvelopeCanonicalDoesNotGrowJSStyleBody(t *testing.T) {
	raw := fixtureJSStyleBody(t, 256<<10, nextFixtureNonce())

	env := newEnvelope(raw)
	if env.doc == nil {
		t.Fatal("fixture did not decode as an object")
	}
	changed := env.applyRTK(rtk.New(rtk.DefaultConfig()))
	if !changed {
		t.Fatal("RTK did not fire on the JS-style fixture (the assert would be vacuous)")
	}
	env.finish(true)

	escaped, err := json.Marshal(env.doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(escaped) <= len(raw) {
		t.Fatalf("fixture is not dense in escapable chars (escaped %d <= raw %d): the guard is vacuous",
			len(escaped), len(raw))
	}
	canon := marshalNoEscape(env.doc)
	if len(canon) > len(raw) {
		t.Fatalf("canonical form grew: %d > raw %d (+%.1f%%)",
			len(canon), len(raw), 100*float64(len(canon)-len(raw))/float64(len(raw)))
	}
	if len(env.body) > len(raw) {
		t.Fatalf("upstream body grew: %d > raw %d", len(env.body), len(raw))
	}
}

// TestEnvelopeRTKNeverGrowFallback exercises the guard directly: when the
// single marshal would be larger than the raw body, the mutation is discarded
// and the original bytes are sent (invalid UTF-8 becomes U+FFFD, which is
// larger, so it is a convenient trigger).
func TestEnvelopeRTKNeverGrowFallback(t *testing.T) {
	raw := []byte(`{"model":"m","messages":[{"role":"tool","content":"`)
	raw = append(raw, bytes.Repeat([]byte{0xff}, 512)...)
	raw = append(raw, []byte(`"}]}`)...)

	env := newEnvelope(raw)
	if env.doc == nil {
		t.Fatal("body with invalid UTF-8 did not decode")
	}
	env.rtkChanged = true // pretend RTK rewrote something
	env.rtkSaved = 12_345 // and credited savings for it
	if canon := marshalNoEscape(env.doc); len(canon) <= len(raw) {
		t.Fatalf("precondition failed: canonical %d must exceed raw %d", len(canon), len(raw))
	}
	env.finish(false)
	if !bytes.Equal(env.body, raw) {
		t.Fatalf("never-grow guard did not fall back: sent %d bytes, raw %d", len(env.body), len(raw))
	}
	if env.rtkChanged {
		t.Fatal("rtkChanged must be cleared when the change is discarded")
	}
	// The saved-token delta must be discarded with it: the pipeline reports
	// env.rtkSaved into rtk_saved_total and the ledger, and crediting a
	// compression that never shipped over-reports savings.
	if env.rtkSaved != 0 {
		t.Fatalf("rtkSaved = %d, want 0 for a discarded compression", env.rtkSaved)
	}
}

// TestEnvelopeMalformedBodyFallbacks proves non-object bodies keep the byte
// path and the legacy key derivation.
func TestEnvelopeMalformedBodyFallbacks(t *testing.T) {
	for _, raw := range []string{`{bad`, ``, `null`, `[1,2,3]`, `"x"`, `42`} {
		env := newEnvelope([]byte(raw))
		if env.doc != nil {
			t.Errorf("%q: expected doc nil, got a document", raw)
			continue
		}
		env.finish(true)
		if !bytes.Equal(env.body, []byte(raw)) {
			t.Errorf("%q: body changed to %q", raw, env.body)
		}
		if want := cacheKeyVersion + legacyKey([]byte(raw), true); env.key != want {
			t.Errorf("%q: key %s, want %s", raw, env.key, want)
		}
	}
}

// TestOrderPromptDocMatchesOrderPrompt is the equivalence property between the
// extracted document half and the original byte API.
func TestOrderPromptDocMatchesOrderPrompt(t *testing.T) {
	bodies := []string{
		`{"messages":[{"role":"user","content":"a"},{"role":"system","content":"s"}]}`,
		`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"a"}]}`,
		`{"messages":[{"role":"user","content":"a"}]}`,
		`{"messages":"nope"}`,
		`{"nope":1}`,
		`{"messages":[{"role":"system","content":"s"},{"role":"system","content":"s2"}]}`,
		`{"messages":[{"role":"user","content":"a"},{"role":"system","content":"s"},{"role":"assistant","content":"b"}]}`,
		`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"a"},{"role":"system","content":"s2"}]}`,
	}
	for _, b := range bodies {
		dec := json.NewDecoder(strings.NewReader(b))
		dec.UseNumber()
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("%s: decode: %v", b, err)
		}
		gotChanged := cache.OrderPromptDoc(doc)
		wantOut := cache.OrderPrompt([]byte(b))
		wantChanged := !bytes.Equal(wantOut, []byte(b))
		if gotChanged != wantChanged {
			t.Errorf("%s: decision doc=%v bytes=%v", b, gotChanged, wantChanged)
		}
		if wantChanged {
			gotOut, err := json.Marshal(doc)
			if err != nil {
				t.Fatalf("%s: marshal: %v", b, err)
			}
			if !bytes.Equal(gotOut, wantOut) {
				t.Errorf("%s:\n got %s\nwant %s", b, gotOut, wantOut)
			}
		}
	}
}
