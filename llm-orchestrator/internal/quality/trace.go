package quality

import (
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Requirement is a test the specification requires, identified by ID.
type Requirement struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind,omitempty"` // unit | integration | e2e
}

// Trace links a required test to the test files that mention its ID and to
// the executed cases that carry it.
type Trace struct {
	Requirement
	Files []string `json:"files,omitempty"`
	// Cases are the executed cases whose name carries the ID, from the
	// commands' reports.
	Cases []CaseRef `json:"cases,omitempty"`
	// Status is what the evidence proves; see the Trace* statuses.
	Status string `json:"status,omitempty"`
	// Commit is the snapshot the cases ran on; empty without git.
	Commit string `json:"commit,omitempty"`
}

// CaseRef is an executed case linked to a requirement.
type CaseRef struct {
	Command     string   `json:"command"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Attachments []string `json:"attachments,omitempty"`
}

// Trace statuses. Only TraceProven says the requirement was shown to hold.
const (
	TraceProven   = "comprovado"         // a linked case passed and none failed
	TraceFailed   = "falhou"             // a linked case failed
	TraceSkipped  = "ignorado"           // every linked case was skipped
	TraceNotRun   = "não executado"      // a test file names it, but no reported case carries it
	TraceNoReport = "sem relatório"      // a configured report was missing or unreadable
	TraceExitOnly = "só código de saída" // no command has a report: only the commands are proven
	TraceMissing  = "sem teste"          // no test file names it
)

// Proven reports whether executed evidence shows the requirement holds.
func (t Trace) Proven() bool { return t.Status == TraceProven }

// Found reports whether some test file references the requirement.
func (t Trace) Found() bool { return len(t.Files) > 0 }

const maxRequirements = 500

var (
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reTestID  = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,9}(?:-[A-Z0-9]{1,10})*-\d{1,4}$`)
	reTesty   = regexp.MustCompile(`(?i)\btest|\bteste|\bcen[aá]rio|\bspec\b|\be2e\b|\bplaywright|\bqa\b`)
	reSepRow  = regexp.MustCompile(`^\|?[\s:|-]+\|?$`)
	// reTestCode recognises test code in files whose name does not say so
	// (e.g. scripts/verify-login.mjs run with node --test).
	reTestCode = regexp.MustCompile("node:test|@playwright/test|\\b(?:describe|it|test)\\s*\\(\\s*['\"`]|\\bfunc Test[A-Z_]|\\bdef test_|@Test\\b")
)

// ExtractRequirements reads test IDs from Markdown tables placed under
// headings about tests ("Plano de testes", "Testes unitários", …): the first
// cell of each row is the ID and the next one its description.
func ExtractRequirements(markdown string) []Requirement {
	var out []Requirement
	seen := map[string]bool{}
	inTests := false
	level := 0
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if m := reHeading.FindStringSubmatch(line); m != nil {
			l := len(m[1])
			switch {
			case reTesty.MatchString(m[2]):
				if !inTests || l <= level {
					inTests, level = true, l
				}
			case inTests && l <= level:
				inTests = false
			}
			continue
		}
		if !inTests || !strings.HasPrefix(line, "|") || reSepRow.MatchString(line) {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		id := strings.Trim(strings.TrimSpace(cells[0]), "`*")
		if !reTestID.MatchString(id) || seen[id] {
			continue
		}
		seen[id] = true
		r := Requirement{ID: id, Kind: kindOf(id)}
		if len(cells) > 1 {
			r.Description = strings.TrimSpace(strings.ReplaceAll(cells[1], "`", ""))
		}
		out = append(out, r)
		if len(out) == maxRequirements {
			break
		}
	}
	return out
}

func kindOf(id string) string {
	switch {
	case strings.HasPrefix(id, "UT-"):
		return "unit"
	case strings.HasPrefix(id, "IT-"):
		return "integration"
	case strings.HasPrefix(id, "E2E-"):
		return "e2e"
	}
	return ""
}

// MergeRequirements combines lists, keeping the first description seen.
func MergeRequirements(lists ...[]Requirement) []Requirement {
	var out []Requirement
	index := map[string]int{}
	for _, list := range lists {
		for _, r := range list {
			r.ID = strings.TrimSpace(r.ID)
			if r.ID == "" {
				continue
			}
			if i, ok := index[r.ID]; ok {
				if out[i].Description == "" {
					out[i].Description = r.Description
				}
				continue
			}
			if len(out) == maxRequirements {
				return out
			}
			index[r.ID] = len(out)
			out = append(out, r)
		}
	}
	return out
}

var testDirs = map[string]bool{
	"test": true, "tests": true, "__tests__": true, "e2e": true, "spec": true, "specs": true,
	"integration": true, "unit": true, "cypress": true, "playwright": true,
}

// IsTestFile recognises test files by directory or naming convention.
func IsTestFile(rel string) bool {
	base := strings.ToLower(path.Base(rel))
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
		strings.Contains(base, "_test.") || strings.HasPrefix(base, "test_") {
		return true
	}
	if !codeExts[ext(rel)] {
		return false
	}
	return slices.ContainsFunc(strings.Split(path.Dir(rel), "/"), func(d string) bool { return testDirs[strings.ToLower(d)] })
}

// TraceTests looks for each requirement ID in the repository's test files:
// those named as tests and any code file that contains test code.
// A test may drop the layer prefix: UT-GRID-04 also matches GRID-04.
func TraceTests(root string, reqs []Requirement) []Trace {
	traces := make([]Trace, len(reqs))
	patterns := make([]*regexp.Regexp, len(reqs))
	for i, r := range reqs {
		traces[i] = Trace{Requirement: r}
		patterns[i] = regexp.MustCompile(`(?:^|[^A-Za-z0-9-])` + idAlternatives(r.ID) + `(?:[^A-Za-z0-9]|$)`)
	}
	if len(reqs) == 0 {
		return traces
	}
	walk(root, func(rel string, _ fs.FileInfo) {
		named := IsTestFile(rel)
		if !named && !codeExts[ext(rel)] {
			return
		}
		text, ok := readFile(root, rel)
		if !ok || (!named && !reTestCode.MatchString(text)) {
			return
		}
		for i, re := range patterns {
			if re.MatchString(text) {
				traces[i].Files = append(traces[i].Files, rel)
			}
		}
	})
	return traces
}

func idAlternatives(id string) string {
	alts := []string{regexp.QuoteMeta(id)}
	if short := shortID(id); short != "" {
		alts = append(alts, regexp.QuoteMeta(short))
	}
	return "(?:" + strings.Join(alts, "|") + ")"
}

// LinkResults ties each trace to the executed cases that carry its ID and
// sets what the evidence proves. A case name may spell the ID with
// underscores (Go test functions) and follow a "Test" prefix. A trace with
// no linked case is never proven, whatever the exit codes say.
func LinkResults(traces []Trace, results []Result) []Trace {
	reported, broken := false, false
	for _, r := range results {
		switch {
		case r.Report.Read():
			reported = true
		case r.Report != nil:
			broken = true
		}
	}
	out := make([]Trace, len(traces))
	for i, t := range traces {
		t.Cases = nil
		re := regexp.MustCompile(`(?:^|[^A-Za-z0-9]|Test)` + idAlternatives(t.ID) + `(?:[^A-Za-z0-9]|$)`)
		for _, r := range results {
			for _, c := range r.Cases {
				if re.MatchString(strings.ReplaceAll(c.Name, "_", "-")) {
					t.Cases = append(t.Cases, CaseRef{Command: r.Name, Name: c.Name, Status: c.Status, Attachments: c.Attachments})
				}
			}
		}
		t.Status = traceStatus(t, reported, broken)
		out[i] = t
	}
	return out
}

func traceStatus(t Trace, reported, broken bool) string {
	has := func(status string) bool {
		return slices.ContainsFunc(t.Cases, func(c CaseRef) bool { return c.Status == status })
	}
	switch {
	case has(CaseFailed):
		return TraceFailed
	case has(CasePassed):
		return TraceProven
	case len(t.Cases) > 0:
		return TraceSkipped
	case !t.Found():
		return TraceMissing
	case broken:
		return TraceNoReport
	case reported:
		return TraceNotRun
	}
	return TraceExitOnly
}

// shortID strips a layer prefix (UT-, IT-, E2E-, TC-) when what remains is
// still a qualified ID such as GRID-04.
func shortID(id string) string {
	for _, p := range []string{"UT-", "IT-", "E2E-", "TC-"} {
		if rest, ok := strings.CutPrefix(id, p); ok && strings.Contains(rest, "-") {
			return rest
		}
	}
	return ""
}

// reCriterionID finds acceptance-criterion IDs inside test code: a qualified
// ID such as UT-CART-01, with at least one segment between prefix and number,
// so tokens like UTF-8 or SHA-256 do not count.
var reCriterionID = regexp.MustCompile(`(?:^|[^A-Za-z0-9-])([A-Z][A-Z0-9]{0,9}(?:-[A-Z0-9]{1,10})+-\d{1,4})(?:[^A-Za-z0-9-]|$)`)

// TestIDs lists, sorted, the acceptance-criterion IDs the repository's test
// files mention. A cleanup may rewrite such a test but never drop its ID.
func TestIDs(root string) []string {
	seen := map[string]bool{}
	walk(root, func(rel string, _ fs.FileInfo) {
		if !IsTestFile(rel) {
			return
		}
		text, ok := readFile(root, rel)
		if !ok {
			return
		}
		for _, m := range reCriterionID.FindAllStringSubmatch(text, -1) {
			seen[m[1]] = true
		}
	})
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// IsCodeFile reports whether rel holds source code the static checks read.
func IsCodeFile(rel string) bool { return codeExts[ext(rel)] }

// TestFiles lists, sorted, the repository's test files.
func TestFiles(root string) []string {
	var files []string
	walk(root, func(rel string, _ fs.FileInfo) {
		if IsTestFile(rel) {
			files = append(files, rel)
		}
	})
	slices.Sort(files)
	return files
}
