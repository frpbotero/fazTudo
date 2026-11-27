package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNoProvider is returned when no provider can ever serve a request
// (e.g. real mode with no CLI installed).
var ErrNoProvider = errors.New("nenhum provider disponível: instale o claude ou o codex CLI, ou ative o modo simulação")

// ErrUnknownProvider is returned for operations on an unregistered ID.
var ErrUnknownProvider = errors.New("provider desconhecido")

// Provider availability as reported to the UI.
const (
	StatusAvailable   = "available"
	StatusCooldown    = "cooldown"
	StatusUnavailable = "unavailable"
)

const (
	// maxQuotaNoticeLen bounds a "successful" output still treated as a quota notice.
	maxQuotaNoticeLen = 400

	installCheckTTL = 30 * time.Second
	catalogTTL      = time.Minute
)

// Logf receives human-readable progress events from the router.
type Logf func(level slog.Level, format string, args ...any)

// Request is a task for a single agent.
type Request struct {
	Role    string
	System  string
	Prompt  string
	WorkDir string
	Tier    Tier
	// ReadOnly runs the CLI in a mode that cannot modify the repository.
	ReadOnly bool
	// Diagnose lets a ReadOnly agent run Commands, without any tool to edit
	// files. CLIs that cannot express it stay read-only.
	Diagnose bool
	// MaxTurns bounds the agent's turns (CLIs that support it); 0 = no limit.
	MaxTurns int
	// Commands are the shell commands an agent may run.
	Commands []string
	// ReadDirs are directories outside WorkDir the agent may read.
	ReadDirs []string
	// StrictCommands demands a provider that restricts a writing agent to
	// Commands; providers that cannot are skipped, even on failover.
	StrictCommands bool
	// Prefer tries this provider first, such as a different one for review;
	// the others stay as failover.
	Prefer ID
}

// Response is the result of a successful task.
type Response struct {
	Output    string
	Provider  ID
	Name      string
	Model     string
	Effort    string
	Simulated bool
	Duration  time.Duration
	// Usage is the token accounting reported by the CLI; nil when it reports none.
	Usage *Usage
	// TurnLimited marks partial work: the agent ran out of turns.
	TurnLimited bool
}

// State is the public snapshot of a provider.
type State struct {
	ID        ID         `json:"id"`
	Name      string     `json:"name"`
	Binary    string     `json:"binary"`
	Default   bool       `json:"default"`
	Installed bool       `json:"installed"`
	Status    string     `json:"status"`
	ResetAt   *time.Time `json:"reset_at,omitempty"`
	Efforts   []string   `json:"efforts"`
	Models    []Model    `json:"models"`
	// Catalog is where Models came from; empty means the built-in fallback.
	Catalog string `json:"catalog,omitempty"`
	// Version is the installed CLI's, once a preflight detected it.
	Version      string       `json:"version,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
	Settings
}

// Config is the routing configuration: which provider runs first, whether
// the others may take over on quota errors, and each provider's models.
type Config struct {
	DefaultProvider ID              `json:"default_provider"`
	Failover        bool            `json:"failover"`
	Providers       map[ID]Settings `json:"providers"`
}

// ConfigUpdate is a partial Config; nil/empty fields are left unchanged.
type ConfigUpdate struct {
	DefaultProvider ID              `json:"default_provider,omitempty"`
	Failover        *bool           `json:"failover,omitempty"`
	Providers       map[ID]Settings `json:"providers,omitempty"`
}

type managed struct {
	cli       CLI
	settings  Settings
	resetAt   time.Time
	installed bool
	checkedAt time.Time

	models    []Model  // live catalog, or cli.Models as fallback
	efforts   []string // union of the visible models' effort levels
	catalog   string   // catalog source; "" for the fallback
	catalogAt time.Time
	version   string // detected by Preflight; "" until then
}

// loadCatalog refreshes the model list from the CLI, keeping the fallback
// when the CLI cannot report one. Caller holds r.mu (or owns p).
func (p *managed) loadCatalog(now time.Time) (Catalog, bool) {
	p.catalogAt = now
	if p.cli.LoadCatalog != nil {
		if cat, err := p.cli.LoadCatalog(); err == nil && len(cat.Models) > 0 {
			p.models, p.catalog = cat.Models, cat.Source
			p.efforts = mergeEfforts(cat.Models, p.cli.Efforts)
			return cat, true
		}
	}
	p.models, p.catalog, p.efforts = p.cli.Models, "", p.cli.Efforts
	return Catalog{}, false
}

// validate checks settings against the provider's current catalog. Unknown
// model IDs are allowed (custom models); known ones must support the effort.
func (p *managed) validate(s Settings) error {
	name := p.cli.Name
	if strings.TrimSpace(s.HeavyModel) == "" || strings.TrimSpace(s.StdModel) == "" {
		return fmt.Errorf("%s: modelos heavy e standard são obrigatórios", name)
	}
	if s.Effort == "" {
		return nil
	}
	if !slices.Contains(p.efforts, s.Effort) {
		return fmt.Errorf("%s: nível de esforço %q inválido (aceitos: %s)", name, s.Effort, strings.Join(p.efforts, ", "))
	}
	for _, id := range []string{s.HeavyModel, s.StdModel} {
		if m, ok := findModel(p.models, id); ok && len(m.Efforts) > 0 && !slices.Contains(m.Efforts, s.Effort) {
			return fmt.Errorf("%s: o modelo %s não suporta esforço %q (aceitos: %s)", name, m.Label, s.Effort, strings.Join(m.Efforts, ", "))
		}
	}
	return nil
}

// Router dispatches tasks to the first available provider and fails over
// to the next one when a quota error is detected.
type Router struct {
	mu        sync.Mutex
	providers []*managed // registration order
	primary   ID
	failover  bool
	dryRun    atomic.Bool

	exec     Runner
	sim      Runner
	lookPath func(string) (string, error)
	version  func(ctx context.Context, binary string) string
	now      func() time.Time
	poll     time.Duration
}

// Option customises a Router.
type Option func(*Router)

// WithRunners overrides the real and simulated runners (mainly for tests).
func WithRunners(real, simulated Runner) Option {
	return func(r *Router) { r.exec, r.sim = real, simulated }
}

// WithSimulationDelay sets how long a simulated task takes.
func WithSimulationDelay(d time.Duration) Option {
	return func(r *Router) { r.sim = SimulatedRunner{Delay: d} }
}

// WithLookPath overrides binary detection (mainly for tests).
func WithLookPath(fn func(string) (string, error)) Option {
	return func(r *Router) { r.lookPath = fn }
}

// WithVersion overrides CLI version detection (mainly for tests).
func WithVersion(fn func(ctx context.Context, binary string) string) Option {
	return func(r *Router) { r.version = fn }
}

// WithPollInterval sets how often a request waiting on cooldowns re-checks.
func WithPollInterval(d time.Duration) Option {
	return func(r *Router) { r.poll = d }
}

// WithDefaultProvider selects the provider tried first.
func WithDefaultProvider(id ID) Option {
	return func(r *Router) { r.primary = id }
}

// NewRouter builds a router over the given CLIs. The first CLI is the
// default provider unless WithDefaultProvider says otherwise; failover is on.
func NewRouter(dryRun bool, clis []CLI, opts ...Option) *Router {
	r := &Router{
		failover: true,
		exec:     ExecRunner{},
		sim:      SimulatedRunner{Delay: 1400 * time.Millisecond},
		lookPath: exec.LookPath,
		version:  cliVersion,
		now:      time.Now,
		poll:     2 * time.Second,
	}
	now := time.Now()
	for _, c := range clis {
		p := &managed{cli: c, settings: c.Defaults}
		if cat, ok := p.loadCatalog(now); ok {
			p.settings = defaultsFromCatalog(c.Defaults, cat, p.efforts)
		}
		r.providers = append(r.providers, p)
	}
	if len(clis) > 0 {
		r.primary = clis[0].ID
	}
	for _, o := range opts {
		o(r)
	}
	r.dryRun.Store(dryRun)
	return r
}

// Validate reports whether the router's initial options are consistent.
func (r *Router) Validate() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.find(r.primary) == nil {
		return fmt.Errorf("%w: %q", ErrUnknownProvider, r.primary)
	}
	return nil
}

// DryRun reports whether tasks are simulated instead of calling real CLIs.
func (r *Router) DryRun() bool { return r.dryRun.Load() }

// SetDryRun toggles simulation mode.
func (r *Router) SetDryRun(v bool) { r.dryRun.Store(v) }

// Config returns the current routing configuration.
func (r *Router) Config() Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg := Config{DefaultProvider: r.primary, Failover: r.failover, Providers: make(map[ID]Settings, len(r.providers))}
	for _, p := range r.providers {
		cfg.Providers[p.cli.ID] = p.settings
	}
	return cfg
}

// UpdateConfig validates and applies a partial update. It is all-or-nothing.
func (r *Router) UpdateConfig(u ConfigUpdate) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if u.DefaultProvider != "" && r.find(u.DefaultProvider) == nil {
		return fmt.Errorf("%w: %q", ErrUnknownProvider, u.DefaultProvider)
	}
	for id, s := range u.Providers {
		p := r.find(id)
		if p == nil {
			return fmt.Errorf("%w: %q", ErrUnknownProvider, id)
		}
		if err := p.validate(s); err != nil {
			return err
		}
	}

	if u.DefaultProvider != "" {
		r.primary = u.DefaultProvider
	}
	if u.Failover != nil {
		r.failover = *u.Failover
	}
	for id, s := range u.Providers {
		r.find(id).settings = s
	}
	return nil
}

// ordered returns providers with the default first. Caller holds r.mu.
func (r *Router) ordered() []*managed {
	out := make([]*managed, 0, len(r.providers))
	if p := r.find(r.primary); p != nil {
		out = append(out, p)
	}
	for _, p := range r.providers {
		if p.cli.ID != r.primary {
			out = append(out, p)
		}
	}
	return out
}

// States returns a snapshot of every provider.
func (r *Router) States() []State {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	dry := r.DryRun()
	states := make([]State, 0, len(r.providers))
	for _, p := range r.ordered() {
		r.refresh(p, now)
		s := State{
			ID:        p.cli.ID,
			Name:      p.cli.Name,
			Binary:    p.cli.Binary,
			Default:   p.cli.ID == r.primary,
			Installed: p.installed,
			Status:    StatusAvailable,
			// Never null: a CLI without effort levels (Gemini) is an empty
			// list, which the panel can iterate.
			Efforts:      append([]string{}, p.efforts...),
			Models:       p.models,
			Catalog:      displayHome(p.catalog),
			Version:      p.version,
			Capabilities: p.cli.Caps,
			Settings:     p.settings,
		}
		switch {
		case !p.resetAt.IsZero():
			reset := p.resetAt
			s.Status, s.ResetAt = StatusCooldown, &reset
		case !dry && !p.installed:
			s.Status = StatusUnavailable
		}
		states = append(states, s)
	}
	return states
}

// MarkExhausted puts a provider in cooldown, as if it had hit its quota.
func (r *Router) MarkExhausted(id ID, d time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.find(id)
	if p == nil {
		return fmt.Errorf("%w: %q", ErrUnknownProvider, id)
	}
	p.resetAt = r.now().Add(d)
	return nil
}

// Restore clears a provider's cooldown. An empty ID restores all providers.
func (r *Router) Restore(id ID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id == "" {
		for _, p := range r.providers {
			p.resetAt = time.Time{}
		}
		return nil
	}
	p := r.find(id)
	if p == nil {
		return fmt.Errorf("%w: %q", ErrUnknownProvider, id)
	}
	p.resetAt = time.Time{}
	return nil
}

// Execute runs the request on the first available provider, failing over on
// quota errors and waiting when every provider is cooling down. An agent that
// ran out of turns returns its partial response together with ErrTurnLimit.
func (r *Router) Execute(ctx context.Context, req Request, logf Logf) (*Response, error) {
	handoff, warned := false, false
	var announced time.Time
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		p, settings, nextReset, lacking := r.pick(req)
		if p == nil {
			if nextReset.IsZero() {
				if lacking != "" {
					return nil, fmt.Errorf("%w: %s", ErrNoCapableProvider, lacking)
				}
				return nil, ErrNoProvider
			}
			if !nextReset.Equal(announced) {
				who := "Todos os providers em cooldown"
				if !r.failoverEnabled() {
					who = "Provider padrão em cooldown (failover desativado)"
				}
				logf(slog.LevelWarn, "%s. Próxima liberação às %s.", who, nextReset.Format("15:04:05"))
				announced = nextReset
			}
			wait := min(r.poll, max(time.Until(nextReset), 100*time.Millisecond))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
				continue
			}
		}

		dry := r.DryRun()
		inv := Invocation{
			CLI:      p.cli,
			Model:    settings.Model(req.Tier),
			Effort:   settings.Effort,
			System:   req.System,
			Prompt:   req.Prompt,
			WorkDir:  req.WorkDir,
			ReadOnly: req.ReadOnly,
			Diagnose: req.Diagnose,
			MaxTurns: req.MaxTurns,
			Commands: req.Commands,
			ReadDirs: req.ReadDirs,
		}
		if req.Tier == TierLight {
			if p.cli.LightModel != "" {
				inv.Model = p.cli.LightModel
			}
			inv.Effort = "low"
		}
		if handoff {
			inv.Prompt += "\n\n[HAND-OFF] Uma tentativa anterior desta tarefa foi interrompida por limite de uso. " +
				"Verifique o estado atual dos arquivos e continue de onde ele parou."
		}

		if !warned && !dry && !req.ReadOnly && r.weakerThanPrimary(p) {
			// Allowed by the policy, but never silent.
			logf(slog.LevelWarn, "%s assume a tarefa sem restringir os comandos à lista da política (isolamento: %s).", p.cli.Name, p.cli.Caps.Isolation)
			warned = true
		}

		runner := r.exec
		if dry {
			runner = r.sim
		}
		logf(slog.LevelInfo, "%s · %s (esforço %s) → %s", p.cli.Name, inv.Model, orDefault(inv.Effort), req.Role)

		start := time.Now()
		out, err := runner.Run(ctx, inv)
		text, res := out, parsed{}
		if !dry && p.cli.Parse != nil {
			if pr, ok := p.cli.Parse(out); ok {
				text, res = pr.Text, pr
			}
		}
		if err == nil && !dry && len(text) <= maxQuotaNoticeLen {
			// Some CLIs print the limit notice and still exit 0.
			if ParseQuotaError(p.cli.Name, text, r.now()) != nil {
				err = errors.New("limite de uso atingido")
			}
		}
		resp := &Response{
			Output:      text,
			Provider:    p.cli.ID,
			Name:        p.cli.Name,
			Model:       inv.Model,
			Effort:      inv.Effort,
			Simulated:   dry,
			Duration:    time.Since(start),
			Usage:       res.Usage,
			TurnLimited: res.TurnLimit,
		}
		if res.TurnLimit && ctx.Err() == nil {
			// Not retried nor failed over: another attempt would hit the same limit.
			return resp, ErrTurnLimit
		}
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if qErr := ParseQuotaError(p.cli.Name, out+"\n"+err.Error(), r.now()); qErr != nil {
			r.mu.Lock()
			p.resetAt = qErr.ResetAt
			r.mu.Unlock()
			if r.failoverEnabled() {
				logf(slog.LevelWarn, "%s. Transferindo a tarefa para o próximo provider.", qErr.Error())
			} else {
				logf(slog.LevelWarn, "%s. Failover desativado: aguardando a liberação.", qErr.Error())
			}
			handoff = true
			continue
		}
		return nil, err
	}
}

func (r *Router) failoverEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failover
}

// pick returns the first provider able to run req now, starting with the
// default one (and only it when failover is off). Providers lacking a
// capability req depends on are skipped, so failover never trades a
// restriction the request demands for a provider that cannot apply it. When
// none can run, it returns the earliest cooldown expiry (zero if waiting
// would not help) and what the skipped providers lacked.
func (r *Router) pick(req Request) (*managed, Settings, time.Time, string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	dry := r.DryRun()
	candidates := r.ordered()
	if i := slices.IndexFunc(candidates, func(p *managed) bool { return p.cli.ID == req.Prefer }); req.Prefer != "" && i > 0 {
		preferred := candidates[i]
		candidates = append([]*managed{preferred}, slices.Delete(slices.Clone(candidates), i, i+1)...)
	}
	if !r.failover {
		candidates = candidates[:min(1, len(candidates))]
	}
	var earliest time.Time
	var lacking []string
	for _, p := range candidates {
		r.refresh(p, now)
		if !dry && !p.installed {
			continue
		}
		if lack := p.cli.Caps.missing(req); lack != "" {
			lacking = append(lacking, fmt.Sprintf("%s sem %s", p.cli.Name, lack))
			continue
		}
		if p.resetAt.IsZero() {
			return p, p.settings, time.Time{}, ""
		}
		if earliest.IsZero() || p.resetAt.Before(earliest) {
			earliest = p.resetAt
		}
	}
	return nil, Settings{}, earliest, strings.Join(lacking, "; ")
}

// weakerThanPrimary reports whether p, taking over from the default
// provider, does not restrict commands as the default would.
func (r *Router) weakerThanPrimary(p *managed) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	primary := r.find(r.primary)
	return primary != nil && primary != p && primary.cli.Caps.CommandAllowlist && !p.cli.Caps.CommandAllowlist
}

// refresh expires cooldowns and re-checks the binary. Caller holds r.mu.
func (r *Router) refresh(p *managed, now time.Time) {
	if !p.resetAt.IsZero() && !now.Before(p.resetAt) {
		p.resetAt = time.Time{}
	}
	if p.checkedAt.IsZero() || now.Sub(p.checkedAt) > installCheckTTL {
		_, err := r.lookPath(p.cli.Binary)
		p.installed, p.checkedAt = err == nil, now
	}
	if now.Sub(p.catalogAt) > catalogTTL {
		p.loadCatalog(now)
	}
}

func (r *Router) find(id ID) *managed {
	for _, p := range r.providers {
		if p.cli.ID == id {
			return p
		}
	}
	return nil
}

func orDefault(effort string) string {
	if effort == "" {
		return "padrão"
	}
	return effort
}
