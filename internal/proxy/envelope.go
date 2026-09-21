package proxy

import (
	"bytes"
	"encoding/json"

	"github.com/mariobgsp/routre/internal/cache"
	"github.com/mariobgsp/routre/internal/rtk"
)

// cacheKeyVersion prefixes every cache key. The canonical form is now built
// without HTML escaping (json.Marshal rewrites <, > and & to \u003c, which grew
// a JS client's own bytes by up to ~50% on tool-heavy traffic), so keys for
// bodies containing those characters change once. The prefix makes that
// one-time invalidation explicit instead of a silent 0% hit ratio; for bodies
// without < > & the derivation is byte-identical to before.
const cacheKeyVersion = "v2:"

// marshalNoEscape marshals v without HTML escaping and without the trailing
// newline json.Encoder adds. A JS client (JSON.stringify) never escapes <, >
// or &, so re-encoding its body with json.Marshal would grow it.
func marshalNoEscape(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// envelope decodes the request body exactly once and hands the same document
// to RTK, prefix ordering and the cache-key derivation. Before this a 1 MiB
// body was decoded four times and marshalled twice per request.
//
// The bytes sent upstream are the client's ORIGINAL bytes unless RTK or
// prefix ordering actually changed the document; the canonical form is used
// for the cache key only. Canonicalising an untouched body is what grew it.
type envelope struct {
	raw        []byte         // body as received (post-sanitize)
	doc        map[string]any // nil when raw is not a JSON object: byte fallbacks apply
	body       []byte         // bytes to send upstream and to key from
	requested  string
	key        string
	rtkSaved   int
	rtkChanged bool
	reordered  bool
}

// newEnvelope decodes raw once with UseNumber. A non-object body (array,
// scalar, malformed) keeps doc nil so every stage falls back to its byte path,
// exactly as before.
func newEnvelope(raw []byte) *envelope {
	e := &envelope{raw: raw, body: raw}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err == nil {
		if m, ok := v.(map[string]any); ok {
			e.doc = m
		}
	}
	if e.doc != nil {
		if s, ok := e.doc["model"].(string); ok {
			e.requested = s
		}
	}
	if e.requested == "" {
		e.requested = modelFromBody(raw)
	}
	return e
}

// applyRTK compresses tool content in place and accumulates the capped-token
// delta RTK computes per rewritten segment.
func (e *envelope) applyRTK(r *rtk.RTK) bool {
	if e.doc == nil {
		return false // RTK's byte path also refuses non-objects
	}
	changed, saved := r.ApplyDoc(e.doc)
	e.rtkChanged = e.rtkChanged || changed
	e.rtkSaved += saved
	return changed
}

// orderPrompt moves system messages to the front of the shared document.
func (e *envelope) orderPrompt() bool {
	if e.doc == nil {
		out := cache.OrderPrompt(e.raw)
		if !bytes.Equal(out, e.raw) {
			e.body = out
			e.reordered = true
		}
		return e.reordered
	}
	e.reordered = cache.OrderPromptDoc(e.doc) || e.reordered
	return e.reordered
}

// finish performs the single marshal, chooses the bytes to send upstream, and
// computes the cache key exactly once. canonicalKeys mirrors the old keyFor
// switch (v2 prefix aside, the derivation is unchanged).
func (e *envelope) finish(canonicalKeys bool) {
	byteKey := func(b []byte) string {
		if canonicalKeys {
			return cacheKeyVersion + cacheKey(cache.CanonicalJSON(b))
		}
		return cacheKeyVersion + cacheKey(b)
	}
	if e.doc == nil {
		e.key = byteKey(e.body)
		return
	}
	canon := marshalNoEscape(e.doc)
	if canon == nil {
		// Cannot happen for a decoded document; fall back to the raw bytes.
		e.body = e.raw
		e.key = byteKey(e.raw)
		return
	}
	if e.rtkChanged && !e.reordered && len(canon) >= len(e.raw) {
		// RTK's never-grow contract, enforced at the envelope: discard the
		// mutation and send the original bytes.
		e.body = e.raw
		e.rtkChanged = false
		e.key = byteKey(e.raw)
		return
	}
	if e.rtkChanged || e.reordered {
		e.body = canon
	}
	if canonicalKeys {
		e.key = cacheKeyVersion + cacheKey(canon)
		return
	}
	e.key = cacheKeyVersion + cacheKey(e.body)
}
