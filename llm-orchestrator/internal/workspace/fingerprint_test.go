package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func put(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFingerprintCoversWhatADiscoveryDependsOn(t *testing.T) {
	root := t.TempDir()
	for rel, want := range map[string]bool{
		"package.json":                    true,
		"api/go.mod":                      true,
		"api/go.sum":                      true,
		"web/pnpm-lock.yaml":              true,
		"AGENTS.md":                       true,
		".specify/memory/constitution.md": true,
		"api/openapi.yaml":                true,
		"proto/user.proto":                true,
		"docs/adr/0001-banco.md":          true,
		"requirements-dev.txt":            true,
		"src/main.go":                     false,
		"docs/guia.md":                    false,
		"node_modules/x/package.json":     false,
	} {
		put(t, root, rel, rel)
		if got := relevantFile(rel); got != want && rel != "node_modules/x/package.json" {
			t.Errorf("relevantFile(%s) = %v", rel, got)
		}
	}
	fp := TakeFingerprint(root)
	if _, ok := fp.Files["node_modules/x/package.json"]; ok {
		t.Error("dependency folders are skipped")
	}
	if _, ok := fp.Files["src/main.go"]; ok {
		t.Error("code files do not invalidate a discovery")
	}
	if len(fp.Files) != 10 {
		t.Errorf("files = %v", fp.Files)
	}
}

func TestChangesAndStaleness(t *testing.T) {
	root := t.TempDir()
	put(t, root, "package.json", `{"name":"a"}`)
	put(t, root, "go.mod", "module a")
	put(t, root, "src/app.ts", "1")
	before := TakeFingerprint(root)
	report := Report{Path: root, Fingerprint: &before}

	put(t, root, "src/app.ts", "2") // code only
	if reason, _ := report.Staleness(TakeFingerprint(root), true); reason != "" {
		t.Errorf("a code change keeps the discovery: %q", reason)
	}

	put(t, root, "package.json", `{"name":"b"}`)
	put(t, root, "AGENTS.md", "regras")
	os.Remove(filepath.Join(root, "go.mod"))
	reason, changes := report.Staleness(TakeFingerprint(root), true)
	if reason == "" || len(changes) != 3 {
		t.Fatalf("reason %q, changes %+v", reason, changes)
	}
	want := map[string]string{"AGENTS.md": "novo", "go.mod": "removido", "package.json": "alterado"}
	for _, c := range changes {
		if want[c.Path] != c.Change {
			t.Errorf("%s = %s, want %s", c.Path, c.Change, want[c.Path])
		}
	}

	if reason, _ := (Report{}).Staleness(before, true); reason == "" {
		t.Error("a report without fingerprint is never valid")
	}
	sim := Report{Fingerprint: &before, Simulated: true}
	if reason, _ := sim.Staleness(before, true); reason == "" {
		t.Error("a simulated report must not serve a real run")
	}
	if reason, _ := sim.Staleness(before, false); reason != "" {
		t.Errorf("it serves another simulation: %q", reason)
	}
}
