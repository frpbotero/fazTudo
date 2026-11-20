package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GraphDir is where graphify writes a repository's code graph.
const GraphDir = "graphify-out"

// graphTimeout bounds one graph update. A code-only update parses the files
// locally (tree-sitter) and takes seconds; a hung one must not block the run.
const graphTimeout = 10 * time.Minute

// ErrGraphUnavailable means the graphify CLI is not installed.
var ErrGraphUnavailable = errors.New("graphify não instalado (uv tool install graphifyy)")

// UpdateGraph builds or refreshes the code graph of repo with
// `graphify update`, which only re-extracts changed files and calls no LLM.
// The output stays out of git through .git/info/exclude. It returns the
// absolute path of graph.json.
func UpdateGraph(ctx context.Context, repo string) (string, error) {
	bin, err := exec.LookPath("graphify")
	if err != nil {
		return "", ErrGraphUnavailable
	}
	ExcludeFromGit(repo, GraphDir+"/")
	ctx, cancel := context.WithTimeout(ctx, graphTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "update", ".")
	cmd.Dir = repo
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("graphify update: %w: %s", err, lastLine(out.String()))
	}
	graph := filepath.Join(repo, GraphDir, "graph.json")
	if _, err := os.Stat(graph); err != nil {
		return "", fmt.Errorf("graphify update não gerou %s", graph)
	}
	return graph, nil
}

// GraphReport reads the report graphify writes next to graph.json.
func GraphReport(graph string) string {
	if graph == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(graph), "GRAPH_REPORT.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

// ExcludeFromGit adds pattern to the repository's local exclude file, so
// generated files never show up as changes nor end up in a commit. Best
// effort: without git there is nothing to exclude from.
func ExcludeFromGit(repo, pattern string) {
	info := filepath.Join(repo, ".git", "info")
	if st, err := os.Stat(info); err != nil || !st.IsDir() {
		return
	}
	file := filepath.Join(info, "exclude")
	data, _ := os.ReadFile(file)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == pattern {
			return
		}
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		pattern = "\n" + pattern
	}
	fmt.Fprintln(f, pattern)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
