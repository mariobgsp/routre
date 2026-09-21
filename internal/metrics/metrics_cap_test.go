package metrics

import (
	"fmt"
	"strings"
	"testing"
)

// TestMetricsModelLabelsCapped: 600 client-supplied model names must not mint
// 600 labels, a configured model must never be folded into "_other", and the
// folded counts must still sum.
func TestMetricsModelLabelsCapped(t *testing.T) {
	m := New()
	m.SetReservedModels([]string{"real-model"})

	total := int64(0)
	for i := 0; i < 600; i++ {
		m.Request("c", "p", fmt.Sprintf("model-%d", i), "ok")
		total++
	}
	m.Request("c", "p", "real-model", "ok")
	total++

	m.mu.Lock()
	defer m.mu.Unlock()
	models := map[string]bool{}
	sum := int64(0)
	for k, v := range m.req {
		parts := strings.Split(k, "|")
		if len(parts) != 4 {
			t.Fatalf("malformed request label %q", k)
		}
		models[parts[2]] = true
		sum += v
	}
	if !models["_other"] {
		t.Error("expected an _other bucket once the label cap is hit")
	}
	if !models["real-model"] {
		t.Error("a configured model must keep its own label")
	}
	// The minted set is capped; "_other" and the reserved model are the only
	// extras allowed beyond it.
	if len(models) > maxModelLabels+2 {
		t.Fatalf("distinct model labels = %d, want <= %d", len(models), maxModelLabels+2)
	}
	if sum != total {
		t.Fatalf("request counts do not sum: got %d, want %d", sum, total)
	}
}
