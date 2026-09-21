package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mariobgsp/routre/internal/mock"
	"github.com/mariobgsp/routre/internal/reqlog"
)

// delayedGateway wires a gateway against a mock that takes delay per request,
// so a measured attempt always has a non-zero duration.
func delayedGateway(t *testing.T, delay time.Duration) (*Handlers, *mock.Server) {
	t.Helper()
	up, err := mock.New("a")
	if err != nil {
		t.Fatalf("mock upstream: %v", err)
	}
	t.Cleanup(up.Close)
	up.Delay = delay
	// serveGateway seeds the keystore from the environment in NewHandlers, so
	// the keys must be set before it runs.
	t.Setenv("TEST_KEY_A", "test-key-a")
	t.Setenv("TEST_KEY_B", "test-key-b")
	t.Setenv("TEST_KEY_C", "test-key-c")
	_, h, _ := serveGateway(t, loadTestStore(t, buildMockConfig(t, "openai", map[string]*mock.Server{"a": up})))
	return h, up
}

// TestProcessReturnsOwnPhases: Process hands back this request's own phases.
func TestProcessReturnsOwnPhases(t *testing.T) {
	h, _ := delayedGateway(t, 10*time.Millisecond)
	resp, err := h.pipeline.Process(context.Background(), Request{
		Body:   chatBody(false, ""),
		Path:   "/v1/chat/completions",
		Header: http.Header{},
		Client: "test",
	})
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if resp.Phases == nil {
		t.Fatal("Process returned no phases for an upstream attempt")
	}
	if resp.Phases.TotalMS < 10 {
		t.Fatalf("phases.TotalMS = %d, want >= 10 (the mock's delay)", resp.Phases.TotalMS)
	}
}

// TestStreamReturnsOwnPhases: Stream hands back this request's own phases.
func TestStreamReturnsOwnPhases(t *testing.T) {
	h, _ := delayedGateway(t, 0)
	rw := httptest.NewRecorder()
	phases, err := h.pipeline.Stream(context.Background(), Request{
		Body:   chatBody(true, ""),
		Path:   "/v1/chat/completions",
		Header: http.Header{},
		Client: "test",
	}, rw)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if phases == nil {
		t.Fatal("Stream returned no phases for an upstream attempt")
	}
}

// TestConcurrentRequestsPhaseIsolation: one Pipeline serves every concurrent
// request, so phase timings must be per-request, never stashed on the shared
// instance. On the pre-fix tree this fails under -race (p.lastPhases) and can
// log one request's timings against another; every logged line here must carry
// its own measured total_ms.
func TestConcurrentRequestsPhaseIsolation(t *testing.T) {
	const workers, perWorker = 32, 5
	const delayMS = 10
	h, _ := delayedGateway(t, delayMS*time.Millisecond)

	logPath := filepath.Join(t.TempDir(), "req.jsonl")
	reqlog.SetPath(logPath)
	defer reqlog.SetPath("")

	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				rw := &captureRW{header: http.Header{}}
				// A unique body per request: an identical body would be a cache
				// hit, which returns no phases (and this test is about the
				// upstream-attempt path).
				body := chatBody(false, fmt.Sprintf("unique-%d-%d", w, i))
				r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
				h.route(rw, r, fmtOpenAI)
				if rw.status != http.StatusOK {
					errs <- "non-200 from a concurrent request"
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != workers*perWorker {
		t.Fatalf("log lines = %d, want %d", len(lines), workers*perWorker)
	}
	for i, line := range lines {
		var e reqlog.Entry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if e.TotalMS < delayMS {
			t.Fatalf("line %d: total_ms = %d, want >= %d (phases leaked or missing)", i, e.TotalMS, delayMS)
		}
	}
}
