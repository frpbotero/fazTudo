package quality

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goJSON = `{"Action":"start","Package":"shop/cart"}
{"Action":"run","Package":"shop/cart","Test":"TestCart"}
{"Action":"run","Package":"shop/cart","Test":"TestCart/UT-CART-01_adds"}
{"Action":"pass","Package":"shop/cart","Test":"TestCart/UT-CART-01_adds","Elapsed":0.01}
{"Action":"skip","Package":"shop/cart","Test":"TestUT_CART_02","Elapsed":0}
{"Action":"fail","Package":"shop/cart","Test":"TestCart","Elapsed":0.2}
# shop/cart [build failed]
`

const junitXML = `<?xml version="1.0"?>
<testsuites>
  <testsuite name="cart" file="src/cart.spec.ts">
    <testcase name="IT-CART-03 persists" classname="cart" time="0.5"/>
    <testcase name="IT-CART-04 rejects" classname="cart"><failure message="boom"/></testcase>
    <testcase name="IT-CART-05 later"><skipped/></testcase>
  </testsuite>
</testsuites>`

const playwrightJSON = `{"suites":[{"title":"cart.spec.ts","file":"e2e/cart.spec.ts","specs":[],
 "suites":[{"title":"Carrinho","specs":[{"title":"E2E-CART-01 checkout","tests":[
   {"projectName":"chromium","status":"expected","results":[{"duration":1200,"attachments":[{"name":"screenshot","path":"test-results/a.png"}]}]},
   {"projectName":"webkit","status":"unexpected","results":[{"duration":900,"attachments":[]}]}]}]}]}]}`

func TestParseReports(t *testing.T) {
	cases, err := ParseReport(ReportGoJSON, []byte(goJSON))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range cases {
		got[c.Name] = c.Status
	}
	want := map[string]string{"TestCart": CaseFailed, "TestCart/UT-CART-01_adds": CasePassed, "TestUT_CART_02": CaseSkipped}
	for name, status := range want {
		if got[name] != status {
			t.Errorf("go %s = %q, want %q (all %v)", name, got[name], status, got)
		}
	}

	cases, err = ParseReport(ReportJUnit, []byte(junitXML))
	if err != nil || len(cases) != 3 {
		t.Fatalf("junit = %+v, %v", cases, err)
	}
	if cases[0].Status != CasePassed || cases[1].Status != CaseFailed || cases[2].Status != CaseSkipped || cases[0].File != "src/cart.spec.ts" {
		t.Errorf("junit cases = %+v", cases)
	}

	cases, err = ParseReport(ReportPlaywright, []byte(playwrightJSON))
	if err != nil || len(cases) != 2 {
		t.Fatalf("playwright = %+v, %v", cases, err)
	}
	if cases[0].Name != "Carrinho › E2E-CART-01 checkout" || cases[0].Status != CasePassed || cases[1].Status != CaseFailed {
		t.Errorf("playwright cases = %+v", cases)
	}
	if len(cases[0].Attachments) != 1 || cases[0].File != "e2e/cart.spec.ts" {
		t.Errorf("playwright attachments/file = %+v", cases[0])
	}

	for format, data := range map[string]string{ReportGoJSON: "ok shop 0.1s\n", ReportJUnit: "<testsuites/>", ReportPlaywright: "{]"} {
		if _, err := ParseReport(format, []byte(data)); err == nil {
			t.Errorf("%s accepted a report without cases: %q", format, data)
		}
	}
}

func TestRunReadsTheReportOrSaysWhyNot(t *testing.T) {
	dir := t.TempDir()
	run := func(c Command) Result { return Run(context.Background(), dir, c, 30*time.Second) }

	res := run(Command{Name: "go", Run: "cat " + shellQuote(writeTemp(t, goJSON)), Report: &ReportSpec{Format: ReportGoJSON}})
	if !res.Report.Read() || len(res.Cases) != 3 || res.Report.Failed != 1 || res.Report.Skipped != 1 {
		t.Fatalf("go-json from stdout: report %+v, cases %d", res.Report, len(res.Cases))
	}

	res = run(Command{Name: "web", Run: "mkdir -p out && cat > out/junit.xml <<'X'\n" + junitXML + "\nX", Report: &ReportSpec{Format: ReportJUnit, Path: "out/junit.xml"}})
	if !res.Report.Read() || len(res.Cases) != 3 {
		t.Fatalf("junit file: %+v (%s)", res.Report, res.Output)
	}

	// The file from the last command is older than this one: not its report.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "out/junit.xml"), old, old); err != nil {
		t.Fatal(err)
	}
	res = run(Command{Name: "web", Run: "true", Report: &ReportSpec{Format: ReportJUnit, Path: "out/junit.xml"}})
	if res.Report.Status != ReportMissing || len(res.Cases) != 0 {
		t.Errorf("stale report = %+v", res.Report)
	}
	res = run(Command{Name: "web", Run: "true", Report: &ReportSpec{Format: ReportJUnit, Path: "nada.xml"}})
	if res.Report.Status != ReportMissing {
		t.Errorf("missing report = %+v", res.Report)
	}
	res = run(Command{Name: "web", Run: "echo '<x'> bad.xml", Report: &ReportSpec{Format: ReportJUnit, Path: "bad.xml"}})
	if res.Report.Status != ReportInvalid {
		t.Errorf("unreadable report = %+v", res.Report)
	}
	res = run(Command{Name: "web", Run: "true", Report: &ReportSpec{Format: "tap"}})
	if res.Report.Status != ReportUnknown {
		t.Errorf("unknown format = %+v", res.Report)
	}
	res = run(Command{Name: "web", Run: "true", Report: &ReportSpec{Format: ReportJUnit, Path: "../fora.xml"}})
	if res.Report.Status != ReportMissing || !strings.Contains(res.Report.Detail, "fora do repositório") {
		t.Errorf("report outside the repository = %+v", res.Report)
	}
	if res := run(Command{Name: "plain", Run: "true"}); res.Report != nil {
		t.Errorf("a command without report got %+v", res.Report)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLinkResultsProvesOnlyWhatRan(t *testing.T) {
	traces := []Trace{
		{Requirement: Requirement{ID: "UT-CART-01"}, Files: []string{"cart_test.go"}},
		{Requirement: Requirement{ID: "UT-CART-02"}, Files: []string{"cart_test.go"}},
		{Requirement: Requirement{ID: "IT-CART-04"}, Files: []string{"cart.spec.ts"}},
		{Requirement: Requirement{ID: "UT-CART-09"}, Files: []string{"cart_test.go"}}, // named in a comment only
		{Requirement: Requirement{ID: "UT-CART-10"}},
	}
	goCases, _ := ParseReport(ReportGoJSON, []byte(goJSON))
	junit, _ := ParseReport(ReportJUnit, []byte(junitXML))
	results := []Result{
		{Name: "go", Passed: false, Report: &ReportOutcome{Status: ReportRead}, Cases: goCases},
		{Name: "web", Passed: false, Report: &ReportOutcome{Status: ReportRead}, Cases: junit},
	}
	want := []string{TraceProven, TraceSkipped, TraceFailed, TraceNotRun, TraceMissing}
	for i, tr := range LinkResults(traces, results) {
		if tr.Status != want[i] {
			t.Errorf("%s = %q, want %q (cases %+v)", tr.ID, tr.Status, want[i], tr.Cases)
		}
	}

	// A passing command without a report proves the command, not the case.
	plain := LinkResults(traces[:1], []Result{{Name: "go", Passed: true}})
	if plain[0].Status != TraceExitOnly || plain[0].Proven() {
		t.Errorf("exit code only = %+v", plain[0])
	}
	// A configured report that could not be read is said so.
	broken := LinkResults(traces[:1], []Result{{Name: "go", Passed: true, Report: &ReportOutcome{Status: ReportMissing}}})
	if broken[0].Status != TraceNoReport {
		t.Errorf("missing report = %+v", broken[0])
	}
}
