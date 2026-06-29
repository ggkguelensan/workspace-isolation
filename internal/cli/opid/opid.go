// Package opid mints and validates wi operation identifiers (op_id), the single
// volatile field in the envelope (DESIGN §2 determinism: an envelope is
// byte-identical across runs modulo op_id and timestamps; DESIGN §3.1, §8).
//
// A root op_id is:
//
//	op_<base36ts>_<base32rand>
//
// where <base36ts> is the mint time in Unix MILLISECONDS, base36-encoded
// (lowercase, rough-chronological + human-debuggable), and <base32rand> is
// randLen random bytes in lowercase, unpadded standard base32 (collision
// resistance within a millisecond). A child job appends ".<n>" (n >= 1) to its
// parent's id, and children nest: op_..._....1.2.
//
// New takes its two volatile inputs explicitly — a time and a randomness reader —
// so it is pure and deterministic under fixed inputs. The CLI Runner wires the
// real clock and crypto/rand at the boundary (internal/clock owns those, per
// DESIGN §4); this package owns only the FORMAT.
package opid

import (
	"encoding/base32"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// randLen is the number of random bytes in a root op_id. 5 bytes encode to
// exactly 8 base32 characters with no padding (40 bits / 5 = 8).
const randLen = 5

// prefix is the literal that begins every op_id.
const prefix = "op_"

// base32Enc is standard base32 without padding; its output is uppercased by the
// stdlib, so callers lowercase it to match the op_id alphabet [a-z2-7].
var base32Enc = base32.StdEncoding.WithPadding(base32.NoPadding)

// idRe is the frozen op_id grammar: prefix, a base36 timestamp, '_', exactly 8
// base32 chars, then zero or more ".<n>" child suffixes (n a positive integer
// with no leading zero).
var idRe = regexp.MustCompile(`^op_[0-9a-z]+_[a-z2-7]{8}(\.[1-9][0-9]*)*$`)

// New mints a root op_id from a timestamp and a randomness source. It reads
// exactly randLen bytes from r and errors (rather than truncating) on a short
// read, so a malformed id can never be emitted.
func New(now time.Time, r io.Reader) (string, error) {
	var b [randLen]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("opid: read randomness: %w", err)
	}
	ts := strconv.FormatInt(now.UnixMilli(), 36)
	rnd := strings.ToLower(base32Enc.EncodeToString(b[:]))
	return prefix + ts + "_" + rnd, nil
}

// Child derives the n-th child id of parent ("<parent>.<n>"). parent must be a
// valid op_id and n must be >= 1.
func Child(parent string, n int) (string, error) {
	if !Valid(parent) {
		return "", fmt.Errorf("opid: invalid parent %q", parent)
	}
	if n < 1 {
		return "", fmt.Errorf("opid: child index must be >= 1, got %d", n)
	}
	return fmt.Sprintf("%s.%d", parent, n), nil
}

// Valid reports whether id matches the frozen op_id grammar (root or child).
func Valid(id string) bool { return idRe.MatchString(id) }
