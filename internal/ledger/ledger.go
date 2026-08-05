// Package ledger records what every API call cost, as an append-only
// JSONL file.
//
// The point is to answer "what did I spend today, and how much did the
// context cache save me" without a dashboard. Token counts are stored
// alongside the estimated cost so old rows can be repriced when DeepSeek
// changes the rate card — the cost field is a convenience, the counts are
// the record.
package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

// Entry is one API call.
type Entry struct {
	Time  time.Time `json:"ts"`
	API   string    `json:"api"`   // chat | fim | responses | anthropic
	Model string    `json:"model"` // as resolved to a priced model
	// Requested is the model name the user asked for, kept only when it
	// differs from Model — the Anthropic endpoint remaps Claude names.
	Requested       string  `json:"requested,omitempty"`
	InputTokens     int     `json:"in"`
	CacheHitTokens  int     `json:"cache_hit"`
	CacheMissTokens int     `json:"cache_miss"`
	OutputTokens    int     `json:"out"`
	ReasoningTokens int     `json:"reasoning,omitempty"`
	CostUSD         float64 `json:"cost_usd"`
	SavedUSD        float64 `json:"saved_usd"`
	DurationMS      int64   `json:"ms"`
	Stream          bool    `json:"stream,omitempty"`
	Session         string  `json:"session,omitempty"`
}

// Path is the ledger file.
func Path() string { return filepath.Join(deepseek.StateDir(), "usage.jsonl") }

// Record appends one call. Ledger failures never fail the command that
// produced them: the user asked for a completion, not for bookkeeping.
// The error is returned so the caller can mention it under --verbose.
func Record(api, requested string, u deepseek.Usage, dur time.Duration, stream bool, session string) (Entry, error) {
	model := deepseek.ResolveModel(requested)
	cost, _ := deepseek.Cost(requested, u)
	saved, _ := deepseek.CacheSavings(requested, u)

	e := Entry{
		Time:            time.Now().UTC(),
		API:             api,
		Model:           model,
		InputTokens:     u.InputTokens,
		CacheHitTokens:  u.CacheHitTokens,
		CacheMissTokens: u.CacheMissTokens,
		OutputTokens:    u.OutputTokens,
		ReasoningTokens: u.ReasoningTokens,
		CostUSD:         cost,
		SavedUSD:        saved,
		DurationMS:      dur.Milliseconds(),
		Stream:          stream,
		Session:         session,
	}
	if requested != model {
		e.Requested = requested
	}
	return e, e.append()
}

func (e Entry) append() error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	// One write of one line: concurrent CLI invocations append to the same
	// file, and a single small O_APPEND write will not interleave.
	_, err = f.Write(append(line, '\n'))
	return err
}

// Load reads entries at or after since. A missing ledger is not an error;
// it means nothing has been spent yet.
func Load(since time.Time) ([]Entry, error) {
	f, err := os.Open(Path())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 8192), 256*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			// A torn or hand-edited line should not hide the rest of the
			// history. Skip it.
			continue
		}
		if !since.IsZero() && e.Time.Before(since) {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Summary aggregates a set of entries.
type Summary struct {
	Calls           int     `json:"calls"`
	InputTokens     int     `json:"input_tokens"`
	CacheHitTokens  int     `json:"cache_hit_tokens"`
	CacheMissTokens int     `json:"cache_miss_tokens"`
	OutputTokens    int     `json:"output_tokens"`
	ReasoningTokens int     `json:"reasoning_tokens"`
	CostUSD         float64 `json:"cost_usd"`
	SavedUSD        float64 `json:"saved_usd"`
}

// CacheHitRate is the share of prompt tokens served from cache.
func (s Summary) CacheHitRate() float64 {
	if s.InputTokens <= 0 {
		return 0
	}
	return float64(s.CacheHitTokens) / float64(s.InputTokens)
}

func (s *Summary) add(e Entry) {
	s.Calls++
	s.InputTokens += e.InputTokens
	s.CacheHitTokens += e.CacheHitTokens
	s.CacheMissTokens += e.CacheMissTokens
	s.OutputTokens += e.OutputTokens
	s.ReasoningTokens += e.ReasoningTokens
	s.CostUSD += e.CostUSD
	s.SavedUSD += e.SavedUSD
}

// Report is a grouped view of the ledger.
type Report struct {
	Since   time.Time          `json:"since,omitempty"`
	Total   Summary            `json:"total"`
	ByModel map[string]Summary `json:"by_model"`
	ByAPI   map[string]Summary `json:"by_api"`
}

// Models returns the model names present, in descending cost order, so
// the biggest spender is always the first line printed.
func (r *Report) Models() []string { return sortedByCost(r.ByModel) }

// APIs returns the wire formats present, in descending cost order.
func (r *Report) APIs() []string { return sortedByCost(r.ByAPI) }

func sortedByCost(m map[string]Summary) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]].CostUSD != m[keys[j]].CostUSD {
			return m[keys[i]].CostUSD > m[keys[j]].CostUSD
		}
		return keys[i] < keys[j]
	})
	return keys
}

// Summarize groups entries by model and by API.
func Summarize(entries []Entry, since time.Time) *Report {
	r := &Report{
		Since:   since,
		ByModel: map[string]Summary{},
		ByAPI:   map[string]Summary{},
	}
	for _, e := range entries {
		r.Total.add(e)

		m := r.ByModel[e.Model]
		m.add(e)
		r.ByModel[e.Model] = m

		a := r.ByAPI[e.API]
		a.add(e)
		r.ByAPI[e.API] = a
	}
	return r
}

// ParseSince turns a human window ("today", "7d", "24h", "all", or a
// YYYY-MM-DD date) into a cutoff time.
func ParseSince(s string) (time.Time, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "today":
		now := time.Now()
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), nil
	case "all", "forever":
		return time.Time{}, nil
	case "yesterday":
		now := time.Now().AddDate(0, 0, -1)
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	// "7d" / "30d" — days are not a time.Duration unit.
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Now().AddDate(0, 0, -days), nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cannot read %q as a time window — try today, yesterday, 7d, 24h, all, or 2026-08-01", s)
}
