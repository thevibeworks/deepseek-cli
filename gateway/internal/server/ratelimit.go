package server

import (
	"sync"
	"time"
)

// limiter is a per-key token bucket.
//
// It sits in front of the quota ledger rather than replacing it. Quota is
// the day's fairness policy and costs a disk write to enforce; this is
// the cheap reflex that stops a runaway loop from reaching that code at
// all. Neither one protects the budget — that is the breaker's job — so
// this can stay as simple as it looks.
type limiter struct {
	mu      sync.Mutex
	burst   float64
	perSec  float64
	buckets map[string]*bucketState
	now     func() time.Time
}

type bucketState struct {
	tokens float64
	last   time.Time
}

func newLimiter(n int, window time.Duration) *limiter {
	if n <= 0 {
		n = 1
	}
	return &limiter{
		burst:   float64(n),
		perSec:  float64(n) / window.Seconds(),
		buckets: map[string]*bucketState{},
		now:     time.Now,
	}
}

// Allow takes one token, reporting whether there was one and how long
// until there will be.
func (l *limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		l.sweepLocked(now)
		b = &bucketState{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	b.tokens += now.Sub(b.last).Seconds() * l.perSec
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) / l.perSec * float64(time.Second))
		return false, wait
	}
	b.tokens--
	return true, 0
}

// sweepLocked drops buckets that have been idle long enough to have
// refilled completely, since a full bucket is indistinguishable from a
// missing one.
func (l *limiter) sweepLocked(now time.Time) {
	if len(l.buckets) < 4096 {
		return
	}
	full := time.Duration(l.burst / l.perSec * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.last) > full {
			delete(l.buckets, k)
		}
	}
}
