package proxy

import (
	"io"

	"github.com/mariobgsp/routre/internal/proxy/dialect"
)

// translateStream is the proxy-package seam for cross-kind SSE translation.
// The full state machines live in internal/proxy/dialect; this stays a thin
// delegate so existing callers/tests keep compiling.
func translateStream(w io.Writer, upstream io.Reader, from, to apiFormat, flush func()) error {
	return dialect.New().Stream(dialect.Format(from), dialect.Format(to), upstream, w, flush)
}
