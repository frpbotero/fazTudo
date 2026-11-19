package pipeline

import (
	"slices"
	"strings"
	"testing"

	"llm-orchestrator/internal/quality"
)

func TestCompareSnapshots(t *testing.T) {
	pass := func(name string) CheckSnapshot { return CheckSnapshot{Name: name, Passed: true} }
	fail := func(name string, fps ...string) CheckSnapshot { return CheckSnapshot{Name: name, Fingerprints: fps} }
	timeout := func(name string) CheckSnapshot {
		return CheckSnapshot{Name: name, TimedOut: true, Fingerprints: []string{"t"}}
	}

	cases := []struct {
		name          string
		before, after []CheckSnapshot
		want          string // substring of the only reason; "" = approved
	}{
		{"still passing", []CheckSnapshot{pass("unit")}, []CheckSnapshot{pass("unit")}, ""},
		{"starts failing", []CheckSnapshot{pass("unit")}, []CheckSnapshot{fail("unit", "a")}, "unit passou a falhar"},
		{"starts timing out", []CheckSnapshot{pass("e2e")}, []CheckSnapshot{timeout("e2e")}, "e2e passou a estourar o tempo"},
		{"same old failure", []CheckSnapshot{fail("unit", "a")}, []CheckSnapshot{fail("unit", "a")}, ""},
		{"old failure fixed", []CheckSnapshot{fail("unit", "a", "b")}, []CheckSnapshot{fail("unit", "a")}, ""},
		{"new failure beside old", []CheckSnapshot{fail("unit", "a")}, []CheckSnapshot{fail("unit", "a", "c")}, "1 falha(s) nova(s)"},
		{"failing to timing out", []CheckSnapshot{fail("unit", "a")}, []CheckSnapshot{timeout("unit")}, "passou a estourar o tempo"},
		{"new command failing", nil, []CheckSnapshot{fail("lint", "x")}, "lint passou a falhar"},
	}
	for _, c := range cases {
		got := compareSnapshots(c.before, c.after)
		switch {
		case c.want == "" && len(got) > 0:
			t.Errorf("%s: want approved, got %v", c.name, got)
		case c.want != "" && (len(got) != 1 || !strings.Contains(got[0], c.want)):
			t.Errorf("%s: got %v, want one reason with %q", c.name, got, c.want)
		}
	}
}

func TestCleanupReasonsCoverAreaAndTestIDs(t *testing.T) {
	changed := []string{"internal/pay/a.go", "internal/payments/b.go", "cmd/main.go"}
	got := cleanupReasons("internal/pay", changed, []string{"UT-PAY-01", "UT-PAY-02"}, []string{"UT-PAY-01"}, nil, nil)
	if len(got) != 2 || !strings.Contains(got[0], "internal/payments/b.go, cmd/main.go") || !strings.Contains(got[1], "UT-PAY-02") {
		t.Errorf("reasons = %v", got)
	}
	if got := cleanupReasons(".", changed, nil, nil, nil, nil); len(got) != 0 {
		t.Errorf("the whole repository allows every file: %v", got)
	}
}

func TestSnapshotOfIdentifiesUnparsedFailures(t *testing.T) {
	c := quality.Command{Name: "lint", Run: "make lint"}
	s := snapshotOf(c, quality.Result{Name: "lint", Passed: false, ExitCode: 2})
	if s.Passed || len(s.Fingerprints) != 1 {
		t.Fatalf("snapshot = %+v", s)
	}
	parsed := snapshotOf(c, quality.Result{Failures: []quality.Failure{{Fingerprint: "f1"}, {Fingerprint: "f1"}, {Fingerprint: "f2"}}})
	if !slices.Equal(parsed.Fingerprints, []string{"f1", "f2"}) {
		t.Errorf("fingerprints = %v", parsed.Fingerprints)
	}
}
