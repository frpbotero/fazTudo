package quality

import (
	"bytes"
	"regexp"
	"strings"
)

// maxEvidenceBytes bounds the failure lines kept from one command.
const maxEvidenceBytes = 32 << 10

var (
	ansiCode = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	// progressLine is a runner's live counter. Karma's carries "(1 FAILED)", so
	// it reads like a failure and would bury the test's real name.
	progressLine = regexp.MustCompile(`Executed \d+ of \d+`)
)

// cleanLine removes terminal control codes; it returns "" for lines that only
// report progress.
func cleanLine(line string) string {
	line = strings.TrimRight(ansiCode.ReplaceAllString(line, ""), " \t\r")
	if progressLine.MatchString(line) {
		return ""
	}
	return line
}

// evidenceLog keeps the failure lines of a command as they are written. The
// output tail cannot do this: a runner prints a failing test's name when it
// fails, and thousands of lines may follow before the command ends.
type evidenceLog struct {
	partial []byte
	lines   []string
	seen    map[string]bool
	size    int
	context int
}

func (e *evidenceLog) Write(p []byte) (int, error) {
	e.partial = append(e.partial, p...)
	for {
		end := bytes.IndexAny(e.partial, "\r\n")
		if end < 0 {
			return len(p), nil
		}
		e.scan(string(e.partial[:end]))
		e.partial = e.partial[end+1:]
	}
}

func (e *evidenceLog) scan(raw string) {
	line := cleanLine(raw)
	if line == "" {
		return
	}
	switch {
	case failureLine.MatchString(line):
		e.context = summaryContext
	case e.context > 0:
		e.context--
	default:
		return
	}
	if e.seen[line] || e.size+len(line) > maxEvidenceBytes {
		return
	}
	if e.seen == nil {
		e.seen = map[string]bool{}
	}
	e.seen[line] = true
	e.size += len(line) + 1
	e.lines = append(e.lines, line)
}

func (e *evidenceLog) String() string {
	if len(e.partial) > 0 {
		e.scan(string(e.partial))
		e.partial = nil
	}
	return strings.Join(e.lines, "\n")
}
