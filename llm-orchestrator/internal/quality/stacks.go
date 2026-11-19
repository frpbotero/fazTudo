package quality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// composeProject is one entry of `docker compose ls --format json`.
type composeProject struct {
	Name        string `json:"Name"`
	ConfigFiles string `json:"ConfigFiles"`
}

// dockerFunc runs the docker CLI; tests replace it.
type dockerFunc func(ctx context.Context, args ...string) ([]byte, error)

// SweepStacks removes the Compose projects a test run left behind: their name
// matches one of patterns and a config file lives under one of roots, so the
// user's other stacks survive. Volumes go too: these stacks are disposable.
// It returns the removed names. A machine without docker has nothing to sweep.
func SweepStacks(ctx context.Context, roots, patterns []string) ([]string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, nil
	}
	return sweepStacks(ctx, dockerCLI, roots, patterns)
}

func sweepStacks(ctx context.Context, docker dockerFunc, roots, patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	listing, err := docker(ctx, "compose", "ls", "-a", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("listar stacks: %w", err)
	}
	var projects []composeProject
	if err := json.Unmarshal(listing, &projects); err != nil {
		return nil, fmt.Errorf("listar stacks: %w", err)
	}
	var removed []string
	var failures []error
	for _, p := range projects {
		if !matchesAny(p.Name, patterns) || !configuredUnder(p.ConfigFiles, roots) {
			continue
		}
		if _, err := docker(ctx, "compose", "-p", p.Name, "down", "-v", "--remove-orphans", "-t", "5"); err != nil {
			failures = append(failures, fmt.Errorf("remover %s: %w", p.Name, err))
			continue
		}
		removed = append(removed, p.Name)
	}
	return removed, errors.Join(failures...)
}

// dockerCLI runs docker away from any project: inside the repository, Compose
// would load its compose.yaml and fail on the variables only a test run sets.
func dockerCLI(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = os.TempDir()
	return cmd.Output()
}

func matchesAny(name string, patterns []string) bool {
	return slices.ContainsFunc(patterns, func(pattern string) bool {
		matched, err := path.Match(pattern, name)
		return err == nil && matched
	})
}

func configuredUnder(configFiles string, roots []string) bool {
	for _, file := range strings.Split(configFiles, ",") {
		file = realPath(strings.TrimSpace(file))
		for _, root := range roots {
			if root != "" && strings.HasPrefix(file, realPath(root)+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// realPath resolves symlinks (macOS temp dirs live behind one) and keeps the
// cleaned path for files that no longer exist.
func realPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// heavyMarkers identify commands that may start containers or a browser.
var heavyMarkers = []string{"e2e", "playwright", "cypress", "compose", "docker"}

// Heavy reports whether c is a command the machine can afford to run only one
// at a time, because it starts a whole stack.
func Heavy(c Command) bool {
	text := strings.ToLower(c.Name + " " + c.Run)
	return slices.ContainsFunc(heavyMarkers, func(marker string) bool { return strings.Contains(text, marker) })
}
