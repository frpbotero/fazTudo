package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ID identifies a provider in the API and configuration.
type ID string

const (
	Claude ID = "claude"
	Codex  ID = "codex"
	Gemini ID = "gemini"
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
	// Stdin is the text piped to the CLI. The prompt goes there, not in
	// Args: `pkill -f <pattern>` run by an agent matches every command line
	// in the machine, and a prompt quoting that pattern got its own agent killed.
	Stdin func(inv Invocation) string
	// Parse reads the CLI's structured output (final text and token usage);
	// false means the output was not structured and is used as is.
	Parse func(out string) (parsed, bool)
	// Caps are what this adapter enforces and reports; see Capabilities.
	Caps Capabilities
}

// agentSystem is the system prompt of every call. It never changes, so the
// CLI's prompt cache covers it and the shared context that follows; the
// stage's own role goes at the end of the prompt (withRole).
const agentSystem = "Agente de engenharia em execução automática: ninguém vai responder perguntas nem liberar nada; decida e siga. " +
	"Economia de contexto: leia só os arquivos da tarefa; consulte o grafo do código e busque em caminhos específicos antes de abrir arquivos; " +
	"não releia arquivo que não mudou; nunca despeje log inteiro (filtre a falha, `| tail -n 50`); resposta final curta."

// withRole appends the stage's role after the prompt, keeping the start of
// the prompt identical across the stages of a story.
func withRole(inv Invocation) string {
	if strings.TrimSpace(inv.System) == "" {
		return inv.Prompt
	}
	return inv.Prompt + "\n\n## Seu papel\n\n" + inv.System
}

// instructionsFirst is the stdin of CLIs without a system-prompt flag.
func instructionsFirst(inv Invocation) string {
	return fmt.Sprintf("<instructions>\n%s\n</instructions>\n\n%s", agentSystem, withRole(inv))
}

// claudeReadTools and claudeWriteTools are the only tools loaded per mode:
// fewer tool definitions, smaller prompt.
const (
	claudeReadTools  = "Read,Grep,Glob"
	claudeWriteTools = "Read,Grep,Glob,Edit,Write,Bash"
	// claudeDiagnoseTools has no tool that edits files.
	claudeDiagnoseTools = "Read,Grep,Glob,Bash"
)

// safeCommands are the shell commands any writing agent may run besides the
// project's own: code-graph queries and read-only git.
var safeCommands = []string{
	"graphify query", "graphify explain", "graphify affected",
	"git status", "git diff", "git log", "git show", "ls",
}

// claudeAllowed pre-approves the edit tools and, for Bash, only the safe
// commands and the ones the repository policy lists. Anything else is denied
// (--permission-mode dontAsk), never prompted.
func claudeAllowed(commands []string) []string {
	return append([]string{"Read", "Grep", "Glob", "Edit", "Write"}, bashRules(commands)...)
}

func claudeDiagnoseAllowed(commands []string) []string {
	return append([]string{"Read", "Grep", "Glob"}, bashRules(commands)...)
}

// claudeReadDirs grants read access to dirs and denies edits inside them;
// deny rules win over the allowlist. "//" marks an absolute path in a rule.
func claudeReadDirs(dirs []string) []string {
	var args []string
	for _, d := range dirs {
		args = append(args, "--add-dir", d)
	}
	if len(dirs) > 0 {
		args = append(args, "--disallowedTools")
		for _, d := range dirs {
			pattern := "/" + filepath.ToSlash(d) + "/**"
			args = append(args, fmt.Sprintf("Edit(%s)", pattern), fmt.Sprintf("Write(%s)", pattern))
		}
	}
	return args
}

func bashRules(commands []string) []string {
	var rules []string
	for _, c := range append(slices.Clone(safeCommands), commands...) {
		if c = strings.TrimSpace(c); c != "" {
			rules = append(rules, fmt.Sprintf("Bash(%s)", c), fmt.Sprintf("Bash(%s *)", c))
		}
	}
	return rules
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
	// Opus plans (heavy: architecture, security); Sonnet implements.
	Defaults:   Settings{HeavyModel: "claude-opus-5-5", StdModel: "claude-sonnet-5-5", Effort: "medium"},
	LightModel: "claude-haiku-5-5",
	Args: func(inv Invocation) []string {
		// --safe-mode drops the user's skills, plugins, hooks, MCP servers and
		// CLAUDE.md (the orchestrator passes the project instructions itself)
		// and, unlike --bare, keeps the claude.ai login.
		args := []string{"-p", "--safe-mode", "--no-session-persistence", "--output-format", "json",
			"--append-system-prompt", agentSystem}
		if inv.ReadOnly && inv.Diagnose {
			args = append(args, "--permission-mode", "dontAsk", "--tools", claudeDiagnoseTools, "--allowedTools")
			args = append(args, claudeDiagnoseAllowed(inv.Commands)...)
		} else if inv.ReadOnly {
			args = append(args, "--permission-mode", "plan", "--tools", claudeReadTools)
		} else {
			args = append(args, "--permission-mode", "dontAsk", "--tools", claudeWriteTools, "--allowedTools")
			args = append(args, claudeAllowed(inv.Commands)...)
		}
		args = append(args, claudeReadDirs(inv.ReadDirs)...)
		args = append(args, "--autocompact", "auto")
		if inv.MaxTurns > 0 {
			args = append(args, "--max-turns", strconv.Itoa(inv.MaxTurns))
		}
		if inv.Model != "" {
			args = append(args, "--model", inv.Model)
		}
		if inv.Effort != "" {
			args = append(args, "--effort", inv.Effort)
		}
		return args
	},
	Stdin: withRole,
	Parse: parseClaude,
	// Permission rules: --tools and --allowedTools under dontAsk deny any
	// command off the list; plan mode for read-only calls.
	Caps: Capabilities{ReadOnly: true, Write: true, CommandAllowlist: true, Diagnose: true,
		StructuredOutput: true, TokenUsage: true, MaxTurns: true, Cancel: killsProcessGroup, Isolation: IsolationPermissionRules},
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
		// project_doc_max_bytes=0: AGENTS.md already comes in the prompt.
		args := []string{"exec", "--json", "--ephemeral", "--sandbox", sandbox, "--skip-git-repo-check",
			"-c", "project_doc_max_bytes=0"}
		if inv.Model != "" {
			args = append(args, "--model", inv.Model)
		}
		if inv.Effort != "" {
			args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", inv.Effort))
		}
		return append(args, "-") // prompt read from stdin
	},
	// Codex has no system-prompt flag; prepend the instructions instead.
	Stdin: instructionsFirst,
	Parse: parseCodex,
	// The OS sandbox limits what may be written, not which commands run:
	// workspace-write runs any command inside the working directory.
	Caps: Capabilities{ReadOnly: true, Write: true, Diagnose: true,
		StructuredOutput: true, TokenUsage: true, Cancel: killsProcessGroup, Isolation: IsolationOSSandbox},
}

// GeminiCLI runs Google's Gemini CLI in headless mode. The prompt comes on
// stdin; -p only adds a fixed line after it. Gemini has no turn limit flag.
var GeminiCLI = CLI{
	ID:     Gemini,
	Name:   "Gemini CLI",
	Binary: "gemini",
	Models: []Model{
		{ID: "gemini-3.1-pro-preview", Label: "Gemini 3.1 Pro (preview)"},
		{ID: "gemini-3-flash-preview", Label: "Gemini 3 Flash (preview)"},
		{ID: "gemini-2.5-pro", Label: "Gemini 2.5 Pro"},
		{ID: "gemini-2.5-flash", Label: "Gemini 2.5 Flash"},
	},
	Defaults:   Settings{HeavyModel: "gemini-3.1-pro-preview", StdModel: "gemini-3-flash-preview"},
	LightModel: "gemini-2.5-flash",
	Args: func(inv Invocation) []string {
		// An untrusted folder forces the approval mode to "default", which
		// headless runs cannot answer; --skip-trust trusts it for this call
		// only. Extensions stay off, like Claude's --safe-mode.
		args := []string{"-p", "Execute a tarefa descrita acima.", "--output-format", "json", "--skip-trust", "-e", "none"}
		if inv.ReadOnly {
			args = append(args, "--approval-mode", "plan")
		} else {
			// auto_edit approves file edits only; shell runs just the listed commands.
			args = append(args, "--approval-mode", "auto_edit")
			for _, c := range append(slices.Clone(safeCommands), inv.Commands...) {
				if c = strings.TrimSpace(c); c != "" {
					args = append(args, "--allowed-tools", fmt.Sprintf("run_shell_command(%s)", c))
				}
			}
		}
		for _, d := range inv.ReadDirs {
			args = append(args, "--include-directories", d)
		}
		if inv.Model != "" {
			args = append(args, "--model", inv.Model)
		}
		return args
	},
	Stdin: instructionsFirst,
	Parse: parseGemini,
	// auto_edit approves edits and --allowed-tools only the listed commands;
	// plan mode is read-only and runs no command, so diagnosis stays read-only.
	Caps: Capabilities{ReadOnly: true, Write: true, CommandAllowlist: true,
		StructuredOutput: true, TokenUsage: true, Cancel: killsProcessGroup, Isolation: IsolationApprovalMode},
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
	Diagnose bool // a ReadOnly agent may also run Commands
	// MaxTurns bounds the agent's turns where the CLI supports it; 0 = no limit.
	MaxTurns int
	// Commands are the shell commands a writing agent may run (the
	// repository's checks and policy); they come from configuration, never
	// from an agent's output.
	Commands []string
	// ReadDirs are directories outside WorkDir the agent may read but not
	// change (the bundled skills).
	ReadDirs []string
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
	if inv.CLI.Stdin != nil {
		cmd.Stdin = strings.NewReader(inv.CLI.Stdin(inv))
	}
	isolate(cmd)
	cmd.WaitDelay = 5 * time.Second // children holding the pipes must not keep Wait blocked
	// Structured CLIs print JSON on stdout; stderr is kept apart so warnings
	// do not break it, and joins stdout only when the run fails.
	var buf, stdout bytes.Buffer
	cmd.Stdout = &buf
	if inv.CLI.Parse != nil {
		cmd.Stdout = &stdout
	}
	cmd.Stderr = &buf

	err := cmd.Run()
	out := buf.String()
	if inv.CLI.Parse != nil {
		if err == nil {
			out = stdout.String()
		} else {
			out = stdout.String() + "\n" + out
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("%s excedeu o tempo limite de %s", inv.CLI.Binary, timeout)
	}
	if err != nil {
		return out, fmt.Errorf("%s falhou: %w%s: %s", inv.CLI.Binary, err, killedNote(err), truncate(strings.TrimSpace(out), 2000))
	}
	return out, nil
}

// killedNote explains an exit caused by a signal the orchestrator did not
// send (143 = SIGTERM, 137 = SIGKILL): usually another agent's pkill/killall.
func killedNote(err error) string {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return ""
	}
	if code := exitErr.ExitCode(); code == 143 || code == 137 || code == -1 {
		return " (encerrado por sinal externo: outro processo matou o agente, ex.: pkill/killall)"
	}
	return ""
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
