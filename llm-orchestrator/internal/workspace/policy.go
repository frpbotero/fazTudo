package workspace

import (
	"errors"
	"fmt"
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
)

// DefaultPolicy is used for repositories without saved settings.
func DefaultPolicy() Policy {
	return Policy{
		AutoChecks:    true,
		Checks:        []quality.Command{},
		Protected:     slices.Clone(quality.DefaultProtected),
		Git:           true,
		MaxLineLength: 160,
		CheckTimeoutS: 600,
		Candidates:    1,
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
	case !p.AutoChecks && len(p.Checks) == 0:
		return errors.New("informe ao menos um comando ou use os detectados automaticamente")
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
