// Discovery: mapping the repository once, reusing it while the files it
// depends on hold, and keeping the code graph fresh.

package pipeline

import (
	"context"
	"fmt"
	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/workspace"
	"slices"
	"strings"
	"time"
)

// discover returns the session's discovery for repo, running it only when
// there is no cached report.
func (o *Orchestrator) discover(ctx context.Context, ss *session, graph string) (*workspace.Report, bool) {
	repo := ss.repo
	fp := workspace.TakeFingerprint(repo)
	if ss.git != nil {
		fp.Commit, _ = ss.git.Head(ctx)
	}
	reason := "primeiro discovery deste repositório"
	var previous *workspace.Report
	var changes []workspace.FileChange
	if cached := o.ws.Discovery(); cached != nil && cached.Path == repo {
		if reason, changes = cached.Staleness(fp, !ss.dry); reason == "" {
			return o.reuseDiscovery(*cached, fp), true
		}
		previous = cached
		o.logf(LevelInfo, "Discovery desatualizado: %s.", reason)
	}

	stacks, err := workspace.Scan(repo)
	if err != nil {
		o.failStep(discoveryRef, 0, fmt.Errorf("varredura do repositório: %w", err))
		return nil, false
	}
	o.logf(LevelInfo, "Varredura inicial: %d stack(s) identificada(s).", len(stacks))

	origin := workspace.OriginFull
	prompt := buildDiscoveryPrompt(o.texts.discovery.Task, repo, stacks, graph, ss.instructions)
	if incrementalDiscovery(previous, changes, ss.dry) {
		origin = workspace.OriginIncremental
		prompt = buildIncrementalDiscoveryPrompt(prompt, previous.Summary, changes)
		o.logf(LevelInfo, "Atualizando o discovery só para os %d arquivo(s) que mudaram.", len(changes))
	}
	resp, ok := o.runStage(ctx, discoveryRef, provider.Request{
		Role:     DiscoveryStage.Role,
		System:   o.texts.discovery.System,
		Prompt:   prompt,
		Tier:     DiscoveryStage.Tier,
		WorkDir:  repo,
		ReadOnly: true,
	})
	if !ok {
		return nil, false
	}

	now := time.Now()
	report := workspace.Report{
		Path:        repo,
		Stacks:      stacks,
		Summary:     resp.Output,
		Provider:    resp.Name,
		Model:       resp.Model,
		Simulated:   resp.Simulated,
		CreatedAt:   now,
		Fingerprint: &fp,
		Origin:      origin,
		Reason:      reason,
		ValidatedAt: &now,
	}
	o.updateStep(discoveryRef, func(s *Step) { s.Note = discoveryNote(report) })
	o.ws.Store(report)
	o.logf(LevelSuccess, "Discovery concluído e salvo.")
	return &report, true
}

// maxIncrementalChanges is how many changed files an incremental discovery
// update takes; beyond it the repository is mapped again from scratch.
const maxIncrementalChanges = 15

// incrementalDiscovery tells whether updating the previous report for the
// changed files is safe: a real report with a fingerprint, a few changed
// files, and none removed (a removed manifest may take a whole stack away).
func incrementalDiscovery(previous *workspace.Report, changes []workspace.FileChange, dry bool) bool {
	if previous == nil || previous.Fingerprint == nil || previous.Simulated || dry || strings.TrimSpace(previous.Summary) == "" {
		return false
	}
	if len(changes) == 0 || len(changes) > maxIncrementalChanges {
		return false
	}
	return !slices.ContainsFunc(changes, func(c workspace.FileChange) bool { return c.Change == "removido" })
}

// reuseDiscovery serves the cached report, confirmed valid for fp.
func (o *Orchestrator) reuseDiscovery(cached workspace.Report, fp workspace.Fingerprint) *workspace.Report {
	now := time.Now()
	cached.ValidatedAt, cached.Fingerprint = &now, &fp
	o.ws.Store(cached)
	o.updateStep(discoveryRef, func(s *Step) {
		s.Status = StepSkipped
		s.Provider, s.Model = cached.Provider, cached.Model
		s.Output = cached.Summary
		s.Note = fmt.Sprintf("Reutilizado (%s): nenhum manifesto, lockfile, instrução ou contrato mudou", cached.CreatedAt.Format("02/01 15:04"))
	})
	o.logf(LevelInfo, "Discovery reutilizado (realizado em %s): %d arquivo(s) relevante(s) conferido(s), nenhum mudou. Refazer mapeia de novo.",
		cached.CreatedAt.Format("02/01 15:04"), len(fp.Files))
	return &cached
}

func discoveryNote(r workspace.Report) string {
	if r.Origin == workspace.OriginIncremental {
		return "Atualizado: " + r.Reason
	}
	return "Completo: " + r.Reason
}

// updateCodeGraph builds the code graph; tests replace it.
var updateCodeGraph = workspace.UpdateGraph

// updateGraph refreshes the repository's code graph, which agents query
// instead of scanning files. Without graphify the run goes on, only slower.
// Simulation leaves the repository untouched.
func (o *Orchestrator) updateGraph(ctx context.Context, ss *session) string {
	if ss.dry {
		return ""
	}
	graph, err := updateCodeGraph(ctx, ss.repo)
	if err != nil {
		o.logf(LevelWarn, "Grafo do código indisponível: %v", err)
		return ""
	}
	o.logf(LevelInfo, "Grafo do código atualizado: %s.", graph)
	return graph
}
