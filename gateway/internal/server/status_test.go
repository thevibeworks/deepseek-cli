package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/quota"
)

func getStatus(t *testing.T, h *harness) PublicStatus {
	t.Helper()
	resp := h.do(t, "GET", "/v1/status", "", "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: HTTP %d: %s", resp.StatusCode, raw)
	}
	var st PublicStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStatusIsUnauthenticatedAndUsable(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(85, 40))
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)
	h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`).Body.Close()
	h.settle(t)

	st := getStatus(t, h)
	if st.State != StateOperational {
		t.Errorf("state = %q, want %q", st.State, StateOperational)
	}
	if st.Detail == "" {
		t.Error("state has no human explanation")
	}
	if st.Usage.Today.Requests != 1 {
		t.Errorf("today's requests = %d, want 1", st.Usage.Today.Requests)
	}
	if st.Usage.Today.InputTokens != 85 || st.Usage.Today.OutputTokens != 40 {
		t.Errorf("today's tokens = %+v, want 85 in / 40 out", st.Usage.Today)
	}
	if st.Usage.SubjectsToday != 1 {
		t.Errorf("subjects_today = %d, want 1", st.Usage.SubjectsToday)
	}
	if len(st.Live.Series) == 0 {
		t.Error("the sparkline series is empty")
	}
	if st.Keys.Active != 1 {
		t.Errorf("key_pool.active = %d, want 1", st.Keys.Active)
	}
	if st.Limits.Requests == 0 {
		t.Error("the per-user limits are not reported")
	}
}

// The public document is the one an attacker reads too. It must not carry
// the numbers that turn the budget breaker into a progress bar.
func TestStatusWithholdsMoney(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(1000, 900))
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)
	h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`).Body.Close()
	h.settle(t)

	resp := h.do(t, "GET", "/v1/status", "", "")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var st PublicStatus
	json.Unmarshal(raw, &st)
	if st.Usage.Today.SpentUSD != 0 || st.Usage.Lifetime.SpentUSD != 0 {
		t.Errorf("the public document reports spend: today=%v lifetime=%v",
			st.Usage.Today.SpentUSD, st.Usage.Lifetime.SpentUSD)
	}
	for _, banned := range []string{"daily_budget_usd", "total_budget_usd", "day_spend_usd", "total_spend_usd", "reserved_usd"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("the public document carries %q", banned)
		}
	}
	// Percentages are the point: they answer "how much is left" without
	// saying how much more it would take to empty it.
	if st.Credit.PoolRemainingPct <= 0 || st.Credit.PoolRemainingPct > 100 {
		t.Errorf("pool_remaining_pct = %v, want a sane percentage", st.Credit.PoolRemainingPct)
	}
}

// A whole subject id can be matched against the one in someone's
// free.json. The leaderboard must not publish one.
func TestStatusTruncatesSubjectIDs(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(10, 10))
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)
	full := subjectOf(t, tok)
	h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`).Body.Close()
	h.settle(t)

	resp := h.do(t, "GET", "/v1/status", "", "")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), full) {
		t.Fatal("the status page published a whole subject id")
	}

	var st PublicStatus
	json.Unmarshal(raw, &st)
	if len(st.Top) != 1 {
		t.Fatalf("top_subjects has %d rows, want 1", len(st.Top))
	}
	if !strings.HasPrefix(full, strings.TrimSuffix(st.Top[0].Subject, "…")) {
		t.Errorf("the truncated id %q is not a prefix of the real one", st.Top[0].Subject)
	}
	if st.Top[0].Requests != 1 {
		t.Errorf("leaderboard requests = %d, want 1", st.Top[0].Requests)
	}
}

// Geography is aggregate or it is nothing.
func TestStatusCountsCountriesNotAddresses(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(10, 10))
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	req, _ := http.NewRequest("POST", h.base+"/chat/completions", strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("CF-IPCountry", "DE")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	h.settle(t)

	st := getStatus(t, h)
	if len(st.Countries) != 1 || st.Countries[0].Name != "DE" {
		t.Fatalf("countries = %+v, want DE", st.Countries)
	}
	// And the country must not be attached to the subject anywhere.
	if len(st.Top) > 0 && strings.Contains(st.Top[0].Subject, "DE") {
		t.Error("a country was joined onto a subject id")
	}
}

func TestStatusReportsExhaustion(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(10, 10))
	})
	h := newHarness(t, up, func(cfg *Config, lim *quota.Limits) {
		lim.TotalBudgetUSD = 0.001
	})
	// Spend the pool for real rather than configuring it to zero: the
	// state has to follow actual spend, which is the thing that goes
	// wrong in production.
	h.ledger.Admit("someone", 0)
	h.ledger.Charge("someone", "chat", "deepseek-v4-flash", 10, 0, 10, 0.002, 0, false)

	st := getStatus(t, h)
	if st.State != StateDry {
		t.Errorf("state = %q with an empty pool, want %q", st.State, StateDry)
	}
	if !strings.Contains(st.Detail, "key") {
		t.Errorf("the exhausted detail does not point anywhere useful: %q", st.Detail)
	}
}

func TestAdminStatusNeedsTheTokenAndCarriesMoney(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(10, 10))
	})
	h := newHarness(t, up, func(cfg *Config, lim *quota.Limits) {
		cfg.AdminToken = "operator-only"
	})

	resp := h.do(t, "GET", "/admin/status", "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unauthenticated /admin/status: HTTP %d, want 404", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", h.base+"/admin/status", nil)
	req.Header.Set("X-Admin-Token", "operator-only")
	resp2, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("authenticated /admin/status: HTTP %d", resp2.StatusCode)
	}
	raw, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(raw), "total_budget_usd") {
		t.Error("the operator view withholds the budget it exists to show")
	}
	// The operator view shows keys, but still never the key itself.
	if strings.Contains(string(raw), upstreamKey) {
		t.Fatal("the operator view leaked the upstream key")
	}
}

func TestAdminKeysDonateAndRetire(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(10, 10))
	})
	h := newHarness(t, up, func(cfg *Config, lim *quota.Limits) {
		cfg.AdminToken = "operator-only"
	})

	admin := func(method, path, body string) *http.Response {
		t.Helper()
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, h.base+path, rdr)
		req.Header.Set("X-Admin-Token", "operator-only")
		req.Header.Set("Content-Type", "application/json")
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := admin("POST", "/admin/keys", `{"key":"sk-donated-by-a-stranger","label":"anon"}`)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("donate: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Fingerprint string `json:"fingerprint"`
		Added       bool   `json:"added"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if !out.Added {
		t.Fatal("the donated key was not added")
	}

	st := getStatus(t, h)
	if st.Keys.Total != 2 || st.Keys.Active != 2 {
		t.Errorf("key_pool = %+v, want 2 total / 2 active", st.Keys)
	}
	if st.Credit.Donated != 1 {
		t.Errorf("donated_keys = %d, want 1", st.Credit.Donated)
	}

	resp2 := admin("DELETE", "/admin/keys?fingerprint="+strings.ReplaceAll(out.Fingerprint, "…", "%E2%80%A6")+"&action=retire", "")
	resp2.Body.Close()

	// Unauthenticated callers get nothing from any of it.
	resp3 := h.do(t, "GET", "/admin/keys", "", "")
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotFound {
		t.Errorf("unauthenticated /admin/keys: HTTP %d, want 404", resp3.StatusCode)
	}
}

// A key upstream refuses with 402 must leave the rotation, and the next
// request must go out on a different one rather than failing.
func TestExhaustedKeyStepsAsideAndTheNextKeyServes(t *testing.T) {
	var seen []string
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seen = append(seen, auth)
		if auth == "sk-empty" {
			w.WriteHeader(http.StatusPaymentRequired)
			io.WriteString(w, `{"error":{"message":"Insufficient Balance"}}`)
			return
		}
		io.WriteString(w, chatReply(10, 10))
	})
	h := newHarness(t, up, func(cfg *Config, lim *quota.Limits) {
		cfg.UpstreamKeys = []string{"sk-empty", "sk-funded"}
		lim.DailyRequests = 50
	})
	tok := h.enrol(t)

	// First request lands on whichever key the rotation starts with; by
	// the third, the empty one must be out of the pool for good.
	for i := 0; i < 3; i++ {
		h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`).Body.Close()
		h.settle(t)
	}
	if got := h.Server.keys.Status(false); got.Dry != 1 || got.Active != 1 {
		t.Fatalf("pool = %+v, want the empty key dry and one active", got)
	}

	seen = nil
	resp := h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("after retirement: HTTP %d, want 200 on the funded key", resp.StatusCode)
	}
	for _, k := range seen {
		if k == "sk-empty" {
			t.Error("a retired key was used again")
		}
	}
}

// With every key gone the service must say so honestly rather than
// relaying a confusing upstream error.
func TestEmptyPoolIsAnHonest402(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		io.WriteString(w, `{"error":{"message":"Insufficient Balance"}}`)
	})
	h := newHarness(t, up, func(cfg *Config, lim *quota.Limits) {
		cfg.UpstreamKeys = []string{"sk-empty"}
		lim.DailyRequests = 50
	})
	tok := h.enrol(t)

	h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`).Body.Close()
	h.settle(t)

	resp := h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("HTTP %d with an empty pool, want 402", resp.StatusCode)
	}
	if e := decodeError(t, resp); !strings.Contains(e.Message, "your own key") {
		t.Errorf("the 402 does not point at the way forward: %q", e.Message)
	}
	if st := getStatus(t, h); st.State != StateDry {
		t.Errorf("state = %q, want %q", st.State, StateDry)
	}
}
