package config_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/config"
)

// Guard CONFIG-PARSE — the read+validate half of internal/config, the SOLE owner
// of the committed manifest wi.config.jsonc (DESIGN §1, §map line 167).
//
// The manifest is JSONC (JSON-with-comments): users annotate it, so Parse must
// tolerate // line and /* */ block comments (decision #C). Validation is closed —
// unknown keys at any level are rejected (encoding/json DisallowUnknownFields),
// repo names route through the shared layout.ValidateSegment traversal chokepoint
// (they become path segments under repos/<name>), and each repo's effective base
// branch is the repo's own base or, falling back, defaults.base — a repo with
// neither is an error.
//
// A base may be a single branch name OR an ordered candidate list (["dev","main"] =
// "prefer dev, fall back to main", DESIGN §1); a bare string decodes to a one-element
// list. The effective base is the repo's own list, or defaults.base when it omits one.
//
// Non-vacuity (guard→mutant): (1) make stripJSONC a no-op (return input unchanged)
// → the golden manifest's comments become JSON syntax errors → TestParseAcceptsGolden
// RED, proving the JSONC strip is load-bearing; (2) drop dec.DisallowUnknownFields()
// → the unknown-key reject cases parse cleanly → TestParseRejectsInvalid RED; (3) in
// baseList.UnmarshalJSON drop the string case (only accept arrays) → the golden's
// string-form defaults.base fails to decode → TestParseAcceptsGolden RED, proving the
// string-or-array equivalence.

const goldenManifest = `{
  // wi manifest — defaults apply when a repo omits a field
  "defaults": { "base": "main" },
  "repos": [
    /* api inherits the default base */
    { "name": "api", "url": "https://example.com/api.git" },
    { "name": "web", "url": "https://example.com/web.git", "base": ["develop", "main"] } // explicit candidate list
  ]
}`

func TestParseAcceptsGolden(t *testing.T) {
	got, err := config.Parse([]byte(goldenManifest))
	if err != nil {
		t.Fatalf("Parse(golden): %v", err)
	}
	want := config.Config{
		Defaults: config.Defaults{Base: []string{"main"}},
		Repos: []config.Repo{
			{Name: "api", URL: "https://example.com/api.git", Base: []string{"main"}},            // inherited (string → 1-elem)
			{Name: "web", URL: "https://example.com/web.git", Base: []string{"develop", "main"}}, // explicit list
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse(golden) mismatch:\n got = %+v\nwant = %+v", got, want)
	}
}

func TestParseAcceptsEmpty(t *testing.T) {
	// A freshly-init'd project: repos present but empty, and the degenerate {}.
	for _, src := range []string{`{ "repos": [] }`, `{}`} {
		cfg, err := config.Parse([]byte(src))
		if err != nil {
			t.Errorf("Parse(%q): unexpected error %v", src, err)
			continue
		}
		if len(cfg.Repos) != 0 {
			t.Errorf("Parse(%q): want 0 repos, got %d", src, len(cfg.Repos))
		}
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"unknown top-level key": `{ "repos": [], "bogus": 1 }`,
		"unknown repo key":      `{ "repos": [ { "name": "a", "url": "u", "base": "main", "branch": "x" } ] }`,
		"unknown defaults key":  `{ "defaults": { "baseX": "main" }, "repos": [] }`,
		"missing name":          `{ "repos": [ { "url": "u", "base": "main" } ] }`,
		"missing url":           `{ "repos": [ { "name": "a", "base": "main" } ] }`,
		"no base anywhere":      `{ "repos": [ { "name": "a", "url": "u" } ] }`,
		"empty base list":       `{ "repos": [ { "name": "a", "url": "u", "base": [] } ] }`,
		"empty base candidate":  `{ "repos": [ { "name": "a", "url": "u", "base": ["", "main"] } ] }`,
		"non-string base":       `{ "repos": [ { "name": "a", "url": "u", "base": 5 } ] }`,
		"empty defaults base":   `{ "defaults": { "base": [" "] }, "repos": [] }`,
		"duplicate name":        `{ "defaults": { "base": "main" }, "repos": [ { "name": "a", "url": "u" }, { "name": "a", "url": "v" } ] }`,
		"unsafe repo name":      `{ "defaults": { "base": "main" }, "repos": [ { "name": "../escape", "url": "u" } ] }`,
		"malformed json":        `{ "repos": [`,
		"trailing content":      `{ "repos": [] } { "repos": [] }`,
		"comments only":         "// just a comment\n",
	}
	for name, src := range cases {
		if _, err := config.Parse([]byte(src)); err == nil {
			t.Errorf("Parse(%s): want error, got nil for %q", name, src)
		}
	}
}

// TestParseBaseListForms pins the string-or-array base declaration: a bare string and
// a one-element array are equivalent, an array preserves candidate order, a repo
// inherits an array defaults.base when it omits its own, and surrounding whitespace
// is trimmed per candidate.
func TestParseBaseListForms(t *testing.T) {
	src := `{
	  "defaults": { "base": ["dev", "main"] },
	  "repos": [
	    { "name": "inherits", "url": "u1" },
	    { "name": "str", "url": "u2", "base": " release " },
	    { "name": "arr", "url": "u3", "base": ["staging", "main"] }
	  ]
	}`
	cfg, err := config.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := map[string][]string{
		"inherits": {"dev", "main"},     // inherited array default
		"str":      {"release"},         // string → 1-elem, trimmed
		"arr":      {"staging", "main"}, // explicit array, order preserved
	}
	for name, wantBase := range want {
		r, ok := cfg.Lookup(name)
		if !ok {
			t.Errorf("Lookup(%q): not found", name)
			continue
		}
		if !reflect.DeepEqual(r.Base, wantBase) {
			t.Errorf("%s base = %v, want %v", name, r.Base, wantBase)
		}
	}
	if !reflect.DeepEqual(cfg.Defaults.Base, []string{"dev", "main"}) {
		t.Errorf("defaults.base = %v, want [dev main]", cfg.Defaults.Base)
	}
}

func TestLoadReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wi.config.jsonc")
	if err := os.WriteFile(path, []byte(goldenManifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r, ok := cfg.Lookup("web")
	if !ok {
		t.Fatalf("Lookup(web): not found")
	}
	if !reflect.DeepEqual(r.Base, []string{"develop", "main"}) {
		t.Errorf("web base = %v, want [develop main]", r.Base)
	}

	// A missing manifest reports a not-exist error the CLI can branch on to
	// suggest `wi init`.
	_, err = config.Load(filepath.Join(dir, "nope.jsonc"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load(missing): want fs.ErrNotExist, got %v", err)
	}
}
