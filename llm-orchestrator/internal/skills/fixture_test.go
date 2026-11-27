package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlaybooksMatchTheirFixture pins every playbook and profile by hash. A
// change to one must be deliberate: run the evaluation suite before and after
// (go run . -eval evals/example.json, see evals/README.md), compare the
// reports, then refresh the fixture with UPDATE_GOLDEN=1.
func TestPlaybooksMatchTheirFixture(t *testing.T) {
	var b strings.Builder
	for _, name := range Names() {
		fmt.Fprintf(&b, "skill %s %s\n", name, Hash(name))
	}
	for _, name := range Profiles() {
		fmt.Fprintf(&b, "profile %s %s\n", name, ProfileHash(name))
	}
	got := b.String()
	fixture := filepath.Join("testdata", "playbooks.sha256")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(fixture, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("a playbook changed. Compare the evaluation reports before and after the change "+
			"(evals/README.md), then run UPDATE_GOLDEN=1 go test ./internal/skills.\nnow:\n%s", got)
	}
}

func TestHashesTellPlaybooksApart(t *testing.T) {
	a, b := Hash("story-readiness"), Hash("functional-acceptance")
	if a == "" || b == "" || a == b {
		t.Errorf("hashes %q and %q", a, b)
	}
	if Hash("nao-existe") != "" || ProfileHash("nao-existe") != "" || HasProfile("../go") {
		t.Error("unknown names have no hash")
	}
	if !HasProfile("go") || !HasProfile("angular-nestjs-postgres") || ProfileHash("go") == "" {
		t.Errorf("profiles = %v", Profiles())
	}
	if Version() == "" {
		t.Error("the bundle has a version")
	}
}
