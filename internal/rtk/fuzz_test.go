package rtk

import "testing"

func FuzzRTKApply(f *testing.F) {
	f.Add([]byte(`{"messages":[{"role":"user","content":"<tool_result>x\n</tool_result>"}]}`))
	f.Add([]byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_result","content":"y"}]}]}`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte(`{"a":`))
	f.Add([]byte(``))
	r := New(Config{Enabled: true, MinBytes: 0, MaxBytes: 10485760})
	f.Fuzz(func(t *testing.T, data []byte) {
		out, _ := r.Apply(data)
		// Invariant: RTK never grows the payload beyond a tiny slack for
		// envelope re-marshaling (json normalization), and never panics.
		if len(out) > len(data)+1024 {
			t.Fatalf("output grew beyond slack: %d -> %d", len(data), len(out))
		}
	})
}
