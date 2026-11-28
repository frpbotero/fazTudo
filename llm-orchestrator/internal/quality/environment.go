package quality

import (
	"regexp"
	"strings"
)

// reUnreachable matches what test runners print when a service the tests
// need (a database, a cache, the Docker daemon) is not running: the code
// under test never got the chance to fail.
var reUnreachable = regexp.MustCompile(`(?i)(ECONNREFUSED|connection refused|could not connect to server|getaddrinfo (ENOTFOUND|EAI_AGAIN)|cannot connect to the docker daemon|is the docker daemon running|no such host|connect: no route to host)`)

// reEnvMessage matches a failure message that only reports the connection:
// Node wraps the attempts on ::1 and 127.0.0.1 in a bare AggregateError.
var reEnvMessage = regexp.MustCompile(`(?i)^\s*(AggregateError:?|Error: connect\b.*|.*ECONNREFUSED.*|.*connection refused.*|.*ENOTFOUND.*|.*could not connect to server.*|.*docker daemon.*)\s*$`)

// Unreachable returns the output line that shows the command failed because
// a service it needs is down, or "" when the failure may be the code's. Every
// failure must be about the connection: one real assertion among them means
// the code is in question and the failure stays with the agents.
func Unreachable(r Result) string {
	if r.Passed || r.TimedOut {
		return ""
	}
	line := unreachableLine(r.Evidence)
	if line == "" {
		line = unreachableLine(r.Output)
	}
	if line == "" {
		return ""
	}
	for _, f := range r.Failures {
		if f.Compile || (f.Message != "" && !reEnvMessage.MatchString(f.Message)) {
			return ""
		}
	}
	return line
}

func unreachableLine(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if reUnreachable.MatchString(l) {
			return clipLine(strings.TrimSpace(l), 160)
		}
	}
	return ""
}

func clipLine(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
