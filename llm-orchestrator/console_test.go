//go:build !desktop

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConsoleRunSimulatesStoryAndReportsResult(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	file := filepath.Join(t.TempDir(), "stories.md")
	if err := os.WriteFile(file, []byte("# US-1: Teste\nComo usuário, quero testar.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errors bytes.Buffer
	code := runConsole([]string{"run", "--repo", repo, "--file", file, "--config", "", "--sim-delay", "1ms"}, strings.NewReader(""), &out, &errors)
	if code != 0 {
		t.Fatalf("código %d, saída %s, erro %s", code, out.String(), errors.String())
	}
	if !strings.Contains(out.String(), "US-1") || !strings.Contains(out.String(), "concluíd") {
		t.Fatalf("saída sem história ou resultado: %s", out.String())
	}
}

func TestConsoleRunRejectsMissingStoryFile(t *testing.T) {
	var out, errors bytes.Buffer
	code := runConsole([]string{"run", "--repo", t.TempDir(), "--file", "missing.md", "--config", ""}, strings.NewReader(""), &out, &errors)
	if code == 0 || !strings.Contains(errors.String(), "missing.md") {
		t.Fatalf("código %d, erro %s", code, errors.String())
	}
}
