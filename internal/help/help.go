// Package help is wi's SOLE owner of the progressive-disclosure help model and the
// next[] follow-up rules (DESIGN §3.1 "internal/help — progressive-disclosure help
// model + next[] rules (SOLE owner)" / IMPLEMENTATION_PLAN M3). It is pure — no I/O, no
// dependencies — so the cli envelope writer (the single injector, DESIGN §7) can call it
// deterministically and the golden envelopes stay byte-stable.
//
// One command-metadata table (Commands) is the single source of truth for the command
// surface, so help can never lie about which commands exist or what to run next: the
// table IS the help text AND the source the cli-layer sync fitness checks the live
// registry against. Topic "" is the top-level overview (lists every command); a command
// name (e.g. "sync", "isolate new") is that command's detail. An unknown topic returns
// ok=false so the caller can turn it into a not_found refusal carrying a did_you_mean
// hint from internal/suggest.
package help

// Command is one row of wi's command-metadata table: the canonical command name (the
// same string the cli registry resolves argv against, e.g. "isolate new"), a one-line
// synopsis, the usage string, and the runnable follow-up commands an agent or human runs
// next. Next entries are full command lines ("wi sync"), runnable verbatim per the
// envelope's next[] contract.
type Command struct {
	Name     string
	Synopsis string
	Usage    string
	Next     []string
}

// Model is the progressive-disclosure help payload for one topic. For the overview
// (Topic == "") Commands holds the full table and Usage is empty; for a single command
// Commands is nil and Usage/Next describe just that command. Next is the runnable
// follow-up set the cli writer projects into the envelope's next[].
type Model struct {
	Topic    string
	Synopsis string
	Usage    string
	Commands []Command
	Next     []string
}

// overview is the top-level tagline shown when `wi help` is run with no topic. It states
// what wi is so an agent reading the JSON help envelope cold knows the tool's purpose.
const overview = "wi — deterministic multi-repo workspace isolation for parallel agents"

// table is wi's command-metadata table: the single source of truth for the command
// surface, ordered as the canonical MVP workflow (init → repo add → sync → isolate new →
// resolve → isolate rm) so the overview reads as a runbook and each command's Next points
// at the natural following step, FOLLOWED BY the maintenance commands (lock ls / lock
// break) that are not workflow steps but real commands an operator runs out-of-band. The
// lock pair cross-reference each other's Next (inspect → break → re-inspect). The cli sync
// fitness checks this against the live registry so the two can never drift.
var table = []Command{
	{
		Name:     "init",
		Synopsis: "create the wi workspace skeleton in the current directory",
		Usage:    "wi init",
		Next:     []string{"wi repo add <url>"},
	},
	{
		Name:     "repo add",
		Synopsis: "register a source repository in the workspace",
		Usage:    "wi repo add <url>",
		Next:     []string{"wi sync"},
	},
	{
		Name:     "sync",
		Synopsis: "fetch every registered repo into its local mirror",
		Usage:    "wi sync",
		Next:     []string{"wi isolate new <task> <repo>…"},
	},
	{
		Name:     "isolate new",
		Synopsis: "materialize an isolated worktree set for a task",
		Usage:    "wi isolate new <task> <repo>…",
		Next:     []string{"wi resolve <task>", "wi isolate rm <task>"},
	},
	{
		Name:     "resolve",
		Synopsis: "print the path bundle for a task's isolate",
		Usage:    "wi resolve <task>",
		Next:     []string{"wi isolate rm <task>"},
	},
	{
		Name:     "isolate rm",
		Synopsis: "remove a task's isolate and release its worktrees",
		Usage:    "wi isolate rm <task>",
		Next:     []string{"wi isolate new <task> <repo>…"},
	},
	{
		Name:     "isolate repair",
		Synopsis: "reconcile a task's isolate with its on-disk worktrees and ownership markers",
		Usage:    "wi isolate repair <task>",
		Next:     []string{"wi resolve <task>", "wi isolate rm <task>"},
	},
	{
		Name:     "gc",
		Synopsis: "reclaim leftover isolate worktrees wi can prove it owns; preview with --dry-run",
		Usage:    "wi gc [--dry-run]",
		Next:     []string{"wi gc --dry-run", "wi lock ls"},
	},
	{
		Name:     "lock ls",
		Synopsis: "list workspace locks and whether each is safe to break",
		Usage:    "wi lock ls",
		Next:     []string{"wi lock break <key>"},
	},
	{
		Name:     "lock break",
		Synopsis: "displace a stale lock, but only when its holder is proven dead",
		Usage:    "wi lock break <key>",
		Next:     []string{"wi lock ls"},
	},
}

// Commands returns a fresh copy of wi's command-metadata table, best-match order. A copy
// is returned so a caller (or a fitness comparison) can never mutate the package's single
// source of truth.
func Commands() []Command {
	out := make([]Command, len(table))
	for i, c := range table {
		c.Next = append([]string(nil), c.Next...)
		out[i] = c
	}
	return out
}

// For maps a topic to its help Model. An empty topic returns the overview — the tagline
// plus the full command table plus the getting-started Next. A topic naming a command
// returns that command's synopsis/usage/next (Commands left nil — drilling in does not
// re-list the whole table). An unknown topic returns the zero Model and ok=false so the
// caller can refuse with a did_you_mean hint rather than inventing help text.
func For(topic string) (Model, bool) {
	if topic == "" {
		return Model{
			Synopsis: overview,
			Commands: Commands(),
			Next:     []string{"wi init", "wi help init"},
		}, true
	}
	for _, c := range table {
		if c.Name == topic {
			return Model{
				Topic:    c.Name,
				Synopsis: c.Synopsis,
				Usage:    c.Usage,
				Next:     c.Next,
			}, true
		}
	}
	return Model{}, false
}
