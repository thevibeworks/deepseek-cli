// Package keyring holds the upstream API keys this service spends and
// decides which one pays for the next request.
//
// One key was always a single point of failure with a hard floor: when it
// empties, the free tier is over until someone is at a keyboard. A pool
// changes that into a queue — a donated key is added at runtime, an
// emptied one steps aside on the first refusal upstream, and the service
// keeps answering across the seam.
//
// Three rules shape everything here:
//
// A key never leaves this package in full. Every accessor returns a
// fingerprint — the last four characters and a truncated SHA-256 — which
// is enough to tell two keys apart in a log or a dashboard and useless to
// anyone who steals the output.
//
// "Out of credit" is upstream's verdict, not ours. Our ledger only knows
// what this gateway spent; the key may be funding something else as well,
// so the authority on "this key is done" is DeepSeek answering 402 or
// reporting no balance.
//
// And that verdict is reversible. A dry key is re-checked on every
// balance cycle and returns to rotation the moment it has credit again —
// a donor who tops their key up should not have to tell anyone. Only an
// operator's Retire is permanent, because only a human knows a key is
// compromised rather than merely empty.
package keyring

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

// ErrNoKeys means the pool has nothing left to spend.
var ErrNoKeys = errors.New("no upstream key is currently usable")

// Source records where a key came from, so an operator can tell the
// service's own key from something a stranger sent in.
type Source string

const (
	SourceConfig Source = "config"
	SourceDonor  Source = "donor"
)

// Key is one credential in the pool.
type Key struct {
	secret string

	// Fingerprint identifies the key in output without revealing it.
	Fingerprint string    `json:"fingerprint"`
	Label       string    `json:"label,omitempty"`
	Source      Source    `json:"source"`
	AddedAt     time.Time `json:"added_at"`

	// Retired is the operator's decision: this key is out, and stays out
	// until someone says otherwise. Nothing automatic sets it.
	Retired   bool      `json:"retired"`
	RetiredAt time.Time `json:"retired_at,omitempty"`
	Reason    string    `json:"retired_reason,omitempty"`

	// Dry is upstream's verdict: DeepSeek refused this key for money or
	// validity. It is deliberately *not* permanent — a donor who tops the
	// key up should not need an operator to notice. The balance watcher
	// re-checks dry keys every cycle and clears this when they answer for
	// themselves again.
	Dry       bool      `json:"dry"`
	DryAt     time.Time `json:"dry_at,omitempty"`
	DryReason string    `json:"dry_reason,omitempty"`

	Requests int64 `json:"requests"`
}

// Available reports whether this key may pay for a request.
func (k Key) Available() bool { return !k.Retired && !k.Dry }

// Ring is the pool.
type Ring struct {
	mu   sync.Mutex
	keys []*Key
	next int
	path string

	now func() time.Time
}

// Fingerprint renders a key as something safe to print. Two keys collide
// only if they share both their tail and a 6-byte hash prefix, which does
// not happen by accident.
func Fingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	tail := secret
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	return "…" + tail + "/" + hex.EncodeToString(sum[:6])
}

// New builds a ring from the configured keys. Duplicates collapse: the
// same key added twice is one key, not two turns in the rotation.
func New(secrets []string, statePath string) *Ring {
	r := &Ring{path: statePath, now: time.Now}
	for _, s := range secrets {
		r.add(strings.TrimSpace(s), SourceConfig, "")
	}
	r.loadDonors()
	return r
}

// SetClock replaces the time source, for tests.
func (r *Ring) SetClock(now func() time.Time) {
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}

func (r *Ring) add(secret string, src Source, label string) bool {
	if secret == "" {
		return false
	}
	fp := Fingerprint(secret)
	for _, k := range r.keys {
		if k.Fingerprint == fp {
			return false
		}
	}
	r.keys = append(r.keys, &Key{
		secret:      secret,
		Fingerprint: fp,
		Label:       label,
		Source:      src,
		AddedAt:     r.now(),
	})
	return true
}

// Next hands out the key that should pay for the next request, round
// robin over everything not retired.
//
// The rotation is per request rather than per key-until-empty on purpose:
// DeepSeek's rate limits and KV cache are per account, so spreading the
// load spreads the scheduling too. It also means a key that is about to
// be retired takes one request to discover that, not a whole burst.
func (r *Ring) Next() (secret string, fingerprint string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := 0; i < len(r.keys); i++ {
		k := r.keys[(r.next+i)%len(r.keys)]
		if !k.Available() {
			continue
		}
		r.next = (r.next + i + 1) % len(r.keys)
		k.Requests++
		return k.secret, k.Fingerprint, nil
	}
	return "", "", ErrNoKeys
}

// Retire takes a key out of rotation permanently. This is an operator
// action; upstream refusals call MarkDry instead, because "no balance"
// is a condition a donor can fix and should not need a human to undo.
func (r *Ring) Retire(fingerprint, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.keys {
		if k.Fingerprint == fingerprint && !k.Retired {
			k.Retired = true
			k.RetiredAt = r.now()
			k.Reason = reason
			r.saveDonorsLocked()
			return
		}
	}
}

// MarkDry records that upstream refused this key. Reversible by design:
// see Key.Dry.
func (r *Ring) MarkDry(fingerprint, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.keys {
		if k.Fingerprint == fingerprint && !k.Dry {
			k.Dry = true
			k.DryAt = r.now()
			k.DryReason = reason
			r.saveDonorsLocked()
			return
		}
	}
}

// MarkFunded clears the dry flag after upstream answers for the key
// again. Reports whether anything changed, so the caller can log a
// recovery rather than a steady state.
func (r *Ring) MarkFunded(fingerprint string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.keys {
		if k.Fingerprint == fingerprint && k.Dry {
			k.Dry, k.DryAt, k.DryReason = false, time.Time{}, ""
			r.saveDonorsLocked()
			return true
		}
	}
	return false
}

// Revive undoes an operator retirement and any dry flag with it.
func (r *Ring) Revive(fingerprint string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.keys {
		if k.Fingerprint == fingerprint && (k.Retired || k.Dry) {
			k.Retired, k.RetiredAt, k.Reason = false, time.Time{}, ""
			k.Dry, k.DryAt, k.DryReason = false, time.Time{}, ""
			r.saveDonorsLocked()
			return true
		}
	}
	return false
}

// Fingerprints lists every key, so a caller that must visit each one —
// the balance watcher — can do so without holding the lock while it
// makes network calls.
func (r *Ring) Fingerprints() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.keys))
	for _, k := range r.keys {
		if !k.Retired {
			out = append(out, k.Fingerprint)
		}
	}
	return out
}

// Donate adds a key at runtime and persists it, so a restart does not
// throw away a gift. Reports whether it was new.
func (r *Ring) Donate(secret, label string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	secret = strings.TrimSpace(secret)
	if !r.add(secret, SourceDonor, label) {
		return Fingerprint(secret), false
	}
	r.saveDonorsLocked()
	return Fingerprint(secret), true
}

// Remove drops a key entirely, for a donor who wants theirs back out.
func (r *Ring) Remove(fingerprint string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, k := range r.keys {
		if k.Fingerprint == fingerprint {
			r.keys = append(r.keys[:i], r.keys[i+1:]...)
			if r.next > len(r.keys) {
				r.next = 0
			}
			r.saveDonorsLocked()
			return true
		}
	}
	return false
}

// Status is the pool as the dashboard and the operator see it. No secret
// is reachable from this type.
type Status struct {
	Active  int   `json:"active"`
	Dry     int   `json:"dry"`
	Retired int   `json:"retired"`
	Total   int   `json:"total"`
	Keys    []Key `json:"keys,omitempty"`
}

// Status reports the pool. Keys carry fingerprints only.
func (r *Ring) Status(includeKeys bool) Status {
	r.mu.Lock()
	defer r.mu.Unlock()

	var st Status
	for _, k := range r.keys {
		st.Total++
		switch {
		case k.Available():
			st.Active++
		case k.Retired:
			st.Retired++
		default:
			st.Dry++
		}
		if includeKeys {
			st.Keys = append(st.Keys, *k)
		}
	}
	return st
}

// Secret returns one key's value by fingerprint, for the balance check
// that has to authenticate as that key specifically. It is the only way
// a secret leaves this package, and it is not reachable from any type
// that gets serialised.
func (r *Ring) Secret(fingerprint string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.keys {
		if k.Fingerprint == fingerprint {
			return k.secret, true
		}
	}
	return "", false
}

// Usable reports whether anything is left to spend.
func (r *Ring) Usable() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.keys {
		if k.Available() {
			return true
		}
	}
	return false
}

// --- donor persistence --------------------------------------------------

// donorFile is what survives a restart. Configured keys are not written
// here — they come from the environment every boot, and copying them into
// a second file would be one more place a secret lives for no gain.
type donorFile struct {
	Keys []donorEntry `json:"keys"`
}

type donorEntry struct {
	Secret    string    `json:"secret"`
	Label     string    `json:"label,omitempty"`
	AddedAt   time.Time `json:"added_at"`
	Retired   bool      `json:"retired,omitempty"`
	RetiredAt time.Time `json:"retired_at,omitempty"`
	Reason    string    `json:"retired_reason,omitempty"`
	Dry       bool      `json:"dry,omitempty"`
	DryAt     time.Time `json:"dry_at,omitempty"`
	DryReason string    `json:"dry_reason,omitempty"`
}

func (r *Ring) loadDonors() {
	if r.path == "" {
		return
	}
	b, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	var f donorFile
	if json.Unmarshal(b, &f) != nil {
		return
	}
	for _, e := range f.Keys {
		if !r.add(e.Secret, SourceDonor, e.Label) {
			continue
		}
		k := r.keys[len(r.keys)-1]
		if !e.AddedAt.IsZero() {
			k.AddedAt = e.AddedAt
		}
		k.Retired, k.RetiredAt, k.Reason = e.Retired, e.RetiredAt, e.Reason
		k.Dry, k.DryAt, k.DryReason = e.Dry, e.DryAt, e.DryReason
	}
}

// saveDonorsLocked writes the donated keys back. 0600 and an atomic
// rename: this file is a list of other people's credentials, and it is
// the one place in the service where such a thing is at rest.
func (r *Ring) saveDonorsLocked() {
	if r.path == "" {
		return
	}
	var f donorFile
	for _, k := range r.keys {
		if k.Source != SourceDonor {
			continue
		}
		f.Keys = append(f.Keys, donorEntry{
			Secret: k.secret, Label: k.Label, AddedAt: k.AddedAt,
			Retired: k.Retired, RetiredAt: k.RetiredAt, Reason: k.Reason,
			Dry: k.Dry, DryAt: k.DryAt, DryReason: k.DryReason,
		})
	}
	b, err := json.Marshal(f)
	if err != nil {
		return
	}
	tmp := r.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	os.Rename(tmp, r.path)
}
