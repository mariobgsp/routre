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
