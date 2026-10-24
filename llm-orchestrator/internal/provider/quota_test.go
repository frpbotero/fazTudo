package provider

import (
	"fmt"
	"testing"
	"time"
)

func TestParseQuotaError(t *testing.T) {
	now := time.Date(2026, 10, 21, 14, 0, 0, 0, time.Local)

	tests := []struct {
		name      string
		output    string
		wantNil   bool
		wantReset time.Time
	}{
		{name: "unrelated error", output: "panic: nil pointer dereference", wantNil: true},
		{name: "number 429 alone is not a quota error", output: "processed 429 files", wantNil: true},
		{name: "retry-after header", output: "HTTP 429\nRetry-After: 90", wantReset: now.Add(90 * time.Second)},
		{name: "relative compact", output: "Rate limit reached. Try again in 2h30m.", wantReset: now.Add(2*time.Hour + 30*time.Minute)},
		{name: "relative spaced words", output: "usage limit hit, resets in 1 hour 15 minutes", wantReset: now.Add(75 * time.Minute)},
		{name: "relative seconds", output: "Too Many Requests: retry after 45 seconds", wantReset: now.Add(45 * time.Second)},
		{name: "clock time pm", output: "Claude usage limit reached. Your limit resets at 4pm", wantReset: time.Date(2026, 10, 21, 16, 0, 0, 0, time.Local)},
		{name: "clock 24h tomorrow", output: "quota exceeded until 09:30", wantReset: time.Date(2026, 10, 22, 9, 30, 0, 0, time.Local)},
		{name: "no hint uses default", output: "insufficient_quota", wantReset: now.Add(DefaultCooldown)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseQuotaError("test", tt.output, now)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got reset %v", got.ResetAt)
				}
				return
			}
			if got == nil {
				t.Fatal("expected quota error, got nil")
			}
			if !got.ResetAt.Equal(tt.wantReset) {
				t.Errorf("reset = %v, want %v", got.ResetAt, tt.wantReset)
			}
		})
	}
}

func TestParseQuotaErrorClaudeFormats(t *testing.T) {
	now := time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)
	epoch := now.Add(3 * time.Hour).Unix()

	got := ParseQuotaError("c", fmt.Sprintf("Claude AI usage limit reached|%d", epoch), now)
	if got == nil || got.ResetAt.Unix() != epoch {
		t.Fatalf("epoch reset not parsed: %+v", got)
	}

	got = ParseQuotaError("c", "You've hit your limit · resets 4pm (America/Sao_Paulo)", now)
	if got == nil {
		t.Fatal("hit your limit not detected")
	}
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	want := time.Date(2026, 10, 21, 16, 0, 0, 0, loc)
	if !got.ResetAt.Equal(want) {
		t.Errorf("reset = %v, want %v", got.ResetAt, want)
	}

	got = ParseQuotaError("x", "You've reached your usage limit. Try again in 2 days 3 hours.", now)
	if got == nil || !got.ResetAt.Equal(now.Add(51*time.Hour)) {
		t.Errorf("days not parsed: %+v", got)
	}
}
