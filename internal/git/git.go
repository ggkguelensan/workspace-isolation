// Package git provides wi's deterministic, typed git verbs on top of
// internal/gitexec (DESIGN §4). It contains no path or policy logic — just the
// thin, well-defined git operations the domain packages compose.
//
// Its keystone is FastForwardBaseRef, the SOLE base-ref-mutation path in the
// entire codebase (DESIGN §5). The SSOT clone is kept on a detached HEAD at the
// base tip, and both v0 sync and v1 land advance refs/heads/<base> through this
// one function — fast-forward-only, via update-ref, with no checkout and no
// merge — so the SSOT base is append-only and the two commands coexist on one
// clone with zero rework.
package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ggkguelensan/workspace-isolation/internal/gitexec"
)

// Git runs typed git verbs through a gitexec.Runner. Almost every verb is local
// and uses the offline Runner.Run path; the three network-permitted verbs —
// EnsureClone, Fetch, and FirstExistingRemoteHead — are the sole verbs that route
// through RunNetwork and dial (DESIGN §2 #3, §5).
type Git struct {
	r *gitexec.Runner
}

// New returns a Git backed by r.
func New(r *gitexec.Runner) *Git { return &Git{r: r} }

// ResolveRef returns the commit SHA that ref resolves to in the repo at dir,
// verifying it exists. ref is taken literally (e.g. "HEAD", a full SHA, or
// "refs/heads/main").
func (g *Git) ResolveRef(ctx context.Context, dir, ref string) (string, error) {
	res, err := g.r.Run(ctx, dir, "rev-parse", "--verify", "--end-of-options", ref)
	if err != nil {
		return "", fmt.Errorf("git: resolve ref %q in %s: %w", ref, dir, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// EnsureClone lazily materializes the SSOT clone for originURL at dir, left in
// wi's SSOT posture: a detached HEAD at refs/heads/<base>'s tip (DESIGN §5).
// The base branch ref is created (it is the ref FastForwardBaseRef advances)
// but is NOT left checked out, so later ref advances never disturb a working
// tree. If dir is already a git repo, EnsureClone is a noop — it never
// re-clones and performs no network I/O. Cloning is the one network-permitted
// verb in wi, so it (and only it) routes through gitexec.RunNetwork; the detach
// step is local.
func (g *Git) EnsureClone(ctx context.Context, dir, originURL, base string) error {
	if g.isRepo(ctx, dir) {
		return nil
	}
	// Clone only the base branch, so refs/heads/<base> exists locally at the
	// origin's base tip. The clone creates dir, so it runs from the current
	// working directory (empty cmd dir) with an explicit target.
	if _, err := g.r.RunNetwork(ctx, "", "clone", "--branch", base, "--", originURL, dir); err != nil {
		return fmt.Errorf("git: clone %s (branch %s) into %s: %w", originURL, base, dir, err)
	}
	// Detach HEAD at the freshly checked-out base tip so no branch is checked
	// out; refs/heads/<base> remains in place for FastForwardBaseRef to advance.
	if _, err := g.r.Run(ctx, dir, "switch", "--detach"); err != nil {
		return fmt.Errorf("git: detach HEAD in %s: %w", dir, err)
	}
	return nil
}

// Fetch updates dir's remote-tracking refs (refs/remotes/<remote>/*) from the
// network. With clone it is one of only three network-permitted verbs in wi, so it
// routes through gitexec.RunNetwork. Fetch never moves a local branch ref and
// never touches the working tree — advancing the SSOT base is FastForwardBaseRef's
// exclusive job on the sync path (DESIGN §5). remote is wi-internal (always
// "origin"), not user input.
func (g *Git) Fetch(ctx context.Context, dir, remote string) error {
	if _, err := g.r.RunNetwork(ctx, dir, "fetch", "--end-of-options", remote); err != nil {
		return fmt.Errorf("git: fetch %s in %s: %w", remote, dir, err)
	}
	return nil
}

// FirstExistingBase returns the first candidate (in the caller's preference order)
// that exists as a LOCAL branch refs/heads/<candidate> in the repo at dir, plus the
// sha it points at. It is the mirror-side resolver for a base declared as an ordered
// candidate list — defaults.base / repo.base = ["dev","main"] meaning "prefer dev,
// fall back to main" (DESIGN §1). Once a single-branch SSOT mirror exists it tracks
// exactly one base branch (EnsureClone clones --branch <base>), so reading the
// candidate list back against the mirror deterministically yields that one branch;
// the genuine candidate-vs-origin choice happens once, at first sync, via
// FirstExistingRemoteHead. It is a local, read-only operation mirroring OwnedRefSHA's
// `rev-parse --verify --quiet` contract: a ref that resolves (exit 0, sha emitted) is
// the answer; a valid-but-absent ref (exit 1, no output) falls through to the next
// candidate; any other failure is a real error. found=false with a nil error means
// none of the candidates exist locally.
func (g *Git) FirstExistingBase(ctx context.Context, dir string, candidates []string) (branch, sha string, found bool, err error) {
	for _, c := range candidates {
		ref := "refs/heads/" + c
		res, runErr := g.r.Run(ctx, dir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
		if runErr != nil {
			var ee *gitexec.ExitError
			if errors.As(runErr, &ee) && ee.Result.ExitCode == 1 {
				continue // valid-but-absent ref: try the next candidate
			}
			return "", "", false, fmt.Errorf("git: resolve base candidate %s in %s: %w", ref, dir, runErr)
		}
		return c, strings.TrimSpace(res.Stdout), true, nil
	}
	return "", "", false, nil
}

// FirstExistingRemoteHead returns the first candidate (in the caller's preference
// order) that exists as a head refs/heads/<candidate> on the remote at originURL,
// discovered with `git ls-remote --heads`. It is the origin-side resolver for an
// ordered base candidate list, used exactly once per repo: at FIRST sync, BEFORE the
// single-branch SSOT mirror is cloned, when there is no local mirror to read the
// candidates against (the mirror, cloned --branch <base>, only ever tracks that one
// branch). It is wi's THIRD and final network-permitted verb (with EnsureClone and
// Fetch), so it routes through gitexec.RunNetwork; the offline belt refuses it.
// found=false with a nil error means none of the candidates exist on the remote.
func (g *Git) FirstExistingRemoteHead(ctx context.Context, originURL string, candidates []string) (branch string, found bool, err error) {
	res, err := g.r.RunNetwork(ctx, "", "ls-remote", "--heads", "--end-of-options", originURL)
	if err != nil {
		return "", false, fmt.Errorf("git: ls-remote heads from %s: %w", originURL, err)
	}
	// Each non-empty line is "<sha>\trefs/heads/<name>"; collect the head refs.
	present := make(map[string]bool)
	for _, line := range strings.Split(res.Stdout, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 {
			present[fields[1]] = true
		}
	}
	for _, c := range candidates {
		if present["refs/heads/"+c] {
			return c, true, nil
		}
	}
	return "", false, nil
}

// DivergedCounts reports how many commits local is ahead of and behind remote,
// computed from LOCAL refs only (no network). It is the basis for both freshness
// (behind = how far the base trails origin as of the last fetch) and main_state
// classification (ahead/behind/diverged). Both refs must resolve; the counts come
// from `rev-list --left-right --count local...remote`, whose left column is the
// ahead count and right column the behind count.
func (g *Git) DivergedCounts(ctx context.Context, dir, local, remote string) (ahead, behind int, err error) {
	res, err := g.r.Run(ctx, dir, "rev-list", "--left-right", "--count", "--end-of-options", local+"..."+remote)
	if err != nil {
		return 0, 0, fmt.Errorf("git: diverged counts %s...%s in %s: %w", local, remote, dir, err)
	}
	fields := strings.Fields(res.Stdout)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("git: unexpected rev-list output %q for %s...%s in %s", res.Stdout, local, remote, dir)
	}
	if ahead, err = strconv.Atoi(fields[0]); err != nil {
		return 0, 0, fmt.Errorf("git: parse ahead count %q: %w", fields[0], err)
	}
	if behind, err = strconv.Atoi(fields[1]); err != nil {
		return 0, 0, fmt.Errorf("git: parse behind count %q: %w", fields[1], err)
	}
	return ahead, behind, nil
}

// AddWorktree materializes a linked git worktree at worktreePath off the SSOT
// clone at ssotDir, checked out DETACHED at rev. It is the per-repo isolate
// materialization primitive isolate new composes (DESIGN §1, §6.3).
//
// Two properties are load-bearing. The worktree shares the SSOT's object store —
// native git worktree sharing, so no objects are duplicated (DESIGN §1 line 30):
// the worktree's .git is a gitlink file into <ssotDir>/.git/worktrees/<id>. And
// it is DETACHED (--detach forces this even when rev names a branch), so it holds
// no branch ref; the SSOT base ref is therefore never "checked out in a worktree"
// and FastForwardBaseRef can always advance it (the keystone, DESIGN §5). It is a
// local operation (offline Run). rev is wi-internal — a SHA or a ref such as
// refs/heads/<base>. Ownership/gc-protection via the refs/wi/owned/<task>/<repo>
// marker (DESIGN §7.1) is layered on by a separate step, not here.
func (g *Git) AddWorktree(ctx context.Context, ssotDir, worktreePath, rev string) error {
	if _, err := g.r.Run(ctx, ssotDir, "worktree", "add", "--detach", worktreePath, rev); err != nil {
		return fmt.Errorf("git: add worktree %s at %s in %s: %w", worktreePath, rev, ssotDir, err)
	}
	return nil
}

// PruneWorktrees deregisters stale linked-worktree admin entries from the SSOT at
// ssotDir with `git worktree prune` — the entries left behind when a worktree's
// directory was removed out-of-band (an external `rm -rf`, a crash mid-materialize)
// instead of via `git worktree remove`. Such a stale entry makes its path "missing but
// already registered", which would make `git worktree add` refuse a re-add; pruning it
// clears the path for re-materialization. It deregisters ONLY entries whose working
// directory is genuinely missing — git never prunes a live worktree — so it can be run
// safely before the HEAL-1 reconciler re-adds a MissingWorktree cell at its marker sha.
// It is a local operation (offline Run) and is idempotent (a no-op when nothing is
// stale).
func (g *Git) PruneWorktrees(ctx context.Context, ssotDir string) error {
	if _, err := g.r.Run(ctx, ssotDir, "worktree", "prune"); err != nil {
		return fmt.Errorf("git: prune worktrees in %s: %w", ssotDir, err)
	}
	return nil
}

// ownedRef is the wi-owned marker ref for (task, repo). Like FastForwardBaseRef's
// "refs/heads/"+base, the namespace is wi convention encoded in exactly one place:
// markers live under refs/wi/owned/ — a ref (so its commit stays gc-reachable) that
// is NOT a branch (so it never appears as a stray branch in the pristine SSOT,
// DESIGN §5).
// ownedRefPrefix is the ref namespace every wi ownership marker lives under
// (refs/wi/owned/<task>/<repo>). It is the single source for both one cell's marker
// name (ownedRef) and the for-each-ref pattern that enumerates the whole set
// (ListOwnedRefs), so the writer and the enumerator provably cannot drift.
const ownedRefPrefix = "refs/wi/owned/"

func ownedRef(task, repo string) string {
	return ownedRefPrefix + task + "/" + repo
}

// backupRefPrefix is the ref namespace every land pre-move backup anchor lives under
// (refs/wi/backup/<task>/<repo>). It is DELIBERATELY DISTINCT from ownedRefPrefix:
// DESIGN §7.1 protects refs/wi/backup/* from gc by keeping it out of the owned-marker
// candidate population (ListOwnedRefs scopes to ownedRefPrefix), so a backup anchor is
// never classified as a reclaimable cell and collected — which would destroy the
// `land abort` restore point. The single-source-of-truth posture mirrors ownedRefPrefix.
const backupRefPrefix = "refs/wi/backup/"

func backupRef(task, repo string) string {
	return backupRefPrefix + task + "/" + repo
}

// CreateOwnedRef records wi's ownership of the (task, repo) worktree by atomically
// creating the marker ref refs/wi/owned/<task>/<repo> at sha (a single update-ref).
// This is the POSITIVE evidence reclamation requires (DESIGN §7.1, decision #2): a
// worktree or branch is reclaimable only if such a marker proves wi created it; an
// unexplained orphan with no marker is a hard block, never auto-pruned. A git ref
// is chosen over a note/reflog precisely because it gives atomic creation and gc-
// protection (the ref keeps its commit reachable) while staying out of the branch
// namespace. It is a local operation. task/repo are wi-internal and already
// segment-validated by the caller before they reach here — this package holds no
// path policy, exactly as base is the caller's concern in FastForwardBaseRef.
func (g *Git) CreateOwnedRef(ctx context.Context, ssotDir, task, repo, sha string) error {
	ref := ownedRef(task, repo)
	if _, err := g.r.Run(ctx, ssotDir, "update-ref", ref, sha); err != nil {
		return fmt.Errorf("git: create owned ref %s -> %s in %s: %w", ref, sha, ssotDir, err)
	}
	return nil
}

// OwnedRefSHA reports the sha the marker ref refs/wi/owned/<task>/<repo> points at
// and whether it exists, cleanly distinguishing a genuinely absent marker
// (exists=false, nil error — the "no ownership recorded" case reclamation inspects
// on an orphan) from a real read failure. It is a local, read-only operation:
// `rev-parse --verify --quiet` emits the sha and exits 0 when the ref resolves, and
// exits 1 with no output for a valid-but-absent ref.
func (g *Git) OwnedRefSHA(ctx context.Context, ssotDir, task, repo string) (sha string, exists bool, err error) {
	ref := ownedRef(task, repo)
	res, runErr := g.r.Run(ctx, ssotDir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
	if runErr != nil {
		var ee *gitexec.ExitError
		if errors.As(runErr, &ee) && ee.Result.ExitCode == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("git: read owned ref %s in %s: %w", ref, ssotDir, runErr)
	}
	return strings.TrimSpace(res.Stdout), true, nil
}

// CreateBackupRef captures sha in the land pre-move anchor refs/wi/backup/<task>/<repo>
// (a single atomic update-ref). `land` writes this BEFORE advancing a base ref so the
// pre-land tip stays gc-reachable and `land abort`/recovery can restore it WITHOUT a
// `git reset --hard` (DESIGN §7.2). It lives in a namespace distinct from owned markers
// so gc never treats it as a reclamation candidate (DESIGN §7.1). It is a local
// operation. task/repo are wi-internal and already segment-validated by the caller
// before they reach here, exactly as in CreateOwnedRef.
func (g *Git) CreateBackupRef(ctx context.Context, ssotDir, task, repo, sha string) error {
	ref := backupRef(task, repo)
	if _, err := g.r.Run(ctx, ssotDir, "update-ref", ref, sha); err != nil {
		return fmt.Errorf("git: create backup ref %s -> %s in %s: %w", ref, sha, ssotDir, err)
	}
	return nil
}

// BackupRefSHA reports the sha the land anchor refs/wi/backup/<task>/<repo> points at
// and whether it exists, cleanly distinguishing a genuinely absent anchor (exists=false,
// nil error — a still-pending repo has none) from a real read failure. `land abort`
// reads it to find the restore point; `land status` reports it. It is a local,
// read-only operation, mirroring OwnedRefSHA's `rev-parse --verify --quiet` contract
// (sha + exit 0 when the ref resolves, exit 1 with no output for a valid-but-absent ref).
func (g *Git) BackupRefSHA(ctx context.Context, ssotDir, task, repo string) (sha string, exists bool, err error) {
	ref := backupRef(task, repo)
	res, runErr := g.r.Run(ctx, ssotDir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
	if runErr != nil {
		var ee *gitexec.ExitError
		if errors.As(runErr, &ee) && ee.Result.ExitCode == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("git: read backup ref %s in %s: %w", ref, ssotDir, runErr)
	}
	return strings.TrimSpace(res.Stdout), true, nil
}

// OwnedRef is one enumerated wi ownership marker: the (task, repo) cell it vouches
// for and the sha it pins. The set of OwnedRefs in a mirror is the set of cells wi
// can PROVE it created there — the evidence-positive candidate population the
// workspace gc sweep classifies (DESIGN §7.1, HEAL-2).
type OwnedRef struct {
	Task string
	Repo string
	SHA  string
}

// ListOwnedRefs enumerates every wi ownership marker refs/wi/owned/<task>/<repo> in
// ssotDir, one OwnedRef per marker sorted by refname for determinism. Where
// OwnedRefSHA answers "does wi own THIS cell?", ListOwnedRefs answers "which cells
// does wi own here at all?" — the positive evidence reclamation is built on (DESIGN
// §7.1). It is a local, read-only operation (no network); no markers is the empty
// result, not an error.
//
// The for-each-ref pattern is scoped to ownedRefPrefix, so refs/wi/backup/* (which
// §7.1 PROTECTS from gc) and ordinary branches are never enumerated as candidates.
// A line that does not parse as a well-formed owned marker is a hard error — a
// corrupt or foreign ref surfacing under the namespace is reported, never silently
// fabricated into a phantom cell that gc might then act on.
func (g *Git) ListOwnedRefs(ctx context.Context, ssotDir string) ([]OwnedRef, error) {
	res, err := g.r.Run(ctx, ssotDir, "for-each-ref", "--sort=refname",
		"--format=%(refname) %(objectname)", ownedRefPrefix)
	if err != nil {
		return nil, fmt.Errorf("git: list owned refs in %s: %w", ssotDir, err)
	}
	var out []OwnedRef
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		refname, sha, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("git: malformed for-each-ref line %q in %s", line, ssotDir)
		}
		rest, ok := strings.CutPrefix(refname, ownedRefPrefix)
		if !ok {
			return nil, fmt.Errorf("git: for-each-ref returned non-owned ref %q in %s", refname, ssotDir)
		}
		task, repo, ok := strings.Cut(rest, "/")
		if !ok || task == "" || repo == "" || strings.Contains(repo, "/") {
			return nil, fmt.Errorf("git: malformed owned ref %q in %s", refname, ssotDir)
		}
		out = append(out, OwnedRef{Task: task, Repo: repo, SHA: sha})
	}
	return out, nil
}

// RemoveWorktree removes the linked worktree at worktreePath from the SSOT at
// ssotDir with `git worktree remove`, which deletes the worktree directory AND
// deregisters it from the SSOT's worktree admin (.git/worktrees/<id>) — unlike a
// bare directory delete, which would strand a stale admin entry. It is the
// reclamation verb `isolate rm` composes AFTER proving wi owns the worktree and it
// is clean + not ahead of base (DESIGN §7.1). It passes NO --force and performs NO
// `git reset --hard` (DESIGN §7.2): a worktree carrying modified or untracked files
// is REFUSED and left intact, a second safety net beneath the isolate layer's own
// cleanliness gate. It is a local operation (offline Run).
func (g *Git) RemoveWorktree(ctx context.Context, ssotDir, worktreePath string) error {
	if _, err := g.r.Run(ctx, ssotDir, "worktree", "remove", worktreePath); err != nil {
		return fmt.Errorf("git: remove worktree %s in %s: %w", worktreePath, ssotDir, err)
	}
	return nil
}

// DeleteOwnedRef clears the ownership marker refs/wi/owned/<task>/<repo> with a
// single `update-ref -d`, called once the worktree the marker vouched for has been
// reclaimed (its evidence-positive job is done — DESIGN §7.1). It is a local
// operation. Deleting an already-absent marker is a no-op success (git's update-ref
// -d with no expected old value succeeds on a missing ref), so a re-run of
// reclamation stays idempotent. task/repo are wi-internal and already segment-
// validated by the caller, exactly as in CreateOwnedRef.
func (g *Git) DeleteOwnedRef(ctx context.Context, ssotDir, task, repo string) error {
	ref := ownedRef(task, repo)
	if _, err := g.r.Run(ctx, ssotDir, "update-ref", "-d", ref); err != nil {
		return fmt.Errorf("git: delete owned ref %s in %s: %w", ref, ssotDir, err)
	}
	return nil
}

// isRepo reports whether dir is an existing git repository. It guards the dir's
// existence first so git is never spawned in a missing directory (which would
// be an opaque start failure rather than a clean "not a repo").
func (g *Git) isRepo(ctx context.Context, dir string) bool {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return false
	}
	_, err := g.r.Run(ctx, dir, "rev-parse", "--git-dir")
	return err == nil
}

// StatusPorcelain returns `git status --porcelain` output for dir (machine
// format, stable across git versions). Empty output means a pristine tree.
func (g *Git) StatusPorcelain(ctx context.Context, dir string) (string, error) {
	res, err := g.r.Run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git: status %s: %w", dir, err)
	}
	return res.Stdout, nil
}

// IsClean reports whether dir's working tree and index are completely
// unmodified, including the absence of untracked files. This is the
// SSOT-pristine check (DESIGN §5): any drift at all is not clean.
func (g *Git) IsClean(ctx context.Context, dir string) (bool, error) {
	out, err := g.StatusPorcelain(ctx, dir)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// NonFastForwardError reports that advancing Base to New would not be a
// fast-forward — Current (the present base tip) is not an ancestor of New — so
// the base ref was left unchanged. This is the safety check that keeps the SSOT
// base append-only (it also rejects rewinds).
type NonFastForwardError struct {
	Base    string // base branch name (e.g. "main")
	Current string // SHA the base ref currently points at
	New     string // SHA the caller asked to advance to
}

func (e *NonFastForwardError) Error() string {
	return fmt.Sprintf("git: refusing non-fast-forward update of %s: %s is not an ancestor of %s",
		e.Base, e.Current, e.New)
}

// StaleBaseRefError reports that a RestoreBaseRef (the `land abort` rewind) was refused
// because the base ref is no longer where the caller expected it — Current (the present
// tip) does not equal Expected (the tip the land left, the value abort intends to undo).
// The base ref was left untouched. This is the guard that keeps abort from discarding work
// another op fast-forwarded onto the base since the land: abort restores a known anchor
// only when nothing has moved on top of it.
type StaleBaseRefError struct {
	Base     string // base branch name (e.g. "main")
	Current  string // SHA the base ref currently points at
	Expected string // SHA the caller expected it to still be at
}

func (e *StaleBaseRefError) Error() string {
	return fmt.Sprintf("git: refusing to restore %s: base is at %s, not the expected %s",
		e.Base, e.Current, e.Expected)
}

// IsAncestor reports whether maybeAncestor is an ancestor of descendant in the repo
// at dir — equivalently, whether advancing a ref from maybeAncestor to descendant
// would be a fast-forward. A commit is its own ancestor (the reflexive case is true).
// It runs `git merge-base --is-ancestor`, which exits 0 when true and 1 when false (a
// genuine non-ancestor); ANY other exit code (e.g. a nonexistent revision) is a real
// error, never silently read as false. It mutates nothing, so it is the non-mutating
// twin of FastForwardBaseRef's ff-safety check — `wi land --atomic`'s pre-flight uses
// it to prove EVERY repo would fast-forward before any base ref moves, and
// FastForwardBaseRef calls it so the pre-flight and the actual advance share one predicate.
func (g *Git) IsAncestor(ctx context.Context, dir, maybeAncestor, descendant string) (bool, error) {
	if _, err := g.r.Run(ctx, dir, "merge-base", "--is-ancestor", maybeAncestor, descendant); err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) && ee.Result.ExitCode == 1 {
			return false, nil
		}
		return false, fmt.Errorf("git: ancestry check %s..%s in %s: %w", maybeAncestor, descendant, dir, err)
	}
	return true, nil
}

// FastForwardBaseRef advances refs/heads/<base> in the repo at dir to newRev,
// but ONLY if doing so is a fast-forward (the current base tip is an ancestor of
// newRev). It performs no checkout and no merge, so it operates on the detached
// SSOT clone; on a non-fast-forward it returns *NonFastForwardError and leaves
// the ref untouched. This is the only function in wi permitted to move a base
// ref (DESIGN §5). The ff-safety predicate is IsAncestor, so this advance and
// `wi land --atomic`'s non-mutating pre-flight agree by construction.
func (g *Git) FastForwardBaseRef(ctx context.Context, dir, base, newRev string) error {
	baseRef := "refs/heads/" + base
	current, err := g.ResolveRef(ctx, dir, baseRef)
	if err != nil {
		return err
	}
	newSHA, err := g.ResolveRef(ctx, dir, newRev)
	if err != nil {
		return err
	}

	// Fast-forward safety: current must be an ancestor of newSHA. IsAncestor already
	// distinguishes a genuine non-ancestor (false) from a real fault (error), so a
	// false here is exactly the non-fast-forward refusal — return its error verbatim.
	ff, err := g.IsAncestor(ctx, dir, current, newSHA)
	if err != nil {
		return err
	}
	if !ff {
		return &NonFastForwardError{Base: base, Current: current, New: newSHA}
	}

	// Move the ref with the old value asserted, so a concurrent change between the
	// ancestry check and here makes update-ref fail atomically rather than racing.
	if _, err := g.r.Run(ctx, dir, "update-ref", baseRef, newSHA, current); err != nil {
		return fmt.Errorf("git: update-ref %s -> %s in %s: %w", baseRef, newSHA, dir, err)
	}
	return nil
}

// RestoreBaseRef rewinds refs/heads/<base> in the repo at dir back to restoreTo (a
// pre-land backup anchor), but ONLY if the base ref currently points at expectCurrent —
// the tip the land left it at. It is the one sanctioned base-REWIND path, used by
// `land abort` to undo a landed repo (DESIGN §7.2): the SSOT base normally moves only by
// fast-forward (FastForwardBaseRef), but undoing a land means moving the base back to an
// ANCESTOR, which is by definition not a fast-forward. Safety here is an exact-match guard
// rather than ancestry: if the base is no longer at expectCurrent — e.g. another op
// fast-forwarded more work on top since the land — the rewind is refused with
// *StaleBaseRefError and the ref left untouched, so abort never silently discards work it
// did not land. Like FastForwardBaseRef it does pure ref motion (update-ref, no checkout,
// no merge, never `git reset --hard`), operating on the detached SSOT clone.
//
// The expectation is checked in Go AND asserted to git (update-ref's old-value), so the
// read→write window is closed atomically: a concurrent change between the check and the
// move makes the update-ref fail rather than race.
func (g *Git) RestoreBaseRef(ctx context.Context, dir, base, expectCurrent, restoreTo string) error {
	baseRef := "refs/heads/" + base
	current, err := g.ResolveRef(ctx, dir, baseRef)
	if err != nil {
		return err
	}
	expected, err := g.ResolveRef(ctx, dir, expectCurrent)
	if err != nil {
		return err
	}
	target, err := g.ResolveRef(ctx, dir, restoreTo)
	if err != nil {
		return err
	}

	// Exact-match guard: only rewind if the base is still exactly where the land left it,
	// so work fast-forwarded onto the base since is never clobbered.
	if current != expected {
		return &StaleBaseRefError{Base: base, Current: current, Expected: expected}
	}

	// CAS at the git layer too (old-value asserted), closing the read→update race.
	if _, err := g.r.Run(ctx, dir, "update-ref", baseRef, target, expected); err != nil {
		return fmt.Errorf("git: restore-ref %s -> %s in %s: %w", baseRef, target, dir, err)
	}
	return nil
}
