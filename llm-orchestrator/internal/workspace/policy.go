package workspace

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"llm-orchestrator/internal/quality"
)

// Policy is the per-repository quality configuration: which commands prove
// the work is correct, which files are read-only inputs and how changes are
// recorded.
type Policy struct {
	// AutoChecks uses the commands detected from the repository manifests;
	// otherwise Checks is used as given.
	AutoChecks bool              `json:"auto_checks"`
	Checks     []quality.Command `json:"checks"`
	// Protected are glob patterns (base name or relative path) of input files
	// agents must not modify.
	Protected []string `json:"protected"`
	// Git records each story on its own branch with one commit per step.
	Git bool `json:"git"`
	// MaxLineLength is the readability limit for changed code files.
	MaxLineLength int `json:"max_line_length"`
	// CheckTimeoutS bounds each command.
	CheckTimeoutS int `json:"check_timeout_s"`
	// Candidates is how many alternative implementations each implementer
	// produces (best-of-N, in separate git worktrees); 1 disables it.
	Candidates int `json:"candidates"`
	// Sequential develops one story at a time and the implementers of a story
	// one after the other; the default (false) runs independent stories and
	// independent fronts in parallel.
	Sequential bool `json:"sequential"`
	// BaseBranches is the development branch each run starts from, per
	// repository: "." for the repository itself, or a nested repository's
	// path. A repository without one starts from its current branch.
	BaseBranches map[string]string `json:"base_branches,omitempty"`
	// ParallelLenses runs each review lens as a separate concurrent agent call;
	// the default (false) evaluates all lenses together in a single consolidated review.
	ParallelLenses bool `json:"parallel_lenses"`
	// MaxFixRounds limits fix-and-verify loops; default is 1 (max 3).
	MaxFixRounds int `json:"max_fix_rounds"`
	// Flow picks which stages run: FlowFull (default), FlowBalanced, FlowFast
	// FlowAudit or FlowCustom, which turns off DisabledStages. Only FlowAudit
	// skips the architect and the implementers.
	Flow           string   `json:"flow,omitempty"`
	DisabledStages []string `json:"disabled_stages,omitempty"`
	// MaxTurns bounds each agent call that writes code (Claude --max-turns);
	// read-only stages get half. 0 uses the engine default.
	MaxTurns int `json:"max_turns,omitempty"`
	// StopOnTurnLimit ends the run when an agent runs out of turns; by
	// default the partial work goes on and the step becomes a pendency.
	StopOnTurnLimit bool `json:"stop_on_turn_limit,omitempty"`
	// AgentCommands are shell command prefixes writing agents may run besides
	// the project checks (e.g. "npm install"). Nil uses DefaultAgentCommands.
	AgentCommands []string `json:"agent_commands"`
	// EphemeralStacks are name patterns (path.Match) of the disposable Docker
	// Compose projects test runs create in this repository; the orchestrator
	// removes the ones left behind. Nil uses DefaultEphemeralStacks; an empty
	// list turns the cleanup off.
	EphemeralStacks []string `json:"ephemeral_stacks"`
}

// DefaultEphemeralStacks match the usual name of an isolated E2E stack.
var DefaultEphemeralStacks = []string{"*-e2e-*"}

// DefaultAgentCommands let the E2E agent run Playwright, which starts the
// application itself through the webServer of playwright.config.
var DefaultAgentCommands = []string{"npx playwright test", "npx playwright install chromium"}

// Flow presets.
const (
	FlowFull     = "full"     // every stage
	FlowBalanced = "balanced" // no security, E2E or documentation
	FlowFast     = "fast"     // Passo 0, test plan, architect, implementers and project checks
	FlowAudit    = "audit"    // no development: checks, review and security of code already written
	FlowCustom   = "custom"   // DisabledStages decides
)

// OptionalStages are the stages a flow may turn off, in pipeline order. The
// architect (it plans the work) and the implementers are not among them.
var OptionalStages = []string{"context", "spec", "checks", "security", "review", "e2e", "docs"}

var presetDisabled = map[string][]string{
	FlowBalanced: {"security", "e2e", "docs"},
	FlowFast:     {"security", "review", "e2e", "docs"},
	FlowAudit:    {"context", "spec", "arch", "frontend", "backend", "e2e", "docs"},
}

// StageDisabled reports whether the flow turns the stage off.
func (p Policy) StageDisabled(id string) bool {
	if p.Flow == FlowCustom {
		return slices.Contains(p.DisabledStages, id)
	}
	return slices.Contains(presetDisabled[p.Flow], id)
}

const (
	maxChecks       = 20
	maxCommandLen   = 1000
	maxPatterns     = 30
	minLineLength   = 80
	maxLineLength   = 1000
	minCheckTimeout = 10
	maxCheckTimeout = 3600
	maxCandidates   = 3
	maxBranchLen    = 200
	maxAgentTurns   = 500
)

// DefaultPolicy is used for repositories without saved settings.
func DefaultPolicy() Policy {
	return Policy{
		AutoChecks:     true,
		Checks:         []quality.Command{},
		Protected:      slices.Clone(quality.DefaultProtected),
		Git:            true,
		MaxLineLength:  160,
		CheckTimeoutS:  600,
		Candidates:     1,
		ParallelLenses: false,
		Flow:           FlowFull,
		MaxFixRounds:   0, // 0 defaults to the engine's maxFixRounds (2)
		AgentCommands:  slices.Clone(DefaultAgentCommands),

		EphemeralStacks: slices.Clone(DefaultEphemeralStacks),
	}
}

// Normalize trims values and fills zero numbers with the defaults.
func (p Policy) Normalize() Policy {
	def := DefaultPolicy()
	if p.MaxLineLength == 0 {
		p.MaxLineLength = def.MaxLineLength
	}
	if p.CheckTimeoutS == 0 {
		p.CheckTimeoutS = def.CheckTimeoutS
	}
	if p.Candidates == 0 {
		p.Candidates = def.Candidates
	}
	if p.Flow == "" {
		p.Flow = FlowFull
	}
	var disabled []string
	for _, id := range OptionalStages {
		if p.Flow == FlowCustom && slices.Contains(p.DisabledStages, id) {
			disabled = append(disabled, id)
		}
	}
	p.DisabledStages = disabled
	checks := make([]quality.Command, 0, len(p.Checks))
	for _, c := range p.Checks {
		c.Name, c.Run = strings.TrimSpace(c.Name), strings.TrimSpace(c.Run)
		if c.Run == "" {
			continue
		}
		if c.Name == "" {
			c.Name = c.Run
		}
		checks = append(checks, c)
	}
	p.Checks = checks
	var patterns []string
	for _, s := range p.Protected {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(patterns, s) {
			patterns = append(patterns, s)
		}
	}
	p.Protected = patterns
	if p.AgentCommands == nil {
		p.AgentCommands = slices.Clone(def.AgentCommands)
	}
	commands := []string{}
	for _, c := range p.AgentCommands {
		if c = strings.TrimSpace(c); c != "" && !slices.Contains(commands, c) {
			commands = append(commands, c)
		}
	}
	p.AgentCommands = commands
	if p.EphemeralStacks == nil {
		p.EphemeralStacks = slices.Clone(def.EphemeralStacks)
	}
	stacks := []string{}
	for _, s := range p.EphemeralStacks {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(stacks, s) {
			stacks = append(stacks, s)
		}
	}
	p.EphemeralStacks = stacks
	bases := map[string]string{}
	for repo, branch := range p.BaseBranches {
		if repo, branch = strings.TrimSpace(repo), strings.TrimSpace(branch); repo != "" && branch != "" {
			bases[repo] = branch
		}
	}
	p.BaseBranches = bases
	if len(bases) == 0 {
		p.BaseBranches = nil
	}
	return p
}

// Validate rejects values outside the supported ranges.
func (p Policy) Validate() error {
	switch {
	case len(p.Checks) > maxChecks:
		return fmt.Errorf("no máximo %d comandos de verificação", maxChecks)
	case len(p.Protected) > maxPatterns:
		return fmt.Errorf("no máximo %d padrões de arquivos protegidos", maxPatterns)
	case p.MaxLineLength < minLineLength || p.MaxLineLength > maxLineLength:
		return fmt.Errorf("o limite de linha deve estar entre %d e %d", minLineLength, maxLineLength)
	case p.CheckTimeoutS < minCheckTimeout || p.CheckTimeoutS > maxCheckTimeout:
		return fmt.Errorf("o tempo limite deve estar entre %d e %d segundos", minCheckTimeout, maxCheckTimeout)
	case p.Candidates < 1 || p.Candidates > maxCandidates:
		return fmt.Errorf("o número de candidatos deve estar entre 1 e %d", maxCandidates)
	case !slices.Contains([]string{FlowFull, FlowBalanced, FlowFast, FlowAudit, FlowCustom}, p.Flow):
		return fmt.Errorf("fluxo desconhecido: %q", p.Flow)
	case p.MaxFixRounds < 0 || p.MaxFixRounds > 3:
		return errors.New("o número de rodadas de correção deve estar entre 0 e 3")
	case p.MaxTurns < 0 || p.MaxTurns > maxAgentTurns:
		return fmt.Errorf("o limite de turnos deve estar entre 0 e %d", maxAgentTurns)
	case len(p.AgentCommands) > maxChecks:
		return fmt.Errorf("no máximo %d comandos liberados aos agentes", maxChecks)
	case len(p.EphemeralStacks) > maxPatterns:
		return fmt.Errorf("no máximo %d padrões de stacks descartáveis", maxPatterns)
	case !p.AutoChecks && len(p.Checks) == 0:
		return errors.New("informe ao menos um comando ou use os detectados automaticamente")
	}
	if len(p.BaseBranches) > maxPatterns {
		return fmt.Errorf("no máximo %d branches base", maxPatterns)
	}
	for repo, branch := range p.BaseBranches {
		if len(branch) > maxBranchLen || len(repo) > maxBranchLen || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " \t\n\r~^:?*[\\") {
			return fmt.Errorf("branch base inválida para %s: %q", repo, branch)
		}
	}
	for _, c := range p.AgentCommands {
		// A prefix becomes a permission rule; it must read as a plain command.
		if len(c) > 200 || strings.HasPrefix(c, "-") || strings.ContainsAny(c, "\n\r()") {
			return fmt.Errorf("comando liberado inválido: %q", c)
		}
	}
	for _, s := range p.EphemeralStacks {
		// A pattern matching every name would remove the user's own stacks.
		if _, err := path.Match(s, ""); err != nil || len(s) > 200 || strings.Trim(s, "*?") == "" {
			return fmt.Errorf("padrão de stack inválido: %q", s)
		}
	}
	for _, c := range p.Checks {
		if len(c.Run) > maxCommandLen || len(c.Name) > 120 {
			return fmt.Errorf("comando muito longo: %q", c.Name)
		}
		if strings.ContainsAny(c.Run, "\n\r") {
			return fmt.Errorf("o comando %q deve ocupar uma linha", c.Name)
		}
	}
	return nil
}
