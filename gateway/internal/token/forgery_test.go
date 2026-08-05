package token

import (
	"strings"
	"testing"
	"time"
)

// A credential is only as good as the length of the MAC it is checked
// against. These are the tests that were missing when split() compared a
// caller-supplied signature against its own prefix: a one-byte MAC
// verified against one byte of the real one, so 256 guesses forged any
// payload without the secret.

// truncate shortens the MAC of a "payload.mac" credential to n bytes,
// keeping the payload and the prefix intact.
func truncate(t *testing.T, raw string, n int) string {
	t.Helper()
	prefix := ""
	if strings.HasPrefix(raw, TokenPrefix) {
		prefix, raw = TokenPrefix, raw[len(TokenPrefix):]
	}
	dot := strings.LastIndexByte(raw, '.')
	if dot < 0 {
		t.Fatalf("no signature in %q", raw)
	}
	mac, err := enc.DecodeString(raw[dot+1:])
	if err != nil {
		t.Fatal(err)
	}
	if n > len(mac) {
		t.Fatalf("cannot truncate a %d byte MAC to %d", len(mac), n)
	}
	return prefix + raw[:dot+1] + enc.EncodeToString(mac[:n])
}

func TestTokenRejectsTruncatedMAC(t *testing.T) {
	s := testSigner(t)
	tok, err := s.NewToken(TierAnon)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ParseToken(tok.String); err != nil {
		t.Fatalf("a freshly minted token must verify: %v", err)
	}

	for n := 1; n < tokenMACLen; n++ {
		if _, err := s.ParseToken(truncate(t, tok.String, n)); err == nil {
			t.Errorf("a %d-byte MAC was accepted on a token; only %d bytes may be", n, tokenMACLen)
		}
	}
}

func TestChallengeRejectsTruncatedMAC(t *testing.T) {
	s := testSigner(t)
	c, err := s.NewChallenge(8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ParseChallenge(c.String, time.Minute); err != nil {
		t.Fatalf("a freshly issued challenge must verify: %v", err)
	}

	for n := 1; n < challengeMACLen; n++ {
		if _, err := s.ParseChallenge(truncate(t, c.String, n), time.Minute); err == nil {
			t.Errorf("a %d-byte MAC was accepted on a challenge; only %d bytes may be", n, challengeMACLen)
		}
	}
}

// TestCannotForgeTokenBySearchingShortMACs is the attack itself: pick a
// subject, then walk every one-byte signature. One of the 256 matches the
// first byte of the real MAC, so before the fix this minted a valid token
// for an attacker-chosen subject in well under a second.
func TestCannotForgeTokenBySearchingShortMACs(t *testing.T) {
	s := testSigner(t)

	payload := make([]byte, tokenPayloadLen)
	payload[0] = tokenVersion
	payload[1] = byte(TierAnon)
	putUint32(payload[2:6], uint32(time.Now().Unix()))
	copy(payload[6:], []byte("ATTACKERSUBJECT!"))

	for b := 0; b < 256; b++ {
		raw := TokenPrefix + enc.EncodeToString(payload) + "." + enc.EncodeToString([]byte{byte(b)})
		if tok, err := s.ParseToken(raw); err == nil {
			t.Fatalf("forged a token for subject %q with a one-byte MAC after %d tries", tok.Subject, b+1)
		}
	}
}

// An over-long MAC must not be accepted either: the check is equality,
// not "starts with".
func TestTokenRejectsOverlongMAC(t *testing.T) {
	s := testSigner(t)
	tok, err := s.NewToken(TierAnon)
	if err != nil {
		t.Fatal(err)
	}
	dot := strings.LastIndexByte(tok.String, '.')
	mac, err := enc.DecodeString(tok.String[dot+1:])
	if err != nil {
		t.Fatal(err)
	}
	raw := tok.String[:dot+1] + enc.EncodeToString(append(mac, 0))
	if _, err := s.ParseToken(raw); err == nil {
		t.Error("a MAC one byte longer than the real one was accepted")
	}
}

// A challenge MAC must not verify as a token MAC or the other way round,
// even now that both lengths are pinned.
func TestDomainSeparationHolds(t *testing.T) {
	s := testSigner(t)
	c, err := s.NewChallenge(8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ParseToken(TokenPrefix + c.String); err == nil {
		t.Error("a challenge verified as a token")
	}
}
