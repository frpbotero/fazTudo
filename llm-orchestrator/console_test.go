//go:build !desktop

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestConsoleRejectsUnknownCommand(t *testing.T) {
	var out, errors bytes.Buffer
	if code := runConsole([]string{"desconhecido"}, strings.NewReader(""), &out, &errors); code != 2 || !strings.Contains(errors.String(), "desconhecido") {
		t.Fatalf("comando desconhecido: %d %s", code, errors.String())
	}
}

func TestConsoleRepositoryAndPolicyUseSavedConfiguration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	config := filepath.Join(t.TempDir(), "config.json")
	call := func(args ...string) (string, string, int) {
		t.Helper()
		var out, errors bytes.Buffer
		code := runConsole(args, strings.NewReader(""), &out, &errors)
		return out.String(), errors.String(), code
	}
	if _, msg, code := call("repo", "set", "--path", repo, "--config", config); code != 0 {
		t.Fatalf("repo set: %d %s", code, msg)
	}
	if out, msg, code := call("repo", "show", "--config", config); code != 0 || !strings.Contains(out, repo) {
		t.Fatalf("repo show: %d %s %s", code, out, msg)
	}
	if out, msg, code := call("policy", "show", "--config", config); code != 0 || !strings.Contains(out, "auto_checks") {
		t.Fatalf("policy show: %d %s %s", code, out, msg)
	}
}

func TestConsoleParsesStoriesWithoutServer(t *testing.T) {
	var out, errors bytes.Buffer
	code := runConsole([]string{"stories", "parse", "--file", "-"}, strings.NewReader("# US-9: Teste\nCorpo"), &out, &errors)
	if code != 0 || !strings.Contains(out.String(), "US-9") {
		t.Fatalf("stories parse: %d %s %s", code, out.String(), errors.String())
	}
}

func TestConsoleRejectsOversizedStoryInput(t *testing.T) {
	var out, errors bytes.Buffer
	code := runConsole([]string{"stories", "parse", "--file", "-", "--config", ""}, strings.NewReader(strings.Repeat("x", 1<<20+1)), &out, &errors)
	if code == 0 || !strings.Contains(errors.String(), "1 MiB") {
		t.Fatalf("entrada longa: %d %s", code, errors.String())
	}
}

func TestConsoleParsingContinuesWithInvalidSavedConfig(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errors bytes.Buffer
	code := runConsole([]string{"stories", "parse", "--file", "-", "--config", config}, strings.NewReader("# US-1: Teste\nCorpo"), &out, &errors)
	if code != 0 || !strings.Contains(out.String(), "US-1") || !strings.Contains(errors.String(), "ignorada") {
		t.Fatalf("parsing: %d %s %s", code, out.String(), errors.String())
	}
}

func TestConsoleStopReachesRunInAnotherCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	config := filepath.Join(t.TempDir(), "config.json")
	file := filepath.Join(t.TempDir(), "stories.md")
	if err := os.WriteFile(file, []byte("# US-1: Teste\nCorpo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var runOut, runErrors bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runConsole([]string{"run", "--repo", repo, "--file", file, "--config", config, "--sim-delay", "300ms"}, strings.NewReader(""), &runOut, &runErrors)
	}()
	var stopOut, stopErrors bytes.Buffer
	stopped := false
	for i := 0; i < 30; i++ {
		var statusOut, statusErrors bytes.Buffer
		if runConsole([]string{"status", "--repo", repo, "--config", config, "--json"}, strings.NewReader(""), &statusOut, &statusErrors) == 0 {
			var snapshot struct {
				Run struct {
					Running bool `json:"running"`
				} `json:"run"`
			}
			if json.Unmarshal(statusOut.Bytes(), &snapshot) == nil && snapshot.Run.Running {
				var quotaOut, quotaErrors bytes.Buffer
				if code := runConsole([]string{"providers", "exhaust", "--id", "codex", "--repo", repo, "--config", config}, strings.NewReader(""), &quotaOut, &quotaErrors); code != 0 {
					t.Fatalf("cota ativa: %d %s", code, quotaErrors.String())
				}
			}
		}
		stopOut.Reset()
		stopErrors.Reset()
		if runConsole([]string{"stop", "--repo", repo, "--config", config}, strings.NewReader(""), &stopOut, &stopErrors) == 0 {
			stopped = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !stopped {
		t.Fatalf("parada não aceita: %s", stopErrors.String())
	}
	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("execução terminou com %d: %s %s", code, runOut.String(), runErrors.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("execução não parou")
	}
	var resumeOut, resumeErrors bytes.Buffer
	code := runConsole([]string{"resume", "--repo", repo, "--config", config, "--sim-delay", "1ms"}, strings.NewReader(""), &resumeOut, &resumeErrors)
	if code != 0 || !strings.Contains(resumeOut.String(), "Execução concluída") {
		t.Fatalf("retomada: %d %s %s", code, resumeOut.String(), resumeErrors.String())
	}
}

func TestConsoleSelectsSeveralStories(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	file := filepath.Join(t.TempDir(), "stories.md")
	content := "# US-1: Primeiro\nUm\n# US-2: Segundo\nDois\n# US-3: Terceiro\nTrês\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errors bytes.Buffer
	code := runConsole([]string{"run", "--repo", repo, "--file", file, "--stories", "US-1,US-3", "--config", "", "--sim-delay", "1ms"}, strings.NewReader(""), &out, &errors)
	if code != 0 || !strings.Contains(out.String(), "US-3") || strings.Contains(out.String(), "US-2:") {
		t.Fatalf("seleção: %d %s %s", code, out.String(), errors.String())
	}
}

func TestConsolePersistsProviderPolicyAndPromptChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	config := filepath.Join(t.TempDir(), "config.json")
	repo := t.TempDir()
	call := func(stdin string, args ...string) string {
		t.Helper()
		var out, errors bytes.Buffer
		if code := runConsole(args, strings.NewReader(stdin), &out, &errors); code != 0 {
			t.Fatalf("%v: %d %s", args, code, errors.String())
		}
		return out.String()
	}
	call("", "repo", "set", "--path", repo, "--config", config)
	call(`{"default_provider":"codex"}`, "config", "apply", "--file", "-", "--config", config)
	if got := call("", "config", "show", "--config", config); !strings.Contains(got, `"default_provider": "codex"`) {
		t.Fatalf("configuração não salva: %s", got)
	}
	var policy map[string]any
	if err := json.Unmarshal([]byte(call("", "policy", "show", "--config", config)), &policy); err != nil {
		t.Fatal(err)
	}
	policy["flow"] = "balanced"
	data, _ := json.Marshal(policy)
	call(string(data), "policy", "apply", "--file", "-", "--config", config)
	if got := call("", "policy", "show", "--config", config); !strings.Contains(got, `"flow": "balanced"`) {
		t.Fatalf("política não salva: %s", got)
	}
	call("Prompt de teste", "prompts", "set", "--id", "fix.task", "--file", "-", "--config", config)
	if got := call("", "prompts", "show", "--id", "fix.task", "--config", config); !strings.Contains(got, "Prompt de teste") {
		t.Fatalf("prompt não salvo: %s", got)
	}
}

func TestConsoleFiltersSavedLogs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	config := filepath.Join(t.TempDir(), "config.json")
	file := filepath.Join(t.TempDir(), "story.md")
	if err := os.WriteFile(file, []byte("# US-1: Teste\nCorpo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errors bytes.Buffer
	if code := runConsole([]string{"run", "--repo", repo, "--file", file, "--config", config, "--sim-delay", "1ms"}, strings.NewReader(""), &out, &errors); code != 0 {
		t.Fatalf("run: %d %s", code, errors.String())
	}
	out.Reset()
	errors.Reset()
	if code := runConsole([]string{"logs", "--repo", repo, "--config", config, "--level", "success"}, strings.NewReader(""), &out, &errors); code != 0 {
		t.Fatalf("logs: %d %s", code, errors.String())
	}
	if !strings.Contains(out.String(), "[success]") || strings.Contains(out.String(), "[info]") {
		t.Fatalf("filtro incorreto: %s", out.String())
	}
}

func TestConsoleHealthSuggestsAndRunsACleanup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	for rel, text := range map[string]string{"src/pay/a.go": "time.Sleep(1)\n", "src/cart/b.go": "package cart\n"} {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	call := func(args ...string) (string, string, int) {
		t.Helper()
		var out, errors bytes.Buffer
		code := runConsole(append(args, "--repo", repo, "--config", ""), strings.NewReader(""), &out, &errors)
		return out.String(), errors.String(), code
	}

	out, msg, code := call("health", "areas")
	if code != 0 || !strings.Contains(out, "1. src/pay") || !strings.Contains(out, "sleep 1") || !strings.Contains(msg, "aviso:") {
		t.Fatalf("health areas: %d %s %s", code, out, msg)
	}
	out, _, code = call("health", "areas", "--json")
	var s struct {
		Areas []struct{ Path string } `json:"areas"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &s) != nil || len(s.Areas) != 1 {
		t.Fatalf("health areas --json: %d %s", code, out)
	}

	out, msg, code = call("health", "run", "--area", "src/pay", "--comments", "--sim-delay", "1ms")
	if code != 0 || !strings.Contains(out, "Limpeza aprovada na área src/pay") {
		t.Fatalf("health run: %d %s %s", code, out, msg)
	}

	for _, args := range [][]string{{"health", "run"}, {"health", "run", "--area", "src", "--rounds", "6"}, {"health"}, {"health", "x"}} {
		if _, _, code := call(args...); code != 2 {
			t.Errorf("%v: code %d, want 2", args, code)
		}
	}
	if _, msg, code := call("health", "run", "--area", "nope", "--sim-delay", "1ms"); code != 1 || !strings.Contains(msg, "área inválida") {
		t.Errorf("missing area: %d %s", code, msg)
	}
}
