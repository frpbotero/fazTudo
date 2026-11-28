package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func fixedVersion(_ context.Context, binary string) string { return binary + " 1.2.3" }

func capRouter(primary ID, failover bool, real *fakeRunner, look func(string) (string, error)) *Router {
	r := NewRouter(false, []CLI{ClaudeCLI, CodexCLI, GeminiCLI},
		WithRunners(real, &fakeRunner{}), WithLookPath(look), WithVersion(fixedVersion),
		WithDefaultProvider(primary), WithPollInterval(10*time.Millisecond))
	off := failover
	if err := r.UpdateConfig(ConfigUpdate{Failover: &off}); err != nil {
		panic(err)
	}
	return r
}

func only(binaries ...string) func(string) (string, error) {
	return func(b string) (string, error) {
		for _, want := range binaries {
			if b == want {
				return "/usr/bin/" + b, nil
			}
		}
		return "", errors.New("not found")
	}
}

func TestCapabilitiesDescribeWhatEachAdapterEnforces(t *testing.T) {
	// Codex runs any command inside its sandbox: promising an allowlist for
	// it would be false. Gemini's plan mode runs no command.
	if !ClaudeCLI.Caps.CommandAllowlist || CodexCLI.Caps.CommandAllowlist || !GeminiCLI.Caps.CommandAllowlist {
		t.Error("command allowlist: only Claude and Gemini pass the list to the CLI")
	}
	if GeminiCLI.Caps.Diagnose || !ClaudeCLI.Caps.Diagnose {
		t.Error("diagnose: Gemini stays read-only, Claude runs the listed commands")
	}
	if !ClaudeCLI.Caps.MaxTurns || CodexCLI.Caps.MaxTurns || GeminiCLI.Caps.MaxTurns {
		t.Error("max turns: only Claude takes --max-turns")
	}
	for _, c := range []CLI{ClaudeCLI, CodexCLI, GeminiCLI} {
		if !c.Caps.ReadOnly || !c.Caps.Write || !c.Caps.StructuredOutput || c.Caps.Isolation == "" {
			t.Errorf("%s: %+v", c.Name, c.Caps)
		}
		// The adapters' arguments must agree with what they claim.
		args := strings.Join(c.Args(Invocation{CLI: c, Commands: []string{"npm test"}}), " ")
		if c.Caps.CommandAllowlist != strings.Contains(args, "npm test") {
			t.Errorf("%s: allowlist %v, but write args are %q", c.Name, c.Caps.CommandAllowlist, args)
		}
		maxed := strings.Join(c.Args(Invocation{CLI: c, MaxTurns: 7}), " ")
		if c.Caps.MaxTurns != strings.Contains(maxed, "7") {
			t.Errorf("%s: max turns %v, but args are %q", c.Name, c.Caps.MaxTurns, maxed)
		}
	}
}

func TestStrictCommandsNeverFailOverToAProviderWithoutAllowlist(t *testing.T) {
	real := &fakeRunner{results: map[string][]result{
		"claude": {{out: "Error: rate limit exceeded, try again in 10m", err: errors.New("exit status 1")}},
	}}
	r := capRouter(Claude, true, real, only("claude", "codex"))

	// Claude cools down and Codex cannot take a strict write: the call waits
	// for Claude instead of loosening the restriction.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := r.Execute(ctx, Request{Role: "dev", StrictCommands: true}, discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	for _, c := range real.calls {
		if c.CLI.ID == Codex {
			t.Fatal("a strict write must not go to Codex, which cannot restrict commands")
		}
	}
}

func TestFailoverToAWeakerProviderIsAnnounced(t *testing.T) {
	real := &fakeRunner{results: map[string][]result{
		"claude": {{out: "Error: rate limit exceeded, try again in 10m", err: errors.New("exit 1")}},
	}}
	r := capRouter(Claude, true, real, only("claude", "codex"))
	var logs []string
	logf := func(_ slog.Level, f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }

	resp, err := r.Execute(context.Background(), Request{Role: "dev"}, logf)
	if err != nil || resp.Provider != Codex {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "sem restringir os comandos") {
		t.Errorf("weaker failover must be logged:\n%s", strings.Join(logs, "\n"))
	}
}

func TestStrictWriteWithOnlyWeakerProvidersInstalledFails(t *testing.T) {
	r := capRouter(Claude, true, &fakeRunner{}, only("codex"))
	_, err := r.Execute(context.Background(), Request{Role: "dev", StrictCommands: true}, discard)
	if !errors.Is(err, ErrNoCapableProvider) || !strings.Contains(err.Error(), "Codex CLI") {
		t.Errorf("err = %v", err)
	}
}

func TestReadOnlyRequestsStillFailOverUnderStrictCommands(t *testing.T) {
	real := &fakeRunner{}
	r := capRouter(Codex, true, real, only("codex"))
	resp, err := r.Execute(context.Background(), Request{Role: "review", ReadOnly: true, StrictCommands: true}, discard)
	if err != nil || resp.Provider != Codex {
		t.Errorf("a read-only call runs no command to restrict: %+v %v", resp, err)
	}
}

func TestPreflightRefusesARunNoProviderCanServe(t *testing.T) {
	r := capRouter(Codex, true, &fakeRunner{}, only("codex"))
	_, err := r.Preflight(context.Background(), Requirements{StrictCommands: true})
	if !errors.Is(err, ErrNoCapableProvider) || !strings.Contains(err.Error(), "Codex CLI sem restrição de comandos") {
		t.Fatalf("err = %v", err)
	}

	warnings, err := r.Preflight(context.Background(), Requirements{MaxTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "limite de turnos") || !strings.Contains(joined, "sandbox do sistema operacional") {
		t.Errorf("warnings = %q", warnings)
	}
	for _, s := range r.States() {
		if s.ID == Codex && (s.Version != "codex 1.2.3" || s.Capabilities.CommandAllowlist) {
			t.Errorf("state = %+v", s)
		}
	}
}

func TestPreflightPointsOutASkippedDefault(t *testing.T) {
	r := capRouter(Codex, true, &fakeRunner{}, only("codex", "claude"))
	warnings, err := r.Preflight(context.Background(), Requirements{StrictCommands: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "provider padrão") {
		t.Errorf("warnings = %q", warnings)
	}

	// Without failover only the default counts.
	r = capRouter(Codex, false, &fakeRunner{}, only("codex", "claude"))
	if _, err := r.Preflight(context.Background(), Requirements{StrictCommands: true}); !errors.Is(err, ErrNoCapableProvider) {
		t.Errorf("failover off: err = %v", err)
	}
}

func TestPreflightWithoutAnyCLI(t *testing.T) {
	r := capRouter(Claude, true, &fakeRunner{}, noneInstalled)
	// Even while the router is in simulation: a real run needs a real CLI.
	r.SetDryRun(true)
	if _, err := r.Preflight(context.Background(), Requirements{}); !errors.Is(err, ErrNoProvider) {
		t.Errorf("err = %v", err)
	}
}

// The mag run warned that Codex does not restrict commands while every task
// went to Claude Code: a reserve is named only when it takes a task.
func TestPreflightDoesNotWarnAboutAReserveProvider(t *testing.T) {
	r := capRouter(Claude, true, &fakeRunner{}, only("claude", "codex"))
	warnings, err := r.Preflight(context.Background(), Requirements{MaxTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(warnings, "\n"); strings.Contains(joined, "Codex") {
		t.Errorf("warnings name an unused reserve: %q", warnings)
	}
	// The same reserve as the default is warned about.
	r = capRouter(Codex, true, &fakeRunner{}, only("claude", "codex"))
	warnings, _ = r.Preflight(context.Background(), Requirements{MaxTurns: true})
	if joined := strings.Join(warnings, "\n"); !strings.Contains(joined, "Codex CLI não restringe os comandos") {
		t.Errorf("the serving provider's weaker isolation must be said: %q", warnings)
	}
}
