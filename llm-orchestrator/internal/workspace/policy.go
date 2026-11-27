package workspace

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/skills"
)

// Policy is the per-repository quality configuration: which commands prove
// the work is correct, which files are read-only inputs and how changes are
// recorded.
type Policy struct {
	// AutoChecks uses the commands detected from the repository manifests;
	// otherwise Checks is used as given.
	AutoChecks bool              `json:"auto_checks"`
	Checks     []quality.Command `json:"checks"`
	// MigrationChecks prove a migration on a disposable database: apply,
	// check integrity, roll back or recover, apply again. They run when a
	// story's plan or risk calls for the migration check.
	MigrationChecks []quality.Command `json:"migration_checks,omitempty"`
	// Benchmarks measure the software against its performance targets,
	// candidate against baseline; see Benchmark.
	Benchmarks []Benchmark `json:"benchmarks,omitempty"`
	// BenchmarkChecks measure the workload a performance requirement is
	// about; the performance specialist reads their output.
	BenchmarkChecks []quality.Command `json:"benchmark_checks,omitempty"`
	// Profiles are the stack profiles (internal/skills/bundle/profiles) the
	// repository's agents follow. Detection only suggests them; they apply
	// once chosen here.
	Profiles []string `json:"profiles,omitempty"`
	// StyleBlocking makes the reviewers' style remarks binding; by default
	// they are advice, since a project's conventions decide its style.
	StyleBlocking bool `json:"style_blocking,omitempty"`
	// MaxParallelReviews bounds the reviewers running at once; 0 is 3.
	MaxParallelReviews int `json:"max_parallel_reviews,omitempty"`
	// ReviewProvider is tried first for the reviews (another provider than
	// the implementers'); empty keeps the default order. It becomes a default
	// only after the evaluation shows it helps.
	ReviewProvider string `json:"review_provider,omitempty"`
	// DeliveryRetention is how many delivery packages a repository keeps;
	// 0 is 10.
	DeliveryRetention int `json:"delivery_retention,omitempty"`
	// SCM decides how a story reaches the base: merged locally (default) or
	// through a pull request a human reviews.
	SCM SCMPolicy `json:"scm,omitempty"`
	// MaxParallelAgents bounds the agent calls of a run at once, across
	// stories, fronts, reviewers and candidates; 0 is 4.
	MaxParallelAgents int `json:"max_parallel_agents,omitempty"`
	// ContextBytes caps each earlier stage's output forwarded to the next
	// ones; 0 is 4000. Cut outputs say so in the prompt.
	ContextBytes int `json:"context_bytes,omitempty"`
	// Budget bounds what a run may spend; zero values leave a bound off.
	Budget Budget `json:"budget,omitempty"`
	// Prices estimate the cost of calls whose CLI reports tokens but no cost,
	// per model, in US$ per million tokens. Without a price the cost stays
	// unknown; PricesVersion names the table the estimates came from.
	Prices        map[string]Price `json:"prices,omitempty"`
	PricesVersion string           `json:"prices_version,omitempty"`
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
	// PromoteUnverified lets a story whose delivery was not evaluated (no
	// project command ran) enter the base branch. Off by default: without
	// executed evidence the story's branch is kept for review instead.
	// Rejected or inconclusive deliveries are never promoted.
	PromoteUnverified bool `json:"promote_unverified,omitempty"`
	// StrictCommands demands providers that restrict writing agents to the
	// listed commands: a CLI that cannot (Codex runs any command inside its
	// sandbox) is never used to write, not even on failover, and a run no
	// installed provider can serve does not start.
	StrictCommands bool `json:"strict_commands,omitempty"`
	// RiskRules make stages mandatory for risky stories; see RiskRule. Nil
	// uses DefaultRiskRules; an empty list turns them off.
	RiskRules []RiskRule `json:"risk_rules"`
	// SkipBaseline turns off running the project commands before each story
	// (the baseline that tells new failures from preexisting ones).
	SkipBaseline bool `json:"skip_baseline,omitempty"`
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
var OptionalStages = []string{"context", "spec", "checks", "security", "review", "e2e", "acceptance", "docs"}

var presetDisabled = map[string][]string{
	FlowBalanced: {"security", "e2e", "docs"},
	FlowFast:     {"security", "review", "e2e", "acceptance", "docs"},
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
	var profiles []string
	for _, name := range p.Profiles {
		if name = strings.TrimSpace(name); name != "" && !slices.Contains(profiles, name) {
			profiles = append(profiles, name)
		}
	}
	p.Profiles = profiles
	p.ReviewProvider = strings.ToLower(strings.TrimSpace(p.ReviewProvider))
	p.Checks = normalizeCommands(p.Checks)
	p.MigrationChecks = normalizeCommands(p.MigrationChecks)
	p.BenchmarkChecks = normalizeCommands(p.BenchmarkChecks)
	if len(p.MigrationChecks) == 0 {
		p.MigrationChecks = nil
	}
	if len(p.BenchmarkChecks) == 0 {
		p.BenchmarkChecks = nil
	}
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
	if p.RiskRules == nil {
		p.RiskRules = slices.Clone(DefaultRiskRules)
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
	if err := validateRiskRules(p.RiskRules); err != nil {
		return err
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
	if p.ContextBytes != 0 && (p.ContextBytes < 1000 || p.ContextBytes > 64000) {
		return errors.New("o orçamento de contexto por artefato deve estar entre 1000 e 64000 bytes")
	}
	if err := validateBenchmarks(p.Benchmarks); err != nil {
		return err
	}
	if err := validateBudget(p.Budget, p.Prices); err != nil {
		return err
	}
	if p.MaxParallelAgents < 0 || p.MaxParallelAgents > 16 {
		return errors.New("o limite de agentes em paralelo deve estar entre 0 e 16")
	}
	if err := validateSCM(p.SCM); err != nil {
		return err
	}
	if p.DeliveryRetention < 0 || p.DeliveryRetention > 100 {
		return errors.New("a retenção de pacotes de entrega deve estar entre 0 e 100")
	}
	if p.MaxParallelReviews < 0 || p.MaxParallelReviews > 5 {
		return errors.New("o limite de revisões em paralelo deve estar entre 0 e 5")
	}
	if !slices.Contains([]string{"", "claude", "codex", "gemini"}, p.ReviewProvider) {
		return fmt.Errorf("provider de revisão desconhecido: %q", p.ReviewProvider)
	}
	for _, name := range p.Profiles {
		if !skills.HasProfile(name) {
			return fmt.Errorf("perfil de stack desconhecido: %q (disponíveis: %s)", name, strings.Join(skills.Profiles(), ", "))
		}
	}
	if len(p.MigrationChecks) > maxChecks || len(p.BenchmarkChecks) > maxChecks {
		return fmt.Errorf("no máximo %d comandos de migration e %d de benchmark", maxChecks, maxChecks)
	}
	for _, c := range slices.Concat(p.Checks, p.MigrationChecks, p.BenchmarkChecks) {
		if len(c.Run) > maxCommandLen || len(c.Name) > 120 {
			return fmt.Errorf("comando muito longo: %q", c.Name)
		}
		if strings.ContainsAny(c.Run, "\n\r") {
			return fmt.Errorf("o comando %q deve ocupar uma linha", c.Name)
		}
		if err := validateReport(c); err != nil {
			return err
		}
		if c.Resource != "" && !reResource.MatchString(c.Resource) {
			return fmt.Errorf("recurso do comando %q inválido: use letras minúsculas, números e hífen", c.Name)
		}
	}
	return nil
}

// validateReport accepts a report the orchestrator can read: a known format,
// and a path inside the repository where the format needs a file.
func validateReport(c quality.Command) error {
	r := c.Report
	if r == nil {
		return nil
	}
	if !slices.Contains(quality.ReportFormats, r.Format) {
		return fmt.Errorf("relatório de %q: formato %q desconhecido (use %s)", c.Name, r.Format, strings.Join(quality.ReportFormats, ", "))
	}
	if r.Path == "" && r.Format != quality.ReportGoJSON {
		return fmt.Errorf("relatório de %q: informe o arquivo gerado pelo formato %s", c.Name, r.Format)
	}
	clean := path.Clean(r.Path)
	if len(r.Path) > 300 || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("relatório de %q: o arquivo deve ficar dentro do repositório", c.Name)
	}
	return nil
}

// normalizeCommands trims commands, drops empty ones and names the unnamed.
func normalizeCommands(list []quality.Command) []quality.Command {
	checks := make([]quality.Command, 0, len(list))
	for _, c := range list {
		c.Name, c.Run = strings.TrimSpace(c.Name), strings.TrimSpace(c.Run)
		if c.Run == "" {
			continue
		}
		if c.Name == "" {
			c.Name = c.Run
		}
		c.Resource = strings.ToLower(strings.TrimSpace(c.Resource))
		if c.Report != nil {
			r := quality.ReportSpec{Format: strings.TrimSpace(c.Report.Format), Path: strings.TrimSpace(c.Report.Path)}
			c.Report = nil
			if r.Format != "" {
				c.Report = &r
			}
		}
		checks = append(checks, c)
	}
	return checks
}

// SCMPolicy is the external review. Publishing to the host happens only in
// SCMPullRequest mode, which the user sets: the default changes nothing
// outside the machine.
type SCMPolicy struct {
	Mode string `json:"mode,omitempty"` // SCMLocal (default) or SCMPullRequest
	// Draft opens the pull requests as drafts.
	Draft bool `json:"draft,omitempty"`
	// ReviewFrom is the lowest risk level that goes through a pull request;
	// empty sends every story. Below it, stories merge locally as usual.
	ReviewFrom string `json:"review_from,omitempty"`
}

// SCM modes.
const (
	SCMLocal       = "local"
	SCMPullRequest = "pr"
)

func validateSCM(s SCMPolicy) error {
	if !slices.Contains([]string{"", SCMLocal, SCMPullRequest}, s.Mode) {
		return fmt.Errorf("modo de SCM desconhecido: %q (use local ou pr)", s.Mode)
	}
	if s.ReviewFrom != "" && !slices.Contains(riskLevels, s.ReviewFrom) {
		return fmt.Errorf("nível de risco desconhecido em review_from: %q", s.ReviewFrom)
	}
	return nil
}

var reResource = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Budget bounds a run. Each bound is checked before every agent call: once
// reached, no new call starts and the work done so far stays. Tokens are
// counted from what the CLIs report, after each call, so a single call may
// go past MaxTokens; no CLI lets a hard token ceiling be set mid-call.
type Budget struct {
	MaxMinutes int   `json:"max_minutes,omitempty"`
	MaxCalls   int   `json:"max_calls,omitempty"`
	MaxTokens  int64 `json:"max_tokens,omitempty"`
	// MaxRetries bounds the retries of failed agent calls across the run;
	// MaxRetryWaitS caps the wait before each one.
	MaxRetries    int `json:"max_retries,omitempty"`
	MaxRetryWaitS int `json:"max_retry_wait_s,omitempty"`
}

// Price is a model's cost in US$ per million tokens.
type Price struct {
	Input       float64 `json:"input"`
	CachedInput float64 `json:"cached_input,omitempty"`
	Output      float64 `json:"output"`
}

func validateBudget(b Budget, prices map[string]Price) error {
	if b.MaxMinutes < 0 || b.MaxCalls < 0 || b.MaxTokens < 0 || b.MaxRetries < 0 || b.MaxRetryWaitS < 0 {
		return errors.New("os limites do orçamento não podem ser negativos")
	}
	for model, p := range prices {
		if strings.TrimSpace(model) == "" || p.Input < 0 || p.CachedInput < 0 || p.Output < 0 {
			return fmt.Errorf("preço inválido para o modelo %q", model)
		}
	}
	return nil
}

// Benchmark is a performance measurement the orchestrator runs on the
// story's starting snapshot and on the candidate, in turns, under the same
// conditions. Its command prints, on a line of its own, a JSON object with
// any of p50_ms, p95_ms, samples, throughput_rps, mem_mb, cpu_pct, queries.
type Benchmark struct {
	Name string `json:"name"`
	Run  string `json:"run"`
	// Workload says what the command exercises: data, concurrency, warm-up.
	Workload string `json:"workload"`
	// Stack limits the benchmark to repositories where the discovery found
	// that stack (e.g. "Go", "NestJS"); empty runs it everywhere.
	Stack string `json:"stack,omitempty"`
	// Warmup runs are discarded; Repeat runs are measured (default 3).
	Warmup int `json:"warmup,omitempty"`
	Repeat int `json:"repeat,omitempty"`
	// MaxRegressionPct is the worsening tolerated (default 10);
	// MaxNoisePct the spread past which a measurement proves nothing
	// (default 15).
	MaxRegressionPct float64 `json:"max_regression_pct,omitempty"`
	MaxNoisePct      float64 `json:"max_noise_pct,omitempty"`
	// Required makes a regression block the delivery.
	Required bool   `json:"required,omitempty"`
	Resource string `json:"resource,omitempty"`
}

func validateBenchmarks(list []Benchmark) error {
	if len(list) > maxChecks {
		return fmt.Errorf("no máximo %d benchmarks", maxChecks)
	}
	for _, b := range list {
		switch {
		case strings.TrimSpace(b.Name) == "" || strings.TrimSpace(b.Run) == "":
			return errors.New("benchmark precisa de nome e comando")
		case strings.TrimSpace(b.Workload) == "":
			return fmt.Errorf("benchmark %q: descreva o workload (dados, concorrência, aquecimento)", b.Name)
		case len(b.Run) > maxCommandLen || strings.ContainsAny(b.Run, "\n\r"):
			return fmt.Errorf("benchmark %q: comando inválido", b.Name)
		case b.Warmup < 0 || b.Warmup > 10 || b.Repeat < 0 || b.Repeat > 20:
			return fmt.Errorf("benchmark %q: aquecimento até 10 e repetições até 20", b.Name)
		case b.MaxRegressionPct < 0 || b.MaxNoisePct < 0:
			return fmt.Errorf("benchmark %q: limites não podem ser negativos", b.Name)
		case b.Resource != "" && !reResource.MatchString(b.Resource):
			return fmt.Errorf("benchmark %q: recurso inválido", b.Name)
		}
	}
	return nil
}
