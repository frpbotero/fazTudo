package quality

import (
	"strings"
	"testing"
)

func TestUnreachable(t *testing.T) {
	nodeDown := "  ● Backups (e2e) › status\n\n    AggregateError:\n      at internalConnectMultiple (node:net:1139:18)\n    connect ECONNREFUSED 127.0.0.1:5432\n"
	for _, tc := range []struct {
		name string
		r    Result
		want string
	}{
		{"postgres down for node", Result{Output: nodeDown, Failures: []Failure{{Test: "Backups", Message: "AggregateError:"}, {Test: "Auth", Message: "AggregateError"}}}, "connect ECONNREFUSED 127.0.0.1:5432"},
		{"evidence wins over a lost tail", Result{Evidence: "dial tcp 127.0.0.1:6379: connect: connection refused", Output: "FAIL"}, "dial tcp 127.0.0.1:6379: connect: connection refused"},
		{"docker down", Result{Output: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"}, "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"},
		{"psql", Result{Output: "psql: error: could not connect to server: No such file or directory"}, "psql: error: could not connect to server: No such file or directory"},
		{"a real assertion among them", Result{Output: nodeDown, Failures: []Failure{{Message: "AggregateError:"}, {Message: "expected 404, got 200"}}}, ""},
		{"compile error", Result{Output: nodeDown, Failures: []Failure{{Compile: true, Message: "TS2304"}}}, ""},
		{"plain assertion", Result{Output: "expected 2 to equal 3", Failures: []Failure{{Message: "expected 2 to equal 3"}}}, ""},
		{"timed out is reported apart", Result{TimedOut: true, Output: nodeDown}, ""},
		{"passed", Result{Passed: true, Output: nodeDown}, ""},
	} {
		if got := Unreachable(tc.r); got != tc.want {
			t.Errorf("%s: Unreachable = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestUnreachableClipsALongLine(t *testing.T) {
	got := Unreachable(Result{Output: "ECONNREFUSED " + strings.Repeat("x", 400)})
	if n := len([]rune(got)); n != 161 || !strings.HasSuffix(got, "…") {
		t.Errorf("clipped to %d runes: %q", n, got)
	}
}
