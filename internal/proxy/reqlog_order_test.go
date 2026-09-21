package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mariobgsp/routre/internal/mock"
	"github.com/mariobgsp/routre/internal/reqlog"
)

// captureRW records that a response was committed, so a test can prove the
// request log was emitted after it.
type captureRW struct {
	header http.Header
	status int
	body   bytes.Buffer
	wrote  bool
}

func (c *captureRW) Header() http.Header { return c.header }
func (c *captureRW) WriteHeader(s int) {
	if !c.wrote {
		c.status = s
		c.wrote = true
	}
}
func (c *captureRW) Write(p []byte) (int, error) {
	c.wrote = true
	return c.body.Write(p)
}
func (c *captureRW) Flush() {}

// routeOnce drives route with a recording ResponseWriter and returns the
// response plus the single request-log line it produced.
func routeOnce(t *testing.T, h *Handlers, body []byte) (*captureRW, reqlog.Entry) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "req.jsonl")
	reqlog.SetPath(logPath)
	t.Cleanup(func() { reqlog.SetPath("") })

	rw := &captureRW{header: http.Header{}}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	h.route(rw, r, fmtOpenAI)

	if !rw.wrote {
		t.Fatal("response was never written")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("request log was not written after the response: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("want exactly 1 log line, got %d: %q", len(lines), data)
	}
	var e reqlog.Entry
	if err := json.Unmarshal(lines[0], &e); err != nil {
		t.Fatalf("log line %q: %v", lines[0], err)
	}
	return rw, e
}

// TestRouteLogsAfterResponseOnEveryPath guards the defer refactor: the log
// must be emitted after the response on the success path and on the early
// error paths, exactly once each.
func TestRouteLogsAfterResponseOnEveryPath(t *testing.T) {
	up, err := mock.New("a")
	if err != nil {
		t.Fatalf("mock upstream: %v", err)
	}
	defer up.Close()
	t.Setenv("TEST_KEY_A", "test-key-a")
	st := loadTestStore(t, buildMockConfig(t, "openai", map[string]*mock.Server{"a": up}))
	_, h, _ := serveGateway(t, st)

	// Success: response written, then one "ok" log line.
	rw, e := routeOnce(t, h, chatBody(false, grepShapedContent(0, 1)))
	if rw.status != http.StatusOK || e.Status != http.StatusOK || e.Class != "ok" {
		t.Fatalf("success path: status=%d entry=%+v", rw.status, e)
	}

	// Empty body: early return, defer must still log it.
	rw, e = routeOnce(t, h, nil)
	if rw.status != http.StatusBadRequest || e.Status != http.StatusBadRequest || e.Class != "error" {
		t.Fatalf("empty-body path: status=%d entry=%+v", rw.status, e)
	}
}
