// Package config is the SOLE owner of wi's committed declarative manifest,
// <root>/wi.config.jsonc (DESIGN §1, §map). It parses, validates, and (in a later
// unit) AST-preservingly edits that one file; it owns nothing else. Downstream
// commands read the validated, resolved Config — they never touch the raw file.
//
// The manifest is JSONC: JSON the user may annotate with `//` line comments and
// `/* */` block comments. Parse strips those comments and decodes the result with
// encoding/json under DisallowUnknownFields, so the manifest's key set is closed —
// an unknown key at any level is a hard error rather than silent drift. Repo names
// are validated through the shared layout.ValidateSegment chokepoint because they
// become path segments under repos/<name> and isolas/<task>/<name>.
//
// Decision #C (PROGRESS): the JSONC read path is a hand-rolled comment stripper +
// stdlib encoding/json, NOT a third-party JSONC dependency — consistent with the
// zero-new-deps posture (decision #6) that keeps INV-NO-LLM trivially green. The
// AST-preserving *edit* path (for `repo add`) is a separate later unit. Trailing
// commas are intentionally NOT yet tolerated; comments are the essential
// annotation feature and are the only JSONC extension this unit accepts.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ggkguelensan/workspace-isolation/internal/layout"
)

// Config is the validated, resolved manifest. Each Repo.Base is the EFFECTIVE base
// candidate list (the repo's own base, or defaults.base when the repo omits one), so
// downstream callers never re-apply the default. The list is an ordered preference —
// ["dev","main"] means "prefer dev, fall back to main" — resolved to one effective
// branch at the command seam (internal/baseref) or, at first sync, against origin.
type Config struct {
	Defaults Defaults
	Repos    []Repo
}

// Defaults holds manifest-wide fallbacks. Base is the ordered base candidate list a
// repo inherits when its own base is unset (nil when no default was declared).
type Defaults struct {
	Base []string
}

// Repo is one declared repository: a wi-internal name (also its path segment), an
// origin URL to clone from, and the resolved ordered base candidate list wi keeps as
// SSOT (a single-element list for the common single-base case).
type Repo struct {
	Name string
	URL  string
	Base []string
}

// Lookup returns the repo declared under name, if any.
func (c Config) Lookup(name string) (Repo, bool) {
	for _, r := range c.Repos {
		if r.Name == name {
			return r, true
		}
	}
	return Repo{}, false
}

// wire* mirror the on-disk JSON shape. They are separate from the resolved Config
// so decoding stays a dumb structural step and all policy lives in Parse. Defaults
// is a pointer so "absent" is distinguishable, though resolution treats absent and
// empty identically.
type wireConfig struct {
	Defaults *wireDefaults `json:"defaults"`
	Repos    []wireRepo    `json:"repos"`
}

type wireDefaults struct {
	Base baseList `json:"base"`
}

type wireRepo struct {
	Name string   `json:"name"`
	URL  string   `json:"url"`
	Base baseList `json:"base"`
}

// baseList is the wire form of a base declaration: a bare string ("main") OR an
// ordered array of candidate branches (["dev","main"]). A bare string decodes to a
// one-element list, so single-base manifests — and the single-string literal `repo
// add` writes — stay valid. Element validation (trimming, non-empty) is Parse's job,
// not the decoder's. (DisallowUnknownFields on the outer decoder does not reach into
// this custom UnmarshalJSON, but base holds only strings, so there is nothing to
// reject here.)
type baseList []string

func (b *baseList) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*b = baseList{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf("base must be a branch name or an array of branch names")
	}
	*b = baseList(arr)
	return nil
}

// Load reads and parses the manifest at path. A missing file surfaces as an error
// wrapping fs.ErrNotExist, so the CLI can branch on errors.Is(err, fs.ErrNotExist)
// to suggest `wi init` rather than reporting a malformed manifest.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: load %s: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes and validates a JSONC manifest from raw bytes. Validation is
// closed: unknown keys are rejected, exactly one JSON value must be present, every
// repo needs a name (a safe path segment), a url, and an effective base branch,
// and repo names must be unique.
func Parse(data []byte) (Config, error) {
	dec := json.NewDecoder(bytes.NewReader(stripJSONC(data)))
	dec.DisallowUnknownFields()
	var wc wireConfig
	if err := dec.Decode(&wc); err != nil {
		return Config{}, fmt.Errorf("config: parse manifest: %w", err)
	}
	if dec.More() {
		return Config{}, fmt.Errorf("config: parse manifest: unexpected content after the manifest object")
	}

	var defaultRaw []string
	if wc.Defaults != nil {
		defaultRaw = wc.Defaults.Base
	}
	defaultBase, err := normalizeBase(defaultRaw)
	if err != nil {
		return Config{}, fmt.Errorf("config: defaults.base: %w", err)
	}
	cfg := Config{Defaults: Defaults{Base: defaultBase}}

	seen := make(map[string]bool, len(wc.Repos))
	for i, wr := range wc.Repos {
		name := strings.TrimSpace(wr.Name)
		if name == "" {
			return Config{}, fmt.Errorf("config: repos[%d]: missing name", i)
		}
		if err := layout.ValidateSegment("repo", name); err != nil {
			return Config{}, fmt.Errorf("config: repos[%d]: %w", i, err)
		}
		if seen[name] {
			return Config{}, fmt.Errorf("config: duplicate repo %q", name)
		}
		seen[name] = true

		url := strings.TrimSpace(wr.URL)
		if url == "" {
			return Config{}, fmt.Errorf("config: repo %q: missing url", name)
		}
		base, err := normalizeBase(wr.Base)
		if err != nil {
			return Config{}, fmt.Errorf("config: repo %q: base: %w", name, err)
		}
		if len(base) == 0 {
			base = defaultBase
		}
		if len(base) == 0 {
			return Config{}, fmt.Errorf("config: repo %q: no base branch (set the repo's base or defaults.base)", name)
		}
		cfg.Repos = append(cfg.Repos, Repo{Name: name, URL: url, Base: base})
	}
	return cfg, nil
}

// normalizeBase trims each candidate and rejects an empty/blank one (a base list
// must not contain a hole). An absent base (empty input) returns a nil list; the
// caller decides whether that — after inheriting defaults.base — is an error.
func normalizeBase(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		c = strings.TrimSpace(c)
		if c == "" {
			return nil, fmt.Errorf("a base candidate must not be empty")
		}
		out = append(out, c)
	}
	return out, nil
}

// stripJSONC returns src with // line comments and /* */ block comments removed,
// leaving plain JSON for encoding/json. It tracks string context (honoring
// backslash escapes) so a // or /* inside a JSON string value is preserved, and
// emits the newlines it skips so decoder error positions stay aligned with the
// source. It is the load-bearing half of JSONC support; everything else is stdlib.
func stripJSONC(src []byte) []byte {
	out := make([]byte, 0, len(src))
	const (
		normal = iota
		inString
		lineComment
		blockComment
	)
	state := normal
	escaped := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case normal:
			switch {
			case c == '"':
				out = append(out, c)
				state = inString
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				state = lineComment
				i++ // consume the second '/'
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				state = blockComment
				i++ // consume the '*'
			default:
				out = append(out, c)
			}
		case inString:
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				state = normal
			}
		case lineComment:
			if c == '\n' {
				out = append(out, c)
				state = normal
			}
		case blockComment:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				i++ // consume the '/'
				state = normal
			} else if c == '\n' {
				out = append(out, c)
			}
		}
	}
	return out
}
