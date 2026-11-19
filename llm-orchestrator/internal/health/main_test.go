package health

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// testRepo is a git repository whose history a test writes commit by commit.
type testRepo struct {
	t    *testing.T
	root string
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	r := &testRepo{t: t, root: t.TempDir()}
	r.git("init", "-q")
	return r
}

func (r *testRepo) git(args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.root, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (r *testRepo) write(rel, text string) {
	r.t.Helper()
	p := filepath.Join(r.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// commit changes each file (appending a line) and commits them together.
func (r *testRepo) commit(files ...string) {
	r.t.Helper()
	for _, f := range files {
		p := filepath.Join(r.root, filepath.FromSlash(f))
		old, _ := os.ReadFile(p)
		r.write(f, string(old)+"x\n")
	}
	r.git("add", ".")
	r.git("commit", "-qm", "c")
}
