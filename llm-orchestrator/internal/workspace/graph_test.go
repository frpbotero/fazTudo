package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExcludeFromGitAddsPatternOnce(t *testing.T) {
	repo := t.TempDir()
	info := filepath.Join(repo, ".git", "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "exclude"), []byte("# local"), 0o644); err != nil {
		t.Fatal(err)
	}
	ExcludeFromGit(repo, "graphify-out/")
	ExcludeFromGit(repo, "graphify-out/")
	data, _ := os.ReadFile(filepath.Join(info, "exclude"))
	if got := string(data); got != "# local\ngraphify-out/\n" {
		t.Errorf("exclude = %q", got)
	}
}

func TestExcludeFromGitWithoutGitIsNoop(t *testing.T) {
	repo := t.TempDir()
	ExcludeFromGit(repo, "graphify-out/")
	if _, err := os.Stat(filepath.Join(repo, ".git")); !os.IsNotExist(err) {
		t.Error("must not create .git")
	}
}

func TestGraphReportWithoutGraph(t *testing.T) {
	if GraphReport("") != "" {
		t.Error("no graph, no report")
	}
	dir := filepath.Join(t.TempDir(), GraphDir)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "GRAPH_REPORT.md"), []byte("# Graph Report"), 0o644)
	if r := GraphReport(filepath.Join(dir, "graph.json")); !strings.HasPrefix(r, "# Graph Report") {
		t.Errorf("report = %q", r)
	}
}
