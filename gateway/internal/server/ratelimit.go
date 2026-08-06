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
// maxLimiterBuckets is the hard size bound. When even a sweep cannot get
// under it — an address-distributed flood arriving faster than buckets
// idle out — new keys are refused rather than allocated. Fail closed:
// the alternative is the map growing until the container is OOM-killed,
// which turns a rate-limit evasion into a whole-service restart.
const maxLimiterBuckets = 1 << 16

type limiter struct {
	mu        sync.Mutex
	burst     float64
	perSec    float64
	buckets   map[string]*bucketState
	lastSweep time.Time
	now       func() time.Time
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
		if len(l.buckets) >= 4096 {
			l.sweepLocked(now)
		}
		if len(l.buckets) >= maxLimiterBuckets {
			return false, time.Second
		}
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
// missing one. At most once a second: under a distinct-key flood every
// insert would otherwise pay an O(n) scan that frees nothing.
func (l *limiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < time.Second {
		return
	}
	l.lastSweep = now
	full := time.Duration(l.burst / l.perSec * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.last) > full {
			delete(l.buckets, k)
		}
	}
}
