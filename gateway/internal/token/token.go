// Package token is the credential codec for the free tier: proof-of-work
// challenges and the bearer tokens they mint.
//
// Both are self-describing and HMAC-signed, so verifying either is one
// hash and no lookup. That is the point — the gateway holds no user
// table, and an anonymous user is not a row in a database, just a
// 16-byte subject that a signature vouches for.
//
// This package is pure: it does not know about HTTP, replay, difficulty
// policy or quota. Those live in package mint and package quota, which
// is what makes the crypto here testable on its own.
package token

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"time"
)

// Wire format versions. A bump invalidates every outstanding credential,
// which is the intended blast radius for a format change.
const (
	challengeVersion = 1
	tokenVersion     = 1
)

// Tier is what a token is allowed to do. Only Anon is issued today;
// GitHub is reserved for the sign-in upgrade rung described in DESIGN.md
// and is defined here so the wire format does not have to change when it
// lands.
type Tier uint8

const (
	TierAnon   Tier = 1
	TierGitHub Tier = 2
)

func (t Tier) String() string {
	switch t {
	case TierAnon:
		return "anon"
	case TierGitHub:
		return "github"
	default:
		return "tier" + strconv.Itoa(int(t))
	}
}

// TokenPrefix marks a free-tier credential in an Authorization header.
// It exists so that a token pasted somewhere it does not belong is
// recognisable on sight, and so the gateway can tell "this is one of
// ours" from "this is a real DeepSeek key" without trying both.
const TokenPrefix = "dsf_"

var (
	ErrMalformed = errors.New("malformed credential")
	ErrSignature = errors.New("bad signature")
	ErrExpired   = errors.New("expired")
	ErrWork      = errors.New("insufficient proof of work")
)

// enc is unpadded base64url: URL-safe, header-safe, and — the reason it
// is this and not hex — already inside DeepSeek's user_id character rule
// of [a-zA-Z0-9\-_]+, so a subject can be passed upstream verbatim.
var enc = base64.RawURLEncoding

// Signer mints and verifies credentials with one secret.
type Signer struct {
	secret []byte
	// now is swappable so tests can age a credential without sleeping.
	now func() time.Time
}

// NewSigner builds a Signer. The secret must be at least 32 bytes: it is
// the only thing standing between a stranger and an unlimited supply of
// free-tier tokens.
func NewSigner(secret []byte) (*Signer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("signing secret is %d bytes, need at least 32", len(secret))
	}
	return &Signer{secret: append([]byte(nil), secret...), now: time.Now}, nil
}

// SetClock replaces the time source. Test-only in practice, but exported
// because the mint's tests live in another package.
func (s *Signer) SetClock(now func() time.Time) { s.now = now }

func (s *Signer) mac(domain string, payload []byte) []byte {
	h := hmac.New(sha256.New, s.secret)
	// Domain separation: a challenge payload must never verify as a token
	// payload even if the byte layouts happen to line up.
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write(payload)
	return h.Sum(nil)
}

// --- challenges ---------------------------------------------------------

// challengePayload is version(1) | difficulty(1) | issued(4) | nonce(16).
const challengePayloadLen = 1 + 1 + 4 + 16

// Challenge is a proof-of-work puzzle the gateway issued. The difficulty
// travels inside the signed payload so the client cannot negotiate it
// down, and the whole thing is stateless — nothing is stored until it
// comes back to be redeemed.
type Challenge struct {
	// String is what the client hashes and sends back.
	String string
	// ID identifies this challenge for single-use enforcement. It is the
	// random component, not the whole string, so the replay set stores 16
	// bytes per outstanding challenge rather than the full credential.
	ID [16]byte
	// Difficulty is the required number of leading zero bits.
	Difficulty uint8
	Issued     time.Time
}

// NewChallenge issues a puzzle at the given difficulty.
func (s *Signer) NewChallenge(difficulty uint8) (*Challenge, error) {
	if difficulty > 40 {
		// 2^40 hashes is hours of CPU. A difficulty this high is a bug in
		// the escalation policy, and shipping it would silently lock out
		// every user behind a busy NAT.
		return nil, fmt.Errorf("difficulty %d is out of range", difficulty)
	}
	var c Challenge
	c.Difficulty = difficulty
	c.Issued = s.now().UTC()
	if _, err := rand.Read(c.ID[:]); err != nil {
		return nil, err
	}

	payload := make([]byte, challengePayloadLen)
	payload[0] = challengeVersion
	payload[1] = difficulty
	putUint32(payload[2:6], uint32(c.Issued.Unix()))
	copy(payload[6:], c.ID[:])

	c.String = enc.EncodeToString(payload) + "." + enc.EncodeToString(s.mac("challenge", payload)[:16])
	return &c, nil
}

// ParseChallenge verifies a challenge came from us and has not aged out.
// It does not check the proof of work — see Verify — and it does not
// check single use, which needs state the caller owns.
func (s *Signer) ParseChallenge(raw string, ttl time.Duration) (*Challenge, error) {
	payload, err := s.split(raw, "challenge", challengePayloadLen)
	if err != nil {
		return nil, err
	}
	if payload[0] != challengeVersion {
		return nil, fmt.Errorf("%w: challenge version %d", ErrMalformed, payload[0])
	}

	c := &Challenge{
		String:     raw,
		Difficulty: payload[1],
		Issued:     time.Unix(int64(uint32From(payload[2:6])), 0).UTC(),
	}
	copy(c.ID[:], payload[6:])

	age := s.now().Sub(c.Issued)
	if age > ttl {
		return nil, fmt.Errorf("%w: challenge is %s old", ErrExpired, age.Round(time.Second))
	}
	// A challenge from the future means a clock jumped or someone is
	// fishing. Either way it is not one we should honour.
	if age < -time.Minute {
		return nil, fmt.Errorf("%w: challenge is dated in the future", ErrMalformed)
	}
	return c, nil
}

// Verify checks that nonce solves the challenge.
//
// The hash is over the literal ASCII "<challenge>:<nonce>", chosen so
// that the browser playground can reimplement it in a few lines of
// WebCrypto and get identical answers. Anything cleverer would have been
// a second implementation to keep in sync.
func Verify(challenge string, difficulty uint8, nonce uint64) error {
	if LeadingZeroBits(PoWDigest(challenge, nonce)) < difficulty {
		return fmt.Errorf("%w: need %d leading zero bits", ErrWork, difficulty)
	}
	return nil
}

// PoWDigest is the hash a solver iterates.
func PoWDigest(challenge string, nonce uint64) [32]byte {
	buf := make([]byte, 0, len(challenge)+21)
	buf = append(buf, challenge...)
	buf = append(buf, ':')
	buf = strconv.AppendUint(buf, nonce, 10)
	return sha256.Sum256(buf)
}

// LeadingZeroBits counts zero bits from the most significant end.
//
// A digest of all zeroes has 256 of them, which does not fit the uint8
// this returns. It saturates at 255 rather than wrapping — wrapping would
// report the most extraordinary proof of work ever produced as the worst
// possible one. Nothing asks for more than 40 bits, so the saturation
// point is unreachable in practice; getting the direction right is free.
func LeadingZeroBits(digest [32]byte) uint8 {
	n := 0
	for _, b := range digest {
		if b != 0 {
			return uint8(n + bits.LeadingZeros8(b))
		}
		n += 8
	}
	return 255
}

// Solve finds a nonce satisfying the challenge. It is the reference
// solver: the CLI ships its own copy so it need not import this module,
// and this one keeps that copy honest in tests.
//
// limit bounds the search so a mistuned difficulty fails loudly instead
// of spinning forever.
func Solve(challenge string, difficulty uint8, limit uint64) (uint64, error) {
	for nonce := uint64(0); nonce < limit; nonce++ {
		if LeadingZeroBits(PoWDigest(challenge, nonce)) >= difficulty {
			return nonce, nil
		}
	}
	return 0, fmt.Errorf("%w: no solution below %d", ErrWork, limit)
}

// --- tokens -------------------------------------------------------------

// tokenPayload is version(1) | tier(1) | issued(4) | subject(16).
const tokenPayloadLen = 1 + 1 + 4 + 16

// Token is a minted free-tier credential.
type Token struct {
	// String is the bearer value the client sends.
	String  string
	Subject Subject
	Tier    Tier
	Issued  time.Time
}

// Subject is the anonymous account identity. It is also what gets sent
// upstream as DeepSeek's user_id, which is what buys content-safety
// attribution, KV cache isolation and per-user scheduling — see DESIGN.md.
type Subject [16]byte

// String renders the subject for use as a map key, a journal field and a
// DeepSeek user_id. base64url of 16 bytes is 22 characters, all of which
// are inside their [a-zA-Z0-9\-_]+ rule.
func (s Subject) String() string { return enc.EncodeToString(s[:]) }

// NewToken mints a credential for a fresh random subject.
func (s *Signer) NewToken(tier Tier) (*Token, error) {
	var sub Subject
	if _, err := rand.Read(sub[:]); err != nil {
		return nil, err
	}
	return s.tokenFor(sub, tier, s.now().UTC())
}

func (s *Signer) tokenFor(sub Subject, tier Tier, issued time.Time) (*Token, error) {
	payload := make([]byte, tokenPayloadLen)
	payload[0] = tokenVersion
	payload[1] = byte(tier)
	putUint32(payload[2:6], uint32(issued.Unix()))
	copy(payload[6:], sub[:])

	return &Token{
		String:  TokenPrefix + enc.EncodeToString(payload) + "." + enc.EncodeToString(s.mac("token", payload)),
		Subject: sub,
		Tier:    tier,
		Issued:  issued,
	}, nil
}

// ParseToken verifies a bearer value and returns what it vouches for.
//
// There is no expiry check. A free-tier token is a name, not a lease —
// quota resets daily and is enforced by the counters, so ageing tokens
// out would only make honest users re-mint for no security gain. Ending
// a token's life is what revocation is for.
func (s *Signer) ParseToken(raw string) (*Token, error) {
	if len(raw) <= len(TokenPrefix) || raw[:len(TokenPrefix)] != TokenPrefix {
		return nil, fmt.Errorf("%w: not a free-tier token", ErrMalformed)
	}
	payload, err := s.split(raw[len(TokenPrefix):], "token", tokenPayloadLen)
	if err != nil {
		return nil, err
	}
	if payload[0] != tokenVersion {
		return nil, fmt.Errorf("%w: token version %d", ErrMalformed, payload[0])
	}
	t := &Token{
		String: raw,
		Tier:   Tier(payload[1]),
		Issued: time.Unix(int64(uint32From(payload[2:6])), 0).UTC(),
	}
	copy(t.Subject[:], payload[6:])
	return t, nil
}

// IsToken reports whether a credential looks like one of ours, without
// verifying it. Used to tell a free-tier token from a real DeepSeek key
// before deciding which failure to report.
func IsToken(raw string) bool {
	return len(raw) > len(TokenPrefix) && raw[:len(TokenPrefix)] == TokenPrefix
}

// split validates the "payload.mac" envelope common to both credentials
// and returns the payload.
func (s *Signer) split(raw, domain string, wantLen int) ([]byte, error) {
	dot := -1
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return nil, fmt.Errorf("%w: no signature", ErrMalformed)
	}
	payload, err := enc.DecodeString(raw[:dot])
	if err != nil {
		return nil, fmt.Errorf("%w: payload is not base64url", ErrMalformed)
	}
	sig, err := enc.DecodeString(raw[dot+1:])
	if err != nil {
		return nil, fmt.Errorf("%w: signature is not base64url", ErrMalformed)
	}
	if len(payload) != wantLen {
		return nil, fmt.Errorf("%w: payload is %d bytes, want %d", ErrMalformed, len(payload), wantLen)
	}

	want := s.mac(domain, payload)
	if len(sig) == 0 || len(sig) > len(want) {
		return nil, ErrSignature
	}
	// Constant time, and length-tolerant so a truncated MAC (challenges
	// carry 16 bytes, tokens 32) compares against its own prefix.
	if subtle.ConstantTimeCompare(sig, want[:len(sig)]) != 1 {
		return nil, ErrSignature
	}
	return payload, nil
}

func putUint32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func uint32From(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
