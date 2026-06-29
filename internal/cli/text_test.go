package cli_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/cli"
	"github.com/ggkguelensan/workspace-isolation/internal/contract"
)

// Guard SHAPE-TEXT-PROJECTION: `--format text` is a PURE, path-scoped projection of the
// SAME contract.Envelope the JSON path serializes — "no extra facts, no dropped facts"
// (DESIGN §3.1), and never a re-read of git/state. The renderer is hand-written (human
// formatting), so losslessness is verified INDEPENDENTLY: a reflection walk collects
// every string leaf in the envelope and asserts each appears in the rendered text. The
// two derivations are separate, so a hand-written renderer that forgets a field is
// caught by the walk — that is the whole point of the guard.
//
// Non-vacuity mutant (registered): drop ANY field from the renderer — e.g. comment out
// the worktree line in renderRepo — => its sentinel leaf ("zworktreeonez") is absent
// from the text => TestRenderTextIsLossless RED. The inline checks (a substantial leaf
// count + a never-present sentinel that must NOT match) keep the containment loop from
// passing vacuously.

func render(t *testing.T, env contract.Envelope) string {
	t.Helper()
	var b bytes.Buffer
	if err := cli.RenderText(&b, env); err != nil {
		t.Fatalf("RenderText: %v", err)
	}
	return b.String()
}

// maximalEnvelope populates every field an MVP envelope can carry. Free-form string
// fields get globally-unique "z…z" sentinels (so a dropped field is an unambiguous
// missing substring); closed-enum fields use real, mutually-distinct values that are
// not substrings of any capability token (e.g. top-level error kind is `internal`, not
// `partial`, since "partial" ⊂ the "partial-success" capability).
func maximalEnvelope() contract.Envelope {
	return contract.Envelope{
		SchemaVersion: contract.SchemaVersion,
		Capabilities:  contract.Capabilities(),
		OpID:          "zopidz",
		Command:       "zcommandz",
		OK:            false,
		Action:        contract.ActionSynced,
		DryRun:        true,
		Repos: []contract.RepoResult{
			{
				Repo:      "zrepoonez",
				Action:    contract.ActionCreated,
				Branch:    "zbranchonez",
				Worktree:  "zworktreeonez",
				Mirror:    "zmirroronez",
				SHA:       "zshaonez",
				Stage:     "zstageonez",
				MainState: "zmainonez",
				Freshness: &contract.MirrorFreshness{Stale: true, FetchedAt: "zfetchedz", BehindOriginAsOfFetch: 73},
			},
			{
				Repo:   "zrepotwoz",
				Action: contract.ActionRemoved,
				Error: &contract.Error{
					Kind:       contract.KindDirtyWorktree,
					Code:       "zcodetwoz",
					Message:    "zmsgtwoz",
					Repo:       "zerrrepotwoz",
					Help:       "zhelptwoz",
					DidYouMean: []string{"zdymtwoaz", "zdymtwobz"},
				},
			},
		},
		Warnings: []contract.Warning{
			{Code: contract.WarnBaseBehindSSOT, Message: "zwarnmsgz", Repo: "zwarnrepoz"},
		},
		Next: []string{"znextonez", "znexttwoz"},
		Resolve: &contract.ResolveBlock{
			IsolateRoot: "zisorootz",
			StateDir:    "zstatedirz",
			Log:         "zlogz",
			Repos: []contract.ResolveRepo{
				{Repo: "zrrrepoz", Worktree: "zrrwtz", Mirror: "zrrmz", Branch: "zrrbz"},
			},
		},
		Planned: []contract.PlanItem{
			{Repo: "zplrepoz", Action: "zplactionz", Detail: "zpldetailz"},
		},
		Blocked: []contract.BlockItem{
			{Repo: "zblrepoz", Kind: contract.KindConflict, Reason: "zblreasonz"},
		},
		Error: &contract.Error{
			Kind:       contract.KindInternal,
			Code:       "ztopcodez",
			Message:    "ztopmsgz",
			Repo:       "ztoprepoz",
			Help:       "ztophelpz",
			DidYouMean: []string{"ztopdymz"},
		},
	}
}

func TestRenderTextIsLossless(t *testing.T) {
	env := maximalEnvelope()
	text := render(t, env)

	leaves := collectStringLeaves(reflect.ValueOf(env))
	if len(leaves) < 25 {
		t.Fatalf("non-vacuity: expected the reflection walk to find many string leaves, got %d", len(leaves))
	}
	// Discrimination: a value never placed in the envelope must NOT be found in the
	// render — otherwise strings.Contains is matching too loosely to be meaningful.
	if strings.Contains(text, "zABSENTzNEVERz") {
		t.Fatal("non-vacuity: the containment check matched a value never placed in the envelope")
	}
	for _, leaf := range leaves {
		if !strings.Contains(text, leaf) {
			t.Errorf("text render dropped a fact: %q is in the envelope but not the rendered text\n--- text ---\n%s", leaf, text)
		}
	}
}

// collectStringLeaves walks v and returns every non-empty string leaf (recursing
// pointers, structs, slices, arrays). Named string types (Action, ErrorKind,
// Capability, WarningCode) have Kind()==String, so their values are collected too.
// This is an INDEPENDENT enumeration of "every fact in the envelope" — deliberately
// not the same code path the renderer uses.
func collectStringLeaves(v reflect.Value) []string {
	var out []string
	switch v.Kind() {
	case reflect.String:
		if s := v.String(); s != "" {
			out = append(out, s)
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			out = append(out, collectStringLeaves(v.Elem())...)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			out = append(out, collectStringLeaves(v.Field(i))...)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			out = append(out, collectStringLeaves(v.Index(i))...)
		}
	}
	return out
}

// TestRenderTextRendersStatusAndCounts covers the non-string facts the lossless walk
// can't (bools and the freshness int) and that the three outcome shapes are visually
// distinguishable — what an operator reads at a glance.
func TestRenderTextRendersStatusAndCounts(t *testing.T) {
	m := cli.Meta{OpID: "op", Command: "isolate new"}

	succ := render(t, cli.Success(m, contract.ActionCreated, nil))
	if !strings.Contains(succ, "OK") {
		t.Errorf("success render must show an OK status, got:\n%s", succ)
	}
	if strings.Contains(succ, "FAILED") {
		t.Errorf("success render must not show FAILED, got:\n%s", succ)
	}
	if strings.Contains(succ, "[dry-run]") {
		t.Errorf("a non-dry-run render must not show the dry-run marker, got:\n%s", succ)
	}

	fail := render(t, cli.Failure(m, contract.ActionNoop, contract.Error{Kind: contract.KindNotFound, Message: "nope"}))
	if !strings.Contains(fail, "FAILED") {
		t.Errorf("failure render must show FAILED, got:\n%s", fail)
	}
	if !strings.Contains(fail, "not_found") {
		t.Errorf("failure render must show the error kind, got:\n%s", fail)
	}

	part := render(t, cli.Failure(m, contract.ActionCreated, contract.Error{Kind: contract.KindPartial, Message: "1 of 2"}))
	if !strings.Contains(part, "partial") {
		t.Errorf("partial render must show the partial kind, got:\n%s", part)
	}

	dry := render(t, cli.Success(cli.Meta{OpID: "op", Command: "isolate new", DryRun: true}, contract.ActionNoop, nil))
	if !strings.Contains(dry, "[dry-run]") {
		t.Errorf("dry-run render must show the dry-run marker, got:\n%s", dry)
	}

	fresh := render(t, cli.Success(m, contract.ActionCreated, []contract.RepoResult{{
		Repo: "api", Action: contract.ActionCreated,
		Freshness: &contract.MirrorFreshness{Stale: true, BehindOriginAsOfFetch: 73},
	}}))
	if !strings.Contains(fresh, "73") {
		t.Errorf("freshness render must show the behind-origin count, got:\n%s", fresh)
	}
	if !strings.Contains(fresh, "stale") {
		t.Errorf("freshness render must show the stale flag, got:\n%s", fresh)
	}
}
