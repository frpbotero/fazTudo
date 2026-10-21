package provider

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultCooldown is applied when a quota error is detected but the CLI
// output carries no usable reset hint.
const DefaultCooldown = 15 * time.Minute

// QuotaError signals that a provider exhausted its rate limit or usage quota.
type QuotaError struct {
	Provider  string
	ResetAt   time.Time
	RawOutput string
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("%s: quota esgotada até %s", e.Provider, e.ResetAt.Format("15:04:05"))
}

var (
	quotaKeywords = []string{
		"rate limit", "rate_limit", "quota exceeded", "usage limit", "session limit",
		"too many requests", "http 429", "status 429", "tokens exhausted",
		"capacity exceeded", "credit balance", "insufficient_quota",
	}

	reRetryAfter = regexp.MustCompile(`(?i)retry-after:\s*(\d+)`)
	reRelative   = regexp.MustCompile(`(?i)(?:resets?|retry|try again|available)\s+(?:in|after)\s+((?:\d+\s*[a-z]+[\s,]*(?:and\s+)?)+)`)
	reDurPart    = regexp.MustCompile(`(?i)(\d+)\s*(hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)`)
	reClock      = regexp.MustCompile(`(?i)(?:until|resets?(?:\s+at)?|at)\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\b`)
)

// ParseQuotaError inspects CLI output and returns a QuotaError when it looks
// like a rate-limit failure, or nil otherwise. The reset time is derived, in
// order of preference, from a Retry-After header, a relative duration
// ("try again in 2h 30m") or a wall-clock time ("resets at 4pm").
func ParseQuotaError(provider, output string, now time.Time) *QuotaError {
	lower := strings.ToLower(output)
	matched := false
	for _, kw := range quotaKeywords {
		if strings.Contains(lower, kw) {
			matched = true
			break
		}
	}
	if !matched {
		return nil
	}

	return &QuotaError{
		Provider:  provider,
		ResetAt:   resetTime(output, now),
		RawOutput: output,
	}
}

func resetTime(output string, now time.Time) time.Time {
	if m := reRetryAfter.FindStringSubmatch(output); m != nil {
		if sec, err := strconv.Atoi(m[1]); err == nil {
			return now.Add(time.Duration(sec) * time.Second)
		}
	}
	if m := reRelative.FindStringSubmatch(output); m != nil {
		if d := parseRelative(m[1]); d > 0 {
			return now.Add(d)
		}
	}
	if m := reClock.FindStringSubmatch(output); m != nil {
		if t, ok := parseClock(m[1], m[2], m[3], now); ok {
			return t
		}
	}
	return now.Add(DefaultCooldown)
}

func parseRelative(s string) time.Duration {
	var total time.Duration
	for _, part := range reDurPart.FindAllStringSubmatch(s, -1) {
		n, err := strconv.Atoi(part[1])
		if err != nil {
			continue
		}
		switch unit := strings.ToLower(part[2]); {
		case strings.HasPrefix(unit, "h"):
			total += time.Duration(n) * time.Hour
		case strings.HasPrefix(unit, "m"):
			total += time.Duration(n) * time.Minute
		default:
			total += time.Duration(n) * time.Second
		}
	}
	return total
}

// parseClock resolves "4pm" / "16:30" to the next occurrence of that local time.
func parseClock(hourStr, minStr, ampm string, now time.Time) (time.Time, bool) {
	hour, err := strconv.Atoi(hourStr)
	if err != nil {
		return time.Time{}, false
	}
	minute := 0
	if minStr != "" {
		if minute, err = strconv.Atoi(minStr); err != nil || minute > 59 {
			return time.Time{}, false
		}
	}
	switch strings.ToLower(ampm) {
	case "am":
		if hour == 12 {
			hour = 0
		}
	case "pm":
		if hour < 12 {
			hour += 12
		}
	default:
		// A bare number without minutes or am/pm is too ambiguous ("at 3 places").
		if minStr == "" {
			return time.Time{}, false
		}
	}
	if hour > 23 {
		return time.Time{}, false
	}

	t := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !t.After(now) {
		t = t.Add(24 * time.Hour)
	}
	return t, true
}
