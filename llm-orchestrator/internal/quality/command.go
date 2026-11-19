// Package quality holds the deterministic checks the pipeline uses to accept
// or reject an agent's work: project commands (tests, lint, build), static
// readability and syntax checks, test traceability and input-file guards.
package quality

import (
	"context"
	"errors"
	"io"
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
	// Evidence holds the failure lines of the whole output, which Output, a
	// tail, may have lost. Failures is what they identify.
	Evidence string    `json:"-"`
	Failures []Failure `json:"failures,omitempty"`
}

// maxOutputBytes is how much of a command's output is kept: the tail, where
// test runners print failures and summaries.
const maxOutputBytes = 8 << 10

// stopGrace is how long a canceled command has to run its teardown; removing
// a Docker stack takes tens of seconds.
const stopGrace = 60 * time.Second

// pipeGrace is how long Wait outlasts the group's kill before closing the
// output pipes that an escaped process may still hold.
const pipeGrace = 5 * time.Second

// Run executes c through the shell inside dir. On timeout or cancellation the
// process group gets stopGrace to clean up and is then killed, so test runners
// leave neither Docker stacks nor browsers or dev servers behind.
func Run(ctx context.Context, dir string, c Command, timeout time.Duration) Result {
	return run(ctx, dir, c, timeout, stopGrace)
}

func run(ctx context.Context, dir string, c Command, timeout, grace time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", c.Run)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CI=1", "NO_COLOR=1", "FORCE_COLOR=0")
	// Teardowns write to the same pipes: Wait must not close them early.
	cmd.WaitDelay = grace + pipeGrace
	finish := isolate(cmd, grace)
	defer finish()
	out := &tailBuffer{max: maxOutputBytes}
	evidence := &evidenceLog{}
	output := io.MultiWriter(out, evidence)
	cmd.Stdout = output
	cmd.Stderr = output

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
	if !res.Passed && !res.TimedOut {
		res.Evidence = evidence.String()
		res.Failures = ParseFailures(c, res.Evidence)
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
