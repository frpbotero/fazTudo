package quality

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Failure is one thing that broke in a command, as precise as its output
// allows. Fingerprint identifies it across commands and fix rounds: the same
// compile error seen by a build and by a test runner has one fingerprint.
type Failure struct {
	Check       string `json:"check"`
	Dir         string `json:"dir,omitempty"`
	Test        string `json:"test,omitempty"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Message     string `json:"message,omitempty"`
	Fingerprint string `json:"fingerprint"`
	// Compile marks an error found before any test ran: it has a position
	// and needs no investigation.
	Compile bool `json:"compile,omitempty"`
}

var (
	reCommandDir = regexp.MustCompile(`^\s*cd\s+([^\s&;|]+)\s*&&`)
	// BuildKit prefixes every line of a build step with "#23 58.22 ".
	reBuildPrefix = regexp.MustCompile(`^#\d+ \d+(\.\d+)? `)
	reCompile     = regexp.MustCompile(`(\S+\.[cm]?[jt]sx?):(\d+):\d+ - error (TS\d+): (.+)$`)
	reJestSuite   = regexp.MustCompile(`^\s*FAIL\s+(\S+)`)
	reJestTest    = regexp.MustCompile(`^\s*●\s+(.+)$`)
	reKarmaTest   = regexp.MustCompile(`\) (.+) FAILED$`)
	rePlaywright  = regexp.MustCompile(`^\s*\d+\)\s+(?:\[\S+\]\s+›\s+)?(\S+\.[jt]s):(\d+):\d+\s+›\s+(.+)$`)
	reGoTest      = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	reGoLine      = regexp.MustCompile(`^\s+(\S+\.go):(\d+): (.+)$`)
	reAnyPosition = regexp.MustCompile(`([\w./@-]+\.[a-z]{1,5}):(\d+)`)
	reDigits      = regexp.MustCompile(`\d+`)
	reSpaces      = regexp.MustCompile(`\s+`)
)

// CommandDir returns the directory a `cd dir && …` command runs in, or "".
func CommandDir(c Command) string {
	if m := reCommandDir.FindStringSubmatch(c.Run); m != nil {
		return m[1]
	}
	return ""
}

// ParseFailures extracts what broke from a command's evidence. Output it
// cannot read still yields one failure, identified by the command itself.
func ParseFailures(c Command, evidence string) []Failure {
	lines := strings.Split(evidence, "\n")
	for i, l := range lines {
		lines[i] = reBuildPrefix.ReplaceAllString(l, "")
	}
	found := parseCompile(lines)
	found = append(found, parseTests(lines)...)
	if len(found) == 0 {
		found = []Failure{genericFailure(c, lines)}
	}
	return withIdentity(c, found)
}

func parseCompile(lines []string) []Failure {
	var found []Failure
	for _, l := range lines {
		if m := reCompile.FindStringSubmatch(l); m != nil {
			found = append(found, Failure{File: m[1], Line: atoi(m[2]), Message: m[3] + ": " + m[4], Compile: true})
		}
	}
	return found
}

func parseTests(lines []string) []Failure {
	var found []Failure
	suite := ""
	for i, l := range lines {
		if m := reJestSuite.FindStringSubmatch(l); m != nil {
			suite = m[1]
			continue
		}
		switch f, ok := parseTestLine(l); {
		case ok:
			if f.File == "" {
				f.File = suite
			}
			if f.Message == "" {
				f.Message = nextMessage(lines[i+1:])
			}
			found = append(found, f)
		}
	}
	return found
}

func parseTestLine(l string) (Failure, bool) {
	if m := rePlaywright.FindStringSubmatch(l); m != nil {
		return Failure{File: m[1], Line: atoi(m[2]), Test: strings.TrimSpace(m[3])}, true
	}
	if m := reJestTest.FindStringSubmatch(l); m != nil {
		return Failure{Test: strings.TrimSpace(m[1])}, true
	}
	if m := reKarmaTest.FindStringSubmatch(l); m != nil {
		return Failure{Test: strings.TrimSpace(m[1])}, true
	}
	if m := reGoTest.FindStringSubmatch(l); m != nil {
		return Failure{Test: m[1]}, true
	}
	if m := reGoLine.FindStringSubmatch(l); m != nil {
		return Failure{File: m[1], Line: atoi(m[2]), Message: m[3]}, true
	}
	return Failure{}, false
}

// nextMessage is the first line under a failing test that is not a stack frame.
func nextMessage(lines []string) string {
	for _, l := range lines[:min(len(lines), summaryContext+2)] {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "at ") {
			return l
		}
	}
	return ""
}

func genericFailure(c Command, lines []string) Failure {
	f := Failure{Test: c.Name}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if f.Message == "" {
			f.Message = l
		}
		if m := reAnyPosition.FindStringSubmatch(l); m != nil {
			f.File, f.Line = m[1], atoi(m[2])
			break
		}
	}
	return f
}

// withIdentity fills what every failure of c shares and drops repetitions:
// runners print a failing test again in their closing summary.
func withIdentity(c Command, found []Failure) []Failure {
	var out []Failure
	seen := map[string]bool{}
	for _, f := range found {
		f.Check, f.Dir = c.Name, CommandDir(c)
		f.Fingerprint = fingerprint(f)
		if !seen[f.Fingerprint] {
			seen[f.Fingerprint] = true
			out = append(out, f)
		}
	}
	return out
}

// fingerprint ignores positions and durations, which change while the failure
// stays the same. A test is identified by its name; its message may change as
// a fix makes progress.
func fingerprint(f Failure) string {
	key := []string{f.File, f.Test}
	if f.Test == "" {
		key = append(key, normalizeMessage(f.Message))
	}
	sum := sha1.Sum([]byte(strings.Join(key, "\x00")))
	return hex.EncodeToString(sum[:4])
}

func normalizeMessage(s string) string {
	s = reDigits.ReplaceAllString(strings.ToLower(s), "#")
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// Fingerprints lists the distinct fingerprints of failures.
func Fingerprints(failures []Failure) []string {
	var out []string
	for _, f := range failures {
		if !slices.Contains(out, f.Fingerprint) {
			out = append(out, f.Fingerprint)
		}
	}
	return out
}
