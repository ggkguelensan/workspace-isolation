package fault

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// META-VACUITY (fitness, level: methodology)
//
// Proves the WI_FAULT fault-injection harness is genuinely able to turn a
// fitness guard RED — i.e. the (guard → mutant) methodology is not vacuous. It
// uses a reference subject and a reference guard as a specimen fitness function,
// then runs that guard in a subprocess twice:
//   - with WI_FAULT=meta.reference active  -> the guard MUST fail (the mutant
//     reddens it), else the harness is vacuous;
//   - with no fault                        -> the same guard MUST pass, proving
//     the RED above is caused by the fault, not a permanently-broken guard.
//
// Non-vacuity (guard→mutant): making refSubject ignore the fault (always return
// the correct value) makes the under-fault subprocess PASS, turning TestMetaVacuity
// RED — confirmed manually and registered.

const refFaultID = "meta.reference"

// refSubject stands in for any unit under test: correct normally, deliberately
// wrong when its paired fault is active. Real subjects (isolate-new, land, gc)
// will consult fault.Active the same way to make their HEAL/crash guards
// non-vacuous.
func refSubject() int {
	if Active(refFaultID) {
		return 0 // faulty output injected by the mutant
	}
	return 42 // correct output
}

// TestMetaReferenceGuard is the specimen fitness function. Run normally (no
// fault) it passes; the META-VACUITY test re-invokes it under WI_FAULT to
// observe it fail. It must therefore be a real, non-skipped guard.
func TestMetaReferenceGuard(t *testing.T) {
	if got := refSubject(); got != 42 {
		t.Fatalf("reference subject wrong: got %d, want 42", got)
	}
}

// runReferenceGuard re-execs this test binary to run ONLY TestMetaReferenceGuard,
// with WI_FAULT set to faultEnv (or stripped when empty). Returns the subprocess
// exit status as an error (nil == the guard passed).
func runReferenceGuard(t *testing.T, faultEnv string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestMetaReferenceGuard$")
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, EnvVar+"=") {
			continue // ensure a clean baseline regardless of the parent's env
		}
		env = append(env, kv)
	}
	if faultEnv != "" {
		env = append(env, EnvVar+"="+faultEnv)
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return out, err
}

func TestMetaVacuity(t *testing.T) {
	// 1. The mutant (fault active) MUST redden the reference guard.
	out, err := runReferenceGuard(t, refFaultID)
	if err == nil {
		t.Fatalf("META-VACUITY: reference guard PASSED under %s=%s; the fault-injection harness is vacuous.\nsubprocess output:\n%s",
			EnvVar, refFaultID, out)
	}
	if testing.Verbose() {
		t.Logf("reference guard correctly reddened under %s=%s", EnvVar, refFaultID)
	}

	// 2. With no fault the SAME guard MUST be green (so the RED above is the
	//    fault's doing, not a permanently-broken guard).
	if out, err := runReferenceGuard(t, ""); err != nil {
		t.Fatalf("META-VACUITY: reference guard FAILED with no fault: %v\nsubprocess output:\n%s", err, out)
	}
}
