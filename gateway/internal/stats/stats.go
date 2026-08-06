// Package stats is what the dashboard reads: live throughput, who is
// active, which endpoints are busy, and where the traffic comes from.
//
// It exists separately from package quota because the two answer
// different questions and must not be confused. Quota is money and it is
// durable — every debit is journalled and survives a restart. This is
// observability and it is deliberately ephemeral: everything here lives
// in a ring buffer in memory, is bounded, and is lost on restart. Losing
// it costs a graph; losing quota state costs the credit pool.
//
// The privacy line is drawn inside this package, not above it. `deepseek
// free` promises no IP addresses are recorded, so no function here
// accepts one. Geography arrives already reduced to a two-letter country
// code by the edge, and is counted into an aggregate histogram that
// cannot be joined back to a subject — a country total is a fact about
// the service, not about a person.
package stats

import (
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"
)

// windowSec is how far back the live figures look. Five minutes is long
// enough that one slow request does not make the graph jump and short
// enough that "live" is not a lie.
const windowSec = 300

// liveWindow is how recently a subject must have sent something to count
// as here now.
const liveWindow = 5 * time.Minute

// maxTracked bounds the per-subject last-seen map. Subjects are cheap to
// mint, so this is a memory bound, not a policy: past it, new subjects
// are simply not counted as live until a sweep frees room.
const maxTracked = 20000

// bucket is one second of traffic.
type bucket struct {
	sec      int64 // unix second this bucket represents, 0 when unused
	requests int64
	input    int64
	output   int64
}

// Collector accumulates the live view.
type Collector struct {
	mu sync.Mutex

	ring [windowSec]bucket

	endpoints map[string]int64
	countries map[string]int64
	lastSeen  map[string]int64 // subject -> unix seconds

	// Since-boot totals. The durable lifetime figures come from the
	// ledger's journals; these are just what this process has seen.
	bootRequests int64
	bootInput    int64
	bootOutput   int64

	inFlight int64
	started  time.Time

	now func() time.Time
}

func New() *Collector {
	return &Collector{
		endpoints: map[string]int64{},
		countries: map[string]int64{},
		lastSeen:  map[string]int64{},
		started:   time.Now(),
		now:       time.Now,
	}
}

// SetClock replaces the time source, for tests.
func (c *Collector) SetClock(now func() time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

// Seen records that a subject sent a request from a country. The country
// is a two-letter code from the edge, or "" when unknown; no address of
// any kind reaches this package.
func (c *Collector) Seen(subject, country, endpoint string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().Unix()

	if endpoint != "" {
		c.endpoints[endpoint]++
	}
	if country != "" && len(country) == 2 && len(c.countries) < 512 {
		c.countries[country]++
	}
	if subject != "" {
		if _, known := c.lastSeen[subject]; known || len(c.lastSeen) < maxTracked {
			c.lastSeen[subject] = now
		} else {
			c.sweepLocked(now)
			if len(c.lastSeen) < maxTracked {
				c.lastSeen[subject] = now
			}
		}
	}
}

// Charged records a completed request's measured tokens. It is called
// from the same place the ledger is charged, so the graph and the money
// cannot drift apart.
func (c *Collector) Charged(input, output int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().Unix()
	b := c.bucketLocked(now)
	b.requests++
	b.input += int64(input)
	b.output += int64(output)

	c.bootRequests++
	c.bootInput += int64(input)
	c.bootOutput += int64(output)
}

// bucketLocked returns the ring slot for a second, clearing it first if
// it still holds an older second's traffic.
func (c *Collector) bucketLocked(sec int64) *bucket {
	b := &c.ring[sec%windowSec]
	if b.sec != sec {
		*b = bucket{sec: sec}
	}
	return b
}

// InFlight adjusts the count of requests currently being proxied.
func (c *Collector) InFlight(delta int) {
	c.mu.Lock()
	c.inFlight += int64(delta)
	if c.inFlight < 0 {
		c.inFlight = 0
	}
	c.mu.Unlock()
}

func (c *Collector) sweepLocked(now int64) {
	cutoff := now - int64(liveWindow.Seconds())
	for sub, at := range c.lastSeen {
		if at < cutoff {
			delete(c.lastSeen, sub)
		}
	}
}

// Count is one row of a histogram.
type Count struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// Live is the moment-in-time view.
type Live struct {
	Subjects5m     int     `json:"subjects_5m"`
	InFlight       int64   `json:"in_flight"`
	TokensPerSec   float64 `json:"tokens_per_sec"`
	RequestsPerMin float64 `json:"requests_per_min"`
	// Series is per-second output tokens over the window, oldest first,
	// for the sparkline. It is the raw material of the graph rather than
	// a rendered one, so the page can draw it however it likes.
	Series []int64 `json:"series"`
}

// System is the box this runs on.
type System struct {
	UptimeSec   int64   `json:"uptime_sec"`
	Load1       float64 `json:"load1"`
	Goroutines  int     `json:"goroutines"`
	HeapMB      float64 `json:"heap_mb"`
	NumCPU      int     `json:"num_cpu"`
	GoVersion   string  `json:"go_version"`
	BootRequest int64   `json:"requests_since_boot"`
	BootInput   int64   `json:"input_tokens_since_boot"`
	BootOutput  int64   `json:"output_tokens_since_boot"`
}

// Snapshot is everything the dashboard needs from this package.
type Snapshot struct {
	Live      Live    `json:"live"`
	Endpoints []Count `json:"endpoints"`
	Countries []Count `json:"countries"`
	System    System  `json:"system"`
}

// Snapshot reads the live view. Cheap enough to call per request, but the
// server caches it anyway because the dashboard polls.
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now().Unix()
	c.sweepLocked(now)

	// Walk the window oldest to newest so the series is in drawing order.
	// The current second is excluded: it is still filling, and a partial
	// second rendered as a full one makes every graph end in a dip.
	series := make([]int64, 0, windowSec-1)
	var reqs, toks int64
	for i := windowSec - 1; i >= 1; i-- {
		sec := now - int64(i)
		b := c.ring[sec%windowSec]
		if b.sec != sec {
			series = append(series, 0)
			continue
		}
		series = append(series, b.output)
		reqs += b.requests
		toks += b.input + b.output
	}

	elapsed := float64(windowSec - 1)
	return Snapshot{
		Live: Live{
			Subjects5m:     len(c.lastSeen),
			InFlight:       c.inFlight,
			TokensPerSec:   round2(float64(toks) / elapsed),
			RequestsPerMin: round2(float64(reqs) / elapsed * 60),
			Series:         series,
		},
		Endpoints: topLocked(c.endpoints, 10),
		Countries: topLocked(c.countries, 12),
		System: System{
			UptimeSec:   int64(c.now().Sub(c.started).Seconds()),
			Load1:       loadAvg1(),
			Goroutines:  runtime.NumGoroutine(),
			HeapMB:      heapMB(),
			NumCPU:      runtime.NumCPU(),
			GoVersion:   runtime.Version(),
			BootRequest: c.bootRequests,
			BootInput:   c.bootInput,
			BootOutput:  c.bootOutput,
		},
	}
}

// topLocked sorts a histogram by count, then by name so that equal counts
// do not shuffle between polls and make the dashboard twitch.
func topLocked(m map[string]int64, n int) []Count {
	out := make([]Count, 0, len(m))
	for k, v := range m {
		out = append(out, Count{Name: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

func heapMB() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return round2(float64(m.HeapAlloc) / (1 << 20))
}

// loadAvg1 reads the one-minute load average. Linux-only by construction:
// this is the only place in the gateway that touches /proc, and a box
// without it simply reports zero rather than the package growing a
// per-platform abstraction for one number on a status page.
func loadAvg1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	for i := 0; i < len(b); i++ {
		if b[i] == ' ' {
			f, err := strconv.ParseFloat(string(b[:i]), 64)
			if err != nil {
				return 0
			}
			return f
		}
	}
	return 0
}
