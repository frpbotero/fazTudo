package quality

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunReportsExitCodeAndOutputTail(t *testing.T) {
	dir := t.TempDir()
	ok := Run(context.Background(), dir, Command{Name: "ok", Run: "echo $CI"}, 10*time.Second)
	if !ok.Passed || ok.Output != "1" {
		t.Fatalf("ok = %+v (CI must be set)", ok)
	}

	fail := Run(context.Background(), dir, Command{Name: "fail", Run: "echo boom >&2; exit 3"}, 10*time.Second)
	if fail.Passed || fail.ExitCode != 3 || fail.Output != "boom" {
		t.Fatalf("fail = %+v", fail)
	}

	long := Run(context.Background(), dir, Command{Run: "yes x | head -c 20000; echo; echo FIM"}, 10*time.Second)
	if len(long.Output) > maxOutputBytes+64 || !strings.HasSuffix(long.Output, "FIM") || !strings.HasPrefix(long.Output, "[…") {
		t.Fatalf("output must keep only the tail, got %d bytes", len(long.Output))
	}
}

func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	start := time.Now()
	// A background job ignores SIGINT, so only the group's kill ends it.
	r := run(context.Background(), t.TempDir(), Command{Run: "sleep 30 & sleep 30"}, 300*time.Millisecond, 200*time.Millisecond)
	if r.Passed || !r.TimedOut {
		t.Fatalf("expected timeout, got %+v", r)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("timeout did not stop the command promptly")
	}
}

func TestSuggestFromManifests(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{"scripts":{"test:unit":"node --test","test:e2e":"playwright test","test":"npm run test:unit","dev":"vite"}}`)
	write(t, root, "api/go.mod", "module api\n")
	write(t, root, "web/package.json", `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`)
	write(t, root, "web/playwright.config.ts", "")
	write(t, root, "node_modules/x/package.json", `{"scripts":{"test":"x"}}`)
	write(t, root, "front/package.json", `{"scripts":{"dev":"ng serve","build":"ng build"}}`)
	write(t, root, "lib/package.json", `{"scripts":{"build":"tsc","lint":"eslint ."}}`)

	var got []string
	for _, c := range Suggest(root) {
		got = append(got, c.Name+"="+c.Run)
	}
	want := []string{
		"test:unit=npm run test:unit",
		"test:e2e=npm run test:e2e",
		"api: go vet=cd api && go vet ./...",
		"api: go test=cd api && go test ./...",
		"front: build=cd front && npm run build",
		"lib: lint=cd lib && npm run lint",
		"web: e2e=cd web && npx playwright test",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Suggest =\n%v\nwant\n%v", got, want)
	}
}

func TestReadabilityFlagsLongAndMinifiedLines(t *testing.T) {
	root := t.TempDir()
	write(t, root, "ok.js", "const a = 1;\n")
	write(t, root, "packed.js", "const a = 1;\n"+strings.Repeat("x", 900)+"\n")
	write(t, root, "lib.min.js", strings.Repeat("y", 900))
	write(t, root, "README.md", strings.Repeat("z", 900))

	f := Readability(root, []string{"ok.js", "packed.js", "lib.min.js", "README.md"}, 160)
	if len(f) != 1 || f[0].File != "packed.js" || f[0].Line != 2 || !strings.Contains(f[0].Message, "minificado") {
		t.Fatalf("findings = %+v", f)
	}
}

func TestSyntaxFindsBrokenInlineScript(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	root := t.TempDir()
	write(t, root, "app.html", "<!doctype html>\n<script type=\"application/json\">{\"a\":1}</script>\n"+
		"<script src=\"x.js\"></script>\n<script>\nfunction draw(plan) {\n  if (plan) {\n    go();\n  else {\n    stop();\n  }\n}\n</script>\n")
	write(t, root, "ok.mjs", "export const a = 1;\n")
	write(t, root, "bad.js", "function (\n")

	f := Syntax(context.Background(), root, []string{"app.html", "ok.mjs", "bad.js"})
	if len(f) != 2 {
		t.Fatalf("findings = %+v", f)
	}
	slices.SortFunc(f, func(a, b Finding) int { return strings.Compare(a.File, b.File) })
	if f[0].File != "app.html" || f[0].Line != 8 || !strings.Contains(f[0].Message, "SyntaxError") {
		t.Errorf("inline script finding = %+v", f[0])
	}
	if f[1].File != "bad.js" {
		t.Errorf("js finding = %+v", f[1])
	}
}

const edd = `# EDD

## 4. Layout
| ID | Item |
|----|------|
| G-01 | gôndola (não é teste) |

## 10. Plano de testes (TDD)

### 10.4 Testes unitários
| ID | Caso | Automatizado |
|----|------|------|
| UT-GRID-04 | ` + "`accessTiles(G-01)`" + ` inclui (2,1) | sim |
| UT-CART-01 | adiciona item | sim |

### 10.5 Testes de integração e E2E (Playwright)
| ID | Fluxo |
|---|---|
| IT-01 | abre o mapa |
| IT-1 | duplicado curto |

## 11. Definição de pronto
| DOD-01 | não é teste |
`

func TestExtractRequirementsReadsTestTablesOnly(t *testing.T) {
	reqs := ExtractRequirements(edd)
	var ids []string
	for _, r := range reqs {
		ids = append(ids, r.ID)
	}
	if want := []string{"UT-GRID-04", "UT-CART-01", "IT-01", "IT-1"}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if reqs[0].Kind != "unit" || reqs[2].Kind != "integration" || !strings.HasPrefix(reqs[0].Description, "accessTiles(G-01)") {
		t.Errorf("unexpected requirement: %+v / %+v", reqs[0], reqs[2])
	}
}

func TestTraceTestsMatchesIDsAndShortForms(t *testing.T) {
	root := t.TempDir()
	write(t, root, "tests/unit/grid.test.mjs", "test('GRID-04 accessTiles', () => {})")
	write(t, root, "tests/e2e/flow.spec.mjs", "test('IT-10 checkout', () => {})")
	write(t, root, "src/app.js", "// UT-CART-01 mentioned outside tests")

	traces := TraceTests(root, []Requirement{{ID: "UT-GRID-04"}, {ID: "UT-CART-01"}, {ID: "IT-1"}, {ID: "IT-10"}})
	found := map[string]bool{}
	for _, tr := range traces {
		found[tr.ID] = tr.Found()
	}
	want := map[string]bool{"UT-GRID-04": true, "UT-CART-01": false, "IT-1": false, "IT-10": true}
	for id, w := range want {
		if found[id] != w {
			t.Errorf("%s found = %v, want %v", id, found[id], w)
		}
	}
}

func TestMergeRequirementsKeepsFirstAndFillsDescription(t *testing.T) {
	got := MergeRequirements([]Requirement{{ID: "A-1"}}, []Requirement{{ID: "A-1", Description: "d"}, {ID: "B-1"}, {ID: " "}})
	if len(got) != 2 || got[0].Description != "d" || got[1].ID != "B-1" {
		t.Fatalf("merge = %+v", got)
	}
}

func TestGuardDetectsAndRestoresInputs(t *testing.T) {
	root := t.TempDir()
	write(t, root, "US_loja.md", "# US original")
	write(t, root, "docs/EDD_loja.md", "# EDD")
	write(t, root, "notes.md", "livre")
	write(t, root, "story-from-ui.md", "carregado")

	g := NewGuard(root, append(DefaultProtected, "story-from-ui.md"))
	if want := []string{"US_loja.md", "docs/EDD_loja.md", "story-from-ui.md"}; !slices.Equal(g.Files(), want) {
		t.Fatalf("protected = %v, want %v", g.Files(), want)
	}
	if c := g.Check(); len(c) != 0 {
		t.Fatalf("unexpected changes: %v", c)
	}

	write(t, root, "US_loja.md", "O protótipo está em …")
	os.Remove(filepath.Join(root, "docs/EDD_loja.md"))
	write(t, root, "notes.md", "editado")
	changed := g.Check()
	if want := []string{"US_loja.md", "docs/EDD_loja.md"}; !slices.Equal(changed, want) {
		t.Fatalf("changed = %v, want %v", changed, want)
	}
	if err := g.Restore(changed); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "US_loja.md")); string(data) != "# US original" {
		t.Errorf("restored content = %q", data)
	}
	if len(g.Check()) != 0 {
		t.Error("files must match after restore")
	}
}

func TestChangedSinceSkipsGeneratedDirs(t *testing.T) {
	root := t.TempDir()
	write(t, root, "old.js", "")
	past := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(root, "old.js"), past, past)
	since := time.Now().Add(-time.Minute)
	write(t, root, "src/new.js", "")
	write(t, root, "node_modules/x/index.js", "")
	write(t, root, "test-results/r.json", "")

	if got := ChangedSince(root, since); !slices.Equal(got, []string{"src/new.js"}) {
		t.Fatalf("changed = %v", got)
	}
}

func TestIsTestFile(t *testing.T) {
	for path, want := range map[string]bool{
		"tests/unit/cart.test.mjs": true, "src/cart_test.go": true, "e2e/flow.ts": true,
		"test_api.py": true, "src/app.js": false, "tests/fixtures/data.json": false,
	} {
		if IsTestFile(path) != want {
			t.Errorf("IsTestFile(%q) = %v", path, !want)
		}
	}
}

func TestTraceFindsTestCodeOutsideTestNames(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("scripts/verify-mfa.mjs", "import test from 'node:test';\ntest('E2E-AUTH-01: login', () => {});\n")
	write("src/app.js", "// E2E-AUTH-02 é citado, mas aqui não há teste\n")

	traces := TraceTests(root, []Requirement{{ID: "E2E-AUTH-01"}, {ID: "E2E-AUTH-02"}})
	if !traces[0].Found() || traces[0].Files[0] != "scripts/verify-mfa.mjs" {
		t.Errorf("test code in a script must count: %+v", traces[0])
	}
	if traces[1].Found() {
		t.Errorf("a plain source comment is not a test: %+v", traces[1])
	}
}
