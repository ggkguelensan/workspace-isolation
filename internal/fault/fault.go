// Package fault is wi's deterministic fault-injection seam: the WI_FAULT half of
// the (guard → mutant) methodology (IMPLEMENTATION_PLAN §1). Production code and
// tests consult fault.Active to simulate a specific, named failure on demand,
// driven solely by the WI_FAULT environment variable (a comma-separated list of
// fault ids). With no id active it is completely inert, so it changes nothing in
// normal operation and ships no behavior of its own into the binary.
//
// It exists so fitness guards can be proven NON-VACUOUS without hand-editing
// source: a guard's paired mutant is "set WI_FAULT=<id>", which makes the code
// under test misbehave so the guard must redden. META-VACUITY (this package's
// meta_vacuity_test.go) proves the mechanism genuinely flips a guard RED.
package fault

import (
	"os"
	"strings"
)

// EnvVar is the environment variable that activates faults.
const EnvVar = "WI_FAULT"

// Active reports whether the named fault id is present in WI_FAULT.
func Active(id string) bool {
	return activeIn(os.Getenv(EnvVar), id)
}

// activeIn is the pure core of Active, testable without touching the process
// environment. Matching is EXACT per comma-separated entry (after trimming
// surrounding whitespace) — never a substring — so fault id "foo" is not
// activated by WI_FAULT=foobar.
func activeIn(env, id string) bool {
	if env == "" || id == "" {
		return false
	}
	for _, f := range strings.Split(env, ",") {
		if strings.TrimSpace(f) == id {
			return true
		}
	}
	return false
}
