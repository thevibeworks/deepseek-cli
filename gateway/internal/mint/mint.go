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
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
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
	// StatePath, when set, persists the per-address issuance counts so a
	// restart does not reset every address's difficulty to base. Without
	// it a deploy would hand an attacker a fresh batch of cheap mints.
	StatePath string
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

// maxTrackedBuckets bounds the issuance-count map. Past it, an unseen
// bucket is treated as if it had exhausted its free mints: the map cannot
// be grown without bound by an address-distributed attacker, and during
// such an attack maximum difficulty for newcomers is the right answer
// anyway. 64k buckets at ~30 bytes each is about 2 MiB, which a 192 MiB
// container can hold.
const maxTrackedBuckets = 1 << 16

// maxOutstanding bounds the single-use set. Every entry cost its solver a
// proof of work, so reaching this bound means someone spent tens of CPU
// hours inside one TTL window — at which point refusing redemptions
// beats being OOM-killed.
const maxOutstanding = 1 << 16

// Mint issues and redeems challenges.
type Mint struct {
	signer *token.Signer
	cfg    Config

	mu sync.Mutex
	// day is the UTC date the per-address counts belong to.
	day string
	// issued counts challenges handed out per address bucket today. The
	// count moves at issuance, not redemption: difficulty priced off
	// completed mints could be bypassed by collecting a batch of cheap
	// challenges first and redeeming them later.
	issued map[string]int
	// redeemed is the single-use set for challenges, keyed by the random
	// component. Bounded by the mint rate times the TTL, and swept.
	redeemed map[[16]byte]time.Time

	now func() time.Time
}

func New(signer *token.Signer, cfg Config) *Mint {
	m := &Mint{
		signer:   signer,
		cfg:      cfg,
		issued:   map[string]int{},
		redeemed: map[[16]byte]time.Time{},
		now:      time.Now,
	}
	m.day = m.now().UTC().Format("2006-01-02")
	m.loadState()
	return m
}

// SetClock replaces the time source, for tests.
func (m *Mint) SetClock(now func() time.Time) {
	m.mu.Lock()
	m.now = now
	m.mu.Unlock()
	m.signer.SetClock(now)
}

// Challenge issues a puzzle sized to how many challenges this address has
// already been given today, and bound to the address so it cannot be
// redeemed from anywhere else.
//
// The issuance count is spent here, before the client has solved
// anything. Spending it at redemption instead would let an attacker
// collect a day's worth of base-difficulty challenges up front and solve
// them at leisure, which is exactly the escalation this exists to prevent.
// The cost is that an address which requests challenges and never redeems
// them escalates itself — which only hurts someone deliberately doing that.
func (m *Mint) Challenge(remoteIP string) (*token.Challenge, error) {
	bucket := MintBucket(remoteIP)

	m.mu.Lock()
	m.rollLocked()
	k, tracked := m.nextLocked(bucket)
	if tracked {
		m.issued[bucket] = k
		m.saveStateLocked()
	}
	m.mu.Unlock()

	return m.signer.NewChallenge(m.difficulty(k), token.Bind(bucket))
}

// nextLocked is the 1-indexed number of the challenge this bucket would
// be issued next, reporting whether the bucket can still be tracked. When
// the map is at its bound, unseen buckets are priced at maximum: during
// an address-distributed attack that is the right answer, and honest
// newcomers recover at the daily reset.
func (m *Mint) nextLocked(bucket string) (int, bool) {
	if n, seen := m.issued[bucket]; seen {
		return n + 1, true
	}
	if len(m.issued) >= maxTrackedBuckets {
		return m.cfg.FreeMints + maxEscalation/escalationBits + 1, false
	}
	return 1, true
}

// difficulty is the required leading-zero-bit count for an address's k-th
// challenge of the day. The first FreeMints are at base; every one past
// that costs escalationBits more.
func (m *Mint) difficulty(k int) uint8 {
	extra := k - m.cfg.FreeMints
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
	if c.Binding != token.Bind(MintBucket(remoteIP)) {
		// The challenge was issued to a different address bucket. Honouring
		// it would let one cheap address farm challenges for a fleet.
		return nil, fmt.Errorf("%w: challenge was issued to a different address", token.ErrMalformed)
	}

	m.mu.Lock()
	m.rollLocked()
	m.sweepLocked()
	if _, used := m.redeemed[c.ID]; used {
		m.mu.Unlock()
		// One solved challenge is one token. Without this, a single proof
		// of work would mint an unlimited supply.
		return nil, fmt.Errorf("%w: challenge already redeemed", token.ErrMalformed)
	}
	if len(m.redeemed) >= maxOutstanding {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: the mint is saturated; retry shortly", token.ErrExpired)
	}
	m.redeemed[c.ID] = m.now()
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
	k, _ := m.nextLocked(MintBucket(remoteIP))
	return m.difficulty(k)
}

func (m *Mint) rollLocked() {
	day := m.now().UTC().Format("2006-01-02")
	if day == m.day {
		return
	}
	m.day = day
	m.issued = map[string]int{}
	m.saveStateLocked()
}

// mintState is the persisted shape of the issuance counts.
type mintState struct {
	Day    string         `json:"day"`
	Issued map[string]int `json:"issued"`
}

// loadState restores the day's issuance counts, so a restart is not a
// difficulty amnesty. Only counts from the current UTC day are honoured.
// Errors are deliberately soft: mint state is an anti-abuse position, not
// money, and refusing to boot over a corrupt count file would be a worse
// trade than starting the day over.
func (m *Mint) loadState() {
	if m.cfg.StatePath == "" {
		return
	}
	b, err := os.ReadFile(m.cfg.StatePath)
	if err != nil {
		return
	}
	var st mintState
	if json.Unmarshal(b, &st) != nil || st.Day != m.day || st.Issued == nil {
		return
	}
	if len(st.Issued) > maxTrackedBuckets {
		return
	}
	m.issued = st.Issued
}

// saveStateLocked persists the issuance counts. Atomic rename, so a crash
// mid-write leaves the previous snapshot rather than a truncated one.
func (m *Mint) saveStateLocked() {
	if m.cfg.StatePath == "" {
		return
	}
	b, err := json.Marshal(mintState{Day: m.day, Issued: m.issued})
	if err != nil {
		return
	}
	tmp := m.cfg.StatePath + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	os.Rename(tmp, m.cfg.StatePath)
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
