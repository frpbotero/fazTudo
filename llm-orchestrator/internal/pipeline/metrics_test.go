package pipeline

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"llm-orchestrator/internal/provider"
)

func TestEveryRecordCarriesTheRunsIDAndTheStorysUsage(t *testing.T) {
	exec := &fakeExecutor{usage: &provider.Usage{InputTokens: 100, OutputTokens: 10}}
	o, _ := newTest(t, exec)
	usageLog := filepath.Join(t.TempDir(), "tokens.jsonl")
	o.SetUsageLog(usageLog)
	_ = o.Start(one(false))
	run := waitDone(t, o)
	if run.ID == "" {
		t.Fatal("the run has no id")
	}
	a := step(run.Stories[0].Steps, "a")
	if len(a.Calls) != 1 || a.Calls[0].RunID != run.ID || a.Calls[0].Story != "US-1" || a.Calls[0].Step != "a" || a.Calls[0].Attempt != 1 {
		t.Errorf("call record = %+v", a.Calls)
	}
	data, _ := os.ReadFile(usageLog)
	if !strings.Contains(string(data), `"run_id":"`+run.ID+`"`) {
		t.Errorf("the token log does not carry the run id:\n%s", data)
	}
	if u := run.Stories[0].Usage; u == nil || u.Calls != 3 || u.Tokens() != 330 {
		t.Errorf("story usage = %+v", u)
	}
	if run.Usage.Calls != 4 { // the discovery is the run's, not the story's
		t.Errorf("run usage = %+v", run.Usage)
	}
	m := run.Metrics()[0]
	if m.Calls != 3 || m.Tokens != 330 || m.Story != "US-1" || m.AgentMS < 0 {
		t.Errorf("metrics = %+v", m)
	}
}

func TestReviewTimeIsLoggedByHandOnly(t *testing.T) {
	o, _ := newTest(t, &fakeExecutor{})
	_ = o.Start(one(false))
	waitDone(t, o)
	if err := o.LogReview(0, HumanReview{Minutes: 25, ReworkMinutes: 10, Note: "ajustei o token ghp_abcdefghijklmnopqrstuvwxyz0123"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []HumanReview{{}, {Minutes: -1}, {Minutes: 5000}, {Minutes: 1, Note: strings.Repeat("x", 501)}} {
		if err := o.LogReview(0, bad); !errors.Is(err, ErrBadReviewLog) {
			t.Errorf("%+v = %v", bad, err)
		}
	}
	if err := o.LogReview(7, HumanReview{Minutes: 1}); !errors.Is(err, ErrBadReviewLog) {
		t.Errorf("unknown story = %v", err)
	}
	run := o.Snapshot()
	h := run.Stories[0].HumanReview
	if len(h) != 1 || strings.Contains(h[0].Note, "ghp_") {
		t.Fatalf("review log = %+v", h)
	}
	if m := run.Metrics()[0]; m.ReviewMinutes != 25 || m.ReworkMinutes != 10 {
		t.Errorf("metrics = %+v", m)
	}
}

func TestLogsAreRedactedAndBounded(t *testing.T) {
	o, _ := newTest(t, &fakeExecutor{})
	policy := o.ws.Policy()
	policy.LogLimit = 100
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	o.logf(LevelInfo, "push com https://user:segredo123@github.com/acme/x.git")
	for i := range 150 {
		o.logf(LevelInfo, "linha %d", i)
	}
	logs := o.Snapshot().Logs
	if len(logs) != 100 || logs[len(logs)-1].Message != "linha 149" {
		t.Errorf("kept %d logs, last %q", len(logs), logs[len(logs)-1].Message)
	}
	o.logf(LevelWarn, "token=abc123 em https://user:segredo123@host/x")
	last := o.Snapshot().Logs
	if msg := last[len(last)-1].Message; strings.Contains(msg, "segredo123") || strings.Contains(msg, "abc123") {
		t.Errorf("log kept a secret: %q", msg)
	}
}

func TestDeliveryPackageHasTheMetrics(t *testing.T) {
	o, _, _ := newDeliveryTest(t)
	if err := o.Start(one(true)); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	data, err := os.ReadFile(filepath.Join(run.Delivery.Dir, "metrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		RunID   string         `json:"run_id"`
		Stories []StoryMetrics `json:"stories"`
	}
	if err := json.Unmarshal(data, &m); err != nil || m.RunID != run.ID || len(m.Stories) != 1 {
		t.Errorf("metrics.json = %s (%v)", data, err)
	}
	index, _ := os.ReadFile(filepath.Join(run.Delivery.Dir, "README.md"))
	if !strings.Contains(string(index), "## Métricas") {
		t.Error("the index lacks the metrics")
	}
}
