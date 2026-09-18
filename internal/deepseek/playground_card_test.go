package deepseek

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// The playground prices calls in the browser from its own copy of the
// Flash card, because the site is static and cannot ask the CLI. That copy
// went unchecked through two changes: it read the dead card for a day
// after the 2026-08-16 repricing, and it billed weekends at peak for
// weeks after 2026-08-22. So it is pinned here to the newest Flash card
// this package encodes.
//
// Newest, not current: reading the card at time.Now() would turn red on
// the day a dated card takes effect, whoever happened to push that day.
func TestPlaygroundCarriesTheNewestFlashCard(t *testing.T) {
	src, err := os.ReadFile("../../site/playground.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`var RATES = \{ cacheHit: ([0-9.]+), cacheMiss: ([0-9.]+), output: ([0-9.]+) \};`).FindSubmatch(src)
	if m == nil {
		t.Fatal("site/playground.js: no RATES line; if it moved, move this test with it")
	}
	var got [3]float64
	for i := range got {
		if got[i], err = strconv.ParseFloat(string(m[i+1]), 64); err != nil {
			t.Fatal(err)
		}
	}
	want, _ := BasePriceAt(ModelFlash, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
	if got != [3]float64{want.CacheHitInput, want.CacheMissInput, want.Output} {
		t.Errorf("playground RATES = %v, newest flash card = %+v", got, want)
	}
}
