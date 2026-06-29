package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ggkguelensan/workspace-isolation/internal/layout"
	"github.com/ggkguelensan/workspace-isolation/internal/lockfs"
)

// ErrDuplicateRepo is returned by Add when the manifest already declares a repo under the
// requested name. The handler maps it to error.kind already_exists (exit 4); callers test
// it with errors.Is.
var ErrDuplicateRepo = errors.New("config: repo already declared")

// Add appends a repo declaration to the manifest at path, AST-preservingly: it splices a
// new object into the existing `repos` array as raw text, leaving every other byte —
// comments, whitespace, key order — untouched. This is deliberately NOT a round-trip
// through encoding/json (Parse → mutate → Marshal), which would discard every comment the
// user wrote; the read path (Parse/stripJSONC) is comment-stripping and one-directional, so
// the edit path is its own primitive.
//
// base is the repo's explicit base branch, or "" to OMIT the base field entirely so the
// repo inherits defaults.base — Add never writes the resolved default, only what the caller
// asked for. The name is validated through layout.ValidateSegment (it becomes a path
// segment) and a non-empty url is required, both BEFORE any read, so a bad request never
// touches the file.
//
// Errors: a missing manifest wraps fs.ErrNotExist (handler → not_found + `wi init`); a
// malformed manifest surfaces the Parse error (handler → usage); a name already present
// returns ErrDuplicateRepo (handler → already_exists). The write itself is atomic
// (lockfs.WriteFileAtomic), and the rewrite is re-parsed before it is committed to disk, so
// Add can never leave a corrupt manifest behind.
func Add(path, name, url, base string) error {
	name = strings.TrimSpace(name)
	url = strings.TrimSpace(url)
	base = strings.TrimSpace(base)
	if name == "" {
		return fmt.Errorf("config: missing repo name")
	}
	if err := layout.ValidateSegment("repo", name); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if url == "" {
		return fmt.Errorf("config: repo %q: missing url", name)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: load %s: %w", path, err)
	}

	// Parse validates the existing manifest (a malformed file is reported, not silently
	// overwritten) and gives us the declared names to refuse a duplicate.
	cfg, err := Parse(data)
	if err != nil {
		return err
	}
	if _, ok := cfg.Lookup(name); ok {
		return fmt.Errorf("%w: %q", ErrDuplicateRepo, name)
	}

	open, closeIdx, err := findReposArray(data)
	if err != nil {
		return err
	}

	// Element indent = the closing bracket's indentation plus one level, so the inserted
	// line sits under the array at a conventional depth regardless of the file's style.
	elemIndent := lineIndent(data, closeIdx) + "  "
	elem := buildRepoLiteral(name, url, base)

	var out []byte
	if relEnd, ok := lastElementEnd(data[open+1 : closeIdx]); ok {
		// Non-empty array: insert right after the last element's closing brace, prefixing
		// a comma so the previous last element is separated from the new one. Everything
		// after (trailing comments, whitespace, the closing bracket) is preserved verbatim.
		at := open + 1 + relEnd
		out = splice(data, at, ",\n"+elemIndent+elem)
	} else {
		// Empty array (only whitespace/comments between the brackets): insert right after
		// the opening bracket, no comma. The original inner bytes — including any comment —
		// follow the new element, so nothing is dropped.
		out = splice(data, open+1, "\n"+elemIndent+elem)
	}

	// Belt: never write a manifest that does not re-parse. A splicing bug (or a future
	// mutant) that produces invalid JSON is caught here, before the atomic write touches
	// the user's file.
	if _, err := Parse(out); err != nil {
		return fmt.Errorf("config: internal: rewrite produced an invalid manifest: %w", err)
	}

	if err := lockfs.WriteFileAtomic(path, out, 0o644); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// buildRepoLiteral renders a single repo object literal. base is written only when non-empty
// (an empty base means the repo inherits defaults.base, so the field is omitted entirely).
// Values are quoted via strconv.Quote so a name/url/base containing a quote or backslash
// stays well-formed JSON.
func buildRepoLiteral(name, url, base string) string {
	var b strings.Builder
	b.WriteString(`{ "name": `)
	b.WriteString(strconv.Quote(name))
	b.WriteString(`, "url": `)
	b.WriteString(strconv.Quote(url))
	if base != "" {
		b.WriteString(`, "base": `)
		b.WriteString(strconv.Quote(base))
	}
	b.WriteString(" }")
	return b.String()
}

// splice returns data with ins inserted at byte offset at, as a fresh slice (no aliasing of
// data's backing array).
func splice(data []byte, at int, ins string) []byte {
	out := make([]byte, 0, len(data)+len(ins))
	out = append(out, data[:at]...)
	out = append(out, ins...)
	out = append(out, data[at:]...)
	return out
}

// Scanner states shared by the two textual locators below. They mirror stripJSONC's state
// machine (string context honoring escapes, // line and /* */ block comments) so a brace or
// bracket inside a string or comment never moves the cursor.
const (
	scNormal = iota
	scString
	scLineComment
	scBlockComment
)

// findReposArray locates the top-level `repos` array in src and returns the byte offsets of
// its opening '[' and matching ']'. It tracks nesting depth so the matching bracket is found
// even though the array's elements are themselves brace-delimited objects, and it identifies
// the repos KEY (a string equal to "repos" read at object depth 1, i.e. a direct member of
// the root object) rather than any incidental occurrence of the text. src is assumed to have
// already parsed (Add calls Parse first), so a well-formed repos array is guaranteed present.
func findReposArray(src []byte) (open, closeIdx int, err error) {
	state := scNormal
	escaped := false
	depth := 0
	// phase: 0 searching for the repos key; 1 key found, awaiting ':'; 2 awaiting '['; 3 inside.
	phase := 0
	arrayDepth := 0
	strStart := -1

	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case scNormal:
			switch {
			case c == '"':
				state = scString
				strStart = i + 1
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				state = scLineComment
				i++
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				state = scBlockComment
				i++
			case c == '{' || c == '[':
				if phase == 2 && c == '[' {
					open = i
					arrayDepth = depth // depth of the array's contents' container, before descending
					phase = 3
				}
				depth++
			case c == '}' || c == ']':
				depth--
				if phase == 3 && c == ']' && depth == arrayDepth {
					return open, i, nil
				}
			case c == ':':
				if phase == 1 {
					phase = 2
				}
			}
		case scString:
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				if phase == 0 && depth == 1 && string(src[strStart:i]) == "repos" {
					phase = 1
				}
				state = scNormal
			}
		case scLineComment:
			if c == '\n' {
				state = scNormal
			}
		case scBlockComment:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				i++
				state = scNormal
			}
		}
	}
	return 0, 0, fmt.Errorf("config: could not locate the repos array in the manifest")
}

// lastElementEnd scans the bytes BETWEEN the repos brackets and returns the offset (relative
// to that content) just past the last element's closing brace, plus whether any element was
// found. Elements are objects (the manifest schema, already validated by Parse), so element
// boundaries are object-depth returns to zero. Returns ok=false for an array holding only
// whitespace and/or comments (an empty repos list).
func lastElementEnd(content []byte) (int, bool) {
	state := scNormal
	escaped := false
	depth := 0
	last := -1
	for i := 0; i < len(content); i++ {
		c := content[i]
		switch state {
		case scNormal:
			switch {
			case c == '"':
				state = scString
			case c == '/' && i+1 < len(content) && content[i+1] == '/':
				state = scLineComment
				i++
			case c == '/' && i+1 < len(content) && content[i+1] == '*':
				state = scBlockComment
				i++
			case c == '{' || c == '[':
				depth++
			case c == '}' || c == ']':
				depth--
				if depth == 0 {
					last = i + 1
				}
			}
		case scString:
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				state = scNormal
			}
		case scLineComment:
			if c == '\n' {
				state = scNormal
			}
		case scBlockComment:
			if c == '*' && i+1 < len(content) && content[i+1] == '/' {
				i++
				state = scNormal
			}
		}
	}
	if last == -1 {
		return 0, false
	}
	return last, true
}

// lineIndent returns the leading whitespace (spaces/tabs) of the line containing byte offset
// idx — used to align an inserted element under the array's closing bracket.
func lineIndent(src []byte, idx int) string {
	start := idx
	for start > 0 && src[start-1] != '\n' {
		start--
	}
	end := start
	for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	return string(src[start:end])
}
