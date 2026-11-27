package provider

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParseClaudeResult(t *testing.T) {
	out := "aviso no stderr\n" + `{"type":"result","subtype":"success","is_error":false,"num_turns":3,"result":"feito",` +
		`"total_cost_usd":0.25,"usage":{"input_tokens":10,"cache_creation_input_tokens":200,"cache_read_input_tokens":1000,"output_tokens":50}}`
	p, ok := parseClaude(out)
	if !ok || p.Text != "feito" || p.TurnLimit {
		t.Fatalf("parsed = %+v, %v", p, ok)
	}
	want := Usage{InputTokens: 1210, CachedTokens: 1000, OutputTokens: 50, CostUSD: 0.25, Turns: 3}
	if *p.Usage != want {
		t.Errorf("usage = %+v, want %+v", *p.Usage, want)
	}

	limited, ok := parseClaude(`{"type":"result","subtype":"error_max_turns","is_error":true,"result":null,"usage":{}}`)
	if !ok || !limited.TurnLimit || limited.Text != "" {
		t.Errorf("max turns = %+v, %v", limited, ok)
	}
	if _, ok := parseClaude("texto livre"); ok {
		t.Error("plain text is not a result")
	}
}

func TestParseCodexEvents(t *testing.T) {
	out := `{"type":"thread.started","thread_id":"x"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"parcial"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","text":""}}
{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"final"}}
{"type":"turn.completed","usage":{"input_tokens":18000,"cached_input_tokens":11000,"output_tokens":50,"reasoning_output_tokens":7}}`
	p, ok := parseCodex(out)
	if !ok || p.Text != "final" {
		t.Fatalf("parsed = %+v, %v", p, ok)
	}
	want := Usage{InputTokens: 18000, CachedTokens: 11000, OutputTokens: 50, ReasoningTokens: 7, Turns: 1}
	if *p.Usage != want {
		t.Errorf("usage = %+v, want %+v", *p.Usage, want)
	}
}

func TestParseGeminiOutput(t *testing.T) {
	out := "Loaded cached credentials.\n{\n  \"session_id\": \"s\",\n  \"response\": \"pronto\",\n  \"stats\": {\"models\": {" +
		"\"gemini-3-flash-preview\": {\"tokens\": {\"prompt\": 900, \"candidates\": 40, \"cached\": 300, \"thoughts\": 12}}}}\n}"
	p, ok := parseGemini(out)
	if !ok || p.Text != "pronto" {
		t.Fatalf("parsed = %+v, %v", p, ok)
	}
	// Gemini reports thoughts apart from candidates; both are generated tokens.
	want := Usage{InputTokens: 900, CachedTokens: 300, OutputTokens: 52, ReasoningTokens: 12}
	if *p.Usage != want {
		t.Errorf("usage = %+v, want %+v", *p.Usage, want)
	}
}

func TestWriteArgsAllowOnlyListedCommands(t *testing.T) {
	inv := Invocation{Model: "m", Prompt: "p", Commands: []string{"npm test"}, MaxTurns: 40}
	claude := ClaudeCLI.Args(inv)
	if !containsSeq(claude, "--max-turns", "40") || !containsSeq(claude, "--tools", claudeWriteTools) {
		t.Errorf("claude write args: %v", claude)
	}
	if !slices.Contains(claude, "Bash(npm test)") || !slices.Contains(claude, "Bash(npm test *)") || slices.Contains(claude, "Bash") {
		t.Errorf("Bash must be allowed only for listed commands: %v", claude)
	}
	gemini := GeminiCLI.Args(inv)
	if !containsSeq(gemini, "--approval-mode", "auto_edit") || !containsSeq(gemini, "--allowed-tools", "run_shell_command(npm test)") ||
		slices.Contains(gemini, "--yolo") {
		t.Errorf("gemini write args: %v", gemini)
	}
	if ro := GeminiCLI.Args(Invocation{ReadOnly: true}); !containsSeq(ro, "--approval-mode", "plan") ||
		!slices.Contains(ro, "--skip-trust") || !containsSeq(ro, "-e", "none") {
		t.Errorf("gemini read-only args: %v", ro)
	}
	codex := CodexCLI.Args(inv)
	if !slices.Contains(codex, "--json") || !containsSeq(codex, "-c", "project_doc_max_bytes=0") {
		t.Errorf("codex args: %v", codex)
	}
	for _, args := range [][]string{claude, gemini, codex} {
		if slices.Contains(args, "p") {
			t.Errorf("prompt must not be in argv: %v", args)
		}
	}
}

func TestExecuteReadsUsageAndStopsAtTurnLimit(t *testing.T) {
	real := &fakeRunner{results: map[string][]result{"claude": {
		{out: `{"type":"result","subtype":"success","result":"ok","usage":{"input_tokens":5,"output_tokens":2}}`},
		{out: `{"type":"result","subtype":"error_max_turns","is_error":true,"result":null,"usage":{"output_tokens":9}}`, err: errors.New("exit status 1")},
	}}}
	r := newTestRouter(false, real, allInstalled)

	resp, err := r.Execute(context.Background(), Request{Tier: TierStandard, MaxTurns: 7, Commands: []string{"go test ./..."}}, discard)
	if err != nil || resp.Output != "ok" || resp.Usage == nil || resp.Usage.InputTokens != 5 {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
	if inv := real.calls[0]; inv.MaxTurns != 7 || !slices.Equal(inv.Commands, []string{"go test ./..."}) {
		t.Errorf("limits not forwarded: %+v", inv)
	}

	resp, err = r.Execute(context.Background(), Request{Tier: TierStandard}, discard)
	if !errors.Is(err, ErrTurnLimit) || resp == nil || !resp.TurnLimited || resp.Usage.OutputTokens != 9 {
		t.Fatalf("turn limit: resp = %+v, err = %v", resp, err)
	}
	if len(real.calls) != 2 || strings.Contains(real.calls[1].CLI.Binary, "codex") {
		t.Errorf("a turn limit is neither retried nor failed over: %d calls", len(real.calls))
	}
}

func TestReadDirsAreReadableNotWritable(t *testing.T) {
	inv := Invocation{Prompt: "p", ReadDirs: []string{"/cache/skills"}}
	claude := ClaudeCLI.Args(inv)
	if !slices.Contains(claude, "--safe-mode") || !containsSeq(claude, "--add-dir", "/cache/skills") {
		t.Errorf("claude must keep --safe-mode and read the skills dir: %v", claude)
	}
	i := slices.Index(claude, "--disallowedTools")
	if i < 0 || !slices.Contains(claude[i:], "Edit(//cache/skills/**)") || !slices.Contains(claude[i:], "Write(//cache/skills/**)") {
		t.Errorf("claude must deny edits in the skills dir: %v", claude)
	}
	if ro := ClaudeCLI.Args(Invocation{ReadOnly: true, ReadDirs: inv.ReadDirs}); !containsSeq(ro, "--add-dir", "/cache/skills") {
		t.Errorf("read-only claude must read the skills dir: %v", ro)
	}
	if plain := ClaudeCLI.Args(Invocation{Prompt: "p"}); slices.Contains(plain, "--add-dir") || slices.Contains(plain, "--disallowedTools") {
		t.Errorf("no read dirs, no extra flags: %v", plain)
	}
	if gemini := GeminiCLI.Args(inv); !containsSeq(gemini, "--include-directories", "/cache/skills") {
		t.Errorf("gemini must read the skills dir: %v", gemini)
	}
}

func TestTotalCountsCachedAndReasoningTokensOnce(t *testing.T) {
	// Cached tokens are part of the input and reasoning tokens part of the output.
	u := Usage{InputTokens: 1000, CachedTokens: 800, OutputTokens: 100, ReasoningTokens: 30}
	if got := u.Total(); got != 1100 {
		t.Errorf("total = %d, want 1100", got)
	}
}
