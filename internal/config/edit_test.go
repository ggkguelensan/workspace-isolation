package config_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/config"
)

// Guard CONFIG-ADD — the AST-preserving WRITE half of internal/config (the companion to
// CONFIG-PARSE). `wi repo add` must append a repo to wi.config.jsonc WITHOUT discarding the
// user's comments and formatting, so the edit path is a TEXTUAL splice into the existing
// byte stream, NOT a round-trip through encoding/json (which would drop every comment). The
// guarded properties: (1) a clean add preserves all existing comments + repos and the
// result re-parses with the new repo present; (2) inserting with no --base omits the base
// field (the repo inherits defaults.base); (3) a duplicate name is refused (ErrDuplicateRepo)
// leaving the file byte-for-byte intact; (4) a traversing/invalid name is refused before any
// write; (5) a missing manifest surfaces fs.ErrNotExist (so the handler can hint `wi init`).
//
// Non-vacuity (guard→mutant): (1) PRIMARY — after reading the manifest, strip comments first
// (`data = stripJSONC(data)`) before splicing → the rewrite is still valid JSON containing
// every repo, but the comments are gone → TestAddAppendsPreservingComments RED on the
// comment-survival assertion (proving the edit is genuinely AST-preserving, not merely
// "produces valid JSON"); (2) drop the "," separator in the non-empty splice → two adjacent
// objects with no separator → the post-rewrite Parse guard rejects it → Add returns an error
// → TestAddAppendsPreservingComments RED.

const addGolden = `{
  // wi manifest — defaults apply when a repo omits a field
  "defaults": { "base": "main" },
  "repos": [
    /* api inherits the default base */
    { "name": "api", "url": "https://example.com/api.git" },
    { "name": "web", "url": "https://example.com/web.git", "base": "develop" } // explicit base
  ]
}`

func writeManifestFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wi.config.jsonc")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
	return path
}

// A clean add preserves comments + existing repos and the rewrite re-parses with the new
// repo (its explicit base recorded). This is the mutant target for the comment-preservation
// property — the whole reason the edit path is textual.
func TestAddAppendsPreservingComments(t *testing.T) {
	path := writeManifestFile(t, addGolden)

	if err := config.Add(path, "svc", "https://example.com/svc.git", "release"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	got := string(raw)
	// Comments must survive the edit (the AST-preserving property).
	for _, want := range []string{
		"// wi manifest — defaults apply when a repo omits a field",
		"/* api inherits the default base */",
		"// explicit base",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("comment not preserved: %q missing from rewrite:\n%s", want, got)
		}
	}

	// The rewrite re-parses and now holds api, web (unchanged) AND svc.
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("rewrite does not re-parse: %v", err)
	}
	if len(cfg.Repos) != 3 {
		t.Fatalf("want 3 repos after add, got %d (%+v)", len(cfg.Repos), cfg.Repos)
	}
	api, ok := cfg.Lookup("api")
	if !ok || api.URL != "https://example.com/api.git" || soleBase(api.Base) != "main" {
		t.Errorf("api changed by the edit: %+v (ok=%v)", api, ok)
	}
	web, ok := cfg.Lookup("web")
	if !ok || soleBase(web.Base) != "develop" {
		t.Errorf("web changed by the edit: %+v (ok=%v)", web, ok)
	}
	svc, ok := cfg.Lookup("svc")
	if !ok || svc.URL != "https://example.com/svc.git" || soleBase(svc.Base) != "release" {
		t.Errorf("svc not added correctly: %+v (ok=%v)", svc, ok)
	}
}

// soleBase returns the single base candidate of a resolved repo (the common
// single-base case `repo add` writes), or "" if the list is not exactly one element.
func soleBase(b []string) string {
	if len(b) == 1 {
		return b[0]
	}
	return ""
}

// With no base supplied the inserted repo omits the base field, so it inherits defaults.base
// (proving the edit writes base ONLY when explicitly given, never the resolved default).
func TestAddOmitsInheritedBase(t *testing.T) {
	path := writeManifestFile(t, addGolden)

	if err := config.Add(path, "svc", "https://example.com/svc.git", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	svc, ok := cfg.Lookup("svc")
	if !ok || soleBase(svc.Base) != "main" { // inherited from defaults.base, not written explicitly
		t.Errorf("svc base = %v (ok=%v), want inherited [main]", svc.Base, ok)
	}
}

// Adding into an empty repos array (with an inner comment) works and preserves the comment.
func TestAddIntoEmptyArray(t *testing.T) {
	path := writeManifestFile(t, "{\n  \"defaults\": { \"base\": \"main\" },\n  \"repos\": [ /* none yet */ ]\n}\n")

	if err := config.Add(path, "api", "https://example.com/api.git", ""); err != nil {
		t.Fatalf("Add into empty: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), "/* none yet */") {
		t.Errorf("inner comment not preserved:\n%s", raw)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if _, ok := cfg.Lookup("api"); !ok || len(cfg.Repos) != 1 {
		t.Errorf("want exactly api after add into empty, got %+v", cfg.Repos)
	}
}

// A duplicate name is refused with ErrDuplicateRepo and leaves the file byte-for-byte intact.
func TestAddRejectsDuplicate(t *testing.T) {
	path := writeManifestFile(t, addGolden)

	err := config.Add(path, "api", "https://example.com/other.git", "")
	if !errors.Is(err, config.ErrDuplicateRepo) {
		t.Fatalf("want ErrDuplicateRepo, got %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(raw) != addGolden {
		t.Errorf("manifest must be untouched on a refused duplicate:\n%s", raw)
	}
}

// A traversing repo name is refused before any write (the name becomes a path segment).
func TestAddRejectsUnsafeName(t *testing.T) {
	path := writeManifestFile(t, addGolden)

	if err := config.Add(path, "../evil", "https://example.com/x.git", ""); err == nil {
		t.Fatal("want an error for a traversing name, got nil")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != addGolden {
		t.Errorf("manifest must be untouched on a refused name:\n%s", raw)
	}
}

// A missing manifest surfaces fs.ErrNotExist so the handler can branch to not_found+`wi init`.
func TestAddMissingManifestIsNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.jsonc")
	if err := config.Add(path, "api", "https://example.com/api.git", ""); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want fs.ErrNotExist, got %v", err)
	}
}
