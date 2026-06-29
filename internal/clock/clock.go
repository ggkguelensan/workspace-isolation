// Package clock is wi's injectable seam for the two volatile inputs that would
// otherwise make output non-reproducible: wall-clock time and randomness
// (DESIGN §2 determinism, §4). The CLI builds one System clock at startup and
// threads it into op_id minting (opid.New) and envelope timestamps; tests inject
// a Fake to get a fixed, reproducible stream so golden envelopes don't flap.
//
// Randomness here is the OS CSPRNG (crypto/rand) — a local syscall, never a
// network dial — so this package is compatible with the no-hidden-network
// invariant (DESIGN §2.3).
package clock

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"io"
	"time"
)

// Clock supplies wi's volatile inputs. Now is the wall-clock reading used for
// envelope timestamps; Rand is the byte source op_id minting draws from.
type Clock interface {
	Now() time.Time
	Rand() io.Reader
}

// System is the production Clock: real UTC time and the OS CSPRNG. Its zero value
// is ready to use.
type System struct{}

// Now returns the current time in UTC (UTC so serialized timestamps never carry
// a machine-local offset).
func (System) Now() time.Time { return time.Now().UTC() }

// Rand returns the OS cryptographic randomness source.
func (System) Rand() io.Reader { return cryptorand.Reader }

// Fake is a deterministic Clock for tests: a fixed (advanceable) instant and a
// reproducible, seed-derived byte stream. Same (instant, seed) always yields the
// same time and the same stream, so envelopes are reproducible before
// normalization masks op_id/timestamps.
type Fake struct {
	instant time.Time
	rng     *detReader
}

// NewFake returns a Fake fixed at instant whose randomness is fully determined by
// seed.
func NewFake(instant time.Time, seed uint64) *Fake {
	return &Fake{instant: instant, rng: &detReader{state: seed}}
}

// Now returns the Fake's current instant.
func (f *Fake) Now() time.Time { return f.instant }

// Rand returns the Fake's deterministic byte stream. The returned reader is
// stateful and shared across calls, so successive reads advance the stream
// (consecutive mints differ) while remaining reproducible across runs.
func (f *Fake) Rand() io.Reader { return f.rng }

// Advance moves the Fake's instant forward, to simulate time passing across
// steps of a multi-stage operation.
func (f *Fake) Advance(d time.Duration) { f.instant = f.instant.Add(d) }

// detReader is a self-contained deterministic byte stream (splitmix64). It is
// OURS rather than math/rand so the sequence is stable regardless of stdlib
// internals, and it never errors or short-reads.
type detReader struct {
	state uint64
	buf   [8]byte
	avail int // unconsumed bytes at the tail of buf
}

// next advances the splitmix64 state and returns the next 64-bit output.
func (r *detReader) next() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// Read fills p completely from the stream. It always returns (len(p), nil).
func (r *detReader) Read(p []byte) (int, error) {
	for i := range p {
		if r.avail == 0 {
			binary.LittleEndian.PutUint64(r.buf[:], r.next())
			r.avail = 8
		}
		p[i] = r.buf[8-r.avail]
		r.avail--
	}
	return len(p), nil
}
