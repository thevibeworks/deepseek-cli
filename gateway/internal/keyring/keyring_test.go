package keyring

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotationSpreadsAcrossKeys(t *testing.T) {
	r := New([]string{"sk-aaa", "sk-bbb", "sk-ccc"}, "")

	seen := map[string]int{}
	for i := 0; i < 9; i++ {
		secret, fp, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if fp == "" {
			t.Fatal("Next returned an empty fingerprint")
		}
		seen[secret]++
	}
	if len(seen) != 3 {
		t.Fatalf("nine requests touched %d keys, want 3", len(seen))
	}
	for k, n := range seen {
		if n != 3 {
			t.Errorf("key %q served %d of 9 requests, want an even 3", k, n)
		}
	}
}

// A key upstream has refused must leave the rotation immediately, and the
// rest must keep serving.
func TestDryKeysAreSkipped(t *testing.T) {
	r := New([]string{"sk-aaa", "sk-bbb"}, "")
	_, fp, _ := r.Next()
	r.MarkDry(fp, "402")

	for i := 0; i < 5; i++ {
		_, got, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if got == fp {
			t.Fatal("a dry key was handed out again")
		}
	}
	st := r.Status(false)
	if st.Active != 1 || st.Dry != 1 || st.Total != 2 {
		t.Errorf("status = %+v, want 1 active / 1 dry / 2 total", st)
	}
}

// "Out of credit" is a condition a donor can fix. It must not need an
// operator to undo, or every topped-up key silently stays out forever.
func TestDryKeysRecoverWhenFundedAgain(t *testing.T) {
	r := New([]string{"sk-aaa"}, "")
	_, fp, _ := r.Next()
	r.MarkDry(fp, "402")
	if r.Usable() {
		t.Fatal("a dry key was still usable")
	}

	if !r.MarkFunded(fp) {
		t.Fatal("MarkFunded did not find the dry key")
	}
	if !r.Usable() {
		t.Fatal("the key did not come back after upstream reported credit")
	}
	if got := r.Status(false); got.Active != 1 || got.Dry != 0 {
		t.Errorf("status = %+v, want 1 active / 0 dry", got)
	}
	// A second call is a no-op, so a steady state does not read as a
	// recovery event every poll.
	if r.MarkFunded(fp) {
		t.Error("MarkFunded reported a change for an already-funded key")
	}
}

// An operator retirement is the permanent one, and the balance watcher
// must not undo it.
func TestOperatorRetirementIsNotUndoneByAHealthyBalance(t *testing.T) {
	r := New([]string{"sk-aaa"}, "")
	_, fp, _ := r.Next()
	r.Retire(fp, "operator: suspected compromised")

	if r.MarkFunded(fp) {
		t.Error("MarkFunded revived an operator-retired key")
	}
	if r.Usable() {
		t.Fatal("an operator-retired key came back into rotation")
	}
	// Fingerprints is what the balance watcher iterates; a retired key
	// must not even be checked.
	for _, got := range r.Fingerprints() {
		if got == fp {
			t.Error("the balance watcher would still poll a retired key")
		}
	}
	if !r.Revive(fp) {
		t.Fatal("an operator could not revive their own retirement")
	}
	if !r.Usable() {
		t.Error("the revived key is not usable")
	}
}

func TestEmptyPoolIsAnError(t *testing.T) {
	r := New([]string{"sk-only"}, "")
	_, fp, _ := r.Next()
	r.MarkDry(fp, "402")

	if _, _, err := r.Next(); err != ErrNoKeys {
		t.Fatalf("err = %v, want ErrNoKeys", err)
	}
	if r.Usable() {
		t.Error("Usable reported true with every key retired")
	}
}

func TestDuplicateKeysCollapse(t *testing.T) {
	r := New([]string{"sk-same", "sk-same"}, "")
	if got := r.Status(false).Total; got != 1 {
		t.Errorf("total = %d, want 1 — the same key twice is one key", got)
	}
	if _, added := r.Donate("sk-same", "donor"); added {
		t.Error("donating an already-present key counted as new")
	}
}

// A secret must never appear in anything that gets serialised.
func TestStatusNeverCarriesTheSecret(t *testing.T) {
	const secret = "sk-super-secret-value-1234"
	r := New([]string{secret}, "")
	st := r.Status(true)

	if len(st.Keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(st.Keys))
	}
	k := st.Keys[0]
	if strings.Contains(k.Fingerprint, secret) {
		t.Fatal("the fingerprint contains the whole key")
	}
	// The tail is deliberately present; the body of the key must not be.
	if strings.Contains(k.Fingerprint, "super-secret") {
		t.Fatal("the fingerprint leaks the body of the key")
	}
	if k.Source != SourceConfig {
		t.Errorf("source = %q, want %q", k.Source, SourceConfig)
	}
}

func TestDonationsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "donated.json")

	r := New([]string{"sk-house"}, path)
	fp, added := r.Donate("sk-donated", "a kind stranger")
	if !added {
		t.Fatal("the donation was not accepted")
	}

	r2 := New([]string{"sk-house"}, path)
	st := r2.Status(true)
	if st.Total != 2 {
		t.Fatalf("after restart the pool has %d keys, want 2", st.Total)
	}
	var found bool
	for _, k := range st.Keys {
		if k.Fingerprint == fp {
			found = true
			if k.Source != SourceDonor {
				t.Errorf("restored source = %q, want %q", k.Source, SourceDonor)
			}
			if k.Label != "a kind stranger" {
				t.Errorf("restored label = %q", k.Label)
			}
		}
	}
	if !found {
		t.Error("the donated key did not come back")
	}
}

// Dryness has to persist too, or a restart puts an empty key straight
// back into rotation and every request through it fails until the next
// balance check.
func TestDrynessSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "donated.json")
	r := New(nil, path)
	fp, _ := r.Donate("sk-donated", "")
	r.MarkDry(fp, "402")

	r2 := New(nil, path)
	if got := r2.Status(false).Dry; got != 1 {
		t.Errorf("dry = %d after restart, want 1", got)
	}
	if r2.Usable() {
		t.Error("a dry donated key came back usable")
	}
}

func TestReviveAndRemove(t *testing.T) {
	r := New(nil, "")
	fp, _ := r.Donate("sk-donated", "")
	r.MarkDry(fp, "402")

	if !r.Revive(fp) {
		t.Fatal("Revive did not find the retired key")
	}
	if !r.Usable() {
		t.Error("the revived key is not usable")
	}
	if !r.Remove(fp) {
		t.Fatal("Remove did not find the key")
	}
	if got := r.Status(false).Total; got != 0 {
		t.Errorf("total = %d after removal, want 0", got)
	}
}

// Removing the key the cursor points past must not leave Next indexing
// off the end of the slice.
func TestRemoveKeepsTheCursorInRange(t *testing.T) {
	r := New([]string{"sk-a", "sk-b", "sk-c"}, "")
	r.Next()
	r.Next()
	r.Next()

	for _, k := range r.Status(true).Keys[:2] {
		r.Remove(k.Fingerprint)
	}
	if _, _, err := r.Next(); err != nil {
		t.Fatalf("Next after removals: %v", err)
	}
}

func TestFingerprintIsStableAndDistinct(t *testing.T) {
	a := Fingerprint("sk-one")
	if a != Fingerprint("sk-one") {
		t.Error("the same key fingerprinted differently twice")
	}
	if a == Fingerprint("sk-two") {
		t.Error("two different keys share a fingerprint")
	}
}

func TestRequestCountsAreTracked(t *testing.T) {
	r := New([]string{"sk-a"}, "")
	r.SetClock(func() time.Time { return time.Unix(1_700_000_000, 0) })
	for i := 0; i < 4; i++ {
		r.Next()
	}
	if got := r.Status(true).Keys[0].Requests; got != 4 {
		t.Errorf("requests = %d, want 4", got)
	}
}
