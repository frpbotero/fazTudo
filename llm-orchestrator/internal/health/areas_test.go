package health

import (
	"context"
	"strings"
	"testing"
)

func TestSuggestRanksAreasWithReasons(t *testing.T) {
	r := newTestRepo(t)
	r.write("internal/pay/retry.go", "package pay\nfunc f() { time.Sleep(1) }\n")
	r.write("node_modules/lib/x.js", "setTimeout(f, 1)\n")
	r.write("main.go", "package main\n")
	for range 25 {
		r.commit("internal/pay/a.go", "internal/cart/b.go") // pair across areas
	}
	r.commit("internal/log/l.go", "docs/readme.go")
	graph := writeGraph(t, `{"nodes": [{"id": "a", "source_file": "internal/pay/a.go"}, {"id": "b", "source_file": "internal/cart/b.go"}],
		"links": [{"source": "a", "target": "b", "relation": "calls"}, {"source": "b", "target": "a", "relation": "calls"}]}`)

	s, err := Suggest(context.Background(), r.root, graph)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Graph || s.Commits != 26 || len(s.Warnings) != 0 || len(s.Areas) != topAreas {
		t.Fatalf("suggestion = %+v", s)
	}
	first := s.Areas[0]
	if first.Path != "internal/pay" || first.SpecialCases != 1 || first.Cycles == nil || *first.Cycles != 1 || first.CoChange != 2 {
		t.Errorf("first = %+v", first)
	}
	if s.Areas[1].Path != "internal/cart" {
		t.Errorf("second = %+v", s.Areas[1])
	}
	joined := strings.Join(first.Reasons, " ")
	for _, want := range []string{"Casos especiais: 1 (sleep 1)", "Ciclo de dependência com: internal/cart", "mudam juntos"} {
		if !strings.Contains(joined, want) {
			t.Errorf("reasons %v missing %q", first.Reasons, want)
		}
	}
	for _, a := range s.Areas {
		if strings.HasPrefix(a.Path, "node_modules") || a.Path == "" {
			t.Errorf("ignored or root area suggested: %+v", a)
		}
	}
}

func TestSuggestWarnsWithoutGraphOrHistory(t *testing.T) {
	r := newTestRepo(t)
	r.commit("src/pay/a.go", "src/cart/b.go")
	s, err := Suggest(context.Background(), r.root, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Graph || len(s.Warnings) != 2 || len(s.Areas) != 2 || s.Areas[0].Cycles != nil || s.Areas[0].Path != "src/cart" {
		t.Errorf("suggestion = %+v", s)
	}
}

func TestAreas(t *testing.T) {
	root := areaFunc("")
	nested := areaFunc("app")
	cases := []struct {
		of         func(string) string
		file, want string
	}{
		{root, "internal/pipeline/x.go", "internal/pipeline"},
		{root, "internal/pipeline/sub/y.go", "internal/pipeline"},
		{root, "cmd/main.go", "cmd"},
		{root, "main.go", ""},
		{nested, "app/internal/pay/a.go", "app/internal/pay"},
		{nested, "app/main.go", ""},
		{nested, "other/x.go", ""},
	}
	for _, c := range cases {
		if got := c.of(c.file); got != c.want {
			t.Errorf("area of %q = %q, want %q", c.file, got, c.want)
		}
	}
	files := productFiles([]string{".specify/x.sh", "app/a.go", "app/web/b.js", "app/.cache/c.js"})
	if base := commonDir(files); base != "app" || len(files) != 2 {
		t.Errorf("files = %v base = %q", files, base)
	}
	if base := commonDir([]string{"start.sh", "app/a.go", "app/b/c.go"}); base != "app" {
		t.Errorf("a loose root file does not count: base = %q", base)
	}
	if base := commonDir([]string{"app/a.go", "cmd/main.go"}); base != "" {
		t.Errorf("two top folders keep the root: base = %q", base)
	}
}
