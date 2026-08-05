package ledger

import (
	"math"
	"testing"
	"time"

	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("DEEPSEEK_STATE_DIR", t.TempDir())
}

func TestRecordThenLoad(t *testing.T) {
	isolate(t)

	u := deepseek.Usage{InputTokens: 1000, CacheHitTokens: 800, CacheMissTokens: 200, OutputTokens: 50}
	if _, err := Record("chat", deepseek.ModelFlash, u, 900*time.Millisecond, false, "work"); err != nil {
		t.Fatal(err)
	}

	got, err := Load(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded %d entries, want 1", len(got))
	}
	e := got[0]
	if e.API != "chat" || e.Model != deepseek.ModelFlash || e.Session != "work" {
		t.Errorf("entry lost fields: %+v", e)
	}
	if e.InputTokens != 1000 || e.CacheHitTokens != 800 || e.OutputTokens != 50 {
		t.Errorf("token counts wrong: %+v", e)
	}
	if e.DurationMS != 900 {
		t.Errorf("duration = %d, want 900", e.DurationMS)
	}
	if e.CostUSD <= 0 || e.SavedUSD <= 0 {
		t.Errorf("cost and savings should both be priced: %+v", e)
	}
}

func TestRecordStoresTheModelThatRan(t *testing.T) {
	// A Claude name sent to the Anthropic endpoint is remapped
	// server-side. The ledger stores what actually ran, and keeps the
	// requested name alongside so the row stays traceable.
	isolate(t)

	e, err := Record("anthropic", "claude-opus-4-1", deepseek.Usage{InputTokens: 10, CacheMissTokens: 10, OutputTokens: 5}, time.Second, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Model != deepseek.ModelPro {
		t.Errorf("Model = %q, want the model that ran (%s)", e.Model, deepseek.ModelPro)
	}
	if e.Requested != "claude-opus-4-1" {
		t.Errorf("Requested = %q, want the name the user asked for", e.Requested)
	}
}

func TestRecordOmitsRequestedWhenItMatches(t *testing.T) {
	isolate(t)

	e, err := Record("chat", deepseek.ModelFlash, deepseek.Usage{InputTokens: 1, CacheMissTokens: 1}, time.Second, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Requested != "" {
		t.Errorf("Requested should stay empty when it equals Model, got %q", e.Requested)
	}
}

func TestLoadFiltersBySince(t *testing.T) {
	isolate(t)

	for i := 0; i < 3; i++ {
		if _, err := Record("chat", deepseek.ModelFlash, deepseek.Usage{InputTokens: 1, CacheMissTokens: 1}, time.Second, false, ""); err != nil {
			t.Fatal(err)
		}
	}

	future, err := Load(time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(future) != 0 {
		t.Errorf("a future cutoff should exclude everything, got %d", len(future))
	}
}

func TestLoadMissingLedgerIsNotAnError(t *testing.T) {
	// Nothing spent yet is a valid state, not a failure.
	isolate(t)

	got, err := Load(time.Time{})
	if err != nil {
		t.Fatalf("missing ledger should load as empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries", len(got))
	}
}

func TestSummarizeGroupsAndTotals(t *testing.T) {
	entries := []Entry{
		{API: "chat", Model: deepseek.ModelFlash, InputTokens: 100, CacheHitTokens: 50, CacheMissTokens: 50, OutputTokens: 10, CostUSD: 0.01, SavedUSD: 0.005},
		{API: "chat", Model: deepseek.ModelFlash, InputTokens: 100, CacheHitTokens: 50, CacheMissTokens: 50, OutputTokens: 10, CostUSD: 0.01, SavedUSD: 0.005},
		{API: "fim", Model: deepseek.ModelPro, InputTokens: 200, CacheMissTokens: 200, OutputTokens: 20, CostUSD: 0.10},
	}

	r := Summarize(entries, time.Time{})
	if r.Total.Calls != 3 || r.Total.InputTokens != 400 {
		t.Errorf("totals wrong: %+v", r.Total)
	}
	if math.Abs(r.Total.CostUSD-0.12) > 1e-9 {
		t.Errorf("total cost = %v, want 0.12", r.Total.CostUSD)
	}
	if got := r.ByModel[deepseek.ModelFlash].Calls; got != 2 {
		t.Errorf("flash calls = %d, want 2", got)
	}
	if got := r.ByAPI["fim"].Calls; got != 1 {
		t.Errorf("fim calls = %d, want 1", got)
	}
	// 100 of 400 prompt tokens came from cache.
	if got := r.Total.CacheHitRate(); math.Abs(got-0.25) > 1e-9 {
		t.Errorf("cache hit rate = %v, want 0.25", got)
	}
}

func TestModelsSortByCostDescending(t *testing.T) {
	// The biggest spender belongs on the first line.
	r := Summarize([]Entry{
		{API: "chat", Model: deepseek.ModelFlash, CostUSD: 0.01},
		{API: "fim", Model: deepseek.ModelPro, CostUSD: 0.50},
	}, time.Time{})

	got := r.Models()
	if len(got) != 2 || got[0] != deepseek.ModelPro {
		t.Errorf("models = %v, want the costliest first", got)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	r := Summarize(nil, time.Time{})
	if r.Total.Calls != 0 || r.Total.CacheHitRate() != 0 {
		t.Errorf("empty summary should be zeroed, got %+v", r.Total)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Now()

	today, err := ParseSince("today")
	if err != nil {
		t.Fatal(err)
	}
	if today.Hour() != 0 || today.Day() != now.Day() {
		t.Errorf("today = %v, want local midnight", today)
	}

	all, err := ParseSince("all")
	if err != nil {
		t.Fatal(err)
	}
	if !all.IsZero() {
		t.Errorf("all = %v, want the zero time", all)
	}

	// "7d" is not a time.Duration unit, so it needs its own handling.
	week, err := ParseSince("7d")
	if err != nil {
		t.Fatal(err)
	}
	if days := now.Sub(week).Hours() / 24; days < 6.9 || days > 7.1 {
		t.Errorf("7d resolved to %.2f days ago", days)
	}

	if _, err := ParseSince("24h"); err != nil {
		t.Errorf("24h should parse as a duration: %v", err)
	}
	if _, err := ParseSince("2026-08-01"); err != nil {
		t.Errorf("a date should parse: %v", err)
	}
	if _, err := ParseSince("last tuesday"); err == nil {
		t.Error("an unparseable window should report the formats it accepts")
	}
}

func TestLoadSkipsCorruptLines(t *testing.T) {
	// A torn write or a hand edit must not hide the rest of the history.
	isolate(t)

	if _, err := Record("chat", deepseek.ModelFlash, deepseek.Usage{InputTokens: 1, CacheMissTokens: 1}, time.Second, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := appendRaw("this is not json\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := Record("chat", deepseek.ModelFlash, deepseek.Usage{InputTokens: 2, CacheMissTokens: 2}, time.Second, false, ""); err != nil {
		t.Fatal(err)
	}

	got, err := Load(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("loaded %d entries, want the 2 good ones", len(got))
	}
}
