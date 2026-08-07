package quota

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLifetimeSpendSurvivesTheDayRolling covers a rollover that happens
// while the process is running. These cover the other half: a day that
// ends while the gateway is *down*.
//
// priorSpend only ever advanced at an in-process rollover, so a gateway
// stopped on one day and started on the next came back believing that
// day's money had never been spent. Every deploy or reboot across
// midnight silently refunded the lifetime credit pool — the daily breaker
// was unaffected, but TotalBudgetUSD, the thing meant to stop a bad
// invoice, was not enforced across restarts.

func poolLimits() Limits {
	return Limits{
		DailyRequests: 100, DailyInputTokens: 1 << 20, DailyOutputTokens: 1 << 20,
		DailyBudgetUSD: 100, TotalBudgetUSD: 20,
	}
}

func dayOffset(n int) string {
	return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02")
}

// writeJournal lays down one day's journal the way a process that ran on
// that day and then exited would have left it.
func writeJournal(t *testing.T, dir, day string, usd float64) {
	t.Helper()
	b, err := json.Marshal(entry{
		Time: time.Now().UTC(), Subject: "subj", Endpoint: "chat",
		Model: "deepseek-v4-flash", InputTokens: 100, OutputTokens: 100, USD: usd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal-"+day+".jsonl")
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeState records how far priorSpend had been folded when the process
// last stopped.
func writeState(t *testing.T, dir string, prior float64, through string) {
	t.Helper()
	b, err := json.Marshal(state{PriorSpendUSD: prior, Through: through})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSpendFromDaysSpentDownIsNotRefunded(t *testing.T) {
	dir := t.TempDir()
	// Ran yesterday, spent $15, exited. Nothing folded it in.
	writeJournal(t, dir, dayOffset(-1), 15.00)
	writeState(t, dir, 0, dayOffset(-1))

	l, done := open(t, dir, poolLimits())
	defer done()

	h := l.Health()
	if h.TotalSpendUSD != 15 {
		t.Errorf("lifetime spend = $%.2f, want $15.00 — $%.2f was refunded by the restart",
			h.TotalSpendUSD, 15-h.TotalSpendUSD)
	}
	if h.DaySpendUSD != 0 {
		t.Errorf("day spend = $%.2f, want $0.00; yesterday is not today", h.DaySpendUSD)
	}
}

// Several days down must all be folded, not just the most recent.
func TestEveryDaySpentDownIsFolded(t *testing.T) {
	dir := t.TempDir()
	writeJournal(t, dir, dayOffset(-3), 1.00)
	writeJournal(t, dir, dayOffset(-2), 2.00)
	writeJournal(t, dir, dayOffset(-1), 4.00)
	writeState(t, dir, 0, dayOffset(-3))

	l, done := open(t, dir, poolLimits())
	defer done()

	if got := l.Health().TotalSpendUSD; got != 7 {
		t.Errorf("lifetime spend = $%.2f, want $7.00", got)
	}
}

// Restarting repeatedly must not count the same journal twice.
func TestFoldingPastDaysIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeJournal(t, dir, dayOffset(-1), 5.00)
	writeState(t, dir, 0, dayOffset(-1))

	for i := 1; i <= 3; i++ {
		l, done := open(t, dir, poolLimits())
		if got := l.Health().TotalSpendUSD; got != 5 {
			t.Fatalf("restart %d: lifetime spend = $%.2f, want $5.00", i, got)
		}
		done()
	}
}

// Today's journal is replayed into daySpend; folding must not also add it
// to priorSpend.
func TestTodayIsNotDoubleCounted(t *testing.T) {
	dir := t.TempDir()
	writeJournal(t, dir, dayOffset(0), 3.00)
	writeState(t, dir, 0, dayOffset(0))

	l, done := open(t, dir, poolLimits())
	defer done()

	h := l.Health()
	if h.TotalSpendUSD != 3 {
		t.Errorf("lifetime spend = $%.2f, want $3.00", h.TotalSpendUSD)
	}
	if h.DaySpendUSD != 3 {
		t.Errorf("day spend = $%.2f, want $3.00", h.DaySpendUSD)
	}
}

// A state file that predates the folding field must not double-count the
// days it already covered.
func TestSpendAlreadyFoldedIsNotCountedAgain(t *testing.T) {
	dir := t.TempDir()
	writeJournal(t, dir, dayOffset(-2), 6.00) // already in priorSpend
	writeJournal(t, dir, dayOffset(-1), 4.00) // not yet folded
	writeState(t, dir, 6.00, dayOffset(-1))

	l, done := open(t, dir, poolLimits())
	defer done()

	if got := l.Health().TotalSpendUSD; got != 10 {
		t.Errorf("lifetime spend = $%.2f, want $10.00", got)
	}
}

// With no state file at all, every journal on disk is history.
func TestMissingStateFoldsEveryPastJournal(t *testing.T) {
	dir := t.TempDir()
	writeJournal(t, dir, dayOffset(-2), 8.00)
	writeJournal(t, dir, dayOffset(-1), 1.00)

	l, done := open(t, dir, poolLimits())
	defer done()

	if got := l.Health().TotalSpendUSD; got != 9 {
		t.Errorf("lifetime spend = $%.2f, want $9.00", got)
	}
}

// The point of all of the above: the breaker must actually fire.
func TestCreditPoolStaysExhaustedAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	writeJournal(t, dir, dayOffset(-1), 20.00) // the whole pool
	writeState(t, dir, 0, dayOffset(-1))

	l, done := open(t, dir, poolLimits())
	defer done()

	err := l.Admit("a-brand-new-subject", Admission{})
	if err == nil {
		t.Fatal("admitted a request with the credit pool already empty")
	}
	if got := reasonOf(t, err); got != ReasonCredits {
		t.Errorf("refusal reason = %s, want %s", got, ReasonCredits)
	}
}

// Folding must survive a journal whose last line was cut off by a crash,
// counting everything before the tear.
func TestFoldingToleratesATruncatedJournal(t *testing.T) {
	dir := t.TempDir()
	day := dayOffset(-1)
	writeJournal(t, dir, day, 2.00)

	f, err := os.OpenFile(filepath.Join(dir, "journal-"+day+".jsonl"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"time":"2026-08-01T00:00:00Z","subject":"subj","usd":99.0`) // no close brace
	f.Close()
	writeState(t, dir, 0, day)

	l, done := open(t, dir, poolLimits())
	defer done()

	if got := l.Health().TotalSpendUSD; got != 2 {
		t.Errorf("lifetime spend = $%.2f, want $2.00 from the one intact line", got)
	}
}
