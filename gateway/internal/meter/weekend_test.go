package meter

import (
	"testing"
	"time"
)

// The gateway keeps its own copy of the rate card and the clock, because
// it is a separate module. `make price-check` guards the numbers from
// drifting against the CLI's copy; nothing guards the clock, so these
// pin it here too.
func TestWeekendsAreOffPeak(t *testing.T) {
	cases := []struct {
		at   time.Time
		peak bool
		why  string
	}{
		{time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC), true, "Friday, inside the first window"},
		{time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC), false, "Saturday, same window"},
		{time.Date(2026, 8, 30, 2, 0, 0, 0, time.UTC), false, "Sunday, same window"},
		{time.Date(2026, 8, 31, 2, 0, 0, 0, time.UTC), true, "Monday, same window"},
		// The rule started 2026-08-22 16:00 UTC. Saturday 2026-08-22 is the
		// only weekend day that ever billed peak under time-of-use, and
		// over-charging our own budget for it is still what actually happened.
		{time.Date(2026, 8, 22, 2, 0, 0, 0, time.UTC), true, "Saturday before the rule"},
		{time.Date(2026, 8, 23, 2, 0, 0, 0, time.UTC), false, "Sunday, first under the rule"},
	}
	for _, c := range cases {
		if got := inPeak(c.at); got != c.peak {
			t.Errorf("%s (%v): inPeak = %v, want %v", c.why, c.at, got, c.peak)
		}
	}
}

func TestWeekendIsReadOnTheVendorClock(t *testing.T) {
	// Both windows close at 10:00 UTC, before the 16:00 UTC point where a
	// UTC date and a Beijing date diverge -- so no billed instant tells a
	// UTC reading from a Beijing one, and every test written against the
	// windows passes with the shift deleted. Only these two catch it.
	if !isBeijingWeekend(time.Date(2026, 8, 28, 16, 30, 0, 0, time.UTC)) {
		t.Error("Friday 16:30 UTC is already Saturday in Beijing")
	}
	if isBeijingWeekend(time.Date(2026, 8, 28, 15, 30, 0, 0, time.UTC)) {
		t.Error("Friday 15:30 UTC is still Friday in Beijing")
	}
	if isBeijingWeekend(time.Date(2026, 8, 30, 16, 30, 0, 0, time.UTC)) {
		t.Error("Sunday 16:30 UTC is already Monday in Beijing")
	}
}

func TestVisionVariantIsMetered(t *testing.T) {
	// A model priced upstream but missing from this card would be served
	// for free out of the budget pool.
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	vision := PriceAt("deepseek-v4-flash-vision-exp", at)
	flash := PriceAt("deepseek-v4-flash", at)
	if vision != flash {
		t.Errorf("vision %+v != flash %+v", vision, flash)
	}
	if vision.CacheMissInput == 0 {
		t.Error("vision meters at zero")
	}
}
