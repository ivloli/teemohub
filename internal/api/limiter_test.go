package api

import (
	"testing"
	"time"
)

func TestLimiterRPMAndConcurrency(t *testing.T) {
	l := newRateLimiter(2)
	if !l.Acquire(1, 10, 0) || !l.Acquire(1, 10, 0) {
		t.Fatal("first two requests should be admitted")
	}
	if l.Acquire(1, 10, 0) {
		t.Fatal("default concurrency cap should reject the third in-flight request")
	}
	l.Release(1)
	l.Release(1)
	if !l.Acquire(1, 10, 0) {
		t.Fatal("slot should be reusable after release")
	}
	l.Release(1)

	// RPM counts every admitted hit, including completed ones.
	if !l.Acquire(3, 2, 0) || !l.Acquire(3, 2, 0) {
		t.Fatal("rpm 2 should admit two hits")
	}
	l.Release(3)
	l.Release(3)
	if l.Acquire(3, 2, 0) {
		t.Fatal("rpm cap should reject the third hit in the minute")
	}

	// A per-key concurrency override replaces the gateway default.
	if !l.Acquire(2, 10, 1) {
		t.Fatal("first request on key 2")
	}
	if l.Acquire(2, 10, 1) {
		t.Fatal("per-key concurrency 1 should reject the second in-flight request")
	}
}

func TestLimiterTPMWindow(t *testing.T) {
	l := newRateLimiter(4)
	if !l.AcquireTokens(1, 100, 60) {
		t.Fatal("first 60 tokens of 100")
	}
	if l.AcquireTokens(1, 100, 50) {
		t.Fatal("110 > 100 should be rejected")
	}
	if !l.AcquireTokens(1, 100, 40) {
		t.Fatal("exactly 100 should be admitted")
	}
	l.AddTokens(1, 30) // settle output, window now 130
	if l.AcquireTokens(1, 100, 1) {
		t.Fatal("window should include settled output tokens")
	}
	if !l.AcquireTokens(2, 0, 1<<40) {
		t.Fatal("tpm=0 means unlimited")
	}

	// Entries older than 60s are pruned.
	l.mu.Lock()
	l.tokens[3] = []tokenHit{{at: time.Now().Add(-2 * time.Minute), tokens: 1000}}
	l.mu.Unlock()
	if !l.AcquireTokens(3, 100, 10) {
		t.Fatal("expired window should free tokens")
	}
}
