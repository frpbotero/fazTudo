package quality

import (
	"fmt"
	"strings"
	"testing"
)

func TestFailureSummaryKeepsFailuresAndClosingLines(t *testing.T) {
	var lines []string
	for i := range 300 {
		lines = append(lines, fmt.Sprintf("=== RUN TestOk%d", i))
	}
	lines = append(lines[:120:120], "--- FAIL: TestCart (0.01s)", "    cart_test.go:42: total = 9, want 10", "    detalhe")
	lines = append(lines, "=== RUN TestMore", "ok   other", "FAIL", "FAIL\tpkg/cart\t0.3s", "1 failed")
	got := FailureSummary(strings.Join(lines, "\n"), 50)

	for _, want := range []string{"--- FAIL: TestCart", "cart_test.go:42: total = 9, want 10", "1 failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "TestOk5\n") {
		t.Errorf("passing tests should be dropped:\n%s", got)
	}
	if n := strings.Count(got, "\n") + 1; n > 50 {
		t.Errorf("summary has %d lines, want ≤ 50", n)
	}
}

func TestFailureSummaryCapsManyFailures(t *testing.T) {
	var lines []string
	for i := range 200 {
		lines = append(lines, fmt.Sprintf("error: item %d broken", i))
	}
	lines = append(lines, "end 1", "end 2", "200 failed")
	got := FailureSummary(strings.Join(lines, "\n"), 50)
	if n := strings.Count(got, "\n") + 1; n != 50 {
		t.Errorf("summary has %d lines, want 50", n)
	}
	if !strings.HasPrefix(got, "error: item 0 broken") || !strings.HasSuffix(got, "200 failed") {
		t.Errorf("first failures and closing summary must stay:\n%s", got)
	}
}

func TestFailureSummaryFallsBackToTail(t *testing.T) {
	var lines []string
	for i := range 80 {
		lines = append(lines, fmt.Sprintf("linha %d", i))
	}
	got := FailureSummary(strings.Join(lines, "\n"), 50)
	if !strings.HasSuffix(got, "linha 79") || strings.Contains(got, "linha 29\n") {
		t.Errorf("want the last 50 lines:\n%s", got)
	}
	if short := FailureSummary("a\nb", 50); short != "a\nb" {
		t.Errorf("short output must stay whole, got %q", short)
	}
}
