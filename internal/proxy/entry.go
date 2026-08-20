package proxy

import "net/http"

// ChatCompletions handles POST /v1/chat/completions (OpenAI dialect).
func (h *Handlers) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	h.route(w, r, fmtOpenAI)
}

// Messages handles POST /v1/messages (Anthropic dialect, used by Claude
// Code when pointed at ANTHROPIC_BASE_URL).
func (h *Handlers) Messages(w http.ResponseWriter, r *http.Request) {
	h.route(w, r, fmtAnthropic)
}

// Responses handles POST /v1/responses (OpenAI Responses dialect, used by
// opencode's built-in `openai` provider). The request is translated to
// chat.completions for relay; responses are wrapped back into the Responses
// envelope for the client.
func (h *Handlers) Responses(w http.ResponseWriter, r *http.Request) {
	h.route(w, r, fmtResponses)
}
