package quality

import (
	"regexp"
	"strings"
)

// failureLine matches what test runners, compilers and linters print where
// something broke: failing test names, error messages, assertions and the
// file:line frames of a stack trace.
var failureLine = regexp.MustCompile(`(?i)(\bfail|\berror\b|error:|panic|exception|traceback|assert|expected|received|` +
	`✕|✗|×|●|not ok|\.[a-z]{1,5}:\d+)`)

// summaryContext is how many lines after a match are kept: the message or
// the first frames under a failing test name.
const summaryContext = 2

// readableLines drops terminal control codes, progress counters and immediate
// repetitions.
func readableLines(output string) []string {
	var lines []string
	for _, raw := range strings.FieldsFunc(strings.TrimSpace(output), func(r rune) bool { return r == '\n' || r == '\r' }) {
		line := cleanLine(raw)
		if line != "" && (len(lines) == 0 || lines[len(lines)-1] != line) {
			lines = append(lines, line)
		}
	}
	return lines
}

// FailureSummary reduces a command's output to its failures: the matching
// lines with a little context and the runner's closing summary, at most
// maxLines. Without any recognisable failure it falls back to the tail.
func FailureSummary(output string, maxLines int) string {
	lines := readableLines(output)
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}
	tail := 3 // runners end with "N passed, M failed"
	maxLines = max(maxLines, tail+2)
	keep := make([]bool, len(lines))
	matched := false
	for i, l := range lines[:len(lines)-tail] {
		if failureLine.MatchString(l) {
			matched = true
			for j := i; j <= i+summaryContext && j < len(lines); j++ {
				keep[j] = true
			}
		}
	}
	if !matched {
		return "[… início omitido]\n" + strings.Join(lines[len(lines)-maxLines:], "\n")
	}
	for i := len(lines) - tail; i < len(lines); i++ {
		keep[i] = true
	}

	var out []string
	skipped := false
	for i, l := range lines {
		if !keep[i] {
			skipped = true
			continue
		}
		if skipped && len(out) > 0 {
			out = append(out, "[…]")
		}
		skipped = false
		out = append(out, l)
	}
	if len(out) > maxLines {
		// Keep the first failures and the closing summary.
		out = append(append(out[:maxLines-tail-1:maxLines-tail-1], "[…]"), out[len(out)-tail:]...)
	}
	return strings.Join(out, "\n")
}
