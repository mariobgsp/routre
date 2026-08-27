package proxy

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// TestFirstByteBodySignalOnRead: first successful Read must close the
// firstByte channel exactly once. Subsequent Reads do not re-close.
func TestFirstByteBodySignalOnRead(t *testing.T) {
	signal := make(chan struct{})
	r := &firstByteBody{ReadCloser: io.NopCloser(strings.NewReader("hello")), firstByte: signal}
	buf := make([]byte, 5)
	n, err := r.Read(buf)
	if n != 5 || err != nil {
		t.Fatalf("first Read: n=%d err=%v want 5/nil", n, err)
	}
	select {
	case <-signal:
		// ok
	default:
		t.Fatal("first Read did not close firstByte channel")
	}
	// Drain remaining byte + EOF.
	buf2 := make([]byte, 5)
	_, _ = r.Read(buf2)
	// Read should be safe to call again.
	_, err = r.Read(buf2)
	if !errors.Is(err, io.EOF) {
		t.Errorf("final Read err: want EOF, got %v", err)
	}
}

// TestFirstByteBodyNoSignalOnEmptyRead: a Read that returns (0, nil)
// must not signal firstByte (we got zero bytes, not a "first byte").
func TestFirstByteBodyNoSignalOnEmptyRead(t *testing.T) {
	empty := &emptyReader{closed: false}
	signal := make(chan struct{})
	r := &firstByteBody{ReadCloser: io.NopCloser(empty), firstByte: signal}
	buf := make([]byte, 4)
	_, _ = r.Read(buf) // (0, nil)
	select {
	case <-signal:
		t.Fatal("empty Read should not signal firstByte")
	default:
	}
}

// emptyReader returns (0, nil) until closed, then EOF.
type emptyReader struct{ closed bool }

func (e *emptyReader) Read(p []byte) (int, error) {
	if e.closed {
		return 0, io.EOF
	}
	return 0, nil
}

// TestFirstByteBodyClosePropagates: the wrapping preserves Close, so
// relayStream's timer-driven body.Close() reaches the underlying
// connection. We can't model the network-level interruption in a
// pure unit test (that needs a real http server), but we verify the
// wrapper forwards Close.
func TestFirstByteBodyClosePropagates(t *testing.T) {
	rc := &countingCloser{ReadCloser: io.NopCloser(strings.NewReader("x"))}
	ch := make(chan struct{})
	r := &firstByteBody{ReadCloser: rc, firstByte: ch}
	// Trigger the first-byte path (so the channel is used) before
	// closing. This makes pi-lens's "unused write" check happy and
	// also exercises the integration of Read + Close.
	buf := make([]byte, 1)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	select {
	case <-ch:
		// ok, firstByte signaled
	default:
		t.Fatal("firstByte not signaled after Read")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if rc.closed != 1 {
		t.Errorf("Close not forwarded: closed=%d", rc.closed)
	}
}

type countingCloser struct {
	io.ReadCloser
	closed int
}

func (c *countingCloser) Close() error {
	c.closed++
	return c.ReadCloser.Close()
}
