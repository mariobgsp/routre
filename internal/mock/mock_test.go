package mock

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestMockNonStreaming(t *testing.T) {
	s, err := New("a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, err := http.Post(s.URL()+"/v1/chat/completions", "application/json",
		bytes.NewBufferString(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.Requests() != 1 {
		t.Fatalf("requests = %d, want 1", s.Requests())
	}
	if s.Body() == nil || !bytes.Contains(s.Body(), []byte(`"model":"m"`)) {
		t.Fatalf("LastBody not captured: %s", s.Body())
	}
}

func TestMockFailureInjection(t *testing.T) {
	s, err := New("a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetFail(503)
	resp, err := http.Post(s.URL()+"/v1/chat/completions", "application/json",
		bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestMockModels(t *testing.T) {
	s, err := New("a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetModels([]string{"m1", "m2"})
	resp, err := http.Get(s.URL() + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	data, _ := out["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("models = %d, want 2", len(data))
	}
}
