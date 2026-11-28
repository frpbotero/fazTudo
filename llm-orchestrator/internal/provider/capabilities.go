package provider

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Capabilities are what an adapter actually enforces or reports, as the
// orchestrator invokes the CLI. They describe this adapter, not the vendor's
// product: a flag the adapter does not pass is not a capability.
type Capabilities struct {
	// ReadOnly: a mode in which the agent cannot change files.
	ReadOnly bool `json:"read_only"`
	// Write: the agent may edit files in the working directory.
	Write bool `json:"write"`
	// CommandAllowlist: a writing agent runs only the commands it is given;
	// anything else is denied, not prompted.
	CommandAllowlist bool `json:"command_allowlist"`
	// Diagnose: a read-only agent may run the given commands. Without it a
	// diagnosis stays read-only, which is safe but cannot reproduce failures.
	Diagnose bool `json:"diagnose"`
	// StructuredOutput: the final answer and usage come as JSON.
	StructuredOutput bool `json:"structured_output"`
	// TokenUsage: the CLI reports the tokens each call used.
	TokenUsage bool `json:"token_usage"`
	// MaxTurns: the agent's turns can be bounded.
	MaxTurns bool `json:"max_turns"`
	// Cancel: stopping a run kills the CLI and every process it started.
	Cancel bool `json:"cancel"`
	// Isolation says how the restrictions are enforced.
	Isolation string `json:"isolation"`
}

// Isolation mechanisms.
const (
	IsolationPermissionRules = "regras de permissão do CLI"
	IsolationOSSandbox       = "sandbox do sistema operacional"
	IsolationApprovalMode    = "modo de aprovação do CLI"
)

// Requirements are what a run demands of the providers that serve it.
type Requirements struct {
	// StrictCommands demands that writing agents run only the listed
	// commands; a provider that cannot restrict them is refused.
	StrictCommands bool
	// MaxTurns asks for a turn limit; providers without one run unbounded.
	MaxTurns bool
}

// ErrNoCapableProvider means providers are installed, but none enforces what
// the request or the run demands.
var ErrNoCapableProvider = errors.New("nenhum provider disponível atende às exigências da tarefa")

// missing returns what caps lacks for req, or "" when it serves it.
func (c Capabilities) missing(req Request) string {
	switch {
	case req.ReadOnly && !c.ReadOnly:
		return "modo somente leitura"
	case !req.ReadOnly && !c.Write:
		return "escrita de arquivos"
	case !req.ReadOnly && req.StrictCommands && !c.CommandAllowlist:
		return "restrição de comandos à lista da política"
	}
	return ""
}

// Preflight checks, before a run starts, that the providers it may use can
// serve it: an installed provider must meet every requirement, or the run is
// refused with the reason. Warnings tell what will work in a weaker way (a
// default provider skipped, a limit the CLI cannot apply). Versions are
// detected and kept for the provider panel. It checks the real CLIs whatever
// the router's current mode: the caller skips it for a simulation.
func (r *Router) Preflight(ctx context.Context, need Requirements) ([]string, error) {
	r.detectVersions(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	candidates := r.ordered()
	if !r.failover {
		candidates = candidates[:min(1, len(candidates))]
	}
	var warnings, refused []string
	capable := 0
	for i, p := range candidates {
		r.refresh(p, now)
		if !p.installed {
			continue
		}
		write := Request{StrictCommands: need.StrictCommands}
		if lack := p.cli.Caps.missing(write); lack != "" {
			refused = append(refused, fmt.Sprintf("%s sem %s", p.cli.Name, lack))
			if i == 0 {
				warnings = append(warnings, fmt.Sprintf("%s (provider padrão) não atende à política: sem %s; as tarefas vão para outro provider.", p.cli.Name, lack))
			}
			continue
		}
		capable++
		if capable > 1 {
			// A reserve only serves on failover, and Execute warns when one
			// weaker than the primary takes a task: naming it here would
			// warn about a provider the run may never use.
			continue
		}
		if need.MaxTurns && !p.cli.Caps.MaxTurns {
			warnings = append(warnings, fmt.Sprintf("%s não aplica limite de turnos; o limite da política não vale para ele.", p.cli.Name))
		}
		if !need.StrictCommands && !p.cli.Caps.CommandAllowlist {
			warnings = append(warnings, fmt.Sprintf("%s não restringe os comandos à lista da política; o isolamento é o %s.", p.cli.Name, p.cli.Caps.Isolation))
		}
	}
	switch {
	case capable > 0:
		return warnings, nil
	case len(refused) > 0:
		return warnings, fmt.Errorf("%w: %s", ErrNoCapableProvider, strings.Join(refused, "; "))
	}
	return warnings, ErrNoProvider
}

// versionTimeout bounds `<cli> --version`.
const versionTimeout = 3 * time.Second

// detectVersions asks each installed CLI for its version once, outside the
// router's lock: a slow CLI must not hold the provider panel.
func (r *Router) detectVersions(ctx context.Context) {
	r.mu.Lock()
	var pending []*managed
	for _, p := range r.providers {
		if p.version == "" {
			pending = append(pending, p)
		}
	}
	version := r.version
	r.mu.Unlock()
	for _, p := range pending {
		if _, err := r.lookPath(p.cli.Binary); err != nil {
			continue
		}
		v := version(ctx, p.cli.Binary)
		r.mu.Lock()
		p.version = v
		r.mu.Unlock()
	}
}

// cliVersion runs `<binary> --version` and keeps its first line.
func cliVersion(ctx context.Context, binary string) string {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return "desconhecida"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return cmp.Or(truncate(strings.TrimSpace(line), 80), "desconhecida")
}
