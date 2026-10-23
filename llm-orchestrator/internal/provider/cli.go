package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// ID identifies a provider in the API and configuration.
type ID string

const (
	Claude ID = "claude"
	Codex  ID = "codex"
)

// Tier selects which of a provider's two configured models a task uses.
type Tier string

const (
	TierHeavy    Tier = "heavy"    // architecture, security, review
	TierStandard Tier = "standard" // spec, implementation, QA
	TierLight    Tier = "light"    // quick classification (e.g. finding stories in a document)
)

// Settings is the user-editable model configuration of a provider.
type Settings struct {
	HeavyModel string `json:"heavy_model"`
	StdModel   string `json:"std_model"`
	Effort     string `json:"effort"`
}

// Model returns the model configured for the given tier.
func (s Settings) Model(t Tier) string {
	if t == TierHeavy {
		return s.HeavyModel
	}
	return s.StdModel
}

// Model is a selectable model. Efforts lists the reasoning levels the model
// accepts (empty = unknown); Hidden models are valid but not offered in lists.
type Model struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Efforts []string `json:"efforts,omitempty"`
	Hidden  bool     `json:"hidden,omitempty"`
}

// Catalog is the live list of models reported by a CLI installation.
type Catalog struct {
	Models        []Model // ordered by preference
	DefaultModel  string  // the CLI's own configured default, if any
	DefaultEffort string
	Source        string // where the catalog was read from
}

// CLI describes how to invoke a vendor command-line agent.
type CLI struct {
	ID       ID
	Name     string
	Binary   string
	Efforts  []string
	Models   []Model // fallback catalog when LoadCatalog is nil or fails
	Defaults Settings
	// LightModel runs TierLight tasks; empty falls back to the standard model.
	LightModel string
	// LoadCatalog reads the models actually available to the installed CLI.
	LoadCatalog func() (Catalog, error)
	// Args builds the argument list for a non-interactive run.
	Args func(inv Invocation) []string
}

// ClaudeCLI runs Anthropic's Claude Code in print mode.
var ClaudeCLI = CLI{
	ID:      Claude,
	Name:    "Claude Code",
	Binary:  "claude",
	Efforts: []string{"low", "medium", "high", "xhigh", "max"},
	Models: []Model{
		{ID: "claude-opus-5-5", Label: "Claude Opus 5.5"},
		{ID: "claude-sonnet-5-5", Label: "Claude Sonnet 5.5"},
		{ID: "claude-haiku-5-5", Label: "Claude Haiku 5.5"},
		{ID: "claude-fable-5-1", Label: "Claude Fable 5.1"},
	},
	Defaults:   Settings{HeavyModel: "claude-opus-5-5", StdModel: "claude-sonnet-5-5", Effort: "high"},
	LightModel: "claude-haiku-5-5",
	Args: func(inv Invocation) []string {
		args := []string{"-p", inv.Prompt, "--append-system-prompt", inv.System}
		if inv.ReadOnly {
			args = append(args, "--permission-mode", "plan")
		} else {
			args = append(args, "--dangerously-skip-permissions")
		}
		if inv.Model != "" {
			args = append(args, "--model", inv.Model)
		}
		if inv.Effort != "" {
			args = append(args, "--effort", inv.Effort)
		}
		return args
	},
}

// CodexCLI runs OpenAI's Codex CLI through `codex exec`. Its models come from
// the CLI's own cache (~/.codex/models_cache.json); the static list below is
// only a fallback for when that cache is unavailable.
var CodexCLI = CLI{
	ID:      Codex,
	Name:    "Codex CLI",
	Binary:  "codex",
	Efforts: []string{"low", "medium", "high", "xhigh", "max"},
	Models: []Model{
		{ID: "gpt-6-astra", Label: "GPT-6-Astra"},
		{ID: "gpt-5.6-sol", Label: "GPT-5.6-Sol"},
		{ID: "gpt-5.6-terra", Label: "GPT-5.6-Terra"},
		{ID: "gpt-5.6-luna", Label: "GPT-5.6-Luna"},
	},
	Defaults:    Settings{HeavyModel: "gpt-6-astra", StdModel: "gpt-5.6-terra", Effort: "medium"},
	LoadCatalog: func() (Catalog, error) { return LoadCodexCatalog(CodexHome()) },
	Args: func(inv Invocation) []string {
		sandbox := "workspace-write"
		if inv.ReadOnly {
			sandbox = "read-only"
		}
		args := []string{"exec", "--sandbox", sandbox, "--skip-git-repo-check"}
		if inv.Model != "" {
			args = append(args, "--model", inv.Model)
		}
		if inv.Effort != "" {
			args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", inv.Effort))
		}
		// Codex has no system-prompt flag; prepend the instructions instead.
		return append(args, fmt.Sprintf("<instructions>\n%s\n</instructions>\n\n%s", inv.System, inv.Prompt))
	},
}

// Invocation is a single resolved call to a provider.
type Invocation struct {
	CLI      CLI
	Model    string
	Effort   string
	System   string
	Prompt   string
	WorkDir  string
	ReadOnly bool // the agent may read the repository but not change it
}

// Runner executes an invocation and returns the combined output. On failure
// the output is still returned so callers can inspect it for quota errors.
type Runner interface {
	Run(ctx context.Context, inv Invocation) (string, error)
}

// DefaultAgentTimeout bounds one CLI run. An agent stuck on a foreground
// server or a hanging test would otherwise block an unattended run forever.
const DefaultAgentTimeout = 60 * time.Minute

// ExecRunner shells out to the real CLI binary.
type ExecRunner struct {
	Timeout time.Duration // zero means DefaultAgentTimeout
}

func (r ExecRunner) Run(ctx context.Context, inv Invocation) (string, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultAgentTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, inv.CLI.Binary, inv.CLI.Args(inv)...)
	cmd.Dir = inv.WorkDir
	isolate(cmd)
	cmd.WaitDelay = 5 * time.Second // children holding the pipes must not keep Wait blocked
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	out := buf.String()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("%s excedeu o tempo limite de %s", inv.CLI.Binary, timeout)
	}
	if err != nil {
		return out, fmt.Errorf("%s falhou: %w: %s", inv.CLI.Binary, err, truncate(strings.TrimSpace(out), 2000))
	}
	return out, nil
}

// SimulatedRunner fakes a successful run without spending tokens.
type SimulatedRunner struct {
	Delay time.Duration
}

func (s SimulatedRunner) Run(ctx context.Context, inv Invocation) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(s.Delay):
	}

	effort := inv.Effort
	if effort == "" {
		effort = "padrão"
	}
	return fmt.Sprintf("Simulação — %s · %s · esforço %s\n\n"+
		"- Artefatos processados para a tarefa solicitada.\n"+
		"- Diretrizes arquiteturais e critérios de aceitação respeitados.\n"+
		"- Próxima etapa pronta para execução.\n\n"+
		"Nenhum token foi consumido. Desative o modo simulação para executar o CLI real.",
		inv.CLI.Name, inv.Model, effort), nil
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
