package settings

import (
	"fmt"
	"slices"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/workspace"
)

// Overrides marks values explicitly set by flags or environment variables,
// which take precedence over the file.
type Overrides struct {
	DefaultProvider bool
	Repository      bool
}

// Apply loads f into the router and workspace. Invalid parts are skipped and
// reported, so one stale entry (e.g. a deleted repository) does not discard
// the rest of the configuration.
func Apply(f File, r *provider.Router, ws *workspace.Manager, o Overrides) []error {
	var errs []error

	routing := provider.ConfigUpdate{Failover: f.Failover}
	if !o.DefaultProvider {
		routing.DefaultProvider = f.DefaultProvider
	}
	if err := r.UpdateConfig(routing); err != nil {
		errs = append(errs, fmt.Errorf("provider padrão: %w", err))
		if err := r.UpdateConfig(provider.ConfigUpdate{Failover: f.Failover}); err != nil {
			errs = append(errs, err)
		}
	}

	ids := make([]provider.ID, 0, len(f.Providers))
	for id := range f.Providers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := r.UpdateConfig(provider.ConfigUpdate{Providers: map[provider.ID]provider.Settings{id: f.Providers[id]}}); err != nil {
			errs = append(errs, fmt.Errorf("modelos de %s: %w", id, err))
		}
	}

	errs = append(errs, ws.LoadPolicies(f.Quality)...)
	if f.Repository != "" && !o.Repository {
		if _, err := ws.SetPath(f.Repository); err != nil {
			errs = append(errs, fmt.Errorf("repositório: %w", err))
		}
	}
	return errs
}

// Capture returns the current configuration as a File.
func Capture(r *provider.Router, ws *workspace.Manager) File {
	cfg := r.Config()
	failover := cfg.Failover
	return File{
		DefaultProvider: cfg.DefaultProvider,
		Failover:        &failover,
		Providers:       cfg.Providers,
		Repository:      ws.Path(),
		Quality:         ws.Policies(),
	}
}
