package stats

import (
	"sync"
	"testing"
	"time"
)

func atClock(t time.Time) (*Collector, *time.Time) {
	c := New()
	now := t
	c.SetClock(func() time.Time { return now })
	return c, &now
}

func TestThroughputOverTheWindow(t *testing.T) {
	c, now := atClock(time.Unix(1_700_000_000, 0))

	// Ten seconds of steady traffic: 100 output tokens a second.
	for i := 0; i < 10; i++ {
		c.Charged(50, 100)
		*now = now.Add(time.Second)
	}

	got := c.Snapshot().Live
	// 10 requests x 150 tokens over the 299-second window.
	want := 1500.0 / 299.0
	if diff := got.TokensPerSec - want; diff > 0.02 || diff < -0.02 {
		t.Errorf("tokens_per_sec = %v, want about %v", got.TokensPerSec, want)
	}
	if got.RequestsPerMin <= 0 {
		t.Error("requests_per_min did not move after ten requests")
	}
	if len(got.Series) != windowSec-1 {
		t.Fatalf("series has %d points, want %d", len(got.Series), windowSec-1)
	}
	var sum int64
	for _, v := range got.Series {
		sum += v
	}
	if sum != 1000 {
		t.Errorf("series totals %d output tokens, want 1000", sum)
	}
}

// A bucket older than the window must not be counted again when the ring
// wraps onto its slot.
func TestOldTrafficLeavesTheWindow(t *testing.T) {
	c, now := atClock(time.Unix(1_700_000_000, 0))
	c.Charged(1000, 1000)

	*now = now.Add(time.Duration(windowSec+5) * time.Second)
	got := c.Snapshot().Live
	if got.TokensPerSec != 0 {
		t.Errorf("tokens_per_sec = %v after the window passed, want 0", got.TokensPerSec)
	}
	for i, v := range got.Series {
		if v != 0 {
			t.Fatalf("series[%d] = %d, want 0 — stale ring slot was read as current", i, v)
		}
	}
}

func TestLiveSubjectsExpire(t *testing.T) {
	c, now := atClock(time.Unix(1_700_000_000, 0))
	c.Seen("alice", "US", "chat")
	c.Seen("bob", "DE", "chat")

	if got := c.Snapshot().Live.Subjects5m; got != 2 {
		t.Fatalf("subjects_5m = %d, want 2", got)
	}
	*now = now.Add(6 * time.Minute)
	if got := c.Snapshot().Live.Subjects5m; got != 0 {
		t.Errorf("subjects_5m = %d six minutes later, want 0", got)
	}
}

// Geography is an aggregate or it is nothing: the collector must not
// accept anything that could be an address, and must count only sane
// two-letter codes.
func TestCountriesAreAggregateAndValidated(t *testing.T) {
	c, _ := atClock(time.Unix(1_700_000_000, 0))
	c.Seen("alice", "US", "chat")
	c.Seen("bob", "US", "chat")
	c.Seen("carol", "", "chat")
	c.Seen("dave", "203.0.113.7", "chat") // not a country code
	c.Seen("erin", "usa", "chat")         // three letters

	got := c.Snapshot().Countries
	if len(got) != 1 || got[0].Name != "US" || got[0].Count != 2 {
		t.Fatalf("countries = %+v, want exactly US:2", got)
	}
}

func TestEndpointsRankByVolume(t *testing.T) {
	c, _ := atClock(time.Unix(1_700_000_000, 0))
	for i := 0; i < 3; i++ {
		c.Seen("a", "", "chat")
	}
	c.Seen("a", "", "anthropic")

	got := c.Snapshot().Endpoints
	if len(got) != 2 || got[0].Name != "chat" || got[0].Count != 3 {
		t.Fatalf("endpoints = %+v, want chat first with 3", got)
	}
}

func TestInFlightNeverGoesNegative(t *testing.T) {
	c, _ := atClock(time.Unix(1_700_000_000, 0))
	c.InFlight(1)
	c.InFlight(-1)
	c.InFlight(-1) // a double release must not underflow the gauge
	if got := c.Snapshot().Live.InFlight; got != 0 {
		t.Errorf("in_flight = %d, want 0", got)
	}
}

func TestTrackedSubjectsAreBounded(t *testing.T) {
	c, _ := atClock(time.Unix(1_700_000_000, 0))
	for i := 0; i < maxTracked+500; i++ {
		c.Seen(string(rune(i%1000))+"-"+time.Duration(i).String(), "", "chat")
	}
	if got := c.Snapshot().Live.Subjects5m; got > maxTracked {
		t.Errorf("tracked %d subjects, want at most %d", got, maxTracked)
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	c := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				c.Seen("sub", "US", "chat")
				c.Charged(10, 20)
				c.InFlight(1)
				c.Snapshot()
				c.InFlight(-1)
			}
		}(i)
	}
	wg.Wait()
	if got := c.Snapshot().Live.InFlight; got != 0 {
		t.Errorf("in_flight = %d after all work finished, want 0", got)
	}
}
