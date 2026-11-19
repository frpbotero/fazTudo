package health

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeGraph(t *testing.T, json string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(p, []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCyclesBetweenAreas(t *testing.T) {
	graph := writeGraph(t, `{"nodes": [
		{"id": "a1", "source_file": "internal/pay/a.go"},
		{"id": "b1", "source_file": "internal/cart/b.go"},
		{"id": "c1", "source_file": "internal/log/c.go"},
		{"id": "pkg", "label": "fmt"}
	], "links": [
		{"source": "a1", "target": "b1", "relation": "calls"},
		{"source": "b1", "target": "a1", "relation": "references"},
		{"source": "a1", "target": "c1", "relation": "calls"},
		{"source": "c1", "target": "a1", "relation": "contains"},
		{"source": "a1", "target": "pkg", "relation": "imports_from"}
	]}`)
	cycles, err := readCycles(graph, areaFunc(""))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cycles["internal/pay"], []string{"internal/cart"}) || !slices.Equal(cycles["internal/cart"], []string{"internal/pay"}) {
		t.Errorf("cycles = %v", cycles)
	}
	if _, ok := cycles["internal/log"]; ok {
		t.Error("contains is not a dependency: internal/log is in no cycle")
	}
}

func TestCyclesWithoutGraph(t *testing.T) {
	if _, err := readCycles(filepath.Join(t.TempDir(), "missing.json"), areaFunc("")); err == nil {
		t.Error("a missing graph must be reported")
	}
}
