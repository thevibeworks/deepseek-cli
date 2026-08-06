package mint

import (
	"testing"
	"time"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/token"
)

func newTestMint(t *testing.T, cfg Config) *Mint {
	t.Helper()
	s, err := token.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return New(s, cfg)
}

// Difficulty is deliberately tiny here. What is under test is the policy
// around the proof of work, not sha256.
func fastConfig() Config { return Config{BaseBits: 4, FreeMints: 3, TTL: time.Minute} }

func mintOne(t *testing.T, m *Mint, ip string) *token.Token {
	t.Helper()
	c, err := m.Challenge(ip)
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	nonce, err := token.Solve(c.String, c.Difficulty, 1<<24)
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	tok, err := m.Redeem(ip, c.String, nonce)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	return tok
}

func TestMintIssuesDistinctSubjects(t *testing.T) {
	m := newTestMint(t, fastConfig())
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		tok := mintOne(t, m, "203.0.113.7")
		if seen[tok.Subject.String()] {
			t.Fatal("two mints produced the same subject; they would share a quota")
		}
		seen[tok.Subject.String()] = true
	}
}

// One proof of work is one token. Without this, an attacker solves a
// single puzzle and mints an unlimited supply from it.
func TestChallengeIsSingleUse(t *testing.T) {
	m := newTestMint(t, fastConfig())
	c, _ := m.Challenge("203.0.113.7")
	nonce, _ := token.Solve(c.String, c.Difficulty, 1<<24)

	if _, err := m.Redeem("203.0.113.7", c.String, nonce); err != nil {
		t.Fatalf("first redeem failed: %v", err)
	}
	if _, err := m.Redeem("203.0.113.7", c.String, nonce); err == nil {
		t.Fatal("the same solved challenge minted a second token")
	}
}

func TestWrongNonceIsRejected(t *testing.T) {
	m := newTestMint(t, Config{BaseBits: 16, FreeMints: 3, TTL: time.Minute})
	c, _ := m.Challenge("203.0.113.7")
	if _, err := m.Redeem("203.0.113.7", c.String, 0); err == nil {
		// Nonce 0 satisfies 16 leading zero bits about once in 65,536
		// challenges; a failure here that repeats is real.
		t.Fatal("nonce 0 was accepted for a 16-bit challenge")
	}
}

func TestForgedChallengeIsRejected(t *testing.T) {
	m := newTestMint(t, fastConfig())
	if _, err := m.Redeem("203.0.113.7", "not-a-challenge.at-all", 1); err == nil {
		t.Fatal("a made-up challenge was redeemed")
	}
}

func TestExpiredChallengeIsRejected(t *testing.T) {
	m := newTestMint(t, fastConfig())
	now := time.Now()
	m.SetClock(func() time.Time { return now })

	c, _ := m.Challenge("203.0.113.7")
	nonce, _ := token.Solve(c.String, c.Difficulty, 1<<24)
	now = now.Add(2 * time.Minute)

	if _, err := m.Redeem("203.0.113.7", c.String, nonce); err == nil {
		t.Fatal("a challenge older than its TTL was redeemed")
	}
}

// The cheap attack is one host minting identities in a loop. Difficulty
// has to climb for that host without a blocklist or an ASN database.
func TestDifficultyEscalatesPerAddress(t *testing.T) {
	m := newTestMint(t, fastConfig())
	const ip = "203.0.113.7"

	base := m.Difficulty(ip)
	if base != 4 {
		t.Fatalf("first challenge difficulty = %d, want the base 4", base)
	}
	for i := 0; i < 2; i++ {
		mintOne(t, m, ip)
	}
	if got := m.Difficulty(ip); got != base {
		t.Errorf("difficulty rose to %d inside the free allowance", got)
	}

	mintOne(t, m, ip)
	fourth := m.Difficulty(ip)
	if fourth <= base {
		t.Errorf("difficulty stayed at %d past the free allowance", fourth)
	}

	// And a different address is unaffected — otherwise one abuser would
	// throttle the whole internet.
	if got := m.Difficulty("198.51.100.9"); got != base {
		t.Errorf("an unrelated address was escalated to %d", got)
	}
}

func TestDifficultyIsCapped(t *testing.T) {
	m := newTestMint(t, fastConfig())
	const ip = "203.0.113.7"
	for i := 0; i < 100; i++ {
		m.issued[MintBucket(ip)] = i
		if got := m.Difficulty(ip); got > 4+maxEscalation {
			t.Fatalf("difficulty reached %d after %d mints; a busy NAT would be locked out for good", got, i)
		}
	}
}

// The escalation must price the challenge you are ASKING for, not the
// ones you have redeemed. Otherwise an attacker collects a batch of
// base-difficulty challenges first and solves them at leisure.
func TestBatchedChallengesEscalate(t *testing.T) {
	m := newTestMint(t, fastConfig())
	const ip = "203.0.113.7"

	var last uint8
	for i := 0; i < 4; i++ {
		c, err := m.Challenge(ip)
		if err != nil {
			t.Fatal(err)
		}
		last = c.Difficulty
	}
	if last <= 4 {
		t.Fatalf("fourth unredeemed challenge still at difficulty %d; batching bypasses escalation", last)
	}
}

// A challenge issued to one address must not be redeemable from another,
// or one cheap address farms base-difficulty challenges for a fleet.
func TestChallengeIsBoundToItsAddress(t *testing.T) {
	m := newTestMint(t, fastConfig())
	c, _ := m.Challenge("203.0.113.7")
	nonce, _ := token.Solve(c.String, c.Difficulty, 1<<24)

	if _, err := m.Redeem("198.51.100.9", c.String, nonce); err == nil {
		t.Fatal("a challenge issued to 203.0.113.7 was redeemed from 198.51.100.9")
	}
	if _, err := m.Redeem("203.0.113.7", c.String, nonce); err != nil {
		t.Fatalf("the issuing address could not redeem its own challenge: %v", err)
	}
}

// A restart must not be a difficulty amnesty.
func TestIssuanceCountsSurviveRestart(t *testing.T) {
	cfg := fastConfig()
	cfg.StatePath = t.TempDir() + "/mint.json"
	m := newTestMint(t, cfg)
	const ip = "203.0.113.7"

	for i := 0; i < 6; i++ {
		if _, err := m.Challenge(ip); err != nil {
			t.Fatal(err)
		}
	}
	escalated := m.Difficulty(ip)
	if escalated <= 4 {
		t.Fatal("difficulty did not escalate before the restart")
	}

	m2 := newTestMint(t, cfg)
	if got := m2.Difficulty(ip); got != escalated {
		t.Errorf("after restart difficulty = %d, want %d; a deploy resets escalation", got, escalated)
	}
}

func TestEscalationResetsDaily(t *testing.T) {
	m := newTestMint(t, fastConfig())
	now := time.Date(2026, 8, 5, 23, 59, 0, 0, time.UTC)
	m.SetClock(func() time.Time { return now })
	const ip = "203.0.113.7"

	for i := 0; i < 6; i++ {
		mintOne(t, m, ip)
	}
	if m.Difficulty(ip) == 4 {
		t.Fatal("difficulty did not escalate at all")
	}

	now = now.Add(2 * time.Minute) // past midnight UTC
	if got := m.Difficulty(ip); got != 4 {
		t.Errorf("difficulty = %d the next day, want the base 4", got)
	}
}

// IPv6 is bucketed per /48 for minting: a /64 bucket would let anyone
// with an ordinary home allocation mint from 65,536 "different"
// addresses. Requests use the looser /64, where one bucket is one LAN.
func TestAddressBucketing(t *testing.T) {
	cases := []struct {
		name              string
		a, b              string
		sameMint, sameReq bool
	}{
		{"same v4", "203.0.113.7", "203.0.113.7", true, true},
		{"different v4", "203.0.113.7", "203.0.113.8", false, false},
		{"v6 in one /64", "2001:db8:1:2::1", "2001:db8:1:2::9999", true, true},
		{"v6 across /64 inside one /48", "2001:db8:1:2::1", "2001:db8:1:ffff::1", true, false},
		{"v6 across /48", "2001:db8:1:2::1", "2001:db8:2:2::1", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MintBucket(tc.a) == MintBucket(tc.b); got != tc.sameMint {
				t.Errorf("MintBucket same = %v, want %v (%s vs %s)", got, tc.sameMint, MintBucket(tc.a), MintBucket(tc.b))
			}
			if got := RequestBucket(tc.a) == RequestBucket(tc.b); got != tc.sameReq {
				t.Errorf("RequestBucket same = %v, want %v", got, tc.sameReq)
			}
		})
	}
}

func TestUnparseableAddressesShareOneBucket(t *testing.T) {
	// Throttled together rather than each getting a fresh allowance.
	if MintBucket("not-an-ip") != MintBucket("also-not-an-ip") {
		t.Error("unparseable addresses got separate buckets, which is an unlimited supply of them")
	}
}

// X-Forwarded-For is attacker-supplied unless something we control set
// it. Trusting it by default would hand out unlimited identities to
// anyone who can write a header.
func TestForwardedForIsOnlyTrustedWhenConfigured(t *testing.T) {
	const remote = "10.0.0.1:5555"

	if got := ClientIP(remote, "1.2.3.4", false); got != "10.0.0.1" {
		t.Errorf("untrusted mode used the header: got %q", got)
	}
	if got := ClientIP(remote, "1.2.3.4", true); got != "1.2.3.4" {
		t.Errorf("trusted mode ignored the header: got %q", got)
	}
	// The leftmost entry is the original client; the rest were added by
	// hops we do trust.
	if got := ClientIP(remote, "1.2.3.4, 10.0.0.9, 10.0.0.10", true); got != "1.2.3.4" {
		t.Errorf("chained header: got %q, want 1.2.3.4", got)
	}
	if got := ClientIP(remote, "   ", true); got != "10.0.0.1" {
		t.Errorf("blank header should fall back to the socket: got %q", got)
	}
}
