// Package mint issues free-tier tokens in exchange for proof of work.
//
// The proof of work is not a security boundary and is not pretending to
// be one. Its job is to make identity cost *something* to an attacker
// while costing an honest user about a second of one core, once. The
// thing that actually bounds our spend is the budget breaker in package
// quota, which does not care how many identities exist.
//
// What proof of work buys, specifically, is that farming identities has
// to be paid for in CPU rather than being free. Difficulty escalates per
// address, so the cheap attack — one host, a thousand mints — gets
// expensive fast without any blocklist or ASN database to maintain.
package mint

import (
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/token"
)

// Config tunes the mint.
type Config struct {
	// BaseBits is the difficulty of the first mint from an address.
	// 20 bits is about a second of one core.
	BaseBits uint8
	// FreeMints is how many tokens an address may mint per day at base
	// difficulty before escalation starts.
	FreeMints int
	// TTL is how long a challenge stays solvable.
	TTL time.Duration
}

// DefaultConfig is the shipped policy.
func DefaultConfig() Config {
	return Config{BaseBits: 20, FreeMints: 3, TTL: 5 * time.Minute}
}

// escalationBits is added per mint beyond the free allowance, so the
// fourth mint from one address today costs 4x the first and the eighth
// costs 1024x.
const escalationBits = 2

// maxEscalation caps the climb. Twelve bits over base is roughly an hour
// of one core: enough that farming is not worth it, bounded so that a
// busy NAT is throttled rather than permanently locked out.
const maxEscalation = 12

// Mint issues and redeems challenges.
type Mint struct {
	signer *token.Signer
	cfg    Config

	mu sync.Mutex
	// day is the UTC date the per-address counts belong to.
	day string
	// mints counts tokens issued per address bucket today.
	mints map[string]int
	// redeemed is the single-use set for challenges, keyed by the random
	// component. Bounded by the mint rate times the TTL, and swept.
	redeemed map[[16]byte]time.Time

	now func() time.Time
}

func New(signer *token.Signer, cfg Config) *Mint {
	m := &Mint{
		signer:   signer,
		cfg:      cfg,
		mints:    map[string]int{},
		redeemed: map[[16]byte]time.Time{},
		now:      time.Now,
	}
	m.day = m.now().UTC().Format("2006-01-02")
	return m
}

// SetClock replaces the time source, for tests.
func (m *Mint) SetClock(now func() time.Time) {
	m.mu.Lock()
	m.now = now
	m.mu.Unlock()
	m.signer.SetClock(now)
}

// Challenge issues a puzzle sized to how much this address has already
// minted today.
func (m *Mint) Challenge(remoteIP string) (*token.Challenge, error) {
	bucket := MintBucket(remoteIP)

	m.mu.Lock()
	m.rollLocked()
	n := m.mints[bucket]
	m.mu.Unlock()

	return m.signer.NewChallenge(m.difficulty(n))
}

// difficulty is the required leading-zero-bit count for an address that
// has already minted n tokens today.
func (m *Mint) difficulty(n int) uint8 {
	extra := n - m.cfg.FreeMints
	if extra < 0 {
		extra = 0
	}
	extra *= escalationBits
	if extra > maxEscalation {
		extra = maxEscalation
	}
	return m.cfg.BaseBits + uint8(extra)
}

// Redeem verifies a solved challenge and issues a token.
func (m *Mint) Redeem(remoteIP, challenge string, nonce uint64) (*token.Token, error) {
	c, err := m.signer.ParseChallenge(challenge, m.cfg.TTL)
	if err != nil {
		return nil, err
	}
	if err := token.Verify(challenge, c.Difficulty, nonce); err != nil {
		return nil, err
	}

	bucket := MintBucket(remoteIP)

	m.mu.Lock()
	m.rollLocked()
	m.sweepLocked()
	if _, used := m.redeemed[c.ID]; used {
		m.mu.Unlock()
		// One solved challenge is one token. Without this, a single proof
		// of work would mint an unlimited supply.
		return nil, fmt.Errorf("%w: challenge already redeemed", token.ErrMalformed)
	}
	m.redeemed[c.ID] = m.now()
	m.mints[bucket]++
	m.mu.Unlock()

	return m.signer.NewToken(token.TierAnon)
}

// Difficulty reports what this address would be asked for next, without
// issuing anything. Used by the status endpoint so a client can show
// "this will take a moment" before it starts.
func (m *Mint) Difficulty(remoteIP string) uint8 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollLocked()
	return m.difficulty(m.mints[MintBucket(remoteIP)])
}

func (m *Mint) rollLocked() {
	day := m.now().UTC().Format("2006-01-02")
	if day == m.day {
		return
	}
	m.day = day
	m.mints = map[string]int{}
}

// sweepLocked drops challenge IDs that can no longer be redeemed anyway,
// because their challenge has expired.
func (m *Mint) sweepLocked() {
	if len(m.redeemed) < 1024 {
		return
	}
	cutoff := m.now().Add(-m.cfg.TTL)
	for id, at := range m.redeemed {
		if at.Before(cutoff) {
			delete(m.redeemed, id)
		}
	}
}

// MintBucket groups addresses for the purpose of minting.
//
// IPv4 is bucketed per address: they are scarce enough that having a
// thousand of them is itself a cost. IPv6 is bucketed per /48, which is
// the block a site is typically delegated — bucketing per /64 would let
// anyone with an ordinary home allocation mint from 65,536 "different"
// addresses.
//
// Requests are bucketed more loosely, at /64; see RequestBucket. Minting
// is the scarce operation and gets the conservative boundary.
func MintBucket(remoteIP string) string { return bucket(remoteIP, 48) }

// RequestBucket groups addresses for per-request rate limiting, where a
// /64 is one LAN and grouping wider would make one household share a
// bucket with its neighbours.
func RequestBucket(remoteIP string) string { return bucket(remoteIP, 64) }

func bucket(remoteIP string, v6bits int) string {
	addr, err := netip.ParseAddr(remoteIP)
	if err != nil {
		// Not an address we can reason about. Group them all together, so
		// an unparseable RemoteAddr is throttled rather than unlimited.
		return "unknown"
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	p, err := addr.Prefix(v6bits)
	if err != nil {
		return addr.String()
	}
	return p.String()
}

// ClientIP extracts the caller's address from a request, trusting
// X-Forwarded-For only when told to.
//
// Behind Caddy the header is the only way to see the real client. Facing
// the internet directly it is attacker-controlled and trusting it would
// hand out unlimited identities to anyone who can set a header — so this
// is a deployment decision, made once at startup, not a heuristic.
func ClientIP(remoteAddr, forwardedFor string, trustProxy bool) string {
	if trustProxy && forwardedFor != "" {
		// The leftmost entry is the original client; everything after it
		// was added by hops we do trust.
		if i := indexByte(forwardedFor, ','); i >= 0 {
			forwardedFor = forwardedFor[:i]
		}
		if ip := trimSpace(forwardedFor); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
