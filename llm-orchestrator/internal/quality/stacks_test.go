package quality

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// fakeDocker answers `compose ls` with listing and records every `down`.
type fakeDocker struct {
	listing  string
	listErr  error
	downErrs map[string]error
	down     []string
	calls    int
}

func (f *fakeDocker) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls++
	if slices.Contains(args, "ls") {
		return []byte(f.listing), f.listErr
	}
	name := args[slices.Index(args, "-p")+1]
	f.down = append(f.down, strings.Join(args, " "))
	return nil, f.downErrs[name]
}

const stackListing = `[
	{"Name": "fastkart-e2e-aaaa", "Status": "running(5)", "ConfigFiles": "/repo/docker/docker-compose.yml,/repo/e2e/docker-compose.e2e.yml"},
	{"Name": "fastkart-e2e-bbbb", "Status": "exited(5)", "ConfigFiles": "/repo/docker/docker-compose.yml"},
	{"Name": "fastkart", "Status": "running(5)", "ConfigFiles": "/repo/docker/docker-compose.yml"},
	{"Name": "paperclip-ai", "Status": "running(2)", "ConfigFiles": "/other/docker-compose.yml"},
	{"Name": "other-e2e-cccc", "Status": "running(5)", "ConfigFiles": "/repo-sibling/docker-compose.yml"}
]`

func TestSweepRemovesOnlyMatchingStacksOfTheRepository(t *testing.T) {
	docker := &fakeDocker{listing: stackListing}

	removed, err := sweepStacks(context.Background(), docker.run, []string{"/repo"}, []string{"*-e2e-*"})

	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"fastkart-e2e-aaaa", "fastkart-e2e-bbbb"}; !slices.Equal(removed, want) {
		t.Fatalf("removed = %v, want %v", removed, want)
	}
	for _, call := range docker.down {
		if !strings.Contains(call, "down -v --remove-orphans") {
			t.Fatalf("stack volumes must go with it: %q", call)
		}
	}
}

func TestSweepAcceptsStacksFromAnyRoot(t *testing.T) {
	docker := &fakeDocker{listing: stackListing}

	removed, err := sweepStacks(context.Background(), docker.run, []string{"/repo-sibling", "/worktree"}, []string{"*-e2e-*"})

	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []string{"other-e2e-cccc"}) {
		t.Fatalf("removed = %v", removed)
	}
}

func TestSweepWithoutPatternsDoesNotCallDocker(t *testing.T) {
	docker := &fakeDocker{listing: stackListing}

	removed, err := sweepStacks(context.Background(), docker.run, []string{"/repo"}, nil)

	if err != nil || removed != nil || docker.calls != 0 {
		t.Fatalf("removed=%v err=%v calls=%d", removed, err, docker.calls)
	}
}

func TestSweepReportsDockerFailure(t *testing.T) {
	docker := &fakeDocker{listErr: errors.New("daemon parado")}

	if _, err := sweepStacks(context.Background(), docker.run, []string{"/repo"}, []string{"*-e2e-*"}); err == nil {
		t.Fatal("a failed listing must be reported")
	}
}

func TestSweepContinuesAfterOneStackFails(t *testing.T) {
	docker := &fakeDocker{listing: stackListing, downErrs: map[string]error{"fastkart-e2e-aaaa": errors.New("em uso")}}

	removed, err := sweepStacks(context.Background(), docker.run, []string{"/repo"}, []string{"*-e2e-*"})

	if err == nil || !slices.Equal(removed, []string{"fastkart-e2e-bbbb"}) {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
}

func TestSweepWithoutDockerIsANoOp(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	removed, err := SweepStacks(context.Background(), []string{"/repo"}, []string{"*-e2e-*"})

	if err != nil || removed != nil {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
}

func TestHeavyDetectsCommandsThatStartStacks(t *testing.T) {
	heavy := []Command{
		{Name: "e2e: test", Run: "cd e2e && npm run test"},
		{Name: "smoke", Run: "npx playwright test"},
		{Name: "ui", Run: "npx cypress run"},
		{Name: "stack", Run: "docker compose up -d"},
	}
	for _, c := range heavy {
		if !Heavy(c) {
			t.Errorf("%q must be heavy", c.Run)
		}
	}
	for _, c := range []Command{{Name: "unit", Run: "go test ./..."}, {Name: "front: build", Run: "cd front && npm run build"}} {
		if Heavy(c) {
			t.Errorf("%q must not be heavy", c.Run)
		}
	}
}
