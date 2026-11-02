package api

import (
	"sync"
	"time"
)

type tokenHit struct {
	at     time.Time
	tokens int64
}

type rateLimiter struct {
	mu       sync.Mutex
	hits     map[int64][]time.Time
	tokens   map[int64][]tokenHit
	inflight map[int64]int
	maxIn    int
}

func newRateLimiter(maxInflight int) *rateLimiter {
	return &rateLimiter{hits: map[int64][]time.Time{}, tokens: map[int64][]tokenHit{}, inflight: map[int64]int{}, maxIn: maxInflight}
}

// Acquire enforces per-key RPM and in-flight limits. concurrent <= 0 falls back
// to the gateway default.
func (l *rateLimiter) Acquire(keyID int64, rpm, concurrent int) bool {
	if rpm <= 0 {
		rpm = 60
	}
	limit := concurrent
	if limit <= 0 {
		limit = l.maxIn
	}
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()
	h := l.hits[keyID]
	i := 0
	for i < len(h) && h[i].Before(cutoff) {
		i++
	}
	h = h[i:]
	if len(h) >= rpm || l.inflight[keyID] >= limit {
		l.hits[keyID] = h
		return false
	}
	l.hits[keyID] = append(h, now)
	l.inflight[keyID]++
	return true
}

func (l *rateLimiter) Release(keyID int64) {
	l.mu.Lock()
	if l.inflight[keyID] > 0 {
		l.inflight[keyID]--
	}
	l.mu.Unlock()
}

// AcquireTokens admits n tokens against the key's sliding 60s TPM window,
// recording them if admitted. tpm <= 0 means unlimited.
func (l *rateLimiter) AcquireTokens(keyID, tpm, n int64) bool {
	if tpm <= 0 {
		return true
	}
	if n < 0 {
		n = 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.pruneTokens(keyID, time.Now().Add(-time.Minute))
	if windowTokens(w)+n > tpm {
		return false
	}
	l.tokens[keyID] = append(w, tokenHit{at: time.Now(), tokens: n})
	return true
}

// AddTokens records tokens consumed after admission (e.g. actual output at
// settle) so they count against the same window.
func (l *rateLimiter) AddTokens(keyID, n int64) {
	if n <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.pruneTokens(keyID, time.Now().Add(-time.Minute))
	l.tokens[keyID] = append(w, tokenHit{at: time.Now(), tokens: n})
}

func (l *rateLimiter) pruneTokens(keyID int64, cutoff time.Time) []tokenHit {
	w := l.tokens[keyID]
	i := 0
	for i < len(w) && w[i].at.Before(cutoff) {
		i++
	}
	w = w[i:]
	if len(w) == 0 {
		delete(l.tokens, keyID)
		return nil
	}
	l.tokens[keyID] = w
	return w
}

func windowTokens(w []tokenHit) int64 {
	var sum int64
	for _, h := range w {
		sum += h.tokens
	}
	return sum
}
