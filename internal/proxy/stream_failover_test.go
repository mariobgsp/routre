package proxy

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/mariobgsp/routre/internal/proxy/dialect"
)

// errReader returns n bytes then err.
type errReader struct {
	src string
	err error
	n   int // bytes to emit before err (0 = error on first read)
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.n > 0 {
		nn := r.n
		if nn > len(p) {
			nn = len(p)
		}
		copy(p, r.src[:nn])
		r.src = r.src[nn:]
		r.n -= nn
		return nn, nil
	}
	return 0, r.err
}

// failWriter fails on the first Write.
type failWriter struct{ err error }

func (w *failWriter) Write(p []byte) (int, error) { return 0, w.err }

var errUpstreamRead = errors.New("upstream read error")
var errTranslate = errors.New("translate error")
var errClientGone = errors.New("client gone")

// a2oRoleFrame is an Anthropic frame that yields OpenAI output (role chunk),
// so st.emitted becomes true after the first frame.
const a2oRoleFrame = "event: message_start\ndata: " +
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}` +
	"\n\n"

// a2oBadFrame is a frame whose data is NOT valid JSON, so the a2o
// translator's json.Unmarshal fails -> perr != nil.
const a2oBadFrame = "data: this-is-not-json\n\n"

func readAllStream(w io.Writer, upstream io.Reader, from, to apiFormat) error {
	return translateStream(w, upstream, from, to, nil)
}

// 1. Upstream read error BEFORE any byte emitted -> the underlying error is
// returned unmodified (retryable by the caller: fail over to next candidate).
func TestTranslateStreamErrBeforeFirstByte(t *testing.T) {
	w := &strings.Builder{}
	err := readAllStream(w, &errReader{err: errUpstreamRead}, fmtAnthropic, fmtOpenAI)
	if !errors.Is(err, errUpstreamRead) {
		t.Fatalf("expected upstream error to propagate before first byte, got %v", err)
	}
	if w.Len() != 0 {
		t.Fatalf("expected no bytes emitted on pre-first-byte failure, got %q", w.String())
	}
}

// 2. Upstream read error AFTER the first frame was emitted -> ErrAborted
// (must NOT fail over; the client already received bytes; relay maps this to
// the stream-abort contract so the truncated stream is never cached).
func TestTranslateStreamErrAfterFirstByteNoFailover(t *testing.T) {
	src := a2oRoleFrame // emits a role chunk -> st.emitted = true
	r := &errReader{src: src, n: len(src), err: errUpstreamRead}
	w := &strings.Builder{}
	err := readAllStream(w, r, fmtAnthropic, fmtOpenAI)
	if !errors.Is(err, dialect.ErrAborted) {
		t.Fatalf("expected dialect.ErrAborted after first-byte upstream failure, got %v", err)
	}
	if w.Len() == 0 {
		t.Fatalf("expected the role chunk to have been emitted before the error")
	}
}

// 3. Translation error BEFORE first byte -> returns the translate error
// (retryable: fail over).
func TestTranslateStreamTranslateErrBeforeFirstByte(t *testing.T) {
	r := &errReader{src: a2oBadFrame, n: len(a2oBadFrame), err: io.EOF}
	w := &strings.Builder{}
	err := readAllStream(w, r, fmtAnthropic, fmtOpenAI)
	if err == nil {
		t.Fatalf("expected a translate error to propagate before first byte")
	}
	if w.Len() != 0 {
		t.Fatalf("expected no bytes emitted on pre-first-byte translate failure")
	}
}

// 4. Translation error AFTER first byte -> ErrAborted (no failover).
func TestTranslateStreamTranslateErrAfterFirstByte(t *testing.T) {
	src := a2oRoleFrame + a2oBadFrame
	r := &errReader{src: src, n: len(src), err: io.EOF}
	w := &strings.Builder{}
	err := readAllStream(w, r, fmtAnthropic, fmtOpenAI)
	if !errors.Is(err, dialect.ErrAborted) {
		t.Fatalf("expected dialect.ErrAborted after first-byte translate failure, got %v", err)
	}
	if w.Len() == 0 {
		t.Fatalf("expected role chunk emitted, got %q", w.String())
	}
}

// 5. Client went away mid-stream -> nil (not an upstream failure, no failover).
func TestTranslateStreamClientGone(t *testing.T) {
	src := a2oRoleFrame
	r := &errReader{src: src, n: len(src), err: io.EOF}
	err := readAllStream(&failWriter{err: errClientGone}, r, fmtAnthropic, fmtOpenAI)
	// The write fails on the first frame, so no bytes ever reached the client;
	// translateStream treats it as the client going away and returns nil.
	if err != nil {
		t.Fatalf("expected nil on client-gone write, got %v", err)
	}
}

// 6. Clean EOF between frames -> nil (normal end).
func TestTranslateStreamCleanEnd(t *testing.T) {
	w := &strings.Builder{}
	err := readAllStream(w, strings.NewReader(a2oRoleFrame), fmtAnthropic, fmtOpenAI)
	if err != nil {
		t.Fatalf("expected nil on clean end, got %v", err)
	}
	if w.Len() == 0 {
		t.Fatalf("expected role chunk to be emitted on clean end")
	}
}
