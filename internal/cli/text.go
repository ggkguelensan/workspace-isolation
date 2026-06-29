package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/ggkguelensan/workspace-isolation/internal/contract"
)

// RenderText writes env as a human-readable report on w: a PURE, path-scoped projection
// of the SAME contract.Envelope the JSON path (Emit) serializes — no extra facts, no
// dropped facts, and never a re-read of git/state (DESIGN §3.1, decision #T). It takes
// the already-assembled struct and only reformats it; the two wire forms can therefore
// never disagree. Losslessness is verified independently by SHAPE-TEXT-PROJECTION (a
// reflection walk over the struct's string leaves), so every field below is mandatory:
// forgetting one reddens the guard.
//
// Layout: a status header (command, ok, dry-run), the per-invocation metadata line, then
// one section per populated block (repos, resolve, planned, blocked, warnings, error,
// next). Empty optional fields/blocks are omitted — absence carries no fact to drop.
func RenderText(w io.Writer, env contract.Envelope) error {
	var b strings.Builder

	status := "OK"
	if !env.OK {
		status = "FAILED"
	}
	fmt.Fprintf(&b, "%s — %s", env.Command, status)
	if env.DryRun {
		b.WriteString(" [dry-run]")
	}
	b.WriteByte('\n')

	fmt.Fprintf(&b, "op_id: %s  action: %s  schema: %s\n", env.OpID, env.Action, env.SchemaVersion)
	if len(env.Capabilities) > 0 {
		caps := make([]string, len(env.Capabilities))
		for i, c := range env.Capabilities {
			caps[i] = string(c)
		}
		fmt.Fprintf(&b, "capabilities: %s\n", strings.Join(caps, ", "))
	}

	if len(env.Repos) > 0 {
		b.WriteString("repos:\n")
		for _, r := range env.Repos {
			renderRepo(&b, r)
		}
	}

	if env.Resolve != nil {
		renderResolve(&b, env.Resolve)
	}

	if len(env.Planned) > 0 {
		b.WriteString("planned:\n")
		for _, p := range env.Planned {
			fmt.Fprintf(&b, "  %s  %s  %s\n", p.Repo, p.Action, p.Detail)
		}
	}

	if len(env.Blocked) > 0 {
		b.WriteString("blocked:\n")
		for _, bl := range env.Blocked {
			fmt.Fprintf(&b, "  %s  %s: %s\n", bl.Repo, bl.Kind, bl.Reason)
		}
	}

	if len(env.Warnings) > 0 {
		b.WriteString("warnings:\n")
		for _, wn := range env.Warnings {
			fmt.Fprintf(&b, "  %s  %s: %s\n", wn.Code, wn.Repo, wn.Message)
		}
	}

	if env.Error != nil {
		b.WriteString("error:\n")
		renderError(&b, *env.Error, "  ")
	}

	if len(env.Next) > 0 {
		b.WriteString("next:\n")
		for _, n := range env.Next {
			fmt.Fprintf(&b, "  %s\n", n)
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// renderRepo projects one repos[] entry: the headline (repo, action, and the present
// scalar facts) then the path bundle and any per-repo freshness/error.
func renderRepo(b *strings.Builder, r contract.RepoResult) {
	fmt.Fprintf(b, "  %s  %s", r.Repo, r.Action)
	if r.SHA != "" {
		fmt.Fprintf(b, "  sha %s", r.SHA)
	}
	if r.Stage != "" {
		fmt.Fprintf(b, "  stage %s", r.Stage)
	}
	if r.MainState != "" {
		fmt.Fprintf(b, "  main_state %s", r.MainState)
	}
	b.WriteByte('\n')

	if r.Branch != "" {
		fmt.Fprintf(b, "    branch:   %s\n", r.Branch)
	}
	if r.Worktree != "" {
		fmt.Fprintf(b, "    worktree: %s\n", r.Worktree)
	}
	if r.Mirror != "" {
		fmt.Fprintf(b, "    mirror:   %s\n", r.Mirror)
	}
	if r.Freshness != nil {
		f := r.Freshness
		state := "fresh"
		if f.Stale {
			state = "stale"
		}
		fmt.Fprintf(b, "    mirror_freshness: %s, behind %d", state, f.BehindOriginAsOfFetch)
		if f.FetchedAt != "" {
			fmt.Fprintf(b, ", fetched %s", f.FetchedAt)
		}
		b.WriteByte('\n')
	}
	if r.Error != nil {
		renderError(b, *r.Error, "    ")
	}
}

// renderError projects an Error payload at the given indent. The leading kind (and
// optional code) is what an operator reads; message/repo/help/did_you_mean follow.
func renderError(b *strings.Builder, e contract.Error, indent string) {
	fmt.Fprintf(b, "%s%s", indent, e.Kind)
	if e.Code != "" {
		fmt.Fprintf(b, " [%s]", e.Code)
	}
	fmt.Fprintf(b, ": %s\n", e.Message)
	if e.Repo != "" {
		fmt.Fprintf(b, "%srepo: %s\n", indent, e.Repo)
	}
	if e.Help != "" {
		fmt.Fprintf(b, "%shelp: %s\n", indent, e.Help)
	}
	if len(e.DidYouMean) > 0 {
		fmt.Fprintf(b, "%sdid_you_mean: %s\n", indent, strings.Join(e.DidYouMean, ", "))
	}
}

// renderResolve projects the resolve path bundle (the heart of a path-scoped render).
func renderResolve(b *strings.Builder, r *contract.ResolveBlock) {
	b.WriteString("resolve:\n")
	fmt.Fprintf(b, "  isolate_root: %s\n", r.IsolateRoot)
	fmt.Fprintf(b, "  state_dir: %s\n", r.StateDir)
	fmt.Fprintf(b, "  log: %s\n", r.Log)
	if len(r.Repos) > 0 {
		b.WriteString("  repos:\n")
		for _, rr := range r.Repos {
			fmt.Fprintf(b, "    %s  worktree=%s mirror=%s branch=%s\n", rr.Repo, rr.Worktree, rr.Mirror, rr.Branch)
		}
	}
}
