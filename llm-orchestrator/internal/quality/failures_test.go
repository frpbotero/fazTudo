package quality

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// karmaOutput is the shape of `front: test` in US-PRC-02: the failing test is
// named once, then hundreds of progress lines push it out of any tail.
func karmaOutput() string {
	var b strings.Builder
	b.WriteString("\x1b[1A\x1b[2KChrome Headless 156.0.0.0 (Mac OS 10.15.7) PriceTableComponent deve carregar a tabela FAILED\n")
	b.WriteString("\tError: NG0205: Injector has already been destroyed.\n")
	b.WriteString("\t    at R3Injector.assertNotDestroyed (node_modules/@angular/core/fesm2022/core.mjs:6:1)\n")
	for i := range 600 {
		fmt.Fprintf(&b, "\x1b[1A\x1b[2KChrome Headless 156.0.0.0 (Mac OS 10.15.7): Executed %d of 610 (1 FAILED) (0 secs / 0.66 secs)\n", i)
	}
	b.WriteString("TOTAL: 1 FAILED, 609 SUCCESS\n")
	return b.String()
}

func TestEvidenceKeepsTheFailingTestPastTheOutputTail(t *testing.T) {
	r := Run(context.Background(), t.TempDir(), Command{Name: "front: test", Run: "cat <<'E'\n" + karmaOutput() + "E\nexit 1"}, 10*time.Second)

	if len(r.Failures) != 1 {
		t.Fatalf("failures = %+v", r.Failures)
	}
	f := r.Failures[0]
	if f.Test != "PriceTableComponent deve carregar a tabela" {
		t.Fatalf("test = %q", f.Test)
	}
	if !strings.Contains(f.Message, "Injector has already been destroyed") {
		t.Fatalf("message = %q", f.Message)
	}
	if strings.Contains(r.Evidence, "Executed") {
		t.Fatal("progress lines are not evidence")
	}
}

func TestFailureSummaryDropsProgressLines(t *testing.T) {
	got := FailureSummary(karmaOutput(), 50)
	if !strings.Contains(got, "PriceTableComponent deve carregar a tabela FAILED") {
		t.Fatalf("summary lost the test name:\n%s", got)
	}
}

func TestSameCompileErrorHasOneFingerprintAcrossCommands(t *testing.T) {
	const message = "src/api/price-list/price-resolver.service.ts:42:18 - error TS2769: No overload matches this call."
	build := ParseFailures(Command{Name: "core-api: build", Run: "cd core-api && npm run build"}, message)
	docker := ParseFailures(Command{Name: "e2e: test", Run: "cd e2e && npm run test"}, "#23 58.22 "+message)

	if len(build) != 1 || len(docker) != 1 {
		t.Fatalf("build=%+v docker=%+v", build, docker)
	}
	if build[0].Fingerprint != docker[0].Fingerprint {
		t.Fatal("one compile error must be one cause")
	}
	if !build[0].Compile || build[0].Line != 42 || build[0].File != "src/api/price-list/price-resolver.service.ts" {
		t.Fatalf("failure = %+v", build[0])
	}
}

func TestFingerprintSurvivesMovedCode(t *testing.T) {
	first := ParseFailures(Command{Name: "b"}, "src/a.ts:42:18 - error TS2345: Argument of type 'x' at 10 is wrong.")
	moved := ParseFailures(Command{Name: "b"}, "src/a.ts:97:18 - error TS2345: Argument of type 'x' at 12 is wrong.")
	other := ParseFailures(Command{Name: "b"}, "src/b.ts:42:18 - error TS2345: Argument of type 'x' at 10 is wrong.")

	if first[0].Fingerprint != moved[0].Fingerprint {
		t.Error("line numbers must not change the identity")
	}
	if first[0].Fingerprint == other[0].Fingerprint {
		t.Error("another file is another failure")
	}
}

func TestParseFailuresReadsTheCommonRunners(t *testing.T) {
	cases := []struct {
		name, output string
		want         Failure
	}{
		{"jest", "FAIL test/us-prc-01-price-visibility.e2e-spec.ts (32.723 s)\n  ● US-PRC-01 visibilidade › oculta preço\n\n    expected 200, received 403\n",
			Failure{File: "test/us-prc-01-price-visibility.e2e-spec.ts", Test: "US-PRC-01 visibilidade › oculta preço", Message: "expected 200, received 403"}},
		{"playwright", "  1) [chromium] › tests/cart.spec.ts:12:5 › E2E-CART-01 adiciona item\n",
			Failure{File: "tests/cart.spec.ts", Line: 12, Test: "E2E-CART-01 adiciona item"}},
		{"go", "--- FAIL: TestCart (0.01s)\n    cart_test.go:42: total = 9, want 10\n", Failure{Test: "TestCart", Message: "cart_test.go:42: total = 9, want 10"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found := ParseFailures(Command{Name: tc.name}, tc.output)
			if len(found) == 0 {
				t.Fatal("no failure found")
			}
			got := found[0]
			if got.File != tc.want.File || got.Test != tc.want.Test || got.Line != tc.want.Line || (tc.want.Message != "" && got.Message != tc.want.Message) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRunnerRepeatingAFailureCountsOnce(t *testing.T) {
	output := "FAIL a.spec.ts\n  ● salva\n\n    boom\n\nSummary of all failing tests\nFAIL a.spec.ts\n  ● salva\n\n    boom\n"
	if found := ParseFailures(Command{Name: "j"}, output); len(found) != 1 {
		t.Fatalf("found = %+v", found)
	}
}

func TestUnreadableOutputStillIdentifiesTheCommand(t *testing.T) {
	one := ParseFailures(Command{Name: "lint"}, "algo quebrou em src/x.css:3")
	other := ParseFailures(Command{Name: "build"}, "outra coisa quebrou")

	if len(one) != 1 || one[0].File != "src/x.css" || one[0].Line != 3 || one[0].Fingerprint == other[0].Fingerprint {
		t.Fatalf("one=%+v other=%+v", one, other)
	}
}

func TestCommandDir(t *testing.T) {
	if got := CommandDir(Command{Run: "cd core-api && npm run build"}); got != "core-api" {
		t.Errorf("dir = %q", got)
	}
	if got := CommandDir(Command{Run: "npm test"}); got != "" {
		t.Errorf("dir = %q", got)
	}
}

func TestRepairCommandsLetAgentsReproduceAFailure(t *testing.T) {
	checks := []Command{
		{Name: "front: test", Run: "cd front && npm run test"},
		{Name: "unit", Run: "go test ./..."},
		{Name: "e2e: test", Run: "cd e2e && npm run test"},
	}

	got := RepairCommands(checks)

	for _, want := range []string{
		"cd front && npm run test", "go test ./...", "go test", "npx jest", "npx tsc", "CI=1 npm run test",
		"cd front && CI=1 npm run test", "cd front", "tail", "grep",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if slices.Contains(got, "cd e2e && npm run test") || slices.Contains(got, "cd e2e && CI=1 npm run test") {
		t.Errorf("the full E2E run is the orchestrator's: %v", got)
	}
	if !slices.Contains(got, "cd e2e && npx jest") {
		t.Errorf("narrow runs stay reachable: %v", got)
	}
}

func TestRepairCommandsLeaveFullStackRunsToTheOrchestrator(t *testing.T) {
	got := RepairCommands([]Command{{Name: "stack", Run: "docker compose up -d"}})

	if slices.Contains(got, "docker compose up -d") {
		t.Fatalf("a command that starts a stack must not be listed: %v", got)
	}
}
