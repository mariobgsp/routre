package proxy

import (
	"bufio"
	"bytes"
	"testing"
)

func FuzzSSEFrame(f *testing.F) {
	f.Add([]byte("data: {\"a\":1}\n\n"))
	f.Add([]byte("data: [DONE]\n\n"))
	f.Add([]byte("event: error\ndata: x\n\n"))
	f.Add([]byte("data: partial"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, data []byte) {
		br := bufio.NewReader(bytes.NewReader(data))
		// readRawFrame must never panic and must terminate (EOF or error).
		for i := 0; i < 10000; i++ {
			_, ok, err := readRawFrame(br)
			if err != nil || !ok {
				return
			}
		}
	})
}
