package cache

import (
	"bytes"
	"encoding/json"
)

// CanonicalJSON reduces a JSON document to a deterministic byte form:
// sorted keys, no insignificant whitespace, numbers preserved exactly
// (json.Number — no float64 mangling). Semantically identical documents
// differing only in key order or whitespace produce identical bytes, so
// they collide on the same cache key. It never drops or rewrites values
// (sampling parameters included). Returns input unchanged when it is not
// valid JSON.
func CanonicalJSON(in []byte) []byte {
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

// OrderPrompt moves system messages to the front of the messages array so
// the request has a stable prefix (a prerequisite for upstream prompt-cache
// hits) and so two semantically identical requests differing only in message
// order collide on the same exact-match cache key.
//
// Conservative contract:
//   - returns the input unchanged if anything looks unusual (missing
//     messages, non-array, already ordered, decode failure);
//   - only re-marshals when a reorder actually happened;
//   - never reorders when the first message is already a system message
//     (i.e., a request that is already cache-friendly is not churned).
func OrderPrompt(body []byte) []byte {
	if !json.Valid(body) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return body
	}
	if !OrderPromptDoc(doc) {
		return body
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// OrderPromptDoc is the document half of OrderPrompt: it reorders the
// messages array in place and reports whether anything moved. It exists so a
// caller that already decoded the request body does not decode it again.
//
// Conservative contract (identical to OrderPrompt):
//   - returns false if anything looks unusual (missing messages, non-array,
//     already ordered);
//   - only writes when a reorder actually happened;
//   - never reorders when the first message is already a system message.
func OrderPromptDoc(doc map[string]any) bool {
	msgs, ok := doc["messages"].([]any)
	if !ok || len(msgs) < 2 {
		return false
	}
	sysIdx := -1
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			return false
		}
		if role, _ := mm["role"].(string); role == "system" && i != 0 && sysIdx == -1 {
			sysIdx = i
		}
	}
	if sysIdx == -1 {
		return false
	}
	// Build: [systems..., non-systems...] preserving relative order.
	var systems, rest []any
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if role, _ := mm["role"].(string); role == "system" {
			systems = append(systems, m)
		} else {
			rest = append(rest, m)
		}
	}
	if len(systems) == 0 || len(rest) == 0 {
		return false
	}
	// Already ordered? First message system and no system later.
	ordered := true
	seenNonSystem := false
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if role, _ := mm["role"].(string); role == "system" {
			if seenNonSystem {
				ordered = false
				break
			}
		} else {
			seenNonSystem = true
		}
	}
	if ordered {
		return false
	}
	doc["messages"] = append(systems, rest...)
	return true
}
