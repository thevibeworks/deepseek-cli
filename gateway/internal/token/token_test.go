package token

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

func TestSignerRejectsWeakSecret(t *testing.T) {
	if _, err := NewSigner([]byte("short")); err == nil {
		t.Fatal("accepted a 5-byte secret; the secret is the only thing between a stranger and unlimited tokens")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	s := testSigner(t)
	tok, err := s.NewToken(TierAnon)
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if !strings.HasPrefix(tok.String, TokenPrefix) {
		t.Errorf("token %q lacks the %q prefix", tok.String, TokenPrefix)
	}

	got, err := s.ParseToken(tok.String)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if got.Subject != tok.Subject {
		t.Errorf("subject round trip: got %v want %v", got.Subject, tok.Subject)
	}
	if got.Tier != TierAnon {
		t.Errorf("tier = %v", got.Tier)
	}
}

// The subject is sent upstream verbatim as DeepSeek's user_id, whose
// documented rule is [a-zA-Z0-9\-_]+ with a 512 character maximum. A
// subject that violated it would 400 every request the gateway made.
func TestSubjectIsAValidDeepSeekUserID(t *testing.T) {
	s := testSigner(t)
	for i := 0; i < 200; i++ {
		tok, err := s.NewToken(TierAnon)
		if err != nil {
			t.Fatal(err)
		}
		sub := tok.Subject.String()
		if sub == "" || len(sub) > 512 {
			t.Fatalf("subject %q has length %d", sub, len(sub))
		}
		for _, r := range sub {
			ok := r == '-' || r == '_' ||
				(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			if !ok {
				t.Fatalf("subject %q contains %q, outside DeepSeek's user_id rule", sub, r)
			}
		}
	}
}

func TestTokenRejectsTampering(t *testing.T) {
	s := testSigner(t)
	tok, _ := s.NewToken(TierAnon)

	body := tok.String[len(TokenPrefix):]
	dot := strings.LastIndex(body, ".")

	cases := map[string]string{
		"flipped payload byte": TokenPrefix + flip(body[:dot]) + "." + body[dot+1:],
		"flipped mac byte":     TokenPrefix + body[:dot] + "." + flip(body[dot+1:]),
		"no prefix":            body,
		"no signature":         TokenPrefix + body[:dot],
		"empty":                "",
		"a real-looking key":   "sk-0123456789abcdef",
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.ParseToken(bad); err == nil {
				t.Errorf("ParseToken accepted %q", bad)
			}
		})
	}
}

// A different gateway's tokens must not verify here, or a second
// deployment would silently spend the first one's budget.
func TestTokenIsBoundToItsSecret(t *testing.T) {
	a := testSigner(t)
	b, _ := NewSigner([]byte("ffffffffffffffffffffffffffffffff"))

	tok, _ := a.NewToken(TierAnon)
	if _, err := b.ParseToken(tok.String); !errors.Is(err, ErrSignature) {
		t.Errorf("a token signed by another secret verified: %v", err)
	}
}

// The two credentials share a signing key and nearly share a layout. If
// domain separation were missing, a challenge could be presented as a
// token — minting an identity with no proof of work at all.
func TestChallengeCannotBePresentedAsAToken(t *testing.T) {
	s := testSigner(t)
	c, err := s.NewChallenge(8, Bind("test"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ParseToken(TokenPrefix + c.String); err == nil {
		t.Fatal("a challenge verified as a token")
	}
	tok, _ := s.NewToken(TierAnon)
	if _, err := s.ParseChallenge(strings.TrimPrefix(tok.String, TokenPrefix), time.Hour); err == nil {
		t.Fatal("a token verified as a challenge")
	}
}

func TestChallengeCarriesItsOwnDifficulty(t *testing.T) {
	s := testSigner(t)
	c, err := s.NewChallenge(14, Bind("test"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ParseChallenge(c.String, 5*time.Minute)
	if err != nil {
		t.Fatalf("ParseChallenge: %v", err)
	}
	if got.Difficulty != 14 {
		t.Errorf("difficulty = %d, want 14", got.Difficulty)
	}
	if got.ID != c.ID {
		t.Error("challenge ID did not survive the round trip; single-use enforcement keys off it")
	}
}

// The difficulty is inside the signed payload precisely so a client
// cannot ask for an easier puzzle than the one it was given.
func TestChallengeDifficultyCannotBeEdited(t *testing.T) {
	s := testSigner(t)
	c, _ := s.NewChallenge(20, Bind("test"))

	dot := strings.LastIndex(c.String, ".")
	forged := flip(c.String[:dot]) + "." + c.String[dot+1:]
	if _, err := s.ParseChallenge(forged, 5*time.Minute); err == nil {
		t.Fatal("an edited challenge payload still verified")
	}
}

func TestChallengeExpires(t *testing.T) {
	s := testSigner(t)
	now := time.Now()
	s.SetClock(func() time.Time { return now })

	c, _ := s.NewChallenge(8, Bind("test"))
	if _, err := s.ParseChallenge(c.String, time.Minute); err != nil {
		t.Fatalf("fresh challenge rejected: %v", err)
	}

	now = now.Add(2 * time.Minute)
	if _, err := s.ParseChallenge(c.String, time.Minute); !errors.Is(err, ErrExpired) {
		t.Errorf("stale challenge accepted: %v", err)
	}
}

func TestProofOfWork(t *testing.T) {
	s := testSigner(t)
	c, _ := s.NewChallenge(12, Bind("test"))

	nonce, err := Solve(c.String, c.Difficulty, 1<<22)
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if err := Verify(c.String, c.Difficulty, nonce); err != nil {
		t.Errorf("the solver produced a nonce Verify rejects: %v", err)
	}
	// A solution to one challenge must not satisfy another, or one proof
	// of work would mint an unlimited supply.
	other, _ := s.NewChallenge(12, Bind("test"))
	if err := Verify(other.String, other.Difficulty, nonce); err == nil {
		t.Error("a nonce solved for one challenge also solved another")
	}
}

// The proof-of-work is implemented twice — here, and in the CLI at
// internal/deepseek/free.go — because the two are separate Go modules
// and neither imports the other. This table is the contract between
// them: the identical one appears in the CLI's tests, so a change to
// either hash construction fails on both sides instead of silently
// making every enrolment attempt fail in production.
//
//	sha256("<challenge>:<nonce>") has >= difficulty leading zero bits,
//	nonce being the smallest such value.
var powVectors = []struct {
	Challenge  string
	Difficulty uint8
	Nonce      uint64
}{
	{"dsgate-protocol-vector.v1", 8, 148},
	{"dsgate-protocol-vector.v1", 12, 2601},
	{"dsgate-protocol-vector.v1", 16, 28337},
	{"abc.def", 8, 125},
	{"abc.def", 12, 1917},
}

func TestProofOfWorkVectors(t *testing.T) {
	for _, v := range powVectors {
		if err := Verify(v.Challenge, v.Difficulty, v.Nonce); err != nil {
			t.Errorf("Verify(%q, %d, %d): %v — this vector is shared with the CLI, so the two halves have diverged",
				v.Challenge, v.Difficulty, v.Nonce, err)
		}
		// And it is the smallest such nonce, which is what a solver
		// searching upward from zero has to find.
		for n := uint64(0); n < v.Nonce; n++ {
			if LeadingZeroBits(PoWDigest(v.Challenge, n)) >= v.Difficulty {
				t.Fatalf("%q at %d bits: nonce %d also solves it, before the vector's %d",
					v.Challenge, v.Difficulty, n, v.Nonce)
			}
		}
	}
}

func TestLeadingZeroBits(t *testing.T) {
	var d [32]byte
	if got := LeadingZeroBits(d); got != 255 {
		// All-zero saturates the uint8 counter at 255 rather than 256;
		// nothing in the system asks for more than 40, so this only has to
		// not wrap to zero.
		t.Errorf("all-zero digest = %d, want 255", got)
	}
	d[0] = 0x80
	if got := LeadingZeroBits(d); got != 0 {
		t.Errorf("0x80... = %d, want 0", got)
	}
	d[0] = 0x01
	if got := LeadingZeroBits(d); got != 7 {
		t.Errorf("0x01... = %d, want 7", got)
	}
	d[0], d[1] = 0x00, 0x0f
	if got := LeadingZeroBits(d); got != 12 {
		t.Errorf("0x000f... = %d, want 12", got)
	}
}

func TestNewChallengeRefusesAbsurdDifficulty(t *testing.T) {
	s := testSigner(t)
	if _, err := s.NewChallenge(64, Bind("test")); err == nil {
		t.Fatal("issued a 64-bit challenge; nobody could ever solve it")
	}
}

// flip changes one character of a base64url string to a different valid one.
func flip(s string) string {
	if s == "" {
		return "A"
	}
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}
