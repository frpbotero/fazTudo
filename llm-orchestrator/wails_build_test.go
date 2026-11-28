package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// wailsBuildSandbox copies scripts/wails-build.sh and wails.json into a
// temporary project with a fake wails that records the wails.json it saw and
// its arguments. It returns the project dir and the fake's path.
func wailsBuildSandbox(t *testing.T, fail bool) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("script de build roda em shells Unix")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"scripts/wails-build.sh", "wails.json"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exit := "0"
	if fail {
		exit = "3"
	}
	fake := filepath.Join(dir, "fake-wails")
	script := "#!/bin/sh\ncp wails.json seen.json\necho \"$@\" > args.txt\nexit " + exit + "\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, fake
}

func runWailsBuild(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("bash", append([]string{filepath.Join(dir, "scripts", "wails-build.sh")}, args...)...)
	return cmd.CombinedOutput()
}

func TestWailsBuildStampsTheVersionAndRestoresTheConfig(t *testing.T) {
	dir, fake := wailsBuildSandbox(t, false)
	before, _ := os.ReadFile(filepath.Join(dir, "wails.json"))

	if out, err := runWailsBuild(dir, "0.3.1", fake, "-clean", "-platform", "darwin/universal"); err != nil {
		t.Fatalf("wails-build.sh: %v\n%s", err, out)
	}

	seen, err := os.ReadFile(filepath.Join(dir, "seen.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seen), `"productVersion": "0.3.1"`) {
		t.Fatalf("wails saw no productVersion 0.3.1:\n%s", seen)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
	if got := strings.TrimSpace(string(args)); got != "build -clean -platform darwin/universal" {
		t.Fatalf("wails args = %q", got)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "wails.json"))
	if string(after) != string(before) {
		t.Fatalf("wails.json not restored:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(dir, "wails.json.orig")); !os.IsNotExist(err) {
		t.Fatalf("backup left behind: %v", err)
	}
}

func TestWailsBuildRestoresTheConfigWhenTheBuildFails(t *testing.T) {
	dir, fake := wailsBuildSandbox(t, true)
	before, _ := os.ReadFile(filepath.Join(dir, "wails.json"))

	if _, err := runWailsBuild(dir, "0.3.1", fake); err == nil {
		t.Fatal("a failed wails build must fail the script")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "wails.json"))
	if string(after) != string(before) {
		t.Fatalf("wails.json not restored after a failed build:\n%s", after)
	}
}

func TestWailsBuildRefusesAVersionThatIsNotSemver(t *testing.T) {
	dir, fake := wailsBuildSandbox(t, false)
	for _, v := range []string{"dev", "v0.3.1", "0.3", `0.3.1"x`} {
		out, err := runWailsBuild(dir, v, fake)
		if err == nil || !strings.Contains(string(out), "versão inválida") {
			t.Fatalf("version %q: err=%v out=%s", v, err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "seen.json")); !os.IsNotExist(err) {
		t.Fatal("wails ran with an invalid version")
	}
	if out, err := runWailsBuild(dir, "0.0.0-abc1234", fake); err != nil {
		t.Fatalf("prerelease version refused: %v\n%s", err, out)
	}
}
