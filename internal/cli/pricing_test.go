package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

// `deepseek pricing` is the CLI face of the same schedule the cost
// estimates use. A wrong answer here names a price, so the shape is
// pinned at fixed instants on both sides of the repricing flip.

func TestPricingBeforeTheFlip(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	res := pricingAt(now)

	if res.Period != "flat" || res.Multiplier != 1 {
		t.Errorf("period = %s at %gx, want flat at 1x", res.Period, res.Multiplier)
	}
	if res.NextChange != deepseek.RepriceAt.Format(time.RFC3339) {
		t.Errorf("next change = %s, want the repricing instant", res.NextChange)
	}
	if got := res.Current["deepseek-v4-flash"]; got.CacheMissInput != 0.14 {
		t.Errorf("current flash miss = %v, want the flat 0.14", got.CacheMissInput)
	}
	// The dated card rides along so a script can see the future without
	// a second source.
	if got := res.OffPeak["deepseek-v4-pro"]; got.Output != 1.98 {
		t.Errorf("off-peak pro output = %v, want 1.98", got.Output)
	}
	if got, want := res.Peak["deepseek-v4-flash"].CacheMissInput, 0.44; got != want {
		t.Errorf("peak flash miss = %v, want %v (2x off-peak)", got, want)
	}

	text := formatPricing(now)
	for _, want := range []string{"period: flat", "2026-08-16 16:00 UTC", "$0.14", "$0.22", "off-peak", "peak"} {
		if !strings.Contains(text, want) {
			t.Errorf("text output is missing %q:\n%s", want, text)
		}
	}
}

func TestPricingInsideAPeakWindow(t *testing.T) {
	now := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC) // 01:00-04:00 UTC window
	res := pricingAt(now)

	if res.Period != "peak" || res.Multiplier != deepseek.PeakMultiplier {
		t.Errorf("period = %s at %gx, want peak at %gx", res.Period, res.Multiplier, deepseek.PeakMultiplier)
	}
	if got := res.Current["deepseek-v4-flash"]; got.CacheMissInput != 0.44 {
		t.Errorf("current flash miss = %v, want the peak 0.44", got.CacheMissInput)
	}
	if res.NextChange != time.Date(2026, 8, 17, 4, 0, 0, 0, time.UTC).Format(time.RFC3339) {
		t.Errorf("next change = %s, want the window end 04:00 UTC", res.NextChange)
	}

	text := formatPricing(now)
	if !strings.Contains(text, "period: peak") {
		t.Errorf("text output does not name the peak period:\n%s", text)
	}
}

func TestPricingJSONAgreesWithTheCostEstimator(t *testing.T) {
	// The command and Cost() must read the same table: the card `pricing`
	// prints for an instant is the card a call at that instant is priced
	// under. If these ever diverge, the tool argues with itself.
	for _, now := range []time.Time{
		time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
	} {
		res := pricingAt(now)
		for _, m := range []string{deepseek.ModelFlash, deepseek.ModelPro} {
			cost, _ := deepseek.CostAt(m, deepseek.Usage{OutputTokens: 1_000_000}, now)
			if got := res.Current[m].Output; got != cost {
				t.Errorf("%s at %s: pricing prints $%v/M output, Cost charges $%v", m, now, got, cost)
			}
		}
	}
}
