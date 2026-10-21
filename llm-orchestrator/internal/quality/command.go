// Package quality holds the deterministic checks the pipeline uses to accept
// or reject an agent's work: project commands (tests, lint, build), static
// readability and syntax checks, test traceability and input-file guards.
package quality

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// Command is a shell command that must exit with status 0.
type Command struct {
	Name string `json:"name"`
	Run  string `json:"run"`
}

// Result is the outcome of one check.
type Result struct {
	Name       string `json:"name"`
	Command    string `json:"command,omitempty"`
	Passed     bool   `json:"passed"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Output     string `json:"output,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// maxOutputBytes is how much of a command's output is kept: the tail, where
// test runners print failures and summaries.
const maxOutputBytes = 8 << 10

// Run executes c through the shell inside dir. The whole process group is
// killed on timeout or cancellation, so test runners do not leave browsers or
// dev servers behind.
func Run(ctx context.Context, dir string, c Command, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", c.Run)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CI=1", "NO_COLOR=1", "FORCE_COLOR=0")
	cmd.WaitDelay = 5 * time.Second
	isolate(cmd)
	out := &tailBuffer{max: maxOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = out

	started := time.Now()
	err := cmd.Run()
	res := Result{
		Name:       c.Name,
		Command:    c.Run,
		Passed:     err == nil,
		Output:     strings.TrimSpace(out.String()),
		DurationMS: time.Since(started).Milliseconds(),
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.TimedOut, res.ExitCode = true, -1
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		res.ExitCode = -1
		res.Output = strings.TrimSpace(res.Output + "\n" + err.Error())
	}
	return res
}

// tailBuffer keeps only the last max bytes written to it.
type tailBuffer struct {
	max       int
	buf       []byte
	truncated bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = t.buf[over:]
		t.truncated = true
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	b := t.buf
	for len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	if t.truncated {
		return "[… início da saída omitido]\n" + string(b)
	}
	return string(b)
}
