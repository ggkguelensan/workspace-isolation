package contract

import (
	"encoding/json"
	"testing"
)

// SHAPE-LOCKS-BLOCK (fitness, level: contract)
//
// Freezes the additive `locks` block — the wire projection of the lock-self-heal
// inventory `wi lock ls` emits (DESIGN §7.3 / §7.4). It is the first wire-contract
// change since M0 froze the envelope: a new omitempty block + a SchemaVersion bump
// 1.0 → 1.1 (additive minor), anticipated by the envelope/schema reserved-block notes.
//
// Two load-bearing properties, each with a registered mutant:
//   - Field set/order + position: `locks` marshals after `help` and before `error`
//     (struct declaration order), every LockInfo bool is always present (agents index
//     blind), and a known holder's identity rides a nested `holder` object. Frozen as
//     golden bytes. Mutant: reorder/rename a LockInfo or LockHolder json tag, or move
//     the Locks struct field → these bytes drift → RED.
//   - Additive/omitempty: a non-lock envelope (Locks nil) carries NO `locks` key, and a
//     body-less lock row (HolderKnown=false, Holder nil) carries NO `holder` key. Mutant:
//     drop `,omitempty` from Envelope.Locks → "locks":null on every envelope → the
//     omitted-when-nil test (and the success/error/help goldens) RED.

const goldenLocks = `{"schema_version":"1.1","capabilities":["help-json"],"op_id":"op_test","command":"lock ls","ok":true,"action":"read","dry_run":false,"repos":[],"warnings":[],"next":[],"locks":[{"key":"repo:api","safe":true,"fs_trustworthy":true,"holder_known":true,"proven_dead":true,"reason":"holder proven dead on a flock-trustworthy filesystem: safe to break","holder":{"pid":999999,"host":"buildhost","boot_id":"boot-xyz","op_id":"op_dead"}},{"key":"project-registry","safe":false,"fs_trustworthy":true,"holder_known":false,"proven_dead":false,"reason":"holder unknown (no or unparseable lock body): refusing to break"}],"error":null}`

// locksEnvelope is the typed source of goldenLocks: a `lock ls` read envelope with two
// rows — a proven-dead holder (identity present, safe to break) and a body-less lock
// (unknown holder, holder omitted, not safe).
func locksEnvelope() Envelope {
	return Envelope{
		SchemaVersion: SchemaVersion,
		Capabilities:  []Capability{CapHelpJSON},
		OpID:          "op_test",
		Command:       "lock ls",
		OK:            true,
		Action:        ActionRead,
		Locks: []LockInfo{
			{
				Key:           "repo:api",
				Safe:          true,
				FSTrustworthy: true,
				HolderKnown:   true,
				ProvenDead:    true,
				Reason:        "holder proven dead on a flock-trustworthy filesystem: safe to break",
				Holder:        &LockHolder{PID: 999999, Host: "buildhost", BootID: "boot-xyz", OpID: "op_dead"},
			},
			{
				Key:           "project-registry",
				Safe:          false,
				FSTrustworthy: true,
				HolderKnown:   false,
				ProvenDead:    false,
				Reason:        "holder unknown (no or unparseable lock body): refusing to break",
			},
		},
	}
}

func TestEnvelopeLocksBlockGolden(t *testing.T) {
	b, err := json.Marshal(locksEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != goldenLocks {
		t.Errorf("locks envelope drift:\n got = %s\nwant = %s", got, goldenLocks)
	}
}

// TestEnvelopeLocksOmittedWhenNil pins the additive invariant: a non-lock envelope
// (Locks nil) carries NO "locks" key. Mutant: drop `,omitempty` from Envelope.Locks →
// "locks":null appears on every envelope → RED here and on the success/error goldens.
func TestEnvelopeLocksOmittedWhenNil(t *testing.T) {
	b, _ := json.Marshal(successEnvelope()) // Locks is nil
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["locks"]; ok {
		t.Errorf(`"locks" key present on a non-lock envelope; the block must be omitempty, got %s`, b)
	}
}

// TestLockInfoHolderOmittedWhenUnknown pins that a body-less lock row carries no nested
// "holder" object — the identity is present iff holder_known. Mutant: drop `,omitempty`
// from LockInfo.Holder → "holder":null on the body-less row → goldenLocks drift → RED.
func TestLockInfoHolderOmittedWhenUnknown(t *testing.T) {
	b, err := json.Marshal(LockInfo{Key: "project-registry", Reason: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	// Decode to a key set so the assertion can't be fooled by "holder" being a
	// substring of the always-present "holder_known" key.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["holder"]; ok {
		t.Errorf(`unknown-holder LockInfo must omit the nested "holder" object, got %s`, b)
	}
	// And every non-holder field must still be present so agents can index blind.
	for _, key := range []string{"key", "safe", "fs_trustworthy", "holder_known", "proven_dead", "reason"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("LockInfo missing always-present key %q, got %s", key, b)
		}
	}
}
