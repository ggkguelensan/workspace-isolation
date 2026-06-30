//go:build unix

package lock

import "fmt"

// BreakDecision is the read-only, side-effect-free assessment of whether a contended
// lock is safe to auto-break (DESIGN §7.3 / §7.4 HEAL-3). AssessBreak never mutates
// the lock; it reads the backing filesystem and the lock's holder body and reports a
// verdict plus the evidence behind it, so `lock ls` can explain each lock and the
// (later) `lock break` command can gate its action on Safe.
type BreakDecision struct {
	// Safe is the conjunction the whole self-heal layer turns on: true ONLY when the
	// backing filesystem is flock-trustworthy AND the holder is known AND that holder
	// is proven dead. Every other state resolves to false — wi refuses to break rather
	// than risk stealing a lock from a live or unknown peer.
	Safe bool
	// FSTrustworthy is whether locksDir sits on a filesystem where flock(2) is locally
	// reliable (the §7.3 fs-trust gate). A network/unknown fs is not trustworthy.
	FSTrustworthy bool
	// HolderKnown is whether the lock body parsed into a Holder. A missing, empty, or
	// unparseable body (ReadHolder error) leaves this false — an unknown holder.
	HolderKnown bool
	// Holder is the parsed holder identity when HolderKnown; the zero Holder otherwise.
	Holder Holder
	// ProvenDead is whether the known holder is provably gone (ProvenDead). Meaningful
	// only when HolderKnown; false otherwise.
	ProvenDead bool
	// Reason is a human-readable diagnostic naming the limb that decided the verdict,
	// for the envelope and `lock ls`. It is not a closed wire enum (internal/contract
	// owns those); the CLI maps the structured fields above to any wire code it needs.
	Reason string
}

// AssessBreak composes the two independent self-heal gates into one verdict for the
// lock named by key within locksDir (DESIGN §7.3 / §7.4 HEAL-3). It is read-only: it
// statfs's locksDir and reads the lock's holder body, taking no flock and writing
// nothing. The verdict is conjunctive and fail-safe — Safe is true only when the fs
// is flock-trustworthy AND the holder is known AND ProvenDead reports it gone:
//
//   - an unknown holder (ReadHolder error: no/empty/unparseable body) is never
//     breakable — we will not break a lock we cannot attribute;
//   - a holder we cannot prove dead (live, foreign host, unknown boot) is never
//     breakable — we will not steal a lock from a running peer;
//   - an untrustworthy filesystem is never breakable — flock may be unreliable there,
//     so even a "dead" holder cannot be trusted.
//
// A genuine I/O fault reading the filesystem (statfs) or judging liveness (boot id /
// hostname) is returned as an error; a merely-unreadable holder body is not an error
// but the not-breakable "unknown holder" outcome.
func AssessBreak(locksDir string, key Key) (BreakDecision, error) {
	var d BreakDecision

	trust, err := FSTrustworthy(locksDir)
	if err != nil {
		return BreakDecision{}, fmt.Errorf("lock: assess break for %q: %w", key.String(), err)
	}
	d.FSTrustworthy = trust

	if holder, herr := ReadHolder(locksDir, key); herr == nil {
		d.HolderKnown = true
		d.Holder = holder
		dead, derr := ProvenDead(holder)
		if derr != nil {
			return BreakDecision{}, fmt.Errorf("lock: assess break for %q: %w", key.String(), derr)
		}
		d.ProvenDead = dead
	}

	switch {
	case !d.HolderKnown:
		d.Reason = "holder unknown (no or unparseable lock body): refusing to break"
	case !d.FSTrustworthy:
		d.Reason = "filesystem is not flock-trustworthy (e.g. a network fs): refusing to break"
	case !d.ProvenDead:
		d.Reason = "holder is live or not provably dead: refusing to break"
	default:
		d.Safe = true
		d.Reason = "holder proven dead on a flock-trustworthy filesystem: safe to break"
	}
	return d, nil
}
