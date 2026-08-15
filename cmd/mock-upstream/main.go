// Command mock-upstream starts the mock LLM provider from internal/mock as
// a standalone HTTP server — used to validate router-cli end to end with
// real clients (e.g. opencode) when no paid provider key is available.
//
// Usage:
//
//	mock-upstream [-addr 127.0.0.1:19999] [-fail 500] [-stream]
package main

import (
	"flag"
	"log"
	"net/http"
	"strconv"

	"routre-cli/internal/mock"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:19999", "listen address")
	fail := flag.Int("fail", 0, "always respond with this status (0 = disabled)")
	stream := flag.Bool("stream", false, "always stream SSE responses")
	portStr := flag.String("port", "", "shorthand for -addr 127.0.0.1:<port>")
	flag.Parse()
	if *portStr != "" {
		if _, err := strconv.Atoi(*portStr); err != nil {
			log.Fatalf("invalid -port: %v", err)
		}
		*addr = "127.0.0.1:" + *portStr
	}

	m, err := mock.NewAt("standalone", *addr)
	if err != nil {
		log.Fatalf("mock: %v", err)
	}
	if *fail != 0 {
		m.SetFail(*fail)
	}
	if *stream {
		m.SetStream(true)
	}
	log.Printf("mock upstream listening on %s (fail=%d stream=%v)", m.URL(), *fail, *stream)

	// Admin endpoint for the harness: request count and last body size.
	http.HandleFunc("/__mock/health", func(w http.ResponseWriter, _ *http.Request) {
		body := m.Body()
		size := 0
		if body != nil {
			size = len(body)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok requests=" + strconv.FormatInt(m.Requests(), 10) + " last_body_bytes=" + strconv.Itoa(size)))
	})
	// Blocks; keeps the harness alive. Any error here is fatal (port busy).
	if err := http.ListenAndServe("127.0.0.1:19998", nil); err != nil {
		log.Fatalf("admin listener: %v", err)
	}
}
