// Package failures renders request-failure outcomes to the client.
//
// A 503 response on the wire (or a one-line human summary in a CLI tool)
// always describes the same conceptual thing: every provider the gateway
// could have used, what each one said (or didn't say), and how long until
// the next try. This package owns that single shape; pipeline emits it on
// the wire, `routre doctor` and the probe emit it on the terminal.
//
// Lossy mappings are invariants, not bugs (see internal/proxy/dialect
// header). The Outcome struct is the smallest struct that carries the
// invariants callers need:
//
//   - Class is the router.ErrClass name ("server", "auth", "rateLimit",
//     "network", "client"). Empty on a successful outcome.
//   - Cooldown is the time until the next request to that provider is
//     allowed (0 = not in cooldown). Rendered rounded to whole seconds.
//   - Err is the human-readable last error; omitted on the wire when
//     empty so OK outcomes stay short.
package failures

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Kind enumerates the 503 shapes this package renders. Each Kind has
// a fixed `type` field on the wire and a fixed omission rule:
//
//   - KindAllFailed        — tried every candidate, none produced a
//     response. Body includes `attempts[]`.
//   - KindProvidersUnavailable — cands==0 because every serving provider
//     is in cooldown. Body includes
//     `cooldown_seconds` (min over the set).
//   - KindModelNotFound    — cands==0 because the model is not on any
//     provider's list. Body has no `attempts`
//     and no `cooldown_seconds`.
type Kind int

const (
	KindAllFailed Kind = iota
	KindProvidersUnavailable
	KindModelNotFound
)

func (k Kind) wireType() string {
	switch k {
	case KindAllFailed:
		return "all_providers_failed"
	case KindProvidersUnavailable:
		return "providers_unavailable"
	case KindModelNotFound:
		return "model_not_found"
	default:
		return "unknown"
	}
}

// Outcome is the structured per-provider failure record. One Outcome
// per attempt (or per dedup'd provider, for the wire shape).
type Outcome struct {
	Provider string
	Kind     string        // "openai" | "anthropic" | "gemini"; empty when unknown
	Class    string        // router.ErrClass.String(); empty on a non-error skip
	Err      string        // human-readable last error; empty when nothing to say
	Cooldown time.Duration // 0 = not in cooldown; rendered as cooldown_remaining_seconds
}

// wireEntry is the on-the-wire per-provider record. Field tags MUST
// stay aligned with the existing 503 body shape (see proxy_test.go
// Test*AllProvidersFailedIncludesReasons) — wire format is the
// module's contract.
type wireEntry struct {
	Provider        string `json:"provider"`
	Kind            string `json:"kind,omitempty"`
	Class           string `json:"class,omitempty"`
	Error           string `json:"error,omitempty"`
	CooldownSeconds int64  `json:"cooldown_remaining_seconds,omitempty"`
}

// body is the wire envelope. Same shape as the inline pipeline 503s
// before this package existed; field names are part of the contract.
type body struct {
	Error struct {
		Message         string      `json:"message"`
		Type            string      `json:"type"`
		Model           string      `json:"model"`
		CooldownSeconds int64       `json:"cooldown_seconds,omitempty"`
		Attempts        []wireEntry `json:"attempts,omitempty"`
	} `json:"error"`
}

// Render writes a 503 to w with the per-provider breakdown (when
// outcomes is non-empty). It sets Content-Type and Retry-After.
//
// outcomes is the per-provider breakdown in try-order; for
// KindProvidersUnavailable and KindModelNotFound the attempts are
// omitted on the wire (those failures describe the *set* of
// providers, not specific attempts).
//
// retryAfter is rounded up to 1 second when non-zero so a half-second
// upstream Retry-After still surfaces as Retry-After: 1.
func Render(w http.ResponseWriter, kind Kind, model string, outcomes []Outcome, retryAfter time.Duration) {
	body, header := RenderBody(kind, model, outcomes, retryAfter)
	for k, vv := range header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write(body)
}

// RenderBody returns the same body as []byte + the headers the caller
// should set. Use this on code paths that build a Response struct
// (non-streaming) instead of writing directly to a ResponseWriter.
func RenderBody(kind Kind, model string, outcomes []Outcome, retryAfter time.Duration) ([]byte, http.Header) {
	b := buildBody(kind, model, outcomes)
	out, err := json.Marshal(b)
	if err != nil {
		// json.Marshal of a struct of strings/ints/[]wireEntry cannot
		// fail in practice; the hand-rolled fallback preserves the
		// legacy wire shape on a programmer error.
		out = []byte(fmt.Sprintf(`{"error":{"message":%q,"type":%q,"model":%q}}`,
			fmt.Sprintf("all providers for model %q failed", model), kind.wireType(), model))
	}
	h := http.Header{"Content-Type": []string{"application/json"}}
	if retryAfter > 0 {
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		h["Retry-After"] = []string{fmt.Sprintf("%d", int(retryAfter.Seconds()))}
	}
	return out, h
}

// RenderHuman writes a one-line per-provider summary to w for CLI tools
// (`routre doctor`) and the probe's logResult path. Same Outcome type;
// different format. Order is preserved (callers already deduplicated).
//
// Format per line (space-separated, no trailing newline — caller adds):
//
//	<provider>  <kind>  <class>  status=<int>  <err>  (<latency>ms)  [cooldown=<dur>]
//
// For OK outcomes: "  <kind>  OK  (<latency>ms)".
// For failures:    "  <kind>  <class>  status=<int>  <err>".
func RenderHuman(w io.Writer, outcomes []Outcome) {
	for _, o := range outcomes {
		if o.Class == "" || o.Class == "ok" {
			fmt.Fprintf(w, "  %-18s %-9s OK", o.Provider, o.Kind)
			if o.Cooldown > 0 {
				fmt.Fprintf(w, "  cooldown=%s", o.Cooldown.Round(time.Second))
			}
			fmt.Fprintln(w)
			continue
		}
		fmt.Fprintf(w, "  %-18s %-9s %s", o.Provider, o.Kind, o.Class)
		if o.Err != "" {
			fmt.Fprintf(w, " %s", o.Err)
		}
		if o.Cooldown > 0 {
			fmt.Fprintf(w, "  cooldown=%s", o.Cooldown.Round(time.Second))
		}
		fmt.Fprintln(w)
	}
}

// buildBody constructs the wire envelope. Kept private so callers
// cannot bypass the JSON shaping rules (field omission, cooldown
// rounding, type-string mapping).
func buildBody(kind Kind, model string, outcomes []Outcome) body {
	var b body
	b.Error.Type = kind.wireType()
	b.Error.Model = model
	switch kind {
	case KindAllFailed:
		b.Error.Message = fmt.Sprintf("all providers for model %q failed", model)
		b.Error.Attempts = dedup(outcomes)
	case KindProvidersUnavailable:
		// The message + cooldown are set by the caller in the existing
		// pipeline flow; this default is overwritten by RenderBody's
		// pre-built shape. Kept here for the marshal-fail fallback.
		b.Error.Message = fmt.Sprintf("all providers that serve model %q are cooling down", model)
		if min := minCooldown(outcomes); min > 0 {
			b.Error.CooldownSeconds = int64(min.Round(time.Second).Seconds())
		}
	case KindModelNotFound:
		b.Error.Message = fmt.Sprintf("no configured provider serves model %q (check config tiers/models)", model)
	}
	return b
}

// dedup returns one entry per provider, last write wins. Matches the
// pipeline loop's lastErr/lastClass semantics: if the same provider
// was tried more than once, the most recent attempt's record is the
// one shown to the user.
func dedup(outcomes []Outcome) []wireEntry {
	seen := make(map[string]int, len(outcomes))
	uniq := make([]Outcome, 0, len(outcomes))
	for _, o := range outcomes {
		if i, ok := seen[o.Provider]; ok {
			uniq[i] = o
			continue
		}
		seen[o.Provider] = len(uniq)
		uniq = append(uniq, o)
	}
	out := make([]wireEntry, 0, len(uniq))
	for _, o := range uniq {
		we := wireEntry{Provider: o.Provider, Kind: o.Kind, Error: o.Err}
		if o.Class != "" && o.Class != "ok" {
			we.Class = o.Class
		}
		if o.Cooldown > 0 {
			we.CooldownSeconds = int64(o.Cooldown.Round(time.Second).Seconds())
		}
		out = append(out, we)
	}
	return out
}

// minCooldown returns the smallest positive Cooldown across outcomes.
// Used for the providers_unavailable envelope's cooldown_seconds.
func minCooldown(outcomes []Outcome) time.Duration {
	var min time.Duration
	for _, o := range outcomes {
		if o.Cooldown <= 0 {
			continue
		}
		if min == 0 || o.Cooldown < min {
			min = o.Cooldown
		}
	}
	return min
}
