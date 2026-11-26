package quality

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ReportSpec tells how to read a command's per-test results: the report
// format and, for formats written to a file, where the file is.
type ReportSpec struct {
	Format string `json:"format"` // ReportGoJSON, ReportJUnit or ReportPlaywright
	// Path is relative to the repository root. Go test JSON may leave it
	// empty: the command's own output is the report.
	Path string `json:"path,omitempty"`
}

// Report formats.
const (
	ReportGoJSON     = "go-json"
	ReportJUnit      = "junit"
	ReportPlaywright = "playwright-json"
)

// ReportFormats are the formats with a parser.
var ReportFormats = []string{ReportGoJSON, ReportJUnit, ReportPlaywright}

// Case is one test case as a report recorded it.
type Case struct {
	Name       string `json:"name"`
	Suite      string `json:"suite,omitempty"`
	File       string `json:"file,omitempty"`
	Status     string `json:"status"` // CasePassed, CaseFailed or CaseSkipped
	DurationMS int64  `json:"duration_ms,omitempty"`
	// Attachments are the files the runner kept for the case: screenshots,
	// traces, videos.
	Attachments []string `json:"attachments,omitempty"`
}

// Case statuses.
const (
	CasePassed  = "passed"
	CaseFailed  = "failed"
	CaseSkipped = "skipped"
)

// ReportOutcome says what came of reading a command's report. Only a report
// read in full gives cases; any other status proves the command alone.
type ReportOutcome struct {
	Format string `json:"format"`
	Path   string `json:"path,omitempty"`
	Status string `json:"status"` // ReportRead, ReportMissing, ReportInvalid or ReportUnknown
	Detail string `json:"detail,omitempty"`
	// Passed, Failed and Skipped count the cases of a report read in full.
	Passed  int `json:"passed,omitempty"`
	Failed  int `json:"failed,omitempty"`
	Skipped int `json:"skipped,omitempty"`
}

// Report statuses.
const (
	ReportRead    = "lido"
	ReportMissing = "ausente"
	ReportInvalid = "inválido"
	ReportUnknown = "formato desconhecido"
)

// Read reports whether the report gave its cases.
func (r *ReportOutcome) Read() bool { return r != nil && r.Status == ReportRead }

// maxReportBytes bounds a report read from a file or from the output.
const maxReportBytes = 32 << 20

// readReport fills res with the cases of c's report. A report file older
// than the command's start is someone else's, never this run's.
func readReport(dir string, c Command, res *Result, started time.Time, stdout *capBuffer) {
	spec := c.Report
	if spec == nil || strings.TrimSpace(spec.Format) == "" {
		return
	}
	out := &ReportOutcome{Format: spec.Format, Path: spec.Path}
	res.Report = out
	if !slices.Contains(ReportFormats, spec.Format) {
		out.Status, out.Detail = ReportUnknown, "formatos aceitos: "+strings.Join(ReportFormats, ", ")
		return
	}
	var data []byte
	switch {
	case spec.Path != "":
		var err error
		if data, err = readReportFile(dir, spec.Path, started); err != nil {
			out.Status, out.Detail = ReportMissing, err.Error()
			return
		}
	case spec.Format != ReportGoJSON:
		out.Status, out.Detail = ReportMissing, "formato gravado em arquivo: informe o caminho"
		return
	case stdout == nil || stdout.overflow:
		out.Status, out.Detail = ReportInvalid, fmt.Sprintf("saída acima de %d MB", maxReportBytes>>20)
		return
	default:
		data = stdout.Bytes()
	}
	cases, err := ParseReport(spec.Format, data)
	if err != nil {
		out.Status, out.Detail = ReportInvalid, err.Error()
		return
	}
	out.Status = ReportRead
	res.Cases = cases
	for _, c := range cases {
		switch c.Status {
		case CasePassed:
			out.Passed++
		case CaseFailed:
			out.Failed++
		default:
			out.Skipped++
		}
	}
}

// WithoutCases drops the cases from results meant to be kept or shown: the
// report's counts stay, and the cases that matter live in the traces.
func WithoutCases(results []Result) []Result {
	out := slices.Clone(results)
	for i := range out {
		out[i].Cases = nil
	}
	return out
}

func readReportFile(dir, rel string, started time.Time) ([]byte, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("caminho fora do repositório")
	}
	p := filepath.Join(dir, clean)
	info, err := os.Stat(p)
	switch {
	case err != nil:
		return nil, errors.New("arquivo não encontrado")
	case info.Size() > maxReportBytes:
		return nil, fmt.Errorf("arquivo acima de %d MB", maxReportBytes>>20)
	case info.ModTime().Before(started.Add(-time.Second)):
		// File systems keep mtimes in coarse units: allow one second.
		return nil, errors.New("arquivo anterior a esta execução do comando")
	}
	return os.ReadFile(p)
}

// ParseReport reads the cases of a report in the given format. A report
// with no case at all is invalid: it proves nothing about any criterion.
func ParseReport(format string, data []byte) ([]Case, error) {
	var cases []Case
	var err error
	switch format {
	case ReportGoJSON:
		cases, err = parseGoJSON(data)
	case ReportJUnit:
		cases, err = parseJUnit(data)
	case ReportPlaywright:
		cases, err = parsePlaywright(data)
	default:
		return nil, fmt.Errorf("formato %q desconhecido", format)
	}
	if err == nil && len(cases) == 0 {
		err = errors.New("nenhum caso de teste no relatório")
	}
	return cases, err
}

// goEvent is one line of `go test -json`.
type goEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
}

// parseGoJSON keeps the last verdict of every test; lines that are not
// events (a build error printed by the shell) are ignored.
func parseGoJSON(data []byte) ([]Case, error) {
	index := map[string]int{}
	var cases []Case
	events := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev goEvent
		if json.Unmarshal(line, &ev) != nil || ev.Action == "" {
			continue
		}
		events++
		status := map[string]string{"pass": CasePassed, "fail": CaseFailed, "skip": CaseSkipped}[ev.Action]
		if ev.Test == "" || status == "" {
			continue
		}
		key := ev.Package + "\x00" + ev.Test
		c := Case{Name: ev.Test, Suite: ev.Package, Status: status, DurationMS: int64(ev.Elapsed * 1000)}
		if i, ok := index[key]; ok {
			cases[i] = c
			continue
		}
		index[key] = len(cases)
		cases = append(cases, c)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if events == 0 {
		return nil, errors.New("nenhum evento de `go test -json`")
	}
	return cases, nil
}

// junitSuite reads both a <testsuites> root and a lone <testsuite>.
type junitSuite struct {
	Name   string       `xml:"name,attr"`
	File   string       `xml:"file,attr"`
	Suites []junitSuite `xml:"testsuite"`
	Cases  []junitCase  `xml:"testcase"`
}

type junitCase struct {
	Name      string    `xml:"name,attr"`
	ClassName string    `xml:"classname,attr"`
	File      string    `xml:"file,attr"`
	Time      string    `xml:"time,attr"`
	Failures  []xmlNode `xml:"failure"`
	Errors    []xmlNode `xml:"error"`
	Skipped   []xmlNode `xml:"skipped"`
}

type xmlNode struct {
	Message string `xml:"message,attr"`
}

func parseJUnit(data []byte) ([]Case, error) {
	var root junitSuite
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("XML JUnit ilegível: %w", err)
	}
	var cases []Case
	var walk func(s junitSuite, file string)
	walk = func(s junitSuite, file string) {
		if s.File != "" {
			file = s.File
		}
		for _, tc := range s.Cases {
			c := Case{Name: tc.Name, Suite: cmpOr(tc.ClassName, s.Name), File: cmpOr(tc.File, file), Status: CasePassed}
			switch {
			case len(tc.Failures) > 0 || len(tc.Errors) > 0:
				c.Status = CaseFailed
			case len(tc.Skipped) > 0:
				c.Status = CaseSkipped
			}
			if secs, err := strconv.ParseFloat(tc.Time, 64); err == nil {
				c.DurationMS = int64(secs * 1000)
			}
			cases = append(cases, c)
		}
		for _, child := range s.Suites {
			walk(child, file)
		}
	}
	walk(root, "")
	return cases, nil
}

// Playwright's JSON reporter nests suites (files, describes) holding specs,
// each run as one test per project.
type pwReport struct {
	Suites []pwSuite `json:"suites"`
}

type pwSuite struct {
	Title  string    `json:"title"`
	File   string    `json:"file"`
	Specs  []pwSpec  `json:"specs"`
	Suites []pwSuite `json:"suites"`
}

type pwSpec struct {
	Title string   `json:"title"`
	File  string   `json:"file"`
	Tests []pwTest `json:"tests"`
}

type pwTest struct {
	ProjectName string     `json:"projectName"`
	Status      string     `json:"status"` // expected | unexpected | flaky | skipped
	Results     []pwResult `json:"results"`
}

type pwResult struct {
	Duration    int64          `json:"duration"`
	Attachments []pwAttachment `json:"attachments"`
}

type pwAttachment struct {
	Path string `json:"path"`
}

func parsePlaywright(data []byte) ([]Case, error) {
	var r pwReport
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("JSON do Playwright ilegível: %w", err)
	}
	var cases []Case
	var walk func(s pwSuite, titles []string, file string)
	walk = func(s pwSuite, titles []string, file string) {
		if s.File != "" {
			file = s.File
		}
		// A file's suite is titled with the file's path: not a describe.
		if s.Title != "" && !strings.HasSuffix(file, s.Title) {
			titles = append(slices.Clone(titles), s.Title)
		}
		for _, sp := range s.Specs {
			name := strings.Join(append(slices.Clone(titles), sp.Title), " › ")
			for _, t := range sp.Tests {
				c := Case{Name: name, Suite: t.ProjectName, File: cmpOr(sp.File, file)}
				switch t.Status {
				case "expected", "flaky":
					c.Status = CasePassed
				case "skipped":
					c.Status = CaseSkipped
				default:
					c.Status = CaseFailed
				}
				for _, res := range t.Results {
					c.DurationMS += res.Duration
					for _, a := range res.Attachments {
						if a.Path != "" {
							c.Attachments = append(c.Attachments, a.Path)
						}
					}
				}
				cases = append(cases, c)
			}
		}
		for _, child := range s.Suites {
			walk(child, titles, file)
		}
	}
	for _, s := range r.Suites {
		walk(s, nil, "")
	}
	return cases, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// capBuffer keeps a command's whole output up to maxReportBytes, for a
// report printed on stdout.
type capBuffer struct {
	bytes.Buffer
	overflow bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if c.overflow || c.Len()+len(p) > maxReportBytes {
		c.overflow = true
		return len(p), nil
	}
	return c.Buffer.Write(p)
}

var _ io.Writer = (*capBuffer)(nil)
