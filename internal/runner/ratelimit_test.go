package runner

import (
	"testing"
	"time"
)

func TestClaudeRateLimitIsClassified(t *testing.T) {
	_, p := fixtureEvents(t, "ratelimit.jsonl")
	r := p.result
	if r == nil || r.Status != StatusFailed || !r.RateLimited {
		t.Fatalf("result = %+v", r)
	}
	if r.RetryAfter <= time.Hour {
		t.Errorf("retry after = %v, want the vendor's far-off reset", r.RetryAfter)
	}
}

func TestCodexRateLimitIsClassified(t *testing.T) {
	_, p := codexFixtureEvents(t, "codex-ratelimit.jsonl")
	if r := p.result; r == nil || r.Status != StatusFailed || !r.RateLimited {
		t.Fatalf("result = %+v", r)
	}
	if _, q := codexFixtureEvents(t, "codex-failed.jsonl"); q.result.RateLimited {
		t.Errorf("a dropped stream classified as a rate limit")
	}
}

func TestOrdinaryFailuresAreNotRateLimits(t *testing.T) {
	_, p := fixtureEvents(t, "failed.jsonl")
	if p.result.RateLimited {
		t.Errorf("error_max_turns classified as a rate limit: %+v", p.result)
	}
	if hit, _ := rateLimitText("stream disconnected before completion"); hit {
		t.Error("a dropped stream is not a rate limit")
	}
}
