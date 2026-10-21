package provider

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner returns scripted results per binary and records calls.
type fakeRunner struct {
	mu      sync.Mutex
	results map[string][]result
	calls   []Invocation
}

type result struct {
	out string
	err error
}

func (f *fakeRunner) Run(_ context.Context, inv Invocation) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, inv)
	queue := f.results[inv.CLI.Binary]
	if len(queue) == 0 {
		return "ok from " + inv.CLI.Binary, nil
	}
	f.results[inv.CLI.Binary] = queue[1:]
	return queue[0].out, queue[0].err
}

func allInstalled(string) (string, error)  { return "/usr/bin/x", nil }
func noneInstalled(string) (string, error) { return "", errors.New("not found") }
func discard(slog.Level, string, ...any)   {}

func newTestRouter(dry bool, real *fakeRunner, look func(string) (string, error)) *Router {
	return NewRouter(dry, []CLI{ClaudeCLI, CodexCLI},
		WithRunners(real, &fakeRunner{}),
		WithLookPath(look),
		WithPollInterval(10*time.Millisecond),
	)
}

func TestExecuteUsesTierModel(t *testing.T) {
	real := &fakeRunner{}
	r := newTestRouter(false, real, allInstalled)

	resp, err := r.Execute(context.Background(), Request{Role: "arch", Tier: TierHeavy}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != Claude || resp.Model != ClaudeCLI.Defaults.HeavyModel {
		t.Errorf("got %s/%s, want claude heavy model", resp.Provider, resp.Model)
	}
}

func TestExecuteFailsOverOnQuota(t *testing.T) {
	real := &fakeRunner{results: map[string][]result{
		"claude": {{out: "Error: rate limit exceeded, try again in 10m", err: errors.New("exit status 1")}},
	}}
	r := newTestRouter(false, real, allInstalled)

	resp, err := r.Execute(context.Background(), Request{Role: "spec", Prompt: "do it"}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != Codex {
		t.Fatalf("expected failover to codex, got %s", resp.Provider)
	}
	if !strings.Contains(real.calls[1].Prompt, "[HAND-OFF]") {
		t.Error("second call should carry hand-off instructions")
	}

	states := r.States()
	if states[0].Status != StatusCooldown || states[0].ResetAt == nil {
		t.Errorf("claude should be in cooldown, got %+v", states[0])
	}
}

func TestExecuteReturnsNonQuotaErrors(t *testing.T) {
	boom := errors.New("syntax error")
	real := &fakeRunner{results: map[string][]result{"claude": {{out: "bad flag", err: boom}}}}
	r := newTestRouter(false, real, allInstalled)

	if _, err := r.Execute(context.Background(), Request{}, discard); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if len(real.calls) != 1 {
		t.Errorf("non-quota errors must not fail over, got %d calls", len(real.calls))
	}
}

func TestExecuteRealModeWithoutBinaries(t *testing.T) {
	r := newTestRouter(false, &fakeRunner{}, noneInstalled)
	if _, err := r.Execute(context.Background(), Request{}, discard); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("err = %v, want ErrNoProvider", err)
	}
}

func TestExecuteDryRunIgnoresMissingBinaries(t *testing.T) {
	real := &fakeRunner{}
	r := newTestRouter(true, real, noneInstalled)
	resp, err := r.Execute(context.Background(), Request{}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Simulated || len(real.calls) != 0 {
		t.Error("dry run must use the simulated runner")
	}
}

func TestExecuteWaitsForCooldown(t *testing.T) {
	r := newTestRouter(false, &fakeRunner{}, allInstalled)
	_ = r.MarkExhausted(Claude, 50*time.Millisecond)
	_ = r.MarkExhausted(Codex, time.Hour)

	start := time.Now()
	resp, err := r.Execute(context.Background(), Request{}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != Claude {
		t.Errorf("expected claude after its cooldown, got %s", resp.Provider)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Error("expected to wait for the cooldown")
	}
}

func TestExecuteHonoursCancellation(t *testing.T) {
	r := newTestRouter(false, &fakeRunner{}, allInstalled)
	_ = r.MarkExhausted(Claude, time.Hour)
	_ = r.MarkExhausted(Codex, time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := r.Execute(ctx, Request{}, discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestUpdateConfigValidates(t *testing.T) {
	r := newTestRouter(true, &fakeRunner{}, allInstalled)

	err := r.UpdateConfig(ConfigUpdate{
		DefaultProvider: Codex,
		Providers: map[ID]Settings{
			Claude: {HeavyModel: "opus", StdModel: "sonnet", Effort: "max"},
			Codex:  {HeavyModel: "gpt-6-astra", StdModel: "gpt-5.6-terra", Effort: "ultra"}, // not in fallback efforts
		},
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	cfg := r.Config()
	if cfg.Providers[Claude] != ClaudeCLI.Defaults || cfg.DefaultProvider != Claude {
		t.Errorf("update must be atomic, got %+v", cfg)
	}

	if err := r.UpdateConfig(ConfigUpdate{Providers: map[ID]Settings{"gemini": {}}}); !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("err = %v, want ErrUnknownProvider", err)
	}
	if err := r.UpdateConfig(ConfigUpdate{DefaultProvider: "gemini"}); !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("err = %v, want ErrUnknownProvider", err)
	}
}

func TestCLIArgs(t *testing.T) {
	inv := Invocation{Model: "opus", Effort: "high", System: "sys", Prompt: "task"}
	claude := ClaudeCLI.Args(inv)
	if !containsSeq(claude, "--model", "opus") || !containsSeq(claude, "--effort", "high") ||
		!containsSeq(claude, "--append-system-prompt", "sys") || !slices.Contains(claude, "--dangerously-skip-permissions") {
		t.Errorf("unexpected claude args: %v", claude)
	}

	inv.Model = "gpt-6-astra"
	codex := CodexCLI.Args(inv)
	if codex[0] != "exec" || !containsSeq(codex, "--sandbox", "workspace-write") || !containsSeq(codex, "-c", `model_reasoning_effort="high"`) {
		t.Errorf("unexpected codex args: %v", codex)
	}
	if slices.Contains(codex, "--full-auto") {
		t.Error("--full-auto was removed from codex exec")
	}
	if last := codex[len(codex)-1]; !strings.Contains(last, "sys") || !strings.HasSuffix(last, "task") {
		t.Errorf("codex prompt should embed system instructions, got %q", last)
	}
}

func containsSeq(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

func TestDefaultProviderRunsFirst(t *testing.T) {
	real := &fakeRunner{}
	r := newTestRouter(false, real, allInstalled)
	if err := r.UpdateConfig(ConfigUpdate{DefaultProvider: Codex}); err != nil {
		t.Fatal(err)
	}

	resp, err := r.Execute(context.Background(), Request{}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != Codex {
		t.Errorf("provider = %s, want codex", resp.Provider)
	}
	states := r.States()
	if states[0].ID != Codex || !states[0].Default || states[1].Default {
		t.Errorf("default provider must be listed first and flagged: %+v", states)
	}
}

func TestFailoverDisabledWaitsForDefault(t *testing.T) {
	real := &fakeRunner{results: map[string][]result{
		"claude": {{out: "rate limit, retry after 1 seconds", err: errors.New("exit 1")}},
	}}
	r := newTestRouter(false, real, allInstalled)
	off := false
	if err := r.UpdateConfig(ConfigUpdate{Failover: &off}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := r.Execute(ctx, Request{}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != Claude {
		t.Errorf("without failover the default provider must be retried, got %s", resp.Provider)
	}
	for _, c := range real.calls {
		if c.CLI.ID != Claude {
			t.Fatalf("codex must not be called when failover is off: %+v", real.calls)
		}
	}
}

func TestFailoverDisabledWithoutDefaultInstalled(t *testing.T) {
	r := newTestRouter(false, &fakeRunner{}, func(bin string) (string, error) {
		if bin == "codex" {
			return "/usr/bin/codex", nil
		}
		return "", errors.New("not found")
	})
	off := false
	_ = r.UpdateConfig(ConfigUpdate{Failover: &off})
	if _, err := r.Execute(context.Background(), Request{}, discard); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("err = %v, want ErrNoProvider", err)
	}
}

func TestReadOnlyArgs(t *testing.T) {
	inv := Invocation{Model: "m", Prompt: "p", System: "s", ReadOnly: true}
	claude := ClaudeCLI.Args(inv)
	if !containsSeq(claude, "--permission-mode", "plan") || slices.Contains(claude, "--dangerously-skip-permissions") {
		t.Errorf("claude read-only args: %v", claude)
	}
	if codex := CodexCLI.Args(inv); !containsSeq(codex, "--sandbox", "read-only") {
		t.Errorf("codex read-only args: %v", codex)
	}
}

func TestReadOnlyIsForwarded(t *testing.T) {
	real := &fakeRunner{}
	r := newTestRouter(false, real, allInstalled)
	if _, err := r.Execute(context.Background(), Request{ReadOnly: true}, discard); err != nil {
		t.Fatal(err)
	}
	if !real.calls[0].ReadOnly {
		t.Error("ReadOnly must reach the invocation")
	}
}
