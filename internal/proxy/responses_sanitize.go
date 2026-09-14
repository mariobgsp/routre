// Package proxy — Responses sanitizer for provider drift.
//
// OpenCode validates caller-bound Responses state (reasoning
// encrypted_content, previous_response_id). Pi replays those across turns;
// the upstream then rejects with 400 invalid_request_error and routre
// surfaces 503 all_failed. Sanitize upfront, keep everything else verbatim.
package proxy

import (
	"bytes"
	"encoding/json"
)

func sanitizeResponsesPayload(body []byte) []byte {
	if len(body) == 0 || !json.Valid(body) {
		return body
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return body
	}
	changed := false
	if _, ok := doc["previous_response_id"]; ok {
		delete(doc, "previous_response_id")
		changed = true
	}
	raw, ok := doc["input"]
	if !ok || len(raw) == 0 || raw[0] == '"' {
		if !changed {
			return body
		}
		out, err := json.Marshal(doc)
		if err != nil {
			return body
		}
		return out
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		if !changed {
			return body
		}
		out, err := json.Marshal(doc)
		if err != nil {
			return body
		}
		return out
	}
	kept := items[:0]
	for _, it := range items {
		var typ struct {
			Type string `json:"type"`
		}
		ib, _ := json.Marshal(it)
		_ = json.Unmarshal(ib, &typ)
		if typ.Type == "reasoning" {
			changed = true
			continue
		}
		if _, has := it["encrypted_content"]; has {
			delete(it, "encrypted_content")
			changed = true
		}
		kept = append(kept, it)
	}
	if !changed {
		return body
	}
	norm, err := json.Marshal(kept)
	if err != nil {
		return body
	}
	doc["input"] = norm
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// responsesCacheable reports whether a Responses body is safe to cache.
// Caller-bound state must never hit the gateway cache. JSON-aware so
// whitespace/key-order variants cannot slip through a substring check.
func responsesCacheable(body []byte) bool {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return false
	}
	if _, ok := doc["previous_response_id"]; ok {
		return false
	}
	raw, ok := doc["input"]
	if !ok || len(raw) == 0 {
		return true
	}
	if bytes.Contains(raw, []byte(`"encrypted_content"`)) {
		return false
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return !bytes.Contains(raw, []byte("reasoning"))
	}
	for _, it := range items {
		var typ string
		if err := json.Unmarshal(it["type"], &typ); err == nil && typ == "reasoning" {
			return false
		}
		if _, has := it["encrypted_content"]; has {
			return false
		}
	}
	return true
}

// cacheableRequest allows safe-prefix caching: Chat always, Responses only
// when free of caller-bound state. This chases pi prefix-reuse hit rate
// without ever replaying encrypted reasoning.
func cacheableRequest(clientFmt apiFormat, processed []byte) bool {
	if clientFmt != fmtResponses {
		return true
	}
	return responsesCacheable(processed)
}

// isReasoningStateError matches the upstream rejection for stale
// caller-bound reasoning so the gateway can retry once sanitized.
func isReasoningStateError(status int, errBody []byte) bool {
	if status != 400 {
		return false
	}
	low := bytes.ToLower(errBody)
	return bytes.Contains(low, []byte("encrypted_content")) ||
		(bytes.Contains(low, []byte("reasoning")) && bytes.Contains(low, []byte("not issued")))
}
