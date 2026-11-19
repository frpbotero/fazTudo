package health

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSpecialCasesPerLanguage(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a.go":     "time.Sleep(time.Second)\nfor retry := 0; retry < 3; retry++ {}\nt.Skip(\"flaky\")\n_ = err\n",
		"b.ts":     "setTimeout(() => {}, 100)\nit.skip('x', f)\nxit('y', f)\ntry { f() } catch (e) {}\n",
		"c.py":     "import time\ntime.sleep(1)\n@pytest.mark.skip\ntry:\n    f()\nexcept Exception: pass\n",
		"D.java":   "Thread.sleep(10);\n@Disabled\nint maxRetries = 3;\n",
		"clean.go": "func sleepy() {}\n// sleeping well\n",
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]map[string]int{
		"a.go":     {"sleep": 1, "retry": 1, "teste pulado": 1, "exceção engolida": 1},
		"b.ts":     {"sleep": 1, "teste pulado": 2, "exceção engolida": 1},
		"c.py":     {"sleep": 1, "teste pulado": 1, "exceção engolida": 1},
		"D.java":   {"sleep": 1, "teste pulado": 1, "retry": 1},
		"clean.go": {},
	}
	for name, kinds := range want {
		got := countSpecial(root, name)
		for k, n := range kinds {
			if got[k] != n {
				t.Errorf("%s: %s = %d, want %d (all: %v)", name, k, got[k], n, got)
			}
		}
		total := 0
		for _, n := range got {
			total += n
		}
		wantTotal := 0
		for _, n := range kinds {
			wantTotal += n
		}
		if total != wantTotal {
			t.Errorf("%s: %d special cases, want %d (%v)", name, total, wantTotal, got)
		}
	}
}
