package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

// beijing is fixed arithmetic, not a timezone database: Asia/Shanghai is
// UTC+8 and has not observed daylight saving since 1991. Loading the
// IANA zone could fail on a machine without tzdata; a constant cannot.
var beijing = time.FixedZone("UTC+8", 8*60*60)

// pricingPrice is one rate card row in USD per 1M tokens.
type pricingPrice struct {
	CacheHitInput  float64 `json:"cache_hit_input"`
	CacheMissInput float64 `json:"cache_miss_input"`
	Output         float64 `json:"output"`
}

// pricingResult is the JSON shape of `deepseek pricing`. Computed
// locally from the same schedule the cost estimates use, so the two can
// never disagree.
type pricingResult struct {
	NowUTC     string  `json:"now_utc"`
	NowLocal   string  `json:"now_local"`
	NowBeijing string  `json:"now_beijing"`
	Period     string  `json:"period"`
	Multiplier float64 `json:"multiplier"`
	NextChange string  `json:"next_change"`

	RepriceAt      string   `json:"reprice_at"`
	PeakWindowsUTC []string `json:"peak_windows_utc"`
	PeakMultiplier float64  `json:"peak_multiplier"`

	// Current is the card in effect at now_utc; OffPeak and Peak are the
	// dated cards that apply from reprice_at.
	Current map[string]pricingPrice `json:"current"`
	OffPeak map[string]pricingPrice `json:"off_peak"`
	Peak    map[string]pricingPrice `json:"peak"`

	Source string `json:"source"`
}

func newPricingCmd(o *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "pricing",
		Short: "The rate card, the time-of-day schedule, and the period right now",
		Long: strings.TrimSpace(`
Answer what a token costs at this instant, and when that changes.

DeepSeek's repricing of 2026-08-13 is dated: until 16:00 UTC on
2026-08-16 every hour bills at the flat card of 2026-08-02, and from
that instant billing is peak/off-peak on a new, higher card — peak hours
01:00-04:00 and 06:00-10:00 UTC daily at twice the off-peak rate.

This command reads no network and spends nothing: the schedule is the
same one the cost estimates use, so what it prints is what the usage
line will charge. The upstream page it encodes is
` + "`deepseek docs show quick_start/pricing`" + `, offline.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			now := time.Now()
			return o.emitValue(pricingAt(now), formatPricing(now))
		},
	}
}

func pricingAt(now time.Time) *pricingResult {
	period := deepseek.PeriodAt(now)
	res := &pricingResult{
		NowUTC:         now.UTC().Format(time.RFC3339),
		NowLocal:       now.Format(time.RFC3339),
		NowBeijing:     now.In(beijing).Format(time.RFC3339),
		Period:         period.Label,
		Multiplier:     period.Multiplier,
		NextChange:     deepseek.NextChange(now).Format(time.RFC3339),
		RepriceAt:      deepseek.RepriceAt.Format(time.RFC3339),
		PeakMultiplier: deepseek.PeakMultiplier,
		Current:        map[string]pricingPrice{},
		OffPeak:        map[string]pricingPrice{},
		Peak:           map[string]pricingPrice{},
		Source:         "https://api-docs.deepseek.com/quick_start/pricing",
	}
	for _, w := range deepseek.PeakWindows {
		res.PeakWindowsUTC = append(res.PeakWindowsUTC,
			fmt.Sprintf("%s-%s", fmtMinutes(w.Start), fmtMinutes(w.End)))
	}
	for _, m := range []string{deepseek.ModelFlash, deepseek.ModelPro} {
		if p, ok := deepseek.PriceAt(m, now); ok {
			res.Current[m] = pricingPrice{p.CacheHitInput, p.CacheMissInput, p.Output}
		}
		if p, ok := deepseek.PriceAt(m, deepseek.RepriceAt); ok {
			res.OffPeak[m] = pricingPrice{p.CacheHitInput, p.CacheMissInput, p.Output}
			res.Peak[m] = pricingPrice{
				p.CacheHitInput * deepseek.PeakMultiplier,
				p.CacheMissInput * deepseek.PeakMultiplier,
				p.Output * deepseek.PeakMultiplier,
			}
		}
	}
	return res
}

func formatPricing(now time.Time) string {
	period := deepseek.PeriodAt(now)
	next := deepseek.NextChange(now)

	var b strings.Builder
	fmt.Fprintf(&b, "period: %s", period.Label)
	if period.Multiplier != 1 {
		fmt.Fprintf(&b, " (%gx the off-peak card)", period.Multiplier)
	}
	fmt.Fprintf(&b, "\nlocal %s · utc %s · beijing %s\n",
		now.Format("15:04 (UTC-07:00)"),
		now.UTC().Format("15:04"),
		now.In(beijing).Format("15:04"))

	switch {
	case now.Before(deepseek.RepriceAt):
		fmt.Fprintf(&b, "next change: %s (%s) — peak/off-peak billing begins on a new card\n",
			deepseek.RepriceAt.Format("2006-01-02 15:04 UTC"), humanUntil(deepseek.RepriceAt.Sub(now)))
	default:
		nextPeriod := deepseek.PeriodAt(next)
		fmt.Fprintf(&b, "next change: %s UTC (%s) — %s\n",
			next.UTC().Format("15:04"), humanUntil(next.Sub(now)), nextPeriod.Label)
	}

	fmt.Fprintf(&b, "\nUSD per 1M tokens, in effect now (%s):\n", period.Label)
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MODEL\tIN (CACHED)\tIN (MISS)\tOUT")
	for _, m := range []string{deepseek.ModelFlash, deepseek.ModelPro} {
		if p, ok := deepseek.PriceAt(m, now); ok {
			fmt.Fprintf(w, "%s\t$%g\t$%g\t$%g\n", m, p.CacheHitInput, p.CacheMissInput, p.Output)
		}
	}
	w.Flush()

	windows := make([]string, 0, len(deepseek.PeakWindows))
	for _, win := range deepseek.PeakWindows {
		windows = append(windows, fmtMinutes(win.Start)+"-"+fmtMinutes(win.End))
	}
	if now.Before(deepseek.RepriceAt) {
		fmt.Fprintf(&b, "\nfrom %s — peak hours %s UTC daily, all other hours off-peak at half of peak:\n",
			deepseek.RepriceAt.Format("2006-01-02 15:04 UTC"), strings.Join(windows, " and "))
	} else {
		fmt.Fprintf(&b, "\nthe full card — peak hours %s UTC daily, all other hours off-peak at half of peak:\n",
			strings.Join(windows, " and "))
	}
	w = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MODEL\tPERIOD\tIN (CACHED)\tIN (MISS)\tOUT")
	for _, m := range []string{deepseek.ModelFlash, deepseek.ModelPro} {
		p, ok := deepseek.PriceAt(m, deepseek.RepriceAt)
		if !ok {
			continue
		}
		fmt.Fprintf(w, "%s\toff-peak\t$%g\t$%g\t$%g\n", m, p.CacheHitInput, p.CacheMissInput, p.Output)
		fmt.Fprintf(w, "%s\tpeak\t$%g\t$%g\t$%g\n", m,
			p.CacheHitInput*deepseek.PeakMultiplier, p.CacheMissInput*deepseek.PeakMultiplier, p.Output*deepseek.PeakMultiplier)
	}
	w.Flush()

	fmt.Fprint(&b, "\ncost estimates switch cards on the effective instant automatically.\nupstream copy, offline: deepseek docs show quick_start/pricing")
	return b.String()
}

func fmtMinutes(m int) string {
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

// humanUntil renders a duration the way a person waits through it:
// days and hours far out, minutes when it is close.
func humanUntil(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("in %dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("in %dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	}
}
