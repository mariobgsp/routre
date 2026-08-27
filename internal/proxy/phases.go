package proxy

// Phases holds per-attempt wall-clock breakdown. Used for request-log
// enrichment and DEBUG tracing. TotalMS is the whole attempt time.
type Phases struct {
	DialMS    int64 `json:"dial_ms"`
	HeadersMS int64 `json:"headers_ms"`
	TTFBMS    int64 `json:"ttfb_ms"`
	TotalMS   int64 `json:"total_ms"`
}
