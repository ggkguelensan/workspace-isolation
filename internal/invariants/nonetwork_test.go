package invariants

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// INV-NO-NETWORK (fitness, level: architecture) — DESIGN §2 #3.
//
// wi performs ZERO hidden network dials: offline commands and startup recovery
// dial nothing, INCLUDING git child processes. The runtime guarantee rests on a
// single chokepoint — internal/gitexec launches every git child and overlays the
// GIT_ALLOW_PROTOCOL=none egress belt on the offline Run path, with RunNetwork as
// the one narrow fetch/clone opt-in. The unit-level half of this (GITEXEC-OFFLINE-
// BELT) proves the belt works; THIS architecture guard proves the belt cannot be
// bypassed: it asserts that no package other than the allowlist spawns a child
// process (imports os/exec) or touches the belt key.
//
// Allowlist: gitexec is the runtime chokepoint; testenv is the test-only git
// fixture harness (a non-_test.go support package that is never reachable from
// cmd/wi, so it never ships in a command path). Everything else routing git
// through gitexec is exactly what keeps the belt unbypassable.
//
// Non-vacuity (guard→mutant): make scanFileForEgress always report (false,false)
// → TestNoNetworkScannerIsNonVacuous RED (a broken detector can't be a silent
// false negative). The real-source / architecture mutant is "spawn a process
// outside the allowlist" (e.g. import os/exec in internal/git, or empty
// egressAllowed so gitexec/testenv themselves trip) → TestNoHiddenNetwork RED.

// egressAllowed maps slash-separated package paths (relative to the module root)
// that are permitted to spawn child processes and/or reference the egress belt
// key. It is data, not logic.
var egressAllowed = map[string]bool{
	"internal/gitexec": true, // the runtime chokepoint (applies the belt)
	"internal/testenv": true, // test-only git fixture harness, never in a command path
}

// beltKey is the environment variable gitexec overlays as its no-network egress
// belt (GIT_ALLOW_PROTOCOL=none). Only the chokepoint may reference it.
const beltKey = "GIT_ALLOW_PROTOCOL"

// scanFileForEgress parses Go source and reports whether it (a) imports os/exec —
// i.e. can spawn a child process — and (b) references the egress belt key in a
// string literal. It parses rather than greps so the belt key in a comment or
// this guard's own prose never trips it; unparseable input reports neither. Pure,
// so the non-vacuity test can drive it directly.
func scanFileForEgress(src string) (importsExec, refsBelt bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "scan.go", src, 0)
	if err != nil {
		return false, false
	}
	for _, imp := range f.Imports {
		if path, err := strconv.Unquote(imp.Path.Value); err == nil && path == "os/exec" {
			importsExec = true
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if s, err := strconv.Unquote(lit.Value); err == nil && strings.Contains(s, beltKey) {
			refsBelt = true
		}
		return true
	})
	return importsExec, refsBelt
}

func TestNoHiddenNetwork(t *testing.T) {
	root := moduleRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		// Source files only; test files are scaffolding, not command paths.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(filepath.Dir(rel))
		if egressAllowed[pkg] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		importsExec, refsBelt := scanFileForEgress(string(b))
		if importsExec {
			t.Errorf("%s (package %q) imports os/exec — every child process must go through internal/gitexec so the no-network egress belt cannot be bypassed (DESIGN §2 #3)", rel, pkg)
		}
		if refsBelt {
			t.Errorf("%s (package %q) references %s — the egress belt is owned solely by internal/gitexec (DESIGN §2 #3)", rel, pkg, beltKey)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module tree: %v", err)
	}
}

func TestNoNetworkScannerIsNonVacuous(t *testing.T) {
	// A file that spawns a child process the way a hidden-network bug would.
	spawns := "package x\nimport \"os/exec\"\nfunc f() { _ = exec.Command(\"git\", \"fetch\") }\n"
	if importsExec, _ := scanFileForEgress(spawns); !importsExec {
		t.Error("INV-NO-NETWORK scanner is vacuous: failed to flag an os/exec import")
	}
	// A file that re-opens the egress belt outside the chokepoint.
	reopensBelt := "package x\nfunc f() string { return \"" + beltKey + "\" }\n"
	if _, refsBelt := scanFileForEgress(reopensBelt); !refsBelt {
		t.Error("INV-NO-NETWORK scanner is vacuous: failed to flag a belt-key reference")
	}
	// A clean file trips neither — the floor that proves the detector isn't a
	// constant-true.
	clean := "package x\nimport \"strings\"\nfunc f() bool { return strings.Contains(\"a\", \"b\") }\n"
	if importsExec, refsBelt := scanFileForEgress(clean); importsExec || refsBelt {
		t.Errorf("INV-NO-NETWORK scanner false-positives on a clean file: exec=%v belt=%v", importsExec, refsBelt)
	}
}
