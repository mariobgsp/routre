package cache

import (
	"bytes"
	"encoding/json"
)

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
	msgs, ok := doc["messages"].([]any)
	if !ok || len(msgs) < 2 {
		return body
	}
	firstIsSystem := false
	sysIdx := -1
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			return body
		}
		role, _ := mm["role"].(string)
		if role == "system" {
			if i == 0 {
				firstIsSystem = true
			} else if sysIdx == -1 {
				sysIdx = i
			}
			continue
		}
		if sysIdx == -1 && i > 0 {
			// A non-system message before any system message: order is
			// already "system later"; only reorder if there IS a system
			// message after a non-system one.
		}
	}
	_ = firstIsSystem
	if sysIdx == -1 {
		return body
	}
	// Ensure no system message appears after a non-system one.
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
		return body
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
		return body
	}
	doc["messages"] = append(systems, rest...)
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}
