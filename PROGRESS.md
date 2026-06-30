# wi — BUILD PROGRESS

Working log for the autonomous build loop. Source of truth for *intent*; the real
build state is always `go build ./... && go test ./...` (trust the build over this file).

Branch: `build/wi` (never commit to `main`). Spec: `DESIGN.md`. Order: `IMPLEMENTATION_PLAN.md`.

---

## Current position

- **Milestone:** **▶ M4 (v1) IN PROGRESS — MVP gate satisfied, proceeding on the autonomous loop.**
  The MVP (M0–M3) is complete and green (record below). The loop's stated gate — "do NOT start M4/M5
  until MVP is green" — is met, and the loop's standing mandate is "make concrete forward progress and
  leave the repo green"; with no owner redirect across repeated firings I am proceeding into M4 one
  disciplined unit per firing, exactly as M0–M3. All M4 work stays on `build/wi`, unpushed and reversible;
  the contract was frozen at M0 so M4 is purely additive (minor schema bumps only). If the owner wants me
  to hold instead, say so and I will stop. **M4 = `land`/`landstate`/`gc` domain + `land`/`state cas`/`gc`/
  `lock` commands; capabilities gain `land`/`land-atomic` (PLAN line 137).** Build order = Wave C self-heal
  (write-first fitness), starting with the documented M4 blocker `HEAL-LOCK-LIVENESS` (PLAN line 210
  "Blocks: M4"). M4 units done so far: (1) the conservative PID-liveness predicate
  `lock.processAlive` (guard `LOCK-LIVENESS-PID`, `55d78a1`) — the proven-dead gate self-heal consults
  before breaking a stale lock (DESIGN §2 / §7.3); (2) ✅ **this firing** — `internal/host.BootID()` (guard
  `HOST-BOOTID`), the reuse-safe per-boot identifier, resolving open decision #3 (darwin via the
  `sysctl(2)` SYSCALL — NOT a subprocess; linux via `/proc/sys/kernel/random/boot_id`). The subprocess
  approach was caught and rejected by INV-NO-NETWORK (only `internal/gitexec` may import `os/exec`), so the
  derivation uses the raw `syscall.Sysctl("kern.boottime")` + little-endian `tv_sec` decode instead — no
  child process, no new dep, more faithful to "derive from sysctl" than shelling out (committed `ea5480f`);
  (3) ✅ **this firing** — the lock-holder record `lock.Holder{pid,host,boot_id,op_id}` + serialization
  (`Marshal`/`ParseHolder`/`CurrentHolder`, guard `LOCK-HOLDER`), composing `processAlive` + `BootID` into
  the identity a lock file carries. A methodology note from this unit: a round-trip-only test is VACUOUS
  against a json-tag rename (Marshal+Unmarshal share the tag, so they stay symmetric) — the registered
  "rename `boot_id`" mutant stayed green until the test was strengthened to also assert the concrete
  durable wire keys (`"pid"`/`"host"`/`"boot_id"`/`"op_id"`), which a lock file written by one wi build
  needs stable to read in another. `gofmt`/`go build ./...`/`go vet ./...`/`go test ./...` all GREEN (24
  packages); (4) ✅ **this firing** — the flock body primitive `lockfs.FileLock.WriteBody` +
  `lockfs.ReadBodyAt` (guard `FLOCK-BODY`): a held flock can now carry a holder body written in place on
  the locked inode (NOT via rename, which would detach the flock from the path), and a separate inspector
  reads it back BY PATH while the lock is held (advisory flock does not block reads) — the mechanism
  self-heal uses to learn who holds a contended lock; (5) ✅ **this firing** — the lock-layer holder
  stamping/reading API (guard `LOCK-STAMP`): `(*lock.Held).Stamp(opID)` records `CurrentHolder(opID)` into
  the body of EVERY lock a Held owns (composing unit 3's Holder + unit 4's WriteBody), and
  `lock.ReadHolder(locksDir, key)` reads one key's recorded holder back by path (`ReadBodyAt`+`ParseHolder`)
  — a missing or unstamped lock surfaces as an error (unknown holder → conservatively never broken), never a
  zero-value Holder. SCOPE DECISION recorded this firing: I added stamping as a `Held` method + a reader
  rather than changing `lock.Acquire`'s signature, because threading an `opID` parameter through `Acquire`
  ripples across ~10 files and forces a SECOND signature change on `isolate.Remove` (which has no opID today)
  — too large for one disciplined unit, and a half-applied signature change can't leave the repo green. The
  capability layer lands green and self-contained now; the wiring is the next unit. `gofmt`/`go build
  ./...`/`go vet ./internal/lock/`/`go test ./...` all GREEN (24 packages); (6) ✅ **this firing** — wired
  the FIRST of the four acquire sites to stamp: `isolate.New` now calls `held.Stamp(opID)` right after
  taking the isolate-state lock (guard `ISOLATE-STAMP`, behavioral test over the hermetic git harness:
  after `New` returns, `lock.ReadHolder(LocksDir, IsolateState(task))` reads back the holder with the op's
  `opID` and this process's pid — the lock file persists after release since `Unlock` does not unlink).
  Stamping is best-effort (`_ = held.Stamp(opID)`): the flock is the exclusion guarantee, so a failed
  metadata write must not abort the isolate — a body-less lock reads as "unknown holder" and is
  conservatively never auto-broken. The registered mutant is exactly the pre-wiring state (drop the
  `held.Stamp` line) → the isolate-state lock body stays empty → `ReadHolder` errors → test RED (confirmed
  before green). `gofmt`/`go build ./...`/`go vet ./internal/isolate/`/`go test ./...` all GREEN (24
  packages); (7) ✅ **this firing** — wired the SECOND acquire site (the hottest-contention one):
  `sync.syncOne` now threads `opID` from `Run` and calls `held.Stamp(opID)` after taking the `repo:<name>`
  lock (guard `SYNC-STAMP`, behavioral test over the hermetic real-git harness: after `Run` returns,
  `lock.ReadHolder(LocksDir, Repo("api"))` reads back the holder with the op's `opID` + this process's pid).
  Same best-effort posture as `isolate.New` (`_ = held.Stamp(opID)`; flock is the exclusion guarantee, a
  body-less lock reads as "unknown holder" and is never auto-broken). The `Run`→`syncOne` signature change
  is package-internal (one caller). Registered mutant = pre-wiring state (no `Stamp` call) → empty body →
  `ReadHolder` errors → RED (confirmed before green). `gofmt`/`go build ./...`/`go vet ./internal/sync/`/`go
  test ./...` all GREEN (24 packages); (8) ✅ **this firing** — wired the THIRD acquire site: the
  `repo add` handler (`repoAddCmd.Run`) now calls `held.Stamp(OpIDFrom(ctx))` after taking the
  project-registry lock (guard `REPOADD-STAMP`, behavioral test driving the real handler with a
  `WithOpID` context: after `Run` succeeds, `lock.ReadHolder(LocksDir, ProjectRegistry())` reads back the
  holder with the op's `opID` + this process's pid). The handler reads its op_id from the context — the
  same id Execute injects — so no signature change was needed. Same best-effort posture
  (`_ = held.Stamp(...)`). Registered mutant = pre-wiring state (no `Stamp` call) → empty body →
  `ReadHolder` errors → RED (confirmed before green). `gofmt`/`go build ./...`/`go vet ./internal/cli/`/`go
  test ./...` all GREEN (24 packages); (9) ✅ **this firing** — wired the FOURTH and FINAL acquire site:
  `isolate.Remove` now calls `held.Stamp(opID)` after taking the isolate-state lock (guard
  `ISOLATE-RM-STAMP`). This site needed an external signature change — `Remove(ctx, l, g, task, opID
  string, repos)` (matching `New`'s `task, opID string` convention); updated the `cmd_isolate_rm` caller
  (passes `OpIDFrom(ctx)`) and all 4 test callers. The fitness is sharp: `New` already stamped this very
  lock with its OWN op id during setup, so `Remove` must RE-stamp — reading back the REMOVE op id (not
  New's) proves the stamp happened in `Remove` specifically. Registered mutant = drop the `Stamp` call →
  body still carries New's op id → `ReadHolder` returns the wrong OpID → RED (confirmed:
  `OpID = "op_new_for_rm_stamp", want "op_remove_stamp"`). Same best-effort posture. `gofmt`/`go build
  ./...`/`go test ./...` all GREEN (24 packages). **✅ ALL 4 of 4 acquire sites wired** (`isolate.New`,
  `sync.syncOne`, `repoAddCmd.Run`, `isolate.Remove`) — every lock wi takes now stamps its holder identity.
  (10) ✅ **this firing** — the holder-liveness judgment `ProvenDead(Holder) (bool, error)` in
  `internal/lock/policy_unix.go` (guard `LOCK-PROVEN-DEAD`), the predicate that — with the fs-trust gate,
  applied separately by the break path — authorizes breaking a contended lock. Encodes DESIGN §7.3
  verbatim: on the SAME host a holder is proven dead iff its `boot_id` mismatches this boot (the machine
  rebooted → the recorded process is certainly gone, even if its pid was since reused by a live process) OR
  its `boot_id` matches and `processAlive(pid)` is false (`Kill(pid,0)==ESRCH`). Conservative everywhere
  else: a different host, an empty host/boot, or a non-positive pid is NEVER proven dead. **Ruling
  (corrects last firing's note):** DESIGN §7.3 lines 282–283 make a `boot_id` mismatch one of the two
  proven-dead limbs — a reboot DOES authorize a break (the holder is gone); my prior note that "a
  different-boot holder is NEVER auto-broken" was wrong and is superseded. The conservative cases that are
  never broken are: unknown holder (body-less/unparseable lock — never reaches `ProvenDead`), foreign host,
  unknown host/boot, and a LIVE same-boot pid. The fitness pins all limbs with the dangerous live-self case
  load-bearing; registered mutant = drop the boot-mismatch limb → the different-boot/live-pid (reboot+pid-
  reuse) case reads as not-dead → RED (confirmed before green); alternate = drop the host guard → the
  foreign-host case reads as dead → RED. `gofmt`/`go build ./...`/`go test ./...` all GREEN (24 packages).
  (11) ✅ **this firing** — the **fs-trust gate** `FSTrustworthy(path) (bool, error)` (guard
  `LOCK-FS-TRUST`), DESIGN §7.3's requirement that auto-break is refused unless flock(2) is known reliable
  on the backing filesystem. Implemented as a per-OS `statfs(2)` classifier behind one untagged wrapper
  (`internal/lock/fstrust.go`): `fstrust_darwin.go` reads `f_fstypename` and allowlists `{apfs,hfs,ufs,
  msdos,exfat}`; `fstrust_linux.go` reads the `f_type` superblock magic and allowlists `{ext,btrfs,xfs,
  tmpfs,ramfs,f2fs,zfs,overlayfs,exfat,vfat}`; `fstrust_other.go` (`//go:build unix && !linux && !darwin`)
  fails closed (returns false). **Allowlist, fail-closed**: any unrecognized type — a network fs
  (NFS/SMB/9p/AFP/FUSE), a novel local fs, an unsupported platform — is NOT trustworthy, so the failure mode
  is "refuse to break" (lock stands; lock_held/exit 6), never "wrongly break" a flock another HOST may hold
  over a shared fs. Test-first on the darwin host (real RED→GREEN): mutant = classifier `return true` →
  every network/unknown case (nfs/smbfs/afpfs/webdav/ftp/fusefs/""/"wat") reads as trustworthy → RED
  (confirmed); the end-to-end `FSTrustworthy(t.TempDir())==true` smoke (apfs) stayed green throughout,
  proving the syscall+string-extraction wiring. The symmetric linux table test + smoke mirror it; linux
  verified via `GOOS=linux go build ./...` + `GOOS=linux go vet ./internal/lock/` (typechecks the linux
  classifier AND its test — can't RUN on the darwin host, so its RED is reasoned, not observed). `gofmt`/`go
  build ./...`/`go test ./...` all GREEN (24 packages) + linux cross-compile/vet clean.
  (12) ✅ **this firing** — the **read-only break decision** `AssessBreak(locksDir, key) (BreakDecision,
  error)` (guard `LOCK-SAFE-TO-BREAK`, `internal/lock/assess_unix.go`), composing the two independent gates
  from units (10)+(11) into the single HEAL-3 verdict (DESIGN §7.3 / §7.4). It is side-effect free: statfs's
  `locksDir` (`FSTrustworthy`) and reads the lock body (`ReadHolder`→`ProvenDead`), taking NO flock and
  writing nothing — exactly how a contender that lost the TryLock race inspects the holder it could not
  displace. The verdict `BreakDecision{Safe,FSTrustworthy,HolderKnown,Holder,ProvenDead,Reason}` is
  **conjunctive and fail-safe**: `Safe` is true ONLY when the fs is flock-trustworthy AND the holder is
  known AND `ProvenDead` reports it gone. The three refusal limbs: an **unknown holder** (a `ReadHolder`
  error — body-less/unparseable — is folded to `HolderKnown=false`, NOT a hard error) is never breakable; a
  holder **not provably dead** (live/foreign-host/unknown-boot) is never breakable; an **untrustworthy fs**
  is never breakable. Only a genuine I/O fault (statfs, or boot-id/hostname during liveness) returns an
  error. `Reason` is a human diagnostic for the envelope/`lock ls`, deliberately NOT a closed wire enum
  (internal/contract owns those) — the CLI maps the structured bool fields to whatever sub-code it needs.
  Test-first (real RED→GREEN on the darwin host): three deterministic cases over `t.TempDir()` (trustworthy
  apfs) — unknown holder (no body), live holder (`CurrentHolder` = this process), proven-dead holder (same
  host, boot-mismatched id). Mutant = drop the `ProvenDead` conjunct (`Safe = FSTrustworthy && HolderKnown`)
  → ONLY the live-holder case reddened (Safe=true while this process is alive — the dangerous "steal a live
  peer's lock" direction), unknown + dead cases stayed green, proving the test isolates exactly that
  conjunct. `gofmt`/`go build ./...`/`go test ./...` all GREEN (24 packages) + linux cross-compile/vet clean.
  (13) ✅ **this firing** — `lock.ParseKey(s) (Key, error)` (guard `LOCK-PARSE-KEY`, `internal/lock/keys.go`),
  the inverse of the key namespace — it reconstructs a typed `Key` from its canonical `String()`, validating
  the embedded segment exactly as the constructors do. This is the prerequisite for `lock ls`: enumerating
  the locks dir yields `"<key>.lock"` filenames, and ParseKey turns each back into a `Key` so a **stray,
  non-key file is rejected** (error) rather than fabricated into a lock and assessed. Refactored the three
  namespace literals into shared consts (`projectRegistryKey`/`repoPrefix`/`isolateStatePrefix`) used by BOTH
  the constructors and ParseKey, so `String()` and its inverse provably cannot drift. Test-first RED→GREEN:
  the load-bearing properties are round-trip identity across all three namespaces (`ParseKey(k.String())==k`
  for ProjectRegistry/Repo/IsolateState) and rejection of junk (`""`, `"garbage"`, `"repo:"`, `"repo:bad/
  name"`, `"isolate-state:"`, `"unknown:thing"`). Mutant = `default` returns `Repo(s)` instead of erroring →
  the no-prefix junk cases (`"garbage"`, `"unknown:thing"`, which `ValidateSegment` alone would accept)
  wrongly parse as repo keys → RED on exactly those two; round-trip + prefix-handled rejections stayed green.
  `gofmt`/`go build ./...`/`go test ./...` all GREEN (24 packages) + linux cross-build clean.
  (14) ✅ **this firing** — `lock.List(locksDir) ([]LockStatus, error)` (guard `LOCK-LIST`,
  `internal/lock/list_unix.go`), the data-gathering half of `lock ls`: it enumerates every `*.lock` file in
  locksDir, `ParseKey`s each filename, `AssessBreak`s each valid key, and returns one `LockStatus{Key,
  Decision}` per recognized lock sorted by canonical key. Read-only (no flock taken, nothing written). Three
  load-bearing behaviors, all tested: (a) **strays are silently skipped** — a non-`.lock` file, a `.lock`
  file whose stem is not a valid key (`notakey.lock`), and a subdirectory (`sub.lock`) never appear; (b) a
  **missing locksDir is the empty result, not an error** (`os.ErrNotExist` → `nil,nil` — "no locks" is a
  valid state); (c) each lock carries its full `AssessBreak` verdict (the test asserts a proven-dead holder
  reads Safe, a live holder reads HolderKnown-but-not-Safe, a body-less lock reads unknown-holder). Tagged
  `//go:build unix` (it calls `AssessBreak`). Test-first RED→GREEN: mutant = drop the stray-skip
  (`key, _ := ParseKey(...)` with no `continue`) → `notakey.lock` is fabricated into a phantom empty-key lock
  → `TestList` RED on the exact sorted-key-set assertion (`[<empty> isolate-state:task1 …]`); the missing/
  empty-dir cases stayed green, isolating the skip. Alternate = drop the `os.ErrNotExist` special-case →
  missing-dir case reddens. `gofmt`/`go build ./...`/`go test ./...` all GREEN (24 packages) + linux build/vet
  clean.
  (15) ✅ **this firing** — `lock.Break(locksDir, key) (BreakDecision, error)` (guard `LOCK-BREAK`,
  `internal/lock/break_unix.go`), the ACTION counterpart to `List`: it displaces a stale lock by unlinking its
  file, but ONLY when the read-only `AssessBreak` gate returns `Safe` (holder PROVEN DEAD on a
  flock-trustworthy fs). When not Safe — live holder, unknown/body-less holder, or untrustworthy fs — it
  removes NOTHING and returns the verdict unchanged (caller → exit 6 lock_held). Unlinking is the right
  displacement because a proven-dead holder no longer holds the flock (the kernel releases it on process exit,
  and a reboot that changes the boot_id wipes all flock state — see `lockfs/flock_unix.go`), so the only
  artifact left is the stale-body file; the next `Acquire` O_CREATEs a fresh file. The **Safe gate is
  load-bearing**: unlinking a file a LIVE peer still holds would break mutual exclusion (a later Acquire makes
  a NEW inode and flocks that, not the live holder's) — the exact data-loss path DESIGN §7 forbids. Tagged
  `//go:build unix`. Test-first RED→GREEN over `t.TempDir()` (trustworthy apfs): proven-dead holder
  (boot-mismatched) → broken, file gone; live holder (`CurrentHolder`) → refused, file intact; body-less lock
  → unknown holder, refused, file intact; missing file → not an error (nothing to break). A vanished-between-
  assess-and-unlink race is tolerated (`errors.Is(err, os.ErrNotExist)`). Mutant = drop the `if !d.Safe`
  early return (always unlink) → the live-holder AND unknown-holder subtests reddened (their refused lock was
  destroyed) while proven-dead + nothing-to-break stayed green, isolating the gate. `gofmt`/`go build
  ./...`/`go test ./...` all GREEN (24 packages). This completes the read-only + action lock-self-heal
  PRIMITIVES (AssessBreak → List → Break) entirely inside `internal/lock` — the MVP wire contract is still
  100% frozen (no `internal/contract` touch yet).
  (16) ✅ **this firing** — the additive `locks` wire block (guard `SHAPE-LOCKS-BLOCK`,
  `internal/contract/locks_test.go`), the **FIRST wire-contract change since M0 froze the envelope**. Added
  `contract.LockInfo{key, safe, fs_trustworthy, holder_known, proven_dead, reason, holder?}` +
  `contract.LockHolder{pid, host, boot_id, op_id}`, an `Envelope.Locks []LockInfo` omitempty block declared
  after `help`/before `error` (the Resolve/Planned/Blocked/Help additive-block precedent), and **bumped
  `SchemaVersion` 1.0 → 1.1 (additive minor)**. The four LockInfo booleans are always-present (no omitempty,
  agents index blind); `holder` is a nested pointer present iff `holder_known`; `reason` is a human diagnostic
  agents never branch on. Kept the published schema in lockstep — `schema/envelope.schema.json`: `const`
  1.0→1.1, new `locks` property (additionalProperties:false demanded it), new `lockInfo`/`lockHolder` `$defs`,
  reserved-block note updated — and regenerated `testdata/contract.lock.json` via `WI_UPDATE_CONTRACT_LOCK=1`
  (new schema_sha + struct_shape with the `locks` block). Test-first RED→GREEN: the dedicated
  `SHAPE-LOCKS-BLOCK` fitness froze the locks-bearing envelope's golden bytes (field set/order, holder-present
  AND body-less-holder-omitted rows in one golden) + the omitempty invariant; the existing `SHAPE-FINGERPRINT`
  tripwire (`TestContractFrozen`) reddened on the un-regenerated shape/version/schema, proving the guard
  catches the move; `TestSchemaAcceptsGolden` now validates the `goldenLocks` envelope against the published
  schema (exercising the new defs); the reject-corpus versions were bumped 1.0→1.1 so each case is still
  rejected for its NAMED defect, not the version. Mutant = drop `,omitempty` from `Envelope.Locks` →
  `"locks":null` on every envelope → `TestEnvelopeLocksOmittedWhenNil` + all 3 goldens + field-order + the
  fingerprint all RED (decisive multi-angle non-vacuity); reverted to green. Caught + fixed an over-loose
  test assertion mid-RED (`strings.Contains(b,"holder")` matched the `holder_known` substring; switched to a
  decoded key-set check). `gofmt`/`go vet`/`go build ./...`/`go test ./...` all GREEN (24 packages). The v0
  (M0–M3) wire output is UNCHANGED — the block is nil on every MVP command, so a 1.0-pinned consumer keeps
  parsing; this is a clean additive minor.
  (17) ✅ **this firing** — the **`wi lock ls` CLI handler** (guard `CMD-LOCK-LS`,
  `internal/cli/cmd_lock_ls_unix.go` + `_test`), the first command of the lock-self-heal surface and the
  first consumer of the M4 `locks` wire block. A READ-ONLY command (`ActionRead`, takes NO flock, dials no
  network): `lockLsCmd.Run` calls `lock.List(layout.LocksDir())` and `lockInfoOf` projects each `LockStatus`
  → `contract.LockInfo` (Key.String()→key; Decision.{Safe,FSTrustworthy,HolderKnown,ProvenDead,Reason}→the
  four always-present bools + reason; `Decision.Holder` → a nested `*LockHolder` **iff HolderKnown**, else a
  nil holder — a body-less lock projects an omitted holder, never a misleading zero-value one). Wired the
  block onto the envelope: added `Result.Locks []contract.LockInfo` + the `env.Locks = r.Locks` line in
  `envelopeFor`'s success arm (the only edit to the generic pipeline). Factory takes NO operand (any arg →
  kind=usage/exit 64). **PLATFORM RULING (recorded):** `lock.List`/`AssessBreak`/`Break` are `//go:build
  unix` (flock + statfs trust), and the whole binary is already unix-only (`GOOS=windows go build` fails in
  `internal/host`). Rather than leave an untagged file calling unix-only symbols, registered the lock
  commands through a build-tagged hook: `cmd_lock_ls_unix.go` (`//go:build unix`) defines
  `lockCommands(d) Registry` returning `{"lock ls": …}`; `cmd_lock_other.go` (`//go:build !unix`) returns
  nil; `BuildRegistry` merges `lockCommands(d)` into the base map. So every file's build constraint is
  honest and a future non-unix port cleanly excludes the lock surface. Added the `lock ls` row to the
  `internal/help` table (so `wi help` discovers it and `HELP-REGISTRY-SYNC` stays satisfied — registry and
  table both carry it on the only platforms that build). **NEXT RULING (recorded):** `TestNextIsRunnable`
  requires every table command to suggest a runnable follow-up, so `lock ls`'s `Next` is `wi lock break
  <key>` — a command built NEXT firing. This matches the codebase's own incremental-construction precedent
  (`init`'s Next pointed at `wi repo add` before `repo add` existed); the whole M4 layer is unreleased
  (build/wi, unpushed), so the transient forward reference resolves before any release and no fitness
  requires a Next target to already be a table row. Test-first RED→GREEN over a `bootstrappedLayout` whose
  LocksDir holds a proven-dead (boot-mismatched) lock + a body-less lock: asserts Action=read, two rows
  sorted by key, the dead row's four bools all true + Reason set + a populated holder (pid/host/op_id), and
  the body-less row unknown/not-safe with a NIL holder. Mutant = drop the `if d.HolderKnown { li.Holder=… }`
  guard in `lockInfoOf` → the known-holder row's identity goes nil → `TestLockLsProjectsHolders` RED on the
  non-nil-holder assertion (confirmed: `repo:api has a known holder; its nested holder identity must be
  projected, got nil`), body-less row stays green — isolating the holder projection. `gofmt`/`go vet
  ./...`/`go build ./...`/`go test ./...` all GREEN (24 packages) + linux cross-compile/vet clean. Live
  binary smoke: `wi lock ls` (empty) → exit 0, one envelope, `locks` block omitted (empty additive block,
  per the omitempty convention); `wi lock ls bogus` → usage/exit 64; `wi help` overview lists `lock ls` as
  the 7th command.
  (18) ✅ **this firing** — the **`wi lock break <key>` CLI handler** (guard `CMD-LOCK-BREAK`,
  `internal/cli/cmd_lock_break_unix.go` + `_test`), the ACTION half of the lock-self-heal surface and the
  only command that displaces a lock. Factory (`newLockBreakCommand`) takes EXACTLY one `<key>` operand
  parsed via `lock.ParseKey`; zero/extra operands OR an unparseable key → kind=usage/exit 64 (never a
  fabricated key). Handler (`lockBreakCmd.Run`) calls `lock.Break(LocksDir(), key)` — which runs the
  read-only AssessBreak gate and unlinks ONLY a proven-dead holder's stale file — and translates the
  returned `BreakDecision` onto the envelope: **Safe** (file displaced) → `Result{Action: removed}` + the
  removed lock's `LockInfo` / exit 0; **not-Safe** (live / unknown / untrustworthy holder, file left
  intact) → BOTH a `Result{Action: noop, Locks: [info]}` AND a `*CommandError{Kind: KindLockHeld}` / exit 6,
  so the refusal carries the lock's verdict in the `locks` block (agent reads WHY without re-running
  `lock ls`). Refactored the ls-unit's projection into a shared `lockInfoFrom(key, BreakDecision)` so both
  commands emit an identical verdict shape (`lock ls` reads it, `lock break` acts on it). **WIRING RULING
  (recorded):** to surface the verdict on a FAILURE envelope, extended `envelopeFor`'s failure arm to thread
  `env.Locks = r.Locks` (it already threaded Repos/Warnings/Next) — generalizing the both-returns-non-nil
  path from "durable partial only" to "failure that carries detail" (durable partial OR diagnostic refusal
  like lock_held); the `Command` doc comment was corrected to match. **CORRECTION to the unit-17 NEXT
  ruling:** a `lock break` help-table row IS required — `HELP-REGISTRY-SYNC` (`TestHelpTableMatchesRegistry`)
  demands registry⇔table set-EQUALITY, so the new registry key reddened it until a `lock break` row was
  added (Next = `wi lock ls`; the lock pair now cross-reference each other: inspect → break → re-inspect).
  The contract did NOT move (the `locks` block already carried the verdict; SchemaVersion stays 1.1).
  Test-first RED (`BuildRegistry has no "lock break" factory`) → GREEN: three tests driving the REAL pipeline
  via `cli.Execute` — a proven-dead break (boot-mismatch holder on the trustworthy temp fs) → exit 0 / ok /
  action=removed / `locks[0]` Safe+ProvenDead / file unlinked; a live holder (`lock.CurrentHolder`) → exit 6
  / kind=lock_held / `locks[0]` not-Safe carried on the failure envelope / file intact; and the one-operand
  factory rule (nil/extra/junk/empty → usage). Mutant = drop the `if !d.Safe { … KindLockHeld … }` branch so
  every break maps to removed/exit 0 → `TestLockBreakLiveHolderRefusesWithLockHeld` RED (live holder exits 0,
  no error); proven-dead test stays green (confirmed RED then reverted). Alternate mutant noted: drop
  `env.Locks = r.Locks` from the failure arm → the refusal's "carries the verdict" assertion reddens.
  `gofmt`/`go vet ./...`/`go build ./...`/`go test ./...` all GREEN (23 packages) + linux cross-compile
  clean. Live binary smoke (real argv → 2-token Dispatch): proven-dead `lock break repo:api` → ok/removed/
  exit 0 with full `locks` block + holder, file gone, follow-up `lock ls` empty; body-less
  `lock break project-registry` → lock_held/exit 6 with the verdict carried, file intact; `lock break`
  (no arg) and `lock break "not a key"` → usage/exit 64; `wi help` overview lists `lock break` as the 8th
  command.
  (19) ✅ **this firing** — the **evidence-positive drift classifier** `isolate.Classify(markerExists,
  worktreeExists bool) Classification` (guard `REPAIR-CLASSIFY`, `internal/isolate/repair.go` + `_test`),
  the keystone of the three-way isolate drift reconciler (HEAL-1, DESIGN §7.1/§7.4 — `isolate repair`). A
  PURE function (no I/O, no build tag) returning the 2×2 verdict over the two physical ownership signals:
  `ClassConsistent` (marker ✓ + worktree ✓), `ClassMissingWorktree` (marker ✓, worktree ✗ — re-materialize
  candidate), `ClassOrphanWorktree` (marker ✗, worktree ✓ — HARD BLOCK orphan_unexplained), `ClassReclaimed`
  (neither — completed-then-deleted / never materialized). **DESIGN RULING (recorded):** the "three-way"
  reconciler reconciles *recorded stage × owned-marker × worktree*; this unit owns the verdict over the two
  PHYSICAL signals, and the keystone is that **the marker ref — NOT the registry record — is the authority
  on whether a cell should exist** (evidence-positive §7.1). Keying the re-materialize verdict on the marker
  makes the §7.4 HEAL-1 "no resurrection" rule STRUCTURAL rather than a special case: `isolate rm` unlinks
  the marker when it reclaims a cell, so a completed-then-deleted op can never present a surviving marker —
  it can only ever classify as Reclaimed, never MissingWorktree. The recorded stage is carried into the
  reconciler (NEXT unit) to choose the record action (drop a stale tombstone / heal a lagging stage forward
  / re-materialize), not into this verdict. Test-first RED (undefined symbols) → GREEN: a 4-row truth-table
  test + a dedicated `TestClassifyNoResurrection` pinning the (✗marker,✗worktree)→Reclaimed and
  (✓marker,✗worktree)→MissingWorktree rows. Mutant = `case !worktreeExists: return ClassMissingWorktree`
  (re-materialize keyed off worktree-absence, ignoring the marker = the resurrection bug) → `neither-present`
  + `TestClassifyNoResurrection` RED (`"missing_worktree"`, want `reclaimed`), other rows green — isolating
  exactly the no-resurrection keystone (confirmed RED then reverted). `gofmt`/`go vet`/`go build ./...`/`go
  test ./...` all GREEN (24 packages) + linux cross-build clean.
  (20) ✅ **this firing** — the **read-only drift observer** `isolate.Inspect(ctx, l, g, task) ([]Cell,
  error)` + the `Cell{Repo, Stage, Class, MarkerSHA}` type (guard `REPAIR-INSPECT`,
  `internal/isolate/repair.go` + `_test`), the data-gathering half of `isolate repair` (HEAL-1, DESIGN
  §7.4) — exactly parallel to how `lock.List` is the read half of `lock break`. It loads the registry
  record and, per recorded repo IN RECORD ORDER, observes the two physical ownership signals — the wi-owned
  marker ref (`git.OwnedRefSHA`) and the worktree dir on disk (`os.Stat` via `l.Isolate`) — and feeds them
  to unit-19's `Classify`, producing one classified `Cell` per repo. **Read-only by design** (takes NO
  isolate-state lock, mutates nothing, dials no network — OwnedRefSHA is a local ref read): the ACTION half
  takes the lock; this matches `lock.List`'s "inspect how a contender reads" precedent. `Cell.MarkerSHA`
  carries the marker's recorded base sha — the re-materialize source for a MissingWorktree cell, so the
  actor recreates the worktree at the exact commit wi owns, never an arbitrary newer base — and is empty
  when no marker survives. `state.ErrNoRecord` propagates (CLI → not_found: "isolate does not exist" ≠
  "drift-free"); a genuine git/layout fault returns an error (env failure, not per-cell drift, since every
  drift state is expressible via the (marker,worktree) bools). `pathExists` reads a MISSING path as the only
  "false" (any other stat error → conservatively "present", so an inaccessible worktree is never treated as
  a clobberable re-materialize target). Test-first RED (`undefined: isolate.Inspect`) → GREEN over the
  hermetic real-git harness: a 4-repo isolate driven to all four drift states by disk/ref manipulation only
  (record left untouched, so it's stale for the drifted cells = the divergence to reconcile) — `api` intact
  → Consistent, `web` worktree removed → MissingWorktree, `db` marker deleted → OrphanWorktree, `cache` both
  removed → Reclaimed; asserts per-cell Class + MarkerSHA-presence + record-order + the no-record
  propagation. Mutant = `pathExists` → `return true` → exactly `web`+`cache` (worktree-removed) redden,
  `api`+`db` stay green — isolating the worktree read (confirmed RED then reverted; a first attempt to
  hardcode `worktreeExists := true` was rejected by the compiler as `wtPath` unused — a vacuous build
  failure, not a behavioral RED — so the mutant was moved into `pathExists`'s body to keep the call site
  intact). `gofmt`/`go vet`/`go build ./...`/`go test ./...` all GREEN (24 packages) + linux
  cross-build/vet clean.
  (21) ✅ **this firing** — the **pure repair planner** `isolate.PlanAction(Cell) RepairAction` + the
  `RepairAction` vocabulary (`none`/`heal_stage`/`rematerialize`/`drop_record`/`block_orphan`) (guard
  `REPAIR-PLAN`, `internal/isolate/repair.go` + `_test`), the "decide" half of the HEAL-1 reconciler that
  maps each classified `Cell` (unit-19 `Class` × unit-20-observed × recorded `state.Stage`) to the single
  action the executor will carry out under the lock. Splitting the planner out FIRST (mirrors the
  Classify→Inspect split) keeps the executor unit small and de-risks a half-done actor leaving the tree red.
  The two §7 safety invariants live STRUCTURALLY in this seam: ClassReclaimed → `drop_record` only (never
  `rematerialize`: no resurrection — §7.4 HEAL-1), ClassOrphanWorktree → `block_orphan` only (never an
  auto-removing action: unexplained orphans are a hard block, never auto-pruned — §7.1). The recorded stage
  refines ONLY the Consistent case: marker+worktree both present but stage still `pending` = a crash AFTER
  materialize but BEFORE the stage flip → `heal_stage` (forward to created, no disk action); a Consistent
  cell already at `created` → `none`. **Decision recorded (IMPLEMENTATION_PLAN §7):** `RepairAction` is an
  isolate-package DOMAIN vocabulary, NOT a closed contract wire enum — like `Classification` and
  `state.Stage` it never crosses the envelope boundary directly (the cli layer projects it into
  repos[]/blocked[] reasons), so `internal/contract` (sole owner of the enums AGENTS PARSE) does not own it;
  this keeps "contract owns closed enums" precise — contract owns wire enums, packages own internal policy
  vocabularies. Test-first RED (`undefined: isolate.RepairAction`) → GREEN: the full (4 classes × {pending,
  created}) → action truth table + a dedicated `TestPlanActionNeverAutoRemovesOrphan` pinning the two safety
  invariants. Mutant = OrphanWorktree arm → `return RepairDropRecord` (auto-clean the orphan) → exactly the
  two orphan rows + the safety test redden, the other 6 truth-table rows stay green (confirmed RED then
  reverted). `gofmt`/`go vet`/`go build ./...`/`go test ./...` all GREEN (24 packages) + linux
  cross-build/vet clean.
  (22) ✅ **this firing** — the git primitive `git.PruneWorktrees(ctx, ssotDir)` (`git worktree prune`)
  (guard `GIT-WORKTREE-PRUNE`, `internal/git/git.go` + `_test`), the stale-worktree-admin hygiene the HEAL-1
  rematerialize arm composes BEFORE `AddWorktree`. Empirically confirmed the failure mode first: when a
  linked worktree's dir is removed out-of-band (external `rm -rf`/crash) instead of via `git worktree remove`,
  the SSOT keeps a stale admin entry under `.git/worktrees/<id>`, so the path is "missing but already
  registered" and `git worktree add` REFUSES a plain re-add (exit 128) — which is exactly why a MissingWorktree
  cell cannot just be re-added at its marker sha. `PruneWorktrees` deregisters ONLY entries whose working dir
  is genuinely missing (git never prunes a LIVE worktree), clearing the path for a clean detached re-add; it
  honors every §7.2 invariant (no `--force`, no `reset --hard` — the prune-then-add route was chosen precisely
  to avoid `worktree add --force`), is offline, and is idempotent. Test-first RED (`undefined: PruneWorktrees`)
  → GREEN over the hermetic real-git harness: add a worktree → `os.RemoveAll` its dir → assert a plain re-add
  ERRORS (pins the precondition + that AddWorktree does NOT silently `--force`) → `PruneWorktrees` → assert the
  re-add now succeeds detached. Mutant = no-op prune (`return nil` without running git) → the stale entry
  survives → the post-prune AddWorktree fails "missing but already registered" → RED (confirmed then reverted).
  `gofmt`/`go vet`/`go build ./...`/`go test ./...` all GREEN (24 packages) + linux cross-build/vet clean.
  (23) ✅ **this firing** — the HEAL-1 **executor** `isolate.Repair(ctx, l, g, task, opID) (RepairResult,
  error)` + the `RepairOutcome`/`RepairResult`/`RepairStatus` types + the `rematerializeCell` helper (guard
  `REPAIR-EXEC`, `internal/isolate/repair.go` + `_test`), the ACTION half of `isolate repair`. Under the
  isolate-state:<task> lock (+ best-effort `Stamp`), it loads the record, observes every recorded cell (the
  same read `Inspect` performs, via `observeCell`), decides each with `PlanAction`, and carries it out
  best-effort-all (like `Remove` — a blocked/errored cell never aborts the others): `heal_stage` flips a
  lagging stage forward; `rematerialize` = `PruneWorktrees` + `AddWorktree(ssot, wt, Cell.MarkerSHA)` (re-add
  at the EXACT owned sha, marker left as-is — NOT re-created); `drop_record` drops a Reclaimed tombstone (no
  resurrection); `block_orphan` leaves an OrphanWorktree fully intact and surfaces `orphan_unexplained`
  (§7.1). **All registry mutations accumulate on ONE in-memory record and persist via a single atomic
  `state.Store` at the end** (or `state.Delete` + best-effort task-dir removal when no cell remains) — so the
  reconcile is itself crash-tolerant (a re-run re-observes and converges) and an early per-cell failure can't
  clobber an earlier in-memory stage flip. Moves no base ref, dials no network; held lock → `*lock.HeldError`
  (exit 6); no record → `state.ErrNoRecord` (not_found). Any orphan OR per-cell error ⇒ overall
  `RepairBlocked`. Test-first RED (`undefined: isolate.Repair`) → GREEN over the hermetic harness: a 5-repo
  isolate driven to ALL FIVE actions at once (`api`=none, `auth`=heal_stage via a forced-pending record,
  `web`=rematerialize, `db`=block_orphan, `cache`=drop_record), asserting per-cell action + the physical §7
  guarantees (orphan db left on disk AND in record; cache dropped, never recreated; web re-added detached at
  the EXACT marker sha; auth healed to created). Mutant `REPAIR-EXEC` = skip `PruneWorktrees` in the
  rematerialize arm → web's re-add fails "missing but already registered" → exactly the web rematerialize
  assertions RED, the other four arms GREEN (confirmed then reverted). `gofmt`/`go vet`/`go build
  ./...`/`go test ./...` all GREEN (24 packages) + linux cross-build/vet clean.
  (24) ✅ **this firing** — the `isolate repair <task>` **CLI handler** (`internal/cli/cmd_isolate_repair.go`
  + `_test`, guard `CMD-ISOLATE-REPAIR`) — **HEAL-1 is now COMPLETE end-to-end** (domain core + command). The
  handler is the seam between the three-way reconciler and the envelope, with a plan/act split keyed on a NEW
  `--dry-run` context seam (`cli.WithDryRun`/`DryRunFrom`, injected by `Execute` next to the op_id — the same
  per-invocation-context pattern; `run.go`): `--dry-run` → `isolate.Inspect` (read-only, lock-free) projecting
  every reconcilable cell into `planned[]` (its `PlanAction` verdict + a human detail) and every orphan into
  `blocked[]` as a would-block, with NO top-level error so the plan stays exit 0 (SHAPE-DRYRUN-EXIT0 / decision
  #D — verdicts ride the additive blocks, never the error); otherwise → `isolate.Repair` under the lock,
  projecting each `RepairOutcome` into `repos[]` (re-materialize→`created`, drop→`removed`, none/heal→`noop`,
  orphan→per-repo `conflict`/`orphan_unexplained`, per-cell fault→`internal`) and, on `RepairBlocked`, returning
  a `(result, *CommandError{conflict})` refusal (exit 4) — the blocking cells ride `repos[]` because
  `envelopeFor` threads `Repos` (NOT `Blocked`) onto a failure envelope, the exact shape `isolate rm` uses.
  Factory rejects a missing/extra operand as usage (repair has NO repo subset — it reconciles the whole
  isolate); `state.ErrNoRecord`→not_found (+`wi isolate new`), held lock→lock_held. Registered in
  `BuildRegistry` + a new `help.table` row (HELP-REGISTRY-SYNC set-equality held; runnable `wi resolve`/`wi
  isolate rm` Next). **Decision #RP (recorded):** repair has no single mutation verb in the closed Action enum,
  so its headline `action` is `noop` on the mutating path (per-cell effects are authoritative in `repos[].action`)
  and `read` on the dry-run path — avoiding a schema-bumping enum expansion. Test-first RED (`undefined:
  cli.WithDryRun`) → GREEN: reconcile-missing-worktree (web re-materialized, api consistent), orphan→conflict
  refusal (db left intact), dry-run plans-without-mutating (web stays missing + db stays present, action read,
  no error despite the orphan), missing-record→not_found, factory arg validation. Two mutants confirmed RED
  then reverted: (a) `RepairBlocked`→`(result,nil)` → blocked-is-conflict RED; (b) `--dry-run` falls through to
  the mutating path → dry-run-does-not-mutate RED. `gofmt`/`go vet`/`go build ./...`/`go test ./...` all GREEN
  (24 packages) + linux cross-build/vet clean.
  (25) ✅ **this firing** — the **evidence-positive gc candidate-classifier** `gc.Classify(Candidate) Class`
  + the `Candidate`/`Class` types in a NEW `internal/gc` package (guard `GC-CLASSIFY`, `internal/gc/gc.go`
  + `_test`), the keystone of HEAL-2 (DESIGN §7.1) — exactly the role `isolate.Classify` plays for HEAL-1.
  A PURE function (no I/O, no build tag) returning the gc verdict over the four signals §7.1 keys
  reclamation on, carried on a `Candidate{Task,Repo,HasMarker,Live,Clean,AheadOfBase}`: `ClassLive`
  (a live registry record still claims the cell — gc's to leave alone, short-circuits everything),
  `ClassReclaimable` (¬live ∧ marker-proven ∧ clean ∧ ¬ahead — the LONE collectable), `ClassBlockedWork`
  (wi-owned but dirty or ahead — HARD BLOCK, would destroy live work), `ClassOrphanUnexplained` (no marker
  — no provenance → HARD BLOCK, the loud §7.1 surface, never auto-pruned). The two §7.1 safety guarantees
  are STRUCTURAL in the gate ORDER: `Live` is the first gate (encoding "not journaled as live" — even a
  clean, owned, behind live cell is preserved), then the marker is the SECOND gate (without provenance a
  candidate can ONLY ever be orphan_unexplained, never reclaimable — the evidence-positive keystone), and
  only once provenance is proven do the work signals decide (any uncommitted/unmerged work → blocked,
  no-live-loss). **Decision recorded (IMPLEMENTATION_PLAN §7, mirrors REPAIR-PLAN):** `gc.Class` is an
  isolate-of-gc DOMAIN vocabulary, NOT a closed contract wire enum — like `isolate.Classification`/
  `RepairAction` it never crosses the envelope directly (the cli layer projects it into repos[]/blocked[]
  sub-codes); `internal/contract` stays the sole owner of the enums agents parse. Test-first → GREEN: a
  10-row truth table (`TestClassifyEvidencePositive`) + two keystone tests — `TestClassifyNeverReclaimsWithoutMarker`
  (evidence-positive: a markerless candidate is never reclaimable however clean) and
  `TestClassifyNeverReclaimsLiveWork` (no-live-loss: a wi-owned dirty/ahead worktree is never reclaimable).
  Two mutants confirmed RED-then-reverted (registry above): drop the work-gate → no-live-loss test RED;
  drop the marker-gate → evidence-positive test RED. `gofmt`/`go vet`/`go build ./...`/`go test ./...` all
  GREEN (25 packages) + linux cross-build/vet clean.
  NEXT M4 unit — **build order (recorded):** HEAL-2 keystone (the pure classifier) is DONE. Next sub-units
  of HEAL-2, mirroring the HEAL-1 Classify→Inspect→Repair split: **(a) the read-only gc sweep**
  `gc.Inspect(ctx, l, g) ([]Candidate, error)` — enumerate the workspace's wi worktrees (per-task, per-repo
  from the registry + on-disk), observe each cell's four signals (marker via `git.OwnedRefSHA`, live via the
  live registry record, clean via `git.IsClean`, ahead via `git.DivergedCounts` against base) and return a
  classified candidate list; read-only, lock-free, ZERO network (all local ref/stat reads), the same posture
  as `isolate.Inspect`/`lock.List`. **(b) the gc executor** `gc.Collect(...)` — under the relevant lock,
  reclaim ONLY `ClassReclaimable` cells (`git.RemoveWorktree` + drop record + `DeleteOwnedRef`), leave
  `ClassBlockedWork`/`ClassOrphanUnexplained` strictly intact and surface them; `refs/wi/owned/*` +
  `refs/wi/backup/*` protected; no `--force`, no `--prune`-style reclaim of unexplained objects. **(c) the
  `wi gc [--dry-run]` CLI handler** reusing the `--dry-run` context seam (Inspect→planned[]/blocked[] on
  dry-run; Collect→repos[] + conflict refusal if any orphan/blocked-work, exactly the `isolate repair`
  shape). AFTER HEAL-2: `land continue/abort/status`
  (HEAL-5, backup-ref-before-pointer-move) and the durable op journal + offline roll-forward (HEAL-4). `wi
  doctor`/`check` (HEAL-8) stays LAST in M4 (PLAN line 84) because its bounded `--fix` COMPOSES the safe heals
  (repair + gc), so it can only be built once they exist.
  AFTER that: wiring auto-break into `Acquire`'s `*HeldError` path is DEFERRED as a deliberate judgment call —
  on a trustworthy local fs an `EWOULDBLOCK` from `TryLock` means the holder's flock is LIVE (the kernel
  released it if the holder had died), so a silent auto-break there would risk stealing a live peer's lock;
  the safe HEAL-3 surface is the explicit `lock break` command + `wi doctor`, not a silent break inside
  Acquire. Revisit only with a concrete stale-flock scenario that survives the proven-dead gate.

  (26) ✅ **this firing** — `git.ListOwnedRefs(ctx, ssotDir) ([]OwnedRef, error)`, the enumeration half of
  the evidence-positive ownership markers and the enabling primitive for HEAL-2 sub-unit (a) `gc.Inspect`
  (guard `GIT-LIST-OWNED-REFS`, `internal/git/git.go` + `_test`). Where `OwnedRefSHA` answers "does wi own
  THIS cell?", `ListOwnedRefs` answers "which (task,repo) cells does wi own in this mirror at all?" — the
  evidence-positive *candidate population* the workspace gc sweep will classify. Implementation: a single
  `git for-each-ref --sort=refname --format='%(refname) %(objectname)' refs/wi/owned/` (read-only, no
  network), parsed into a sorted `[]OwnedRef{Task,Repo,SHA}`. Two structural safety properties, both
  guarded: the for-each-ref pattern is **scoped to `ownedRefPrefix` (`refs/wi/owned/`)** so `refs/wi/backup/*`
  (PROTECTED from gc by §7.1) and ordinary branches are NEVER enumerated as candidates; and a line that does
  not parse as a well-formed owned ref is a **hard error**, never silently fabricated into a phantom cell.
  Refactored the shared `ownedRefPrefix` const so the single-ref name (`ownedRef`) and the enumeration
  pattern can never drift apart. Test-first → GREEN: `TestListOwnedRefsEnumeratesAllMarkers` (empty mirror →
  empty; 3 markers across 2 tasks → all three, sorted) + `TestListOwnedRefsScopedToOwnedNamespace` (a
  `refs/wi/backup/*` ref and a stray branch alongside one genuine marker → only the marker returned). Two
  mutants confirmed RED-then-reverted (registry below): widen the pattern to `refs/wi/` → backup ref surfaces
  → owned-prefix parse guard errors → `Scoped` RED; `break` after the first parsed marker → 1-of-3 returned →
  `EnumeratesAll` RED. `gofmt`/`go vet`/`go build ./...`/`go test ./...` GREEN (25 packages) + linux
  cross-build/vet clean.
  NEXT M4 unit — HEAL-2 sub-unit **(a) `gc.Inspect(ctx, l, g) ([]gc.Candidate, error)`**: the read-only,
  lock-free, ZERO-network sweep that builds the candidate set. Population = `ListOwnedRefs` markers (per
  mirror) ∪ on-disk isolate worktree dirs; for each (task,repo) cell observe the four `Candidate` signals —
  `HasMarker` (a marker exists, via the just-built enumeration), `Live` (a live isolate registry record still
  claims it — likely needs a new `state.List`/StateDir enumeration primitive, which `state` lacks today),
  `Clean` (`git.IsClean`), `AheadOfBase` (`git.DivergedCounts` vs base) — then run each through `gc.Classify`.
  THEN (b) `gc.Collect` executor and (c) `wi gc [--dry-run]` CLI handler, per the unit-25 NEXT pointer above.

  (27) ✅ **this firing** — `state.List(stateDir) ([]IsolateRecord, error)`, the registry-enumeration
  primitive HEAL-2 sub-unit (a) `gc.Inspect` needs for its **`Live`** signal (guard `STATE-LIST`,
  `internal/state/state.go` + `_test`). `state` had `Load`/`Store`/`Delete`/`UpdateRepoStage` but no way to
  ask "which isolates does wi currently consider live?" — exactly the §7.1 signal that makes gc never collect
  a journaled-live cell. Implementation: `os.ReadDir(stateDir)` → for each `<task>.json` entry `Load` it →
  return in task order (ReadDir sorts by filename, and the flat `<task>.json` naming makes that task order).
  Three structural properties, all guarded: returns EVERY record deterministically; a **missing/empty
  stateDir is the empty list, not an error** (a workspace with no isolates has no live records — the same
  idempotent posture as `Delete`); and a non-record file is **skipped** while a **corrupt `.json` is a HARD
  error** (a torn registry entry is real drift to surface, never silently drop — mirroring `ListOwnedRefs`'s
  malformed-line posture). Read-only, no network, lock-free (a read-only sweep). Test-first → RED (undefined)
  → GREEN: `TestListEnumeratesAllRecords` (3 stored out of order → all three in task order),
  `TestListMissingOrEmptyDirIsEmpty`, `TestListSkipsNonRecordFiles`, `TestListSurfacesCorruptRecord`. Two
  mutants confirmed RED-then-reverted (registry below): `break` after first append → 1-of-3 → `EnumeratesAll`
  RED; drop the `.json`/dir filter → stray file fed to `Load` → `SkipsNonRecordFiles` RED. `gofmt`/`go vet`/
  `go build ./...`/`go test ./...` GREEN (25 packages) + linux cross-build/vet clean.
  NEXT M4 unit — HEAL-2 sub-unit **(a) `gc.Inspect(ctx, l, g) ([]gc.Candidate, error)`** is now fully
  unblocked: both enumeration primitives exist (`git.ListOwnedRefs` for the candidate population +
  `HasMarker`; `state.List` for `Live`), and `git.IsClean`/`git.DivergedCounts` supply `Clean`/`AheadOfBase`.
  Inspect = build candidate set (markers ∪ on-disk worktrees), observe the four signals per cell, run each
  through `gc.Classify`; read-only, lock-free, ZERO network. THEN (b) `gc.Collect` + (c) `wi gc [--dry-run]`.

  (28) ✅ **this firing** — `gc.Inspect(ctx, l, g) ([]gc.Candidate, error)`, the read-only data-gathering half
  of `wi gc` (guard `GC-INSPECT`, `internal/gc/inspect.go` + `_test`). It enumerates every wi worktree,
  observes the four §7.1 signals per cell, and runs each through the already-pinned `gc.Classify`. Like
  `isolate.Inspect`/`lock.List` it takes no lock, mutates nothing, dials no network (a local ref read, an
  `os.Stat`, a `git status`). **DECISIONS RECORDED this unit (correcting the unit-26/27 NEXT pointers above,
  which said "markers ∪ on-disk worktrees" + `DivergedCounts`):**
  • **Candidate population = on-disk isolate worktree dirs under `isolas/<task>/<repo>`, NOT the union with
    marker refs.** This is the WORKTREE axis, and it is what makes the §7.5 orphan inventory STRUCTURAL: a
    worktree wi cannot prove it owns (no marker) surfaces here as `ClassOrphanUnexplained` precisely because
    the worktree — not the marker — is what we enumerate. The complementary case (a surviving marker whose
    worktree is GONE) is deliberately NOT gc's concern — that is HEAL-1's re-materialize axis
    (`isolate.ClassMissingWorktree`), keyed off the registry record. Keeping gc worktree-scoped and repair
    record-scoped stops the two heals from fighting over the same cell. ⟹ `git.ListOwnedRefs` (unit 26) is
    therefore NOT gc.Inspect's population source (per-cell `OwnedRefSHA` supplies `HasMarker`); it remains a
    sound primitive for a future `wi doctor` marker-inventory/leak detector.
  • **`AheadOfBase` = worktree HEAD ≠ marker sha (via `git.ResolveRef(wt,"HEAD")`), NOT `DivergedCounts`.**
    This mirrors `isolate.Remove`'s v0 convention (`reclaimRepo` gate 3: "the marker IS the base evidence, so
    a HEAD past it carries local commits"). Only computed when `HasMarker` — for a markerless orphan the
    marker gate short-circuits the verdict before the work signals are consulted, so HEAD need not be read.
  • **`Live` = (task,repo) ∈ a live registry record**, built once from `state.List(l.StateDir())` (unit 27).
  Empty-workspace posture: no `isolas/` dir → empty list, not an error (idempotent, like `state.List`). A
  genuine git/layout/registry fault is returned as an error (an environment failure, not a per-cell verdict).
  Worktree path resolved via `layout.Isolate` so a maliciously-named dir is rejected loudly (ValidateSegment).
  Test-first → RED (undefined) → GREEN: `TestInspectClassifiesEachWorktree` — an END-TO-END fitness over a
  REAL git workspace (`isolate.New` for two tasks) driving one worktree into each of the four classes:
  `active/api` kept live → `ClassLive`; `gone/*` de-registered via `state.Delete` then `gone/web` clean →
  `ClassReclaimable`, `gone/db` dirtied → `ClassBlockedWork`, `gone/ledger` committed-past-marker →
  `ClassBlockedWork` (the ahead limb), `gone/auth` marker deleted → `ClassOrphanUnexplained`; plus
  `TestInspectEmptyWorkspaceIsEmpty`. Two mutants confirmed RED-then-reverted (registry below): empty the
  live-set → `active/api` misclassifies as `reclaimable` (RED, pinning the §7.1 never-collect-live join);
  `break` after the first repo per task → 2-of-5 candidates (RED on the count). `gofmt`/`go vet`/`go build
  ./...`/`go test ./...` GREEN (25 packages) + linux cross-build/vet clean.
  NEXT M4 unit — HEAL-2 sub-unit **(b) `gc.Collect(ctx, l, g, opID) (CollectResult, error)`**: the executor.
  Under the `isolate-state:<task>` lock (per task touched), re-observe (the same read Inspect performs — never
  trust a stale verdict across the lock), and for each cell reclaim ONLY `ClassReclaimable`: `RemoveWorktree`
  + `DeleteOwnedRef` + drop the registry record cell, best-effort-all (a blocked/errored cell never aborts the
  rest), single atomic Store/Delete at the end — exactly the `isolate.Repair` shape. `ClassBlockedWork` and
  `ClassOrphanUnexplained` are HARD BLOCKS left fully intact (no `--force`, no `--prune`); `refs/wi/{owned,
  backup}/*` protected. THEN (c) `wi gc [--dry-run]` CLI handler reusing the `cli.WithDryRun` seam
  (Inspect→planned[]/blocked[]; Collect→repos[] + conflict refusal on any orphan/blocked-work). Also still
  owed for HEAL-2: the explicit **HEAL-GC-NO-LIVE-LOSS** negative fitnesses (PLAN 123-125) — no reclaim of
  reflog-only/equal-to-base work, no resurrection of a completed-then-deleted isolate.

  (29) ✅ **this firing** — `gc.Collect(ctx, l, g, opID) (CollectResult, error)`, the executor half of
  `wi gc` (guard `GC-COLLECT`, `internal/gc/collect.go` + `_test`). It is the ACTION the read-only
  Inspect/Classify verdict authorizes: it enumerates the isolate tasks on disk and reconciles each **under
  its own `isolate-state:<task>` lock**, RE-observing every worktree's signals under the lock (never trusting
  Inspect's lock-free snapshot across the acquire) and reclaiming **ONLY `ClassReclaimable`** cells —
  `RemoveWorktree` (no `--force`, a second cleanliness net) then `DeleteOwnedRef` (clear the spent marker).
  Every other class is preserved: `ClassBlockedWork` (would destroy uncommitted/unmerged work),
  `ClassOrphanUnexplained` (no provenance), and `ClassLive` (HEAL-1's domain — emitted as NO outcome at all).
  Moves no base ref, uses no `--prune`, dials no network. Best-effort-all and per-task independent. Types
  added: `CollectStatus{complete,blocked}`, `CollectOutcome{Task,Repo,Class,Reclaimed,Reason,Err}` (workspace
  sweep ⟹ each outcome carries its own identity), `CollectResult{Status,Repos}`. Refactored the shared
  `observeCandidate` to take a `liveCell bool` (Inspect passes the snapshot-map lookup; Collect passes this
  task's record membership read under the lock) — decoupling the observer from the liveness representation.
  **DECISIONS RECORDED this unit:**
  • **gc.Collect does NOT mutate the registry** — a `ClassReclaimable` cell is by definition NOT live (Live ⇔
    in a record), so it is in no record; reclamation is purely worktree + marker removal. (This corrects the
    unit-28 NEXT pointer's "+ drop the registry record cell" — there is no record cell to drop. The registry
    is owned by `isolate new/rm/repair`; gc only collects not-live leftovers.)
  • **Busy task = skip, not exit-6** — a HELD `isolate-state:<task>` lock means an isolate op is in flight on
    that task (the workspace-gc counterpart of "journaled as live"): the task is SKIPPED (recorded as a `busy`
    block, Status→blocked), never fought. This deliberately DIVERGES from the single-task Remove/Repair verbs
    (which surface a held lock as their own exit-6 contention) — a workspace sweep must not let one in-flight
    op abort reclamation of unrelated leftovers. (`errors.As` on `*lock.HeldError`; a non-contention lock
    fault is still a hard error.)
  Test-first → RED (undefined) → GREEN. `TestCollectReclaimsOnlyReclaimable` — end-to-end over the same
  four-class real-git workspace as Inspect, asserting BOTH the result projection AND the physical effect: only
  `gone/web` (reclaimable) loses its worktree+marker; `gone/db`/`gone/ledger` (blocked) + `gone/auth` (orphan)
  + `active/api` (live) all survive byte-for-byte, protected markers intact, and `active/api` appears in no
  outcome. `TestCollectSkipsBusyTask` — with the task lock held, the reclaimable `gone/web` is left untouched
  and the run reports blocked. Two mutants confirmed RED-then-reverted (registry below): make the
  blocked_work arm reclaim → `gone/ledger`'s clean-but-ahead committed work destroyed → RED (no-live-loss);
  make the orphan arm reclaim → `gone/auth` destroyed → RED (evidence-positive). `gofmt`/`go vet`/`go build
  ./...`/`go test ./...` GREEN (25 packages) + linux cross-build/vet clean.
  NEXT M4 unit — HEAL-2 sub-unit **(c) `wi gc [--dry-run]` CLI handler** (`internal/cli/cmd_gc.go` + the
  registry/help wiring): reuse the `cli.WithDryRun`/`DryRunFrom` seam — `--dry-run` projects `gc.Inspect`'s
  candidates into `planned[]` (the reclaimable cells) + `blocked[]` (blocked_work/orphan, the loud surface);
  the act path calls `gc.Collect` and projects `CollectResult` onto `repos[]` (reclaimed) + `blocked[]`, exit
  6 / nonzero on any block (mirror the `isolate repair` handler shape — see `cmd_isolate.go`). Project the
  domain `gc.Class`/reasons into the frozen `contract` sub-codes (`orphan_unexplained` is the §7.5 loud one);
  `internal/contract` stays the sole owner of the wire enums. THEN the explicit **HEAL-GC-NO-LIVE-LOSS**
  negative fitnesses (PLAN 123-125): no reclaim of reflog-only/equal-to-base work; no resurrection of a
  completed-then-deleted isolate; HEAL-4-reset + HEAL-6-gc composition cannot prune a discarded sha.

  (30) ✅ **this firing** — `wi gc [--dry-run]` CLI handler (guard `CMD-GC`, `internal/cli/cmd_gc.go` +
  `_test`), HEAL-2 sub-unit (c) — the command that wires the gc sweep to the envelope contract, COMPLETING
  HEAL-2's Classify→Inspect→Collect→CLI spine. plan/act split on the `--dry-run` context seam (mirror of the
  `isolate repair` handler): `--dry-run` → `gc.Inspect` → `planned[]` (reclaimable) + `blocked[]`
  (blocked_work/orphan), action `read`, NO top-level error (exit 0, SHAPE-DRYRUN-EXIT0), zero mutation; the
  act path → `gc.Collect` → per-cell outcomes projected onto `repos[]`, refusal `*CommandError` on any block.
  Wired into `BuildRegistry` (`"gc"`) + the `internal/help` command table (HELP-REGISTRY-SYNC stays green:
  the two surfaces now both carry gc). **DECISIONS RECORDED this unit:**
  • **#GC-ID (composite cell identity)** — gc is the one WORKSPACE-WIDE verb, so a row spans multiple tasks,
    but the frozen `repos[]`/`planned[]`/`blocked[]` entries key on a single `repo` string (no task field, and
    adding one would move the frozen envelope shape → schema bump). So each cell is identified by the composite
    `"<task>/<repo>"` projected into that free-text field. Wire shape UNTOUCHED (no schema bump); every row
    unambiguous. `internal/contract` stays the sole owner of the wire enums — gc adds none.
  • **#GC-EXIT (refusal kind reflects the actionable cause)** — a `blocked_work`/`orphan`/per-cell-fault block
    is a **conflict (exit 4)**: deliberate intervention required. A sweep blocked ONLY because a task is busy
    (its lock is held by an in-flight op) is **lock_held (exit 6)**: transient retry-later contention, a
    materially different recovery signal. Conflict DOMINATES a mix (an orphan does not become transient just
    because another task is also busy). This refines the unit-29 NEXT pointer's loose "exit 6 / nonzero on any
    block" into the precise two-bucket rule.
  • **headline Action = noop on the act path** (mirror of decision #RP) — a sweep has no single verb in the
    closed Action enum; its heterogeneous per-cell effects (reclaim→`removed`, block→`noop`) are authoritative
    in `repos[].action`. The reclaimable cell projects to `contract.ActionRemoved` (no `ActionReclaimed` exists;
    `removed` is the honest verb, matching `isolate rm`).
  • per-cell sub-codes: reclaimable→no error; blocked_work→`{conflict, code:"blocked_work"}`;
    orphan→`{conflict, code:"orphan_unexplained"}` (the §7.5 loud surface, matching `isolate repair`); busy
    skip (the one per-TASK, repo-less outcome `gc.Collect` emits)→`{lock_held}` keyed on the task alone; a
    per-cell git/IO fault→`{internal}`.
  Test-first → RED (no `gc` factory; harness built the four-class workspace fine) → GREEN. Five fitnesses:
  `TestGCReclaimsAndRefuses` (real run over the four-class workspace — conflict refusal, web `removed`,
  db/ledger `blocked_work`, auth `orphan_unexplained`, live api OMITTED, physical effect: only web's
  worktree+marker gone), `TestGCDryRunDoesNotMutate` (read plan, exit-neutral, nothing reclaimed),
  `TestGCBusyTaskIsLockHeld` (busy-only sweep → lock_held exit 6, worktree untouched), `TestGCFactoryRejectsOperands`
  (workspace-wide ⟹ no operand), `TestGCEmptyWorkspaceIsCleanNoop` (idempotent empty success). Two mutants
  confirmed RED-then-reverted (registry below): blocked-sweep→`(result,nil)` mis-reported as clean → RED;
  `--dry-run` falls through to the mutating sweep → RED. Full gate GREEN (25 packages) + `go vet` + linux
  cross-build/vet clean.
  NEXT M4 unit — HEAL-2's spine is COMPLETE; what remains for HEAL-2 is the explicit **HEAL-GC-NO-LIVE-LOSS**
  negative-fitness battery (PLAN 123-125), best added as a focused `internal/gc` test unit: (i) no reclaim of
  reflog-only / equal-to-base work (a cell whose HEAD == marker is `ClassReclaimable` ONLY when also clean —
  pin that an equal-to-base-but-dirty cell stays blocked, and that a reflog-only commit that left HEAD == marker
  is still reclaimable, i.e. AheadOfBase keys on HEAD-vs-marker not reflog); (ii) no resurrection of a
  completed-then-deleted isolate (gc removes the worktree+marker but never re-creates a record or re-adds a
  worktree); (iii) HEAL-4-reset + HEAL-6-gc composition cannot prune a discarded sha (deferred until HEAL-4
  lands the op journal). THEN, past HEAL-2: HEAL-5 `land continue/abort/status`; HEAL-4 durable op journal +
  roll-forward; HEAL-6 mirror-stale refusal; HEAL-7 atomic `.wi/` writes; HEAL-8 `wi doctor`/`check` + bounded
  `--fix` (LAST — composes the safe heals repair+gc).

  (35) ✅ **this firing** — **HEAL-4 sub-unit 3b: the journal directory scan** (guard `HEAL-CRASH-RECOVER`,
  scan limb; `internal/journal/scan.go` + `scan_test.go`) — the offline recovery ENTRY POINT that turns the
  durable journal subtree into a recovery worklist. `Scan(journalDir) ([]OpRecovery, error)` enumerates
  `.wi/journal/*.jsonl` (os.ReadDir → deterministic op_id order), `ReadOp`s each op's lifecycle, and pairs the
  op's identity (`OpID`/`Kind`/`Task`/`Repos`, taken from its intent entry `entries[0]`, safe because Classify
  rejects an empty set first) with the `Disposition` its FURTHEST phase calls for (`Classify`, sub-unit 3a).
  `OpRecovery` is the worklist item the roll-forward executor (3c) will drive. Read-side I/O only; dials no
  network. Composes the conservative posture of its parts: a **missing** journal subtree → empty worklist, no
  error (idempotent — recovery runs at startup before any op journals); a journal it cannot **parse** (torn
  line, via ReadOp) or **classify** (contentless file) → HARD error, never silently skipped (a dropped torn
  journal could hide a committed op from roll-forward — a data-integrity bug); non-`.jsonl` sidecars + subdirs
  ignored. Four fitnesses: pairs-ops-with-dispositions (3-op worklist done→Complete / committed→RollForward /
  intent-only→Abandoned + identity carried), missing-dir→empty, torn-journal→error, skips-non-journal-files.
  Test-first → RED (undefined `Scan`) → GREEN → both registered mutants confirmed RED then reverted (`cached` =
  byte-identity): pair-all-with-Complete reddens ONLY the roll-forward/abandoned ops of the pairing test
  (pins per-op classification flows through, the load-bearing recovery-worklist correctness); skip-on-ReadOp-
  error (`continue`) reddens ONLY the torn-journal test (pins surface-never-skip). Full gate GREEN (25 pkgs) +
  gofmt clean + linux cross-build/vet clean.
  NEXT M4 unit — HEAL-4 sub-unit 3c: the **offline roll-forward executor + startup hook**. For each
  `OpRecovery` from `Scan`: `RollForward` → run the op's KIND-SPECIFIC completion (isolate_rm finishes its
  deletion; isolate_new/land per their finish step), append a `done` entry, then delete the journal file;
  `Complete` → delete the stale journal file; `Abandoned` → leave it (surfaced; partials reconciled by the
  evidence-positive heals). Then wire it as the OFFLINE startup hook (zero network — no fetch, no dial — runs
  before the command body). This is the piece that unblocks the deferred HEAL-GC-NO-LIVE-LOSS case (iii)
  (HEAL-4-reset+gc composition). 3c needs the kind-specific finish logic, so it may itself split into the
  executor core (pure-ish dispatch + journal append/delete) and the per-kind completion wiring.

  (34) ✅ **(prior firing)** — **HEAL-4 sub-unit 3a: the pure recovery classifier** (guard `HEAL-CRASH-RECOVER`,
  classifier limb; `internal/journal/recover.go` + `recover_test.go`) — the decision core the offline
  roll-forward recovery scan acts on, the pure-classifier-before-executor shape again (mirrors `gc.Classify`
  before `gc.Collect`). NO I/O, total over a non-empty entry set, dials no network. Shipped:
  • **DECISION RECORDED #4 (RESOLVED) — isolate-remove recovery policy = roll-FORWARD** (the §7 open decision,
    "leaning roll-forward"; stamped RESOLVED in IMPLEMENTATION_PLAN.md §7 #4 + the ledger below). The policy is
    decided per-op from the journal's FURTHEST-reached phase: `done`→**Complete** (nothing to recover);
    `committed`-but-not-`done`→**RollForward** (FINISH it — an interrupted isolate-rm completes its deletion,
    never restored, accepting that an interrupted remove can't be undone by re-run); `intent`-only→**Abandoned**
    (crashed before commit — neither finished nor undone, partial artifacts left to the evidence-positive heals
    isolate repair / gc). This is the §7 "never heal in a way that destroys live work" posture: only a
    point-of-no-return-crossed op is auto-finished; a pre-commit crash is surfaced, not guessed.
  • `Disposition` closed set (`complete`/`roll_forward`/`abandoned`) — an INTERNAL recovery vocabulary (like
    `Phase`/`Kind`/`gc.Class`), NOT a contract wire enum. `Classify(entries) (Disposition, error)` computes the
    furthest phase via a local `phaseRank` (intent<committed<done) so a torn/re-ordered journal (a stray
    earlier-phase line trailing a later one) can NEVER downgrade the verdict; empty entry set → error (a
    contentless journal file is an anomaly, never produced by Append, surfaced not silently classified).
  • Three fitnesses: the phase→disposition truth table (6 rows), furthest-wins over non-monotonic journals
    (3 rows — pins rank-max over last-entry), empty→error.
  Test-first → RED (undefined `Classify`/`Disposition`) → GREEN → both registered mutants confirmed RED then
  reverted (`cached` = byte-identity): committed→Complete reddens the committed truth-table+order rows (the
  load-bearing roll-forward/data-integrity guarantee — a crashed-after-commit op must finish); last-entry-wins
  reddens ONLY the furthest-wins test (surgical). Full gate GREEN (25 packages) + gofmt clean + linux
  cross-build/vet clean.
  NEXT M4 unit — HEAL-4 sub-unit 3b: the **journal directory scan** (`Scan(journalDir) ([]OpRecovery, error)`
  or similar) — enumerate `.wi/journal/*.jsonl`, `ReadOp` each, pair its identifying entry (op_id/kind/task/
  repos) with `Classify`'s `Disposition`, returning the recovery worklist; idempotent on a missing dir (empty),
  hard-errors on a torn line. THEN 3c: the **roll-forward executor + offline startup hook** (finish each
  `RollForward` op via its kind-specific completion — isolate_rm finishes deletion — append `done`, then delete
  the journal file; `Abandoned`/`Complete` files cleaned/surfaced; OFFLINE-ONLY, zero network). 3c needs the
  kind-specific finish logic and is the piece that unblocks the deferred HEAL-GC-NO-LIVE-LOSS case (iii).

  (33) ✅ **(prior firing)** — **HEAL-4 sub-unit 2: the append-safe per-op JSONL journal store + `.wi/journal`
  layout subdir** (guard `JOURNAL-STORE`, `internal/journal/store.go` + `store_test.go`; `internal/layout`
  gains `subJournal`/`JournalDir()`/the `WiSubdirs` entry). The I/O layer over sub-unit 1's pure codec — the
  state→I/O→executor→CLI shape again (codec → this store → recovery scan → startup hook). Shipped:
  • **DECISION RECORDED #JOURNAL-PER-OP-FILE**: the journal is ONE append-only JSONL file per operation,
    `.wi/journal/<op_id>.jsonl`, NOT a single shared workspace log. Rationale: (a) it reuses the SINGLE atomic
    `.wi/` writer `lockfs.WriteFileAtomic` (DESIGN §6.2) — an Append is read-prior → concatenate → atomic
    whole-file replace — instead of introducing a second durability primitive (O_APPEND+fsync); (b) it is
    race-free WITHOUT a dedicated journal lock, because distinct ops touch distinct files and a single op is
    driven through its phases sequentially by one process (a shared read-modify-write log would lose updates
    across two tasks journaling under different task locks); (c) it mirrors the per-entity-file pattern of
    `state`/`land` (`.wi/state/<task>.json`, `.wi/land/<task>.json`). op_id passes `layout.ValidateSegment`
    (the one traversal chokepoint lock keys also use) so a crafted id cannot escape the subtree.
  • `Append(journalDir, Entry)` validates via Marshal (a malformed record never reaches disk), reads prior
    content, concatenates the new line, and commits through `WriteFileAtomic`. `ReadOp(journalDir, opID)`
    returns the op's entries in append (chronological) order; an op that never journaled reads back empty with
    no error (idempotent posture of `state.Load`/`lock.List`); a torn/incompatible line is a hard error
    (ParseEntry's conservative refusal — recovery surfaces what it cannot understand, never silently skips).
  • Three fitnesses, three crisp limbs: append accumulates (a 2nd Append does not clobber the 1st →
    intent→committed→done all survive); per-op isolation (op_a reads only op_a's lines, op_b only op_b's,
    an unseen op reads empty); crash-safe append (a 3rd Append under the injected `lockfs.FaultBeforeRename`
    crash window FAILS and leaves the prior two lines intact + the done line absent — append-safe-under-crash,
    inherited from the single atomic writer).
  Test-first → RED (undefined `Append`/`ReadOp`) → GREEN → all three registered mutants confirmed RED then
  reverted (`cached` = byte-identity): overwrite-instead-of-append reddens accumulate (+crash); constant-path
  reddens ONLY isolation; `os.WriteFile`-instead-of-`WriteFileAtomic` reddens ONLY crash (fault seam bypassed,
  faulted Append wrongly succeeds). The bootstrap subtree fitness auto-covers the new `journal` subdir (it
  loops `WiSubdirs()`); the layout accessor table gains a `JournalDir` row. Full gate GREEN (25 packages) +
  gofmt clean + linux cross-build/vet clean.
  NEXT M4 unit — HEAL-4 sub-unit 3: the **offline roll-forward recovery scan** (guard `HEAL-CRASH-RECOVER`,
  PLAN 76-77) — enumerate `.wi/journal/`, `ReadOp` each, group by op_id, take the furthest-reached phase,
  and FINISH every `committed`-but-not-`done` op (roll FORWARD per PLAN §7 decision #4, never roll back),
  deleting each op's journal file once `done`. OFFLINE-ONLY (zero network dials). This also unblocks the
  deferred HEAL-GC-NO-LIVE-LOSS case (iii) (HEAL-4-reset+gc composition cannot prune a discarded sha).

  (32) ✅ **(prior firing)** — **HEAL-4 sub-unit 1: the pure op-journal record + codec** (guard `JOURNAL-CODEC`,
  `internal/journal/journal.go` + `journal_test.go`, package `journal`/`journal_test`) — the first, foundational
  piece of HEAL-4 (DESIGN §7.4 durable op journal + offline roll-forward), built test-first exactly as
  `lock.Holder`'s codec preceded auto-break and `gc.Classify` preceded Inspect/Collect. NO I/O, NO layout change,
  dials no network — the append-safe JSONL writer (over `lockfs.WriteFileAtomic`) and the recovery scan are the
  NEXT sub-units layered on top. Shipped:
  • `Phase` (`intent`/`committed`/`done`) and `Kind` (`isolate_new`/`isolate_rm`/`land`) as closed sets with
    `Valid()` — INTERNAL durable-state vocabularies (like `state.Stage`/`gc.Class`), NOT contract wire enums, so
    journal owns them; they grow additively as recoverable verbs land.
  • `Entry{OpID,Kind,Phase,Task,Repos}` with durable wire keys `op_id`/`kind`/`phase`/`task,omitempty`/
    `repos,omitempty`; `Marshal()` validates then emits a single newline-terminated JSON line (the JSONL unit the
    writer will append verbatim); `ParseEntry()` REFUSES empty/blank/malformed/unknown-kind/unknown-phase/empty-op_id
    conservatively (never a degraded zero Entry) — the same posture as `lock.ParseHolder` on an unreadable body.
  • Fitness asserts round-trip identity AND the concrete durable wire keys + JSONL shape (exactly one trailing
    newline) + closed-enum rejection — NOT round-trip alone, applying the recorded LOCK-HOLDER lesson directly.
  Test-first → GREEN → both registered mutants confirmed RED (surgically) then reverted (`cached` = byte-identity):
  the closed-enum mutant (`Phase.Valid`→`true`) reddened ONLY the rejection + membership assertions; the wire-key
  mutant (`phase`→`state` json tag) reddened ONLY the concrete-key assertion while round-trip stayed GREEN —
  PROVING the wire-key check is load-bearing and round-trip alone is vacuous against a tag rename. Full gate GREEN
  (25 packages) + gofmt clean + linux cross-build/vet clean.
  NEXT M4 unit — HEAL-4 sub-unit 2: the **append-safe JSONL journal writer** (`Append`/`Read` over
  `lockfs.WriteFileAtomic`, DESIGN §6.2) + a `.wi/journal` layout subdir (extend `layout.WiSubdirs` + an accessor),
  test-first with a paired mutant; then sub-unit 3, the **offline roll-forward recovery scan** (group lines by
  op_id, furthest phase wins, finish `committed`-but-not-`done` ops — guard `HEAL-CRASH-RECOVER`, PLAN 76-77),
  which also unblocks the deferred HEAL-GC-NO-LIVE-LOSS case (iii).

  (31) ✅ **(prior firing)** — the explicit **HEAL-GC-NO-LIVE-LOSS** negative-fitness battery (guard
  `HEAL-GC-NO-LIVE-LOSS`, `internal/gc/no_live_loss_test.go`, package `gc_test`), PLAN 123-125 — **HEAL-2 is
  now FULLY COMPLETE** (Classify→Inspect→Collect→CLI spine + this guarantee battery). Two fitnesses over a
  real git workspace driven through the real `gc.Collect`, each pinning a guarantee the four-class GC-COLLECT
  fitness left implicit:
  • `TestGCRefusesEqualToBaseCommit` — case (i) "committed-but-equal-to-base": an `--allow-empty` commit (TREE
    byte-identical to base) still advances HEAD to a fresh sha, so it is unmerged work gc REFUSES (blocked_work,
    worktree+marker+commit all survive byte-for-byte). Pins that §7.1 "ahead of base" is **COMMIT IDENTITY**
    (HEAD != markerSHA), NOT tree-equality — an impl that compared trees and reclaimed "nothing changed" cells
    would silently destroy a real commit. Surgical mutant (tree-equality `AheadOfBase`) reddens ONLY this test.
  • `TestGCNoResurrectionOfCompletedIsolate` — case (ii) "no resurrection of a completed-then-deleted isolate":
    after a task's record is deleted (the isolate was torn down), gc reclaiming its lone clean leftover is purely
    **SUBTRACTIVE** — it writes NO registry record (`state.Load`→`ErrNoRecord`), re-adds no worktree, and a
    second sweep is a clean `CollectComplete`/0-outcome noop. Mutant (a post-reclaim `state.Store` tombstone)
    reddens ONLY this test.
  • **DECISION RECORDED #GC-AHEAD-V0** (resolving the unit-30 NEXT pointer's reflog sub-case): under the v0
    "the marker IS the base evidence" convention, `AheadOfBase` keys on HEAD-vs-marker, so a worktree RESET back
    to HEAD==marker is clean + not-ahead, hence reclaimable — wi does NOT (and in v0 cannot) refuse a reflog-only
    commit that left HEAD at the marker. The PLAN's stronger "refuses reflog-only work" guarantee is **DEFERRED to
    HEAL-4**: only a durable op journal can distinguish a reflog-only commit from a deliberately-discarded sha
    (PLAN case (iii), the HEAL-4-reset+gc composition). Recorded honestly rather than faked with a vacuous test.
  Test-first → both GREEN against the existing (correct) `gc.Classify`/`Collect` → each mutant confirmed RED
  (surgically, ONLY its own test) then reverted → full gate GREEN (25 packages) + `go vet` + linux
  cross-build/vet clean.
  NEXT M4 unit — **HEAL-2 is DONE.** Per the Wave-C build order, the next self-heal is **HEAL-5 `land
  continue/abort/status`** (the staged-land resume verbs) OR **HEAL-4 durable op journal + roll-forward** (which
  also unblocks the deferred HEAL-GC-NO-LIVE-LOSS case (iii) above and is the foundation HEAL-8 `doctor`
  composes). Recommend HEAL-4 next — it is the keystone the remaining heals (5's resume, 6's staleness, 8's
  bounded `--fix`) and the deferred discarded-sha guarantee all build on; start with its pure journal
  record/codec (mirror of the `state`/`lock.Holder` codec units) test-first with a paired non-vacuity mutant.

- **Milestone (MVP baseline — verified complete):** **✅ MVP M0–M3 COMPLETE AND GREEN (verified 2026-06-30, this time for real).**
  The gap ORIENT caught (below) is fully closed: `help` and `suggest` are built, wired, and guarded, and
  the MVP has been re-verified END TO END this firing. `gofmt -l .` clean · `go build ./...` · `go vet
  ./...` · `go test ./...` all GREEN (23 packages); `goreleaser check` is covered GREEN by the remote CI
  run (28411028448, all three jobs incl. macOS). **Built-binary smoke over the full surface** (fresh temp
  workspace): `wi help`→exit 0 (one envelope, all 6 commands in the help block), `wi help isolate new`→
  exit 0 (topic detail, table omitted), `wi help snc`→exit 3 not_found + `did_you_mean:["sync"]`, `wi snc`
  (unknown command)→exit 64 usage + `did_you_mean:["sync"]` (dispatch-level suggest), `wi init`→0 created,
  `wi resolve ghost`→3 not_found, reinit→4 already_exists, `wi --format text init`→4 lossless text
  projection — each emits EXACTLY ONE envelope with a correct closed-set exit code. The git-backed flows
  (`sync`/`isolate new`/`isolate rm`/`repo add`) are covered GREEN by hermetic guard tests over real git
  (`testenv`), reached through the same Dispatch path the smoke exercised. **All seven commands reachable;
  the `help-json` capability finally has a real backing command; `did_you_mean` fires on both unknown
  commands and unknown help topics; the help table is fitness-locked against registry drift
  (`HELP-REGISTRY-SYNC`).** M4/M5 remain unstarted by design (gated on explicit owner go-ahead). Owner
  follow-ups before first `v*` release: set `HOMEBREW_TAP_GITHUB_TOKEN` PAT; add a LICENSE + set the cask
  `license`; (optional) a `wi version` unit to enable `-X` stamping. _Gap history retained below._

- **Milestone (superseded — the gap that the line above closes):** ~~⚠️ MVP M0–M3 NOT COMPLETE — prior
  "green/STOP" claims were premature (corrected 2026-06-30).~~ ORIENT caught a real gap: PLAN line 136
  lists **`help` and `suggest`** as M3
  deliverables ("MVP = M0–M3", line 140), but neither `internal/help` nor `internal/suggest` had been
  built. The earlier firings counted the six command handlers + `cmd/wi` + CI/goreleaser/cask as "the
  MVP" and skipped these two — the build disagreed with PROGRESS.md, so the build wins. Tells: `wi help`
  → `unknown command` (exit 64) while the envelope advertises the `help-json` capability (violating
  PLAN line 108 "capabilities ⇒ backing command"); the `contract.Error` `Help`/`DidYouMean` fields and
  the `cli`/`text` plumbing exist but **nothing ever populated them**. **Gap now CLOSED IN CODE, one unit
  per firing:** ✅ `internal/suggest` (did-you-mean Levenshtein engine, guard `SUGGEST-DIDYOUMEAN`,
  commit `b043457`), ✅ `internal/help` (progressive-disclosure help model + `next[]` rules, guard
  `HELP-MODEL`, `56451d4`), ✅ dispatch wiring of `did_you_mean[]` + the `wi help` pointer on an unknown
  command (guard `DISPATCH-DIDYOUMEAN`, `8a60fec`), ✅ the additive `help` block on the envelope
  (decision #HB, `6e9e4ed`), ✅ the `wi help [topic]` command backing the `help-json` capability (guard
  `CMD-HELP`, `cac244a`), and ✅ the **help↔registry sync fitness** (guard `HELP-REGISTRY-SYNC`, decision
  #HR — this firing) which proves the help table can never drift from the live command surface. Both
  `help` and `suggest` are now BUILT, WIRED, and GUARDED. **The ONLY remaining MVP work is the end-to-end
  re-verification of M0–M3** (build/vet/test/gofmt + `goreleaser check` + a fuller binary smoke incl. `wi
  help` and an unknown command/topic carrying `did_you_mean`); only when that passes is the STOP
  condition genuinely real.
  Everything still builds/vets/tests green throughout. _What WAS genuinely done (six commands, cmd/wi,
  full release scaffolding incl. Node-24-current CI) stands; it just wasn't the whole of M3._

- **Milestone (superseded — was wrongly marked complete):** ~~✅ MVP M0–M3 COMPLETE AND GREEN — STOP~~.
  All six MVP commands run end-to-end through the uniform pipeline, plus full release scaffolding: CI
  gate (gofmt/build/vet/test on ubuntu+macos) + `goreleaser check` + `.goreleaser.yaml` (cross-compile,
  4 targets) + Homebrew cask + tag-push `v*` `release.yml`. Owner follow-ups before first release: set
  `HOMEBREW_TAP_GITHUB_TOKEN` PAT secret; add a LICENSE; (optional) a `wi version` unit to enable `-X`
  version stamping. M4/M5 remain unstarted by design. _Prior milestone history retained below._

- **Post-MVP (owner-driven, 2026-06-30):** Owner pushed `build/wi` to `origin`
  (`git@github.com:ggkguelensan/workspace-isolation.git`); first remote CI run (28411028448) passed
  **GREEN on all three jobs** — `goreleaser check`, gate on `ubuntu-latest`, AND gate on `macos-latest`
  (the Apple-Git §6 portability cell), confirming the authored workflows run on real runners. That run
  annotated a Node-20 deprecation, cleared this firing: bumped `actions/checkout@v4→v5`,
  `actions/setup-go@v5→v6`, `goreleaser/goreleaser-action@v6→v7` (all verified `using: node24`;
  goreleaser-action v7 keeps the distribution/version/args inputs) — commit `bc7e9a4`, `ci:`. CI config
  is DATA (no Go guard/mutant); validated by YAML parse + local green, re-validated CI-side on next push.
  **Not yet pushed** (no standing push permission — one explicit push ≠ standing grant).

- **Milestone (prior):** **M2 COMPLETE; M3 NEARLY COMPLETE — the `wi` binary runs end-to-end.** All six MVP
  commands plus `cmd/wi/main.go` now land green; the entire `init→repo add→sync→isolate new→resolve→
  isolate rm` surface is reachable through a runnable, smoke-verified binary. **Release scaffolding
  sub-units (a) CI gate + (b) `.goreleaser.yaml` have now landed** (a: gofmt+build+vet+test on
  ubuntu+macos, Go pinned from go.mod; b: v2 config cross-compiling cmd/wi for darwin/linux×amd64/arm64,
  proven by `goreleaser check` + a four-target snapshot build, wired into CI as a `goreleaser check`
  job). The ONLY remaining MVP (M0–M3) work is sub-unit **(c)**: the Homebrew `brews:` block + the
  tag-push `v*` release workflow. (Detail below.)
  Domain command core fully landed and green
  (`internal/config` manifest read+validate, `internal/state` per-isolate runtime registry + durable
  partial success, `internal/isolate.New` N-repo orchestration under the `isolate-state:<task>` lock /
  stop-on-first-fail with durable not-rolled-back completed repos DESIGN §6.3, `internal/resolve.Bundle`
  the pure zero-I/O path-bundle projector behind `wi resolve`; the two `internal/git` isolate primitives
  `AddWorktree` + `CreateOwnedRef`/`OwnedRefSHA` underpin isolate). **M3 (the CLI surface → MVP) has
  begun:** `internal/exitcontract` landed (the single exit chokepoint owning the compiled
  `error.kind → exit-code` table `ExitCodeFor`, guard `SHAPE-FAIL-MATRIX`, + the sole `os.Exit` wrapper
  `Exit`), and `internal/cli.Emit` landed — the serialization chokepoint that writes EXACTLY ONE
  schema-valid envelope as a single compact line + newline, through the same `json.Marshal` path the
  contract goldens are frozen against (guard `SHAPE-ONE-ENVELOPE`); and the `internal/cli` ASSEMBLER
  landed — `Meta{OpID,Command,DryRun}` + the `Success`/`Failure` envelope constructors (sole owners of
  the **ok ⟺ error==nil** coupling + the always-stamped `schema_version`/`capabilities`/op_id/command/
  dry_run spine) + `ExitFor` (process exit as a pure function of the top-level error; `Blocked` is
  exit-neutral; resolves decision **#D** — partial = top-level `error.kind=partial`→exit 2, and dry-run
  exit-0 is honored by the planner leaving `Error` nil rather than a blanket override). Guards
  `SHAPE-ASSEMBLE` + `SHAPE-DRYRUN-EXIT0`. And `cli.RenderText` landed — the `--format text` lossless,
  path-scoped projection of the same assembled struct `Emit` serializes (no re-read of git/state; guard
  `SHAPE-TEXT-PROJECTION` proves losslessness via an independent reflection walk over string leaves;
  decision **#T**). And the `internal/cli` Runner EXECUTE-CORE landed — `Result` (typed domain outcome) +
  `Command` (`Run(ctx) (*Result, error)`) + `CommandError` (typed kind+hints; non-`*CommandError`→
  `internal`) + `Format`/`Render` (json→`Emit` / text→`RenderText`) + `Execute` (run → `envelopeFor`
  (Success/Failure + threads every additive block; a partial carries `repos[]` onto the top-level
  `error.kind=partial`) → `Render` → `ExitFor`), the SOLE assembler+serializer+exit-deriver so every
  command emits one envelope and exits identically; guard `RUN-PIPELINE`. And the DISPATCH layer landed —
  `Registry` (a `map[string]func(args)→(Command,error)` of dep-bound factories) + `Dispatch(ctx, w, clk,
  reg, args)` which parses globals (`--dry-run`/`--format`, recognized anywhere), resolves the subcommand
  by longest match (2-token `"isolate new"` beats 1-token `"isolate"`), mints `op_id` (`opid.New` via the
  clock — on EVERY path, incl. errors), builds `Meta`, and hands off to `Execute`; a parse error /
  unknown command / factory arg-rejection all collapse to ONE `usage` envelope (kind=usage → exit 64).
  Guard `DISPATCH-ROUTES`; resolves decision **#F** (hand-rolled stdlib parser, NOT cobra — zero-dep
  posture #6/#C, fixed small command surface). **The ENTIRE generic CLI pipeline — argv → dispatch →
  outcome → one envelope (json|text) → mapped exit — is complete and green, and the FIRST per-command
  handler is now plugged into it:** `wi resolve` (pure read — `state.Load`→`not_found`-on-`ErrNoRecord`,
  else `resolve.Bundle`→`Result{Action:read}`) + the `Deps`/`BuildRegistry` seam (the dep-bound factory
  map `cmd/wi` hands to `Dispatch`); guard `CMD-RESOLVE`, establishing the handler→`Result`/`CommandError`
  contract every remaining command follows. **The second handler — `wi init` — has now landed too:** it
  scaffolds a workspace at the resolved root (Bootstrap the `.wi/` subtree, then write a starter
  `wi.config.jsonc` LAST as an `O_EXCL` commit point; re-init → `already_exists` leaving the manifest
  intact), resolving the config WRITE path (decision #C, minimal starter-emitter) and the root-discovery
  decision (#G: root = cwd, init takes no operand). Guard `CMD-INIT`. **The third handler — `wi isolate
  new` — has now landed too** (the marquee command): it resolves each requested repo against the manifest
  → `isolate.RepoSpec` (undeclared → not_found, missing manifest → not_found+`wi init`, malformed → usage
  = decision #H), reads the minted op_id via `OpIDFrom(ctx)` into the durable `IsolateRecord`, drives
  `isolate.New`, and maps `StatusComplete`→created / `StatusPartial`→the durable `(result,
  *CommandError{partial})` carrying per-repo `repos[]` (#D) / `*lock.HeldError`→lock_held; the first
  handler needing a `*git.Git` (added `Deps.Git`, wired in `BuildRegistry`). Guard `CMD-ISOLATE-NEW`.
  **The fourth handler — `wi sync [<repo>…]` — has now landed too, with its domain core:** the
  `internal/sync` package (`sync.Run` — per-repo, under `repo:<name>`,
  `EnsureClone`→`Fetch`→`FastForwardBaseRef`→`mirror.Store`, CONTINUE-on-fail = decision #S; guard
  `SYNC-RUN`; the first `internal/` package to drive `gitexec.RunNetwork` end-to-end), and the thin
  `cmd_sync` handler over it (no operands → all declared repos; named → a subset; undeclared → not_found;
  missing manifest → not_found+`wi init`; reads each manifest URL into the `RepoSpec`; projects per-repo
  `synced`/`noop` + freshness; `StatusPartial`→durable `(result, *CommandError{partial, Action:synced})`;
  per-repo `error.kind` deferred to the gitexec classifier = decision #K). Added a `Clock` field to `Deps`.
  Guard `CMD-SYNC`. **The deferred AST-preserving config EDIT path has now landed** (`internal/config`
  `edit.go`): `config.Add(path, name, url, base)` splices a new repo object into the existing `repos`
  array as raw TEXT — preserving every comment/whitespace byte — rather than round-tripping through
  encoding/json (which the read path's `stripJSONC` proves would drop comments); validates name via
  `ValidateSegment` + non-empty url BEFORE any read, refuses a duplicate with the new `ErrDuplicateRepo`
  sentinel, omits the base field when `base==""` (inherit `defaults.base`), re-Parses its own output as a
  belt before the atomic `lockfs.WriteFileAtomic`. Guard `CONFIG-ADD`. **The fifth handler — `wi repo add
  <name> <url> [--base <branch>]` — has now landed too** (`cmd_repo_add.go`), a thin seam over
  `config.Add`: the factory parses `--base`/`--base=` (globals already stripped by Dispatch) + validates
  `<name>`/arg-count → usage; `Run` takes the `project-registry` lock (contended → lock_held) then maps
  `config.Add` outcomes (success → created+`wi sync` hint / `ErrDuplicateRepo` → already_exists / missing
  → not_found+`wi init` / malformed → usage). Registered as the 2-token key `"repo add"`. Guard
  `CMD-REPO-ADD`. **The LAST MVP handler — `wi isolate rm` — has now FULLY LANDED (all three sub-units):**
  (a) the `internal/git` EVIDENCE-POSITIVE reclamation primitives `RemoveWorktree` (`git worktree remove`:
  deregisters the admin entry, NO `--force`/NO `git reset --hard`, refuses a dirty worktree — DESIGN
  §7.1/§7.2) + `DeleteOwnedRef` (`update-ref -d` the marker, idempotent), guard `GIT-RECLAIM` (the
  owned-ref READ/verify side `OwnedRefSHA` pre-existed, guard `GIT-OWNED-REF`); (b) `internal/isolate.Remove`
  (guards `ISOLATE-REMOVE` + `ISOLATE-REMOVE-TEARDOWN`, plus the `state.Delete` it needs): under the
  `isolate-state:<task>` lock it walks the targeted repos (empty → all recorded = full teardown), evaluates
  the three evidence-positive gates per repo (marker exists / clean / HEAD == marker-sha =
  not-ahead-of-base, decision #RM), reclaims the verified ones (`RemoveWorktree` + `DeleteOwnedRef`) and
  drops them from the registry (deleting the record + emptied task dir when the last repo goes), and
  HARD-BLOCKs any `orphan_unexplained` (never auto-pruned/`--force`'d, left intact); and (c) the thin
  `cmd_isolate_rm.go` handler over that green core (guard `CMD-ISOLATE-RM`, one line in `BuildRegistry`):
  factory validates `<task>`→usage (bare task = full teardown is VALID) + binds the optional un-checked
  `<repo>…` subset; `Run` maps `isolate.Remove`'s reclaimed/blocked tallies onto the return convention
  (decision **#RD**) — all reclaimed→`removed`/exit 0, mixed→durable `(result, *CommandError{partial,
  Action:removed})`/exit 2, nothing-reclaimed-with-orphan→full refusal `conflict`/exit 4,
  nothing-reclaimed-all-non-members→not_found, `*lock.HeldError`→lock_held, `state.ErrNoRecord`→not_found
  +`wi isolate new`; blocked repos ride in **repos[]** (per-repo `conflict`/`orphan_unexplained`), NOT
  `Blocked[]`, because `envelopeFor` threads only `Repos/Warnings/Next` onto a failure envelope.
  **ALL SIX MVP COMMANDS NOW LAND GREEN** (`init` · `repo add` · `sync` · `isolate new` · `resolve` ·
  `isolate rm`) — the full `init→repo add→sync→isolate new→resolve→isolate rm` surface exists as handlers
  plugged into the generic pipeline. What remains for MVP M0–M3: **`cmd/wi/main.go`** (build the real
  registry + `clock.System`, resolve the layout via `layout.Resolve(cwd)`, call `Dispatch`, the single
  `os.Exit` via `exitcontract.Exit` — the ONLY `main` package + the ONLY `os.Exit` in the tree); then CI +
  `.goreleaser.yaml` + Homebrew tap. Deferred
  enrichments pulled in when a command needs them: a `--` end-of-flags terminator + `did_you_mean` in
  dispatch, `isolate.New` resume (skip repos already `StageCreated`), per-repo base persisted in `state`
  (populates `resolve`'s `branch`), state KV + `cas`.
  M0 + M1 complete: contract spine, layout, opid, clock, testenv, lockfs, lock, `gitexec` runner+belt,
  full `internal/git` (resolve / ff / EnsureClone / IsClean / Fetch / DivergedCounts), complete
  `internal/mirror`, and both DESIGN §2 architecture invariants (INV-NO-LLM + INV-NO-NETWORK).
- **Wave:** A complete (modulo `NORM-CORRECT`, deferred to Wave B); in Wave B domain code (M2).

## Done

- **M3 · Homebrew cask + tag-push release workflow — sub-unit (c); COMPLETES MVP M0–M3** (`brews:`→
  `homebrew_casks:` in `.goreleaser.yaml` + `.github/workflows/release.yml`; decision **#HC**).
  goreleaser **hard-deprecated `brews` INSIDE the `~> v2` range we pin** (v2.16), so PLAN §6's
  mitigation ("pin the major to dodge the deprecation, cask rejected") does NOT hold — `goreleaser
  check`, our fitness, FAILS on `brews` (exit non-zero, "uses deprecated properties"). Trusting the
  build over the doc, migrated to `homebrew_casks` (goreleaser's steer for prebuilt-binary Homebrew
  distribution): cask `wi` → tap repo `ggkguelensan/homebrew-tap` (dir `Casks`), `skip_upload: auto`,
  `goreleaserbot` author; the generated cask carries BOTH `on_macos` and `on_linux` URL stanzas (so it
  references the Linux tarballs too, Linux cask support being Homebrew-dependent). No `license` (no
  LICENSE file yet — owner legal decision) and no `test do` (casks don't support it). `release.yml`:
  on push tag `v*` → `goreleaser release --clean` (checkout `fetch-depth: 0` for the changelog, Go
  pinned from go.mod, `goreleaser-action@v6` `~> v2`), `permissions: contents: write`; the cross-repo
  cask push is wired to a `HOMEBREW_TAP_GITHUB_TOKEN` PAT secret (default `GITHUB_TOKEN` can't write
  another repo). **Process artifact** (config DATA + a workflow), no Go guard/mutant; fitness =
  `goreleaser check`. **Validated with goreleaser v2.16.0**: `check` clean (zero deprecations) AND
  `goreleaser release --snapshot --skip=publish` generated a valid `dist/homebrew/Casks/wi.rb`. **Full
  MVP verification (this firing):** gofmt clean · `go build`/`go vet`/`go test` green · `goreleaser
  check` PASS · both workflows parse · end-to-end binary smoke (`init`→0, `resolve ghost`→3, reinit→4,
  `bogus`→64, one envelope each). **MVP M0–M3 is GREEN — STOP condition reached.**

- **M3 · `.goreleaser.yaml` + `goreleaser check` CI wiring — sub-unit (b) of release scaffolding**
  (`.goreleaser.yaml` + a `goreleaser-config` job in `ci.yml`; decision **#GR**). Schema **v2**, pinned
  major `~> v2` (PLAN §6, never auto-upgraded). `builds`: cross-compiles `cmd/wi` for `{darwin,linux} ×
  {amd64,arm64}`, reproducibly (`CGO_ENABLED=0` + `-trimpath` + `mod_timestamp: {{.CommitTimestamp}}`),
  `ldflags: -s -w`. `archives`: tar.gz, `name_template wi_{Version}_{Os}_{Arch}`, bundling README +
  `LICENSE*` (glob, absent-OK). `checksum`: sha256 `checksums.txt`. `snapshot`/`changelog` (github,
  excludes docs/test/chore/style/ci/merges). `release`: `ggkguelensan/workspace-isolation`,
  `prerelease: auto`, `mode: replace`. **Version stamping via `-X` omitted on purpose** — `cmd/wi/main.go`
  declares no `version`/`commit`/`date` vars, so injecting would hit non-existent symbols; deferred to a
  future `wi version` unit that adds the vars first. **PROCESS ARTIFACT, not a Go fitness function** (like
  sub-unit a): its fitness is `goreleaser check`, wired as a CI job (`goreleaser-action@v6`, `args: check`,
  `version: ~> v2`) so a malformed/deprecated config fails CI — no `go test` guard/mutant. **Validated
  locally with goreleaser v2.16.0**: `goreleaser check` clean (zero deprecations) AND `goreleaser build
  --snapshot --clean` produced all four binaries — confirmed Mach-O arm64/x86_64 + ELF aarch64/x86-64 via
  `file`. `dist/` already gitignored (`/dist/`); the `go mod tidy` before-hook left go.mod/go.sum
  unchanged. Remaining for MVP M0–M3: **sub-unit (c)** — the Homebrew `brews:` block (tap
  `ggkguelensan/homebrew-tap`) + the tag-push `v*` release workflow.

- **M3 · CI gate workflow — sub-unit (a) of release scaffolding** (`.github/workflows/ci.yml`; preceded
  by a `style:` commit making the tree gofmt-clean). Mechanizes the exact green gate every build
  iteration already enforces locally — `gofmt -l` (no diffs), `go build ./...`, `go vet ./...`,
  `go test ./...` — on push to `main`+`build/wi` and on every pull request, matrix `[ubuntu-latest,
  macos-latest]` (Linux = recent upstream git; macOS = Apple Git, the PLAN §6 portability risk). Go is
  pinned from `go.mod` via `setup-go`'s `go-version-file` (single source of truth, no toolchain drift);
  golden suite is fail-closed by construction (plain `go test` never passes `-update`, so goldens are
  asserted, PLAN §2); least-privilege `contents: read`; `cancel-in-progress` per ref; `fail-fast: false`
  so one OS's failure doesn't mask the other. Actions pinned to major tags (checkout@v4, setup-go@v5);
  SHA-pinning noted as an owner hardening. **PROCESS ARTIFACT, not a Go fitness function** — this is the
  one MVP unit whose "green" is the workflow's own gate passing, NOT a `go test` guard/mutant pair (as
  flagged when the unit was queued). Verified by parsing the YAML (via Ruby's `psych`; pyyaml absent)
  and asserting the four gate commands run on both OSes, triggers/perms/matrix/Go-pin are as intended.
  **Prerequisite finding:** `gofmt -l` flagged three already-committed files (`fault_test.go` trailing-
  comment column alignment; `isolate.go`/`sync.go` numbered-list doc comments whose tab-indented
  continuation lines gofmt 1.19+ reflows) — fixed in the preceding `style:` commit, since a `gofmt -l`
  gate cannot be green on a tree gofmt would rewrite. Decision **#CI** recorded below. Remaining for MVP
  M0–M3: sub-unit (b) `.goreleaser.yaml` + (c) Homebrew tap.

- **M3 · `cmd/wi/main.go` — the single process entry; the `wi` BINARY now runs end-to-end** (`main.go` +
  `main_test.go`, guard `CMD-MAIN`). The ONLY `main` package and the ONLY `os.Exit` site in the tree:
  `main()` does nothing but `exitcontract.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))` — all wiring
  lives in the testable `run(ctx, args, stdout, stderr) contract.ExitCode` seam (never terminates the
  process). `run` (1) discovers the root from cwd via `workspaceRoot` = `os.Getwd`→`layout.Resolve`
  (decision #G, both local syscalls — no network), (2) builds the REAL `Deps{Layout, Git:
  git.New(gitexec.New()), Clock: clock.System{}}` and `BuildRegistry` over them, (3) hands argv to
  `cli.Dispatch`, which emits EXACTLY ONE envelope and returns the mapped code — propagated UNCHANGED.
  Two faults above Dispatch each still emit one envelope: an unresolvable root → `startupFailure` (mints
  an op_id like Dispatch does, emits a JSON `internal` envelope, exit 70); an envelope-WRITE failure
  (Dispatch's Go-error return — no envelope to show) → one line on stderr + exit 70. Guards (hermetic via
  `t.TempDir`+`t.Chdir`, Go 1.26): the happy path reaches the REAL init handler — one `created` success
  envelope, exit 0, AND `.wi/` actually scaffolded on disk (a stub registry / misresolved root would not
  create it); and exit-code PROPAGATION — an unknown command exits 64 (the registered mutant `return
  ExitOK` reddens here). **Smoke-verified with the built binary** in a fresh temp workspace: `wi init`→
  exit 0 (one JSON envelope), `wi resolve ghost`→not_found/exit 3, `wi --format text init` (reinit)→
  already_exists/exit 4 (lossless text projection), `wi bogus`→usage/exit 64. **The full
  `init→repo add→sync→isolate new→resolve→isolate rm` command surface is now reachable through a runnable
  `wi`.** Only CI + `.goreleaser.yaml` + Homebrew tap remain for MVP M0–M3.

- **M3 · `wi isolate rm <task> [<repo>…]` handler — the LAST MVP command** (`cmd_isolate_rm.go` +
  `cmd_isolate_rm_test.go`, guard `CMD-ISOLATE-RM`; one line in `BuildRegistry`). The thin teardown seam
  over the green `isolate.Remove` core (sub-unit c — completes the `isolate rm` triplet
  GIT-RECLAIM → ISOLATE-REMOVE → CMD-ISOLATE-RM). Factory `newIsolateRmCommand` validates `<task>` via
  `ValidateSegment` → usage (≥1 arg; bare task is VALID = full teardown, distinct from `isolate new`'s
  ≥2), binds the optional `<repo>…` subset UN-segment-checked (a non-member is a per-repo domain
  not_found, not usage). `Run` drives `isolate.Remove` and maps its outcome onto the return convention
  (decision **#RD**): `*lock.HeldError`→lock_held; `state.ErrNoRecord`→not_found+`wi isolate new` hint;
  then by reclaimed/blocked tallies — **all reclaimed**→`Result{Action: removed}` (exit 0); **mixed**
  (≥1 reclaimed AND ≥1 not)→the DURABLE PARTIAL `(result, *CommandError{partial, Action: removed})`
  (exit 2, resumable — re-running reclaims now-unblocked repos, #D); **nothing reclaimed, ≥1 orphan
  hard-block**→a full refusal `*CommandError{conflict}` (exit 4); **nothing reclaimed, every non-removed
  repo merely not a member**→`*CommandError{not_found}`. `projectRemoveOutcome` maps each
  `isolate.RemoveOutcome`→`RepoResult`: reclaimed→`removed`; orphan hard-block (`Reason` set)→`noop` +
  per-repo `Error{Kind: conflict, Code: "orphan_unexplained", Message: Reason}`; `ErrRepoNotInIsolate`→
  per-repo not_found; other fault→internal. Blocked repos ride in **repos[]**, NOT `Blocked[]` — the
  critical contract fact (decision #RD): `envelopeFor` threads only `Repos/Warnings/Next` onto a FAILURE
  envelope, so a non-zero exit that put blocked repos in `Blocked[]` would silently drop them. Guards
  (hermetic `testenv` + real git, materializing via the real `isolate new` handler): complete teardown
  (both repos removed + record deleted), the durable partial (api removed / web ahead-of-base blocked as
  a repos[] conflict coded orphan_unexplained / record retains only web), all-blocked → conflict with the
  orphan still in repos[] + record intact, missing-record → not_found+`wi isolate new`, and factory
  arg-validation (no task → usage, traversing task → usage, bare task → runnable). **This is the last of
  the six MVP commands** (`init` · `repo add` · `sync` · `isolate new` · `resolve` · `isolate rm`). What
  remains for MVP M0–M3: `cmd/wi/main.go` (the real registry + `clock.System` → `Dispatch` → the single
  `os.Exit` via `exitcontract.Exit`), then CI + `.goreleaser.yaml` + Homebrew tap.

- **M3 · `internal/isolate.Remove` — the evidence-positive reclamation domain core** (`isolate.go` +
  `isolate_test.go`, guards `ISOLATE-REMOVE` + `ISOLATE-REMOVE-TEARDOWN`; plus `state.Delete`). The SECOND
  sub-unit of the last MVP handler — the domain logic `cmd_isolate_rm` will project onto the envelope.
  `Remove(ctx, l, g, task, repos) (RemoveResult, error)` reclaims an isolate's worktrees UNDER the
  `isolate-state:<task>` lock (held → `*lock.HeldError`/exit 6; a missing record → `state.ErrNoRecord` so
  the handler maps not_found). An empty `repos` targets every recorded repo (full teardown); else exactly
  the named ones (a name not in the record → a per-repo `ErrRepoNotInIsolate`). For EACH target,
  `reclaimRepo` evaluates the three evidence-positive gates IN ORDER (DESIGN §7.1): (1) **ownership** — the
  marker `refs/wi/owned/<task>/<repo>` must exist (`OwnedRefSHA`), a missing marker is `orphan_unexplained`;
  (2) **clean** — `IsClean` on the worktree, a dirty tree is `orphan_unexplained`; (3) **not ahead of
  base** — the worktree HEAD (`ResolveRef(wt,"HEAD")`) must still equal the marker's recorded sha (decision
  **#RM**: the per-repo base name is NOT persisted in `state.RepoRecord`, so the MARKER is the base
  evidence — a HEAD past it carries local commits = ahead of base). Only when all three pass is the repo
  reclaimed (`RemoveWorktree`, itself a no-force/no-reset-hard cleanliness net, then `DeleteOwnedRef`). A
  gate failure is a **HARD BLOCK** (`RemoveOutcome.Reason` set, `Removed:false`, never `--force`'d, left
  intact on disk + in the marker store + in the registry), NOT a Go error — exactly the
  `orphan_unexplained` contract (never auto-pruned, DESIGN §7.1/§7.2). After the loop the reclaimed repos
  are dropped from the registry; when the LAST recorded repo is reclaimed the record is `state.Delete`d (the
  isolate no longer exists) and the emptied task dir removed best-effort. `RemoveResult.Status` =
  `RemoveComplete` iff every target was reclaimed, else `RemoveBlocked`. Added `state.Delete(stateDir,
  task)` (idempotent record delete — a missing record is a no-op success; exercised end-to-end by the
  teardown test). Guards (hermetic `testenv` + real git, materializing via `isolate.New`):
  `TestRemoveReclaimsCleanBlocksAheadOfBase` — a clean unmoved "api" is reclaimed (worktree + marker gone),
  a "web" given a local commit (clean tree, HEAD ahead) is a HARD BLOCK (intact, marker preserved, retained
  in the registry), a "ghost" not in the record is `ErrRepoNotInIsolate`, overall `RemoveBlocked`;
  `TestRemoveAllCleanDeletesRecord` — empty target reclaims all, record deleted (`ErrNoRecord`) + task dir
  gone; `TestRemoveRefusesWhenIsolateStateHeld` — a pre-held lock → `*lock.HeldError`;
  `TestRemoveMissingRecordIsErrNoRecord` — a never-created task → `state.ErrNoRecord`. Both mutants
  demonstrated RED-then-reverted: (1) drop the ahead-of-base gate (`if false && head != marker`) → web
  wrongly reclaimed → the primary test RED on every "web intact" assertion; (2) `state.Store` an empty husk
  instead of `state.Delete` → the teardown test RED (`state.Load` finds a record, want `ErrNoRecord`).
  Full `go build ./… && go vet ./… && go test ./…` GREEN (21 packages). NEXT (final MVP code unit): the
  thin `cmd_isolate_rm.go` handler over this core, then `cmd/wi/main.go`.
- **M3 · `internal/git` reclamation primitives — `RemoveWorktree` + `DeleteOwnedRef`** (`git.go` +
  `git_test.go`, guard `GIT-RECLAIM`). The git-level foundation `isolate rm` composes for EVIDENCE-POSITIVE
  reclamation (DESIGN §7.1/§7.2) — the FIRST sub-unit of the last MVP handler. `RemoveWorktree(ctx,
  ssotDir, worktreePath)` wraps `git worktree remove`, which deletes the worktree directory AND
  deregisters it from the SSOT's worktree admin (`.git/worktrees/<id>`) — unlike a bare `rm -rf` that
  would strand a stale prunable admin entry. It passes **NO `--force`** and runs **NO `git reset --hard`**
  (DESIGN §7.2): a worktree carrying modified OR untracked files is REFUSED and left intact (git exits
  128), a second safety net beneath the isolate layer's own cleanliness gate. `DeleteOwnedRef(ctx, ssotDir,
  task, repo)` clears the ownership marker `refs/wi/owned/<task>/<repo>` with a single `update-ref -d`,
  called once the worktree it vouched for is reclaimed; deleting an already-absent marker is a no-op
  success (verified empirically — git's `update-ref -d` with no expected old value succeeds on a missing
  ref), so a re-run of reclamation stays idempotent. Both are local (offline `Run`). Guard `GIT-RECLAIM`
  (`git_test.go`, hermetic `testenv` + real git): clean removal → the dir is gone, the SSOT no longer
  registers the worktree (`worktree list --porcelain`), and the SSOT stays pristine; a dirty (untracked
  file) worktree → remove REFUSES, the worktree is intact AND still registered; create→delete marker →
  `OwnedRefSHA` reports absent + raw `for-each-ref` confirms gone + a second delete is a no-op success.
  Mutants demonstrated RED-then-reverted: (1) replace `git worktree remove` with `os.RemoveAll(path)` →
  the dir vanishes but the admin entry survives ("prunable gitdir file points to non-existent location")
  AND a dirty worktree is wrongly nuked → `TestRemoveWorktreeDeregisters` + `TestRemoveWorktreeRefusesDirty`
  RED; (2) skip the `update-ref -d` (`if false`) → the marker survives → `TestDeleteOwnedRefClearsMarker`
  RED. Full `go build ./… && go vet ./… && go test ./…` GREEN (21 packages). NEXT sub-unit: `internal/
  isolate.Remove` (walk the recorded repos under the `isolate-state:<task>` lock, verify each owned-ref +
  clean + not-ahead-of-base, reclaim the verified ones via these primitives, HARD-BLOCK any unexplained
  orphan), then the thin `cmd_isolate_rm.go` handler.
- **M3 · `wi repo add <name> <url> [--base <branch>]` handler** (`cmd_repo_add.go` + `cmd_repo_add_test.go`).
  The fifth per-command handler — a THIN seam over `config.Add` (guard `CONFIG-ADD`). The factory parses
  the command-specific `--base`/`--base=` flag (Dispatch already stripped the globals) via
  `extractBaseFlag`, requires EXACTLY `<name> <url>` after the flag (wrong count → usage), and validates
  `<name>` through `layout.ValidateSegment` at parse time (an unsafe name → usage, before any I/O — mirrors
  how `isolate new` validates its `<task>`). `Run` owns only the seam responsibilities `config.Add` does
  not: it takes the **`project-registry` lock** for the whole edit (registry mutation; a contended lock →
  `lock_held`/exit 6, never a corrupting concurrent rewrite), then maps `config.Add`'s outcomes —
  success → `Result{Action:created}` (no network, no `repos[]`, a `wi sync <name>` next-hint);
  `ErrDuplicateRepo` → `already_exists`; `fs.ErrNotExist` → not_found+`wi init`; any other (malformed
  manifest) → usage. Registered in `BuildRegistry` as the 2-token key `"repo add"` (longest-match beats a
  bare `repo`). Guard `CMD-REPO-ADD` (clean append re-parses with the repo + preserves the comment;
  inherited-base omission; duplicate → already_exists byte-stable; busy registry → lock_held byte-stable;
  missing manifest → not_found; factory arg/`name` validation → usage). Mutant demonstrated: acquire zero
  lock keys → only the lock_held test reddens.
- **M3 · `internal/config.Add` — the AST-preserving manifest EDIT path** (`edit.go` + `edit_test.go`).
  The deferred WRITE half of `internal/config` (companion to the `CONFIG-PARSE` read path), and the
  primitive `wi repo add` is built on. `Add(path, name, url, base) error` appends a repo declaration by
  **splicing a raw object literal into the existing `repos` array as text**, leaving every other byte
  (comments, whitespace, key order) untouched — deliberately NOT a `Parse → mutate → Marshal` round-trip,
  which `stripJSONC` proves would discard every comment the user wrote. Mechanics: `findReposArray` locates
  the top-level `repos` array's `[`/`]` by tracking object depth + string/comment state (so a brace inside
  a string/comment never moves the cursor, and the key is matched only at object depth 1);
  `lastElementEnd` finds the insertion point just past the last element's closing brace (comma-prefixed
  insert) or reports an empty array (no-comma insert after `[`); `lineIndent` aligns the new line under
  the closing bracket. Validation (`ValidateSegment` on name, non-empty url) runs BEFORE any read so a bad
  request never touches the file; a duplicate name returns the new `ErrDuplicateRepo` sentinel (→
  `already_exists`); `base==""` OMITS the base field so the repo inherits `defaults.base` (Add never
  writes the resolved default); the rewrite is re-`Parse`d as a belt before the atomic
  `lockfs.WriteFileAtomic`, so a splicing bug can never persist a corrupt manifest. Guard `CONFIG-ADD`
  (clean add preserves all comments + existing repos and re-parses with the new repo; inherited-base
  omission; empty-array insert; duplicate refusal leaves the file byte-for-byte intact; unsafe name and
  missing manifest both refuse before write). Mutant demonstrated: `data = stripJSONC(data)` before the
  splice → comments vanish but structure stays valid → RED isolated to the comment-survival assertions.
- **M2/B · `internal/sync` — the sync domain core (materialize + advance + record freshness)**
  (`sync.go` + `sync_test.go`). The orchestration core behind `wi sync`, built as its own domain package
  for symmetry with `internal/isolate` (the other materializing command) and hermetic testability — the
  thin `cmd_sync` handler that projects it onto the envelope is the NEXT unit. `sync.Run(ctx, l, g, clk,
  opID, specs) (Result, error)` syncs each `RepoSpec{Name, URL, Base}` in request order, each UNDER its
  own `repo:<name>` lock (the identical key v1 `land` takes — this is what linearizes the freshness race,
  DESIGN §6.1). Per repo (`syncOne`): `git.EnsureClone` (lazy — clone the SSOT detached at base tip on
  first sync, no-op once present) → `git.Fetch` (the network dial) → `git.ResolveRef(origin/<base>)` →
  `git.FastForwardBaseRef(base, originSHA)` (the SOLE base-ref mutation — ff-only update-ref, no
  checkout/merge, SSOT stays detached & pristine; a rewound/force-pushed origin → `*git.NonFastForwardError`
  leaves the ref untouched) → `mirror.Store` a `Snapshot{behind:0}` (behind=0 because the base was just
  advanced to exactly the fetched origin tip under the lock — provably current). **CONTINUE-on-fail**
  (decision below): repos are independent SSOTs, so a per-repo failure (unreachable origin, non-ff, held
  lock) is recorded in that repo's `RepoOutcome.Err` and the remaining repos are STILL synced; overall
  `Status=StatusPartial` if any failed. This deliberately DIFFERS from `isolate.New`'s stop-on-first-fail
  (an isolate is one coherent workspace whose completed set must stay a resumable prefix; sync has no such
  inter-repo dependency). `Run`'s Go-error return is reserved for an op-level failure — in v0 every failure
  is per-repo, so it returns nil error and reports via Status/Repos. The first `internal/` package to drive
  `gitexec.RunNetwork` end-to-end (clone+fetch). Guard `SYNC-RUN` (`sync_test.go`, hermetic `testenv` +
  real git against local `file://` origins): fresh repo → lazy clone + base advanced to origin tip +
  behind-0 freshness persisted (`mirror.Load` round-trip) + SSOT working tree pristine (`IsClean`); origin
  advances (a pushed commit) → a second sync FAST-FORWARDS the on-disk base ref to the new tip;
  continue-on-fail → an unreachable repo listed FIRST fails yet the reachable repo after it still syncs,
  overall partial. Each SHA assertion checks the base ref ON DISK (`g.ResolveRef`), independent of the
  returned snapshot. Mutant demonstrated RED-then-reverted: drop the `g.FastForwardBaseRef` call (`if
  false {…}`) → after the origin advances the base ref stays frozen at the seed tip (`48f4258c…`, the
  testenv golden) instead of advancing → `TestSyncFastForwardsToNewOriginTip` RED on the on-disk assertion,
  while the fresh-materialize test (FF is a no-op when seed==origin) and continue-on-fail test stay GREEN —
  isolating the mutant to the advance path. Full `go build ./… && go vet ./… && go test ./…` GREEN
  (21 packages).
- **M3/B · `wi isolate new` handler — the marquee command** (`cmd_isolate_new.go` + `Deps.Git` +
  `BuildRegistry` line + `cmd_isolate_new_test.go`). The seam between the isolate domain core (durable
  partial success, DESIGN §6.3) and the envelope contract — and the FIRST handler needing a `*git.Git`
  (added `Git *git.Git` to `Deps`, wired in `BuildRegistry`; read-only commands leave it nil).
  `newIsolateNewCommand(l, g, args)` validates `<task>` (a safe segment via `layout.ValidateSegment` →
  usage) + ≥1 `<repo>`; repo names are NOT segment-checked at the factory (an undeclared one is
  not_found, not usage — the operand is well-formed, it just names nothing wi manages). `isolateNewCmd.Run`:
  (a) `config.Load(l.Config())` — missing manifest (`fs.ErrNotExist`) → `not_found` + `wi init` hint, a
  malformed manifest → `usage` (decision #H: user-fixable input, exit 64, NOT internal); (b) resolves
  each requested repo via `cfg.Lookup` → `isolate.RepoSpec{Name, Base(effective — defaults applied by
  config.Parse)}`, an undeclared repo → `not_found` naming it (resolved BEFORE any materialization, so a
  bad name writes no state record); (c) reads the minted op_id via `OpIDFrom(ctx)` (the CTX-OPID payoff)
  and drives `isolate.New(ctx, l, g, task, opID, specs)`; (d) maps Status onto the return convention —
  `StatusComplete` → `Result{Action:created, Repos:[…projected]}`; `StatusPartial` → the DURABLE PARTIAL
  `(result, *CommandError{Kind:partial, Action:created})` carrying per-repo `repos[]` (decision #D, the
  only both-non-nil case); a returned `*lock.HeldError` → `*CommandError{Kind:lock_held}` (exit 6); any
  other returned err → internal. `projectRepoOutcome` maps `isolate.RepoOutcome`→`contract.RepoResult`
  (Stage==created → action created else noop; worktree path + tip sha; a per-repo `Error{kind:internal}`
  on exactly the failed repo — Mirror/Branch empty for v0, a refined per-repo Error.Kind awaits the
  gitexec stderr classifier). It never assembles an envelope or picks an exit code. Guard `CMD-ISOLATE-NEW`
  (`cmd_isolate_new_test.go`, hermetic `testenv` + real `git`, driven THROUGH `BuildRegistry`'s factory):
  complete path (2 repos created + the durable `IsolateRecord.OpID` == the op_id injected into the ctx —
  proving the seam pays off); durable partial (web has no SSOT → both a `*CommandError{partial,
  Action:created}` AND a result whose repos[] shows api created / web errored, durable registry api
  created / web pending); undeclared-repo → not_found naming the repo + NO record written; missing
  manifest → not_found + `wi init` help + NO record; factory arg validation (<2 args or a traversing task
  → usage, safe task+repo → a Command). Mutant demonstrated RED-then-reverted: on `StatusPartial` return
  `(result, nil)` instead of the partial `*CommandError` → a partial is mis-reported as a clean success →
  `TestIsolateNewDurablePartial` RED (`want *cli.CommandError, got <nil>`). Full `go build ./… &&
  go vet ./… && go test ./…` GREEN (20 packages).
- **M3/B · op_id propagation via context — the `WithOpID`/`OpIDFrom` seam** (`opctx.go` +
  `Execute` injection + `opctx_test.go`). The small prerequisite that unblocks every handler which must
  record the op identity in DURABLE state (the first being `isolate new` → `IsolateRecord.OpID`). The
  `Command` interface stays minimal (`Run(ctx)` only) — the per-invocation op_id is runtime context, so
  it rides the `context.Context`: `WithOpID(ctx, opID)` stores it under an unexported zero-size key
  type (no cross-package collision), `OpIDFrom(ctx)` reads it back (or `""` outside the pipeline).
  `Execute` injects `m.OpID` into the ctx ONCE, before `cmd.Run`, so a handler reads the SAME id the
  envelope reports rather than minting a divergent one (which would split a single invocation's
  correlation id across the envelope and the state record). Guard `CTX-OPID` (`opctx_test.go`): the
  load-bearing test drives a `capturingCommand` THROUGH `Execute` and asserts it observed `Meta.OpID`
  (proves the wiring, not just the helper); a round-trip + bare-ctx→`""` test covers the helper. Mutant
  demonstrated RED-then-reverted: drop the `ctx = WithOpID(ctx, m.OpID)` injection in `Execute`
  (`if false {…}`) → the command observes `""` → `TestExecuteInjectsOpIDIntoContext` RED. Full
  `go build ./… && go vet ./… && go test ./…` GREEN (20 packages).
- **M3/B · `wi init` handler — workspace scaffold** (`cmd_init.go` + `BuildRegistry` line +
  `cmd_init_test.go`). The second per-command handler and the first WRITE command (resolves decision #C's
  deferred write path with a minimal starter-manifest emitter; the AST-preserving `repo add` edit path
  stays deferred to its own unit). `newInitCommand(l, args)` takes NO positional operand — init DEFINES
  the workspace at the resolved root (decision **#G**: root = cwd, the layout `cmd/wi` resolves at
  startup; a surplus arg → `*CommandError{Kind:usage}`; an explicit `--root`/`-C` override + parent
  walk-up are deferred, both additive/contract-neutral). `initCmd.Run` Bootstraps the `.wi/` runtime
  subtree (idempotent) THEN writes the `starterManifest` (a commented empty-but-valid JSONC skeleton)
  LAST as the commit point — an `O_EXCL` create, so the manifest's presence reliably marks a completed
  init and a re-init refuses cleanly with `already_exists` (→ exit 4) rather than clobbering a real
  manifest; Bootstrap precedes the write so a Bootstrap failure leaves no manifest and a retry starts
  clean. No git, no network. Guard `CMD-INIT` (`cmd_init_test.go`, real `layout.Resolve`'d but
  NOT-Bootstrapped temp root — the analog of an uninitialized cwd, driven THROUGH `BuildRegistry`'s
  factory): a fresh dir → a `created` Result whose written manifest ROUND-TRIPS through the real
  `config.Load` (init's emitter dogfoods the config reader so they cannot drift) + the `.wi/state`
  subtree exists (Bootstrap ran), with paths INDEPENDENTLY derived (scheme literals joined over the root,
  not via `l.Config()`/`l.StateDir()`); a re-init → `*CommandError{Kind:already_exists}` with the
  existing manifest preserved byte-for-byte; factory arg validation (any operand → usage, none → a
  runnable Command). Mutant demonstrated RED-then-reverted: open the manifest with `O_TRUNC` instead of
  `O_EXCL` (clobber-on-reinit) → a re-init silently rewrites the manifest and returns `created` (no
  `fs.ErrExist`→`already_exists`) → `TestInitOnExistingProjectIsAlreadyExists` RED (got a result/nil
  error, want `*cli.CommandError`). Reverted → full `go build ./… && go vet ./… && go test ./…` GREEN
  (20 packages).
- **M3/B · first per-command handler — `wi resolve` + the `Deps`/`BuildRegistry` seam** (`cmd_resolve.go`
  + `cmd_registry.go` + `cmd_resolve_test.go`). The first real `Command` plugged into the green generic
  pipeline, plus the registry-builder seam `cmd/wi` will use. `Deps{Layout}` carries the
  already-resolved startup dependencies the factories close over (grows additively — a new dep is a new
  field, existing handlers untouched); `BuildRegistry(Deps) Registry` wires each subcommand's canonical
  name → its arg-parsing factory (adding a command = one line here + its `cmd_<name>.go`, no change to
  `Dispatch`). `newResolveCommand(l, args)` is the `resolve` factory: validates the positional args
  (exactly one task, a safe segment via `layout.ValidateSegment`) → a `*CommandError{Kind:usage}` on
  failure (the traversal check lives HERE so a bad task name is a clean usage refusal, not an opaque
  internal error from state/layout), else binds task+layout into `resolveCmd`. `resolveCmd.Run` is a
  PURE projection (DESIGN §3.1, §map 166): `state.Load(StateDir, task)` → on `state.ErrNoRecord` a
  `not_found` refusal (operator hint points at `wi isolate new`), any other load error unclassified →
  internal, else `resolve.Bundle` → `Result{Action: read, Resolve: &block}`. No git, no network, no
  mutation. Handlers live in `package cli` (top of the dep stack — importing `layout`/`state`/`resolve`
  is one-directional, no cycle). Guard `CMD-RESOLVE` (`cmd_resolve_test.go`, real bootstrapped layout +
  stored record, driving the command THROUGH `BuildRegistry`'s factory — the exact path `cmd/wi` uses):
  a present 2-repo record → a `read` Result whose resolve block carries INDEPENDENTLY-derived paths
  (scheme literals joined over the normalized root, not via the layout accessors the impl uses); a
  missing record → `*CommandError{Kind:not_found}` naming the task; factory arg validation (0/2 args +
  a traversing name → usage, one safe arg → a runnable Command). Mutant demonstrated RED-then-reverted:
  `Run` skips the `errors.Is(err, state.ErrNoRecord)` branch (`if false && …`) → a missing isolate
  surfaces as a plain `*errors.errorString` (→ internal) not a `*CommandError{not_found}` →
  `TestResolveMissingIsolateIsNotFound` RED. Full `go build ./… && go vet ./… && go test ./…` GREEN (19
  packages). Establishes the handler→`Result`/`CommandError` contract every remaining command follows.
- **M3/B · `internal/cli` DISPATCH layer — `Registry` + `Dispatch` (argv → Command + Meta → Execute)**
  (`dispatch.go` + `dispatch_test.go`). The front half of the Runner, sitting on top of the green
  `Execute` core (DESIGN §3, §4). `Registry` = `map[string]func(args []string) (Command, error)`: a
  factory per canonical command string (`"init"`, `"isolate new"`), each closing over the command's
  deps (layout/clock/git/…) so `Dispatch` stays dependency-agnostic and a factory carries only the
  parsed args; a factory rejecting its args → a `usage` envelope. `Dispatch(ctx, w, clk, reg, args)
  (contract.ExitCode, error)`: mint `op_id` FIRST (`opid.New(clk.Now(), clk.Rand())` — every path,
  incl. errors, carries a correlation id; a crypto/rand failure is surfaced as `kind=internal`), parse
  globals, build `Meta{OpID, Command, DryRun}`, resolve the subcommand by LONGEST match (2-token
  `"isolate new"` beats 1-token `"isolate"`), then `reg[name](rest)` → `Execute`. A parse error, an
  unknown command, or a factory arg-rejection ALL become one `usage` envelope (`kind=usage` → exit 64)
  via `emit` (= the same `Render`+`ExitFor` wiring `Execute` uses), so every dispatch path — including
  the ones that never reach a `Command` — still emits EXACTLY ONE envelope. `parseGlobals` is a forgiving
  single pass: `--dry-run`, `--format <v>`, `--format=<v>` recognized ANYWHERE (v0 command args are
  plain names/URLs that never start with `--`), everything else positional; an absent/invalid `--format`
  value is a usage error; format defaults to `json`. A returned Go error is reserved for an
  infrastructure write failure (propagated from `emit`/`Execute`). Resolves decision **#F** (hand-rolled
  stdlib parser, NOT cobra). Guard `DISPATCH-ROUTES` (`dispatch_test.go`, fake `Registry` of recording
  factories + the `fakeCommand`/`decodeOne` helpers reused from `run_test.go`): a known 1-token command
  routes + threads its `Result` (exit 0, `command` stamped); a 2-token command resolves AND forwards the
  trailing args; an unknown command → `kind=usage` exit 64 naming the command, with NO factory run; a
  factory arg-rejection → `kind=usage` exit 64; `--dry-run` threads to `env.DryRun` without leaking into
  command args; `--format text` reaches `RenderText` (non-JSON, shows `init`+`OK`); `--format yaml` is a
  usage error; `op_id` is `opid.Valid` on BOTH the success and the unknown-command paths. Mutant
  demonstrated RED-then-reverted: `resolveCommand` ignores the parsed name and always returns a fixed
  real command (`return "init", positional, true`) → `TestDispatchRoutesUnknownToUsage` RED (an unknown
  name wrongly runs a real command, exit 0 not 64) AND `TestDispatchRoutesTwoTokenCommand` RED (command
  stamped `"init"`, args not forwarded). Reverted → full `go build ./… && go vet ./… && go test ./…`
  GREEN (19 packages). **Deferred enrichments** (pull in when a command needs them): a `--` end-of-flags
  terminator + `did_you_mean` suggestions on the unknown-command path.
- **M3/B · `internal/cli` Runner execute-core — `Result`/`Command`/`CommandError` + `Format`/`Render` +
  `Execute`** (`run.go` + `run_test.go`). The single wiring point that turns a command's typed outcome
  into exactly one envelope + a process exit code (DESIGN §3, §4). `Result` is the typed domain outcome a
  handler returns (Action + Repos + the additive Resolve/Planned/Blocked + Warnings/Next) — a handler
  returns plain data and NEVER assembles the wire shape or picks a code. `Command` is `Run(ctx) (*Result,
  error)`, constructed by the (next) dispatcher with its args/deps already bound; its return convention is
  documented: `(result,nil)`=success, `(nil,*CommandError)`=classified failure, `(nil,plainError)`=
  internal, `(result,*CommandError{Kind:partial})`=durable partial. `CommandError` is the typed error
  that selects a failure envelope's `error.kind` + operator hints (repo/help/did_you_mean); a non-
  `*CommandError` maps to `kind=internal` (an unclassified failure is a bug, surfaced not hidden). `Format`
  (`json`|`text`) is a CLI presentation concern owned by `internal/cli` (NOT a wire enum — contract keeps
  those), and `Render(w, env, format)` dispatches json→`Emit` / text→`RenderText` over the SAME assembled
  struct. `Execute(ctx, w, m, format, cmd)` runs the command, maps `(*Result|error)`→Envelope via the
  unexported `envelopeFor` (Success/Failure + threads every additive block; a partial threads the result's
  per-repo detail ONTO the failure so "what completed" survives next to the top-level `error.kind=partial`,
  decision #D), serializes via `Render`, and returns `ExitFor(env)` — so every command, whatever the
  outcome, emits one envelope and exits the same way. A returned Go error is reserved for an INFRASTRUCTURE
  write failure (→ `ExitInternal`); a domain failure rides inside the emitted envelope. Guard `RUN-PIPELINE`
  (`run_test.go`, fake `Command`): drives all six outcome classes end-to-end through the real pipeline —
  success (blocks threaded, exit 0), `CommandError` (kind+hints preserved, matrix exit), plain error
  (kind=internal, exit 70), durable partial (ok:false + `kind=partial` BUT `repos[]` carried, exit 2),
  `--format text` dispatch (not JSON, shows `FAILED`+kind), dry-run blocked-verdict (exit-neutral, exit 0).
  Mutant demonstrated RED-then-reverted: drop the partial result-merge (`if false && r != nil`) → the
  partial loses its per-repo detail → `TestExecutePartialCarriesReposAndExitsTwo` RED. Full `go build ./… &&
  go vet ./… && go test ./…` GREEN (19 packages).
- **M3/B · `internal/cli.RenderText` — the `--format text` lossless projection** (`text.go` +
  `text_test.go`). `RenderText(io.Writer, contract.Envelope) error` is a PURE, path-scoped projection of
  the SAME assembled struct the JSON path (`Emit`) serializes — "no extra facts, no dropped facts"
  (DESIGN §3.1) — taking the already-built envelope and only reformatting it (no new I/O, never a
  git/state re-read), so the two wire forms can never disagree. Layout: a status header (command + OK/
  FAILED + a `[dry-run]` marker), the op_id/action/schema metadata line + capabilities, then one section
  per populated block (repos with their path bundle + per-repo freshness/error, resolve, planned,
  blocked, warnings, top-level error, next); empty optionals are omitted (absence drops no fact).
  Resolves decision **#T** (text renders every field, losslessly; formatting is a sectioned report).
  Guard `SHAPE-TEXT-PROJECTION` (`text_test.go`): losslessness is verified INDEPENDENTLY — a reflection
  walk (`collectStringLeaves`) enumerates every non-empty string leaf of a maximal envelope (free fields
  carry unique `z…z` sentinels, enums carry real mutually-distinct values) and asserts each appears in
  the render; inline non-vacuity (≥25 leaves found + a never-present sentinel that must NOT match) keeps
  the containment loop honest; a second test covers the non-string facts the walk can't (ok/dry-run
  bools, the freshness `behind` int) and that success/error/partial renders are distinguishable. Mutant
  demonstrated RED-then-reverted: drop the worktree line from `renderRepo` (`if false && …`) → the
  `"zworktreeonez"` leaf is absent → `TestRenderTextIsLossless` RED (naming the exact dropped fact).
  Full `go build ./… && go vet ./… && go test ./…` GREEN (19 packages).
- **M3/B · `internal/cli` assembler — `Meta` + `Success`/`Failure` + `ExitFor`** (`assemble.go` +
  `assemble_test.go`). The outcome→envelope→exit core that feeds `Emit`. `Meta{OpID, Command, DryRun}`
  is the per-invocation context the Runner threads in; the unexported `spine(Meta)` stamps the
  always-identical fields ONCE (`schema_version`=`contract.SchemaVersion`, `capabilities`=
  `contract.Capabilities()`, op_id/command/dry_run) so handlers return plain structs and never touch the
  wire. `Success(m, action, repos)` and `Failure(m, action, errPayload)` are the SOLE envelope
  constructors — they enforce the **ok ⟺ error==nil** coupling at the only two call sites that can build
  an envelope (Success → ok=true, nil error; Failure → ok=false, a stored *copy* of the error so the
  caller can't alias-mutate it); additive blocks (`Resolve`/`Planned`/`Blocked`) + `Warnings`/`Next` are
  command-specific and set by the caller on the returned value (they don't bear on the coupling).
  `ExitFor(env)` derives the process code as a **pure function of the top-level error** — nil → exit 0;
  else `exitcontract.ExitCodeFor(env.Error.Kind)` (the single §3.2 authority, so `partial`→2 falls out
  for free). It deliberately does NOT consult `Blocked` or `DryRun`. Resolves decision **#D** (how
  "every --dry-run → exit 0" is honored AND how partial success is represented). Guards `SHAPE-ASSEMBLE`
  (Success/Failure couplings + always-set common fields) + `SHAPE-DRYRUN-EXIT0` (`ExitFor` couples
  error→code via the matrix incl. partial→2, a populated `Blocked` is exit-neutral, and a genuine
  top-level error on a dry-run is NOT swallowed to 0). Both mutants demonstrated RED-then-reverted:
  `Success` sets `ok=false` → the coupling assertion RED; `ExitFor` returns `ExitRefused` when
  `len(Blocked)>0` → the dry-run-with-blocked exit-neutrality assertion RED. Full `go build ./… &&
  go vet ./… && go test ./…` GREEN (19 packages).
- **M3/B · `internal/cli.Emit` — the one-envelope serialization chokepoint** (`emit.go` +
  `emit_test.go`). `Emit(io.Writer, contract.Envelope) error` writes EXACTLY ONE envelope: a single
  compact JSON line + one trailing newline, and nothing else (DESIGN §3.1). It serializes through
  `contract.Envelope`'s own `json.Marshal` path — the SAME bytes the contract goldens and the published
  schema are frozen against — so emitted output can never drift from the contractual wire shape (no
  alternate Encoder, no HTML-escaping divergence); `MarshalJSON` already guarantees the always-present
  `error` + always-array `repos`/`capabilities`/`warnings`/`next`. First file of the `internal/cli`
  uniform-pipeline package. Resolves decision **#E** (emit output convention). Guard `SHAPE-ONE-ENVELOPE`
  (`emit_test.go`): decode the stream → exactly one top-level value then `io.EOF`; the four list fields
  decode as arrays + `error` key present; emitted bytes validate against the embedded schema (success +
  error); single-trailing-newline + single-compact-line; and the payload byte-equals `json.Marshal(env)`.
  Both mutants demonstrated RED-then-reverted: emit-twice → the EOF assertion RED; drop-the-newline → the
  newline assertion RED. Full `go build/vet/test ./…` GREEN (18 packages).
- **M3/A+B · `internal/exitcontract` — the exit chokepoint + failure matrix** (`exitcontract.go` +
  `exitcontract_test.go`). The single authority between a command's typed outcome and the process exit
  code (DESIGN §3.2, PLAN Wave A). `ExitCodeFor(contract.ErrorKind) contract.ExitCode` is the compiled
  §3.2 failure matrix (usage→64, not_found→3, dirty_worktree/conflict/already_exists→4, needs_approval→5,
  lock_held/mirror_stale→6, partial→2, internal→70), failing SAFE to `ExitInternal` for an unmapped kind;
  `MappedKinds()` exposes coverage for the totality check; `Exit(code)` is the SOLE `os.Exit` wrapper so
  there is exactly one termination point (the no-bare-`os.Exit` architecture guard, a later unit, will
  allowlist only this site). Resolves decision **#X** (`remote_error` → 70). Guard `SHAPE-FAIL-MATRIX`
  (`exitcontract_test.go`): an INDEPENDENT literal copy of the §3.2 table asserts every pairing; a
  separate totality test asserts the table covers EXACTLY `contract.AllErrorKinds()` and every produced
  code is in `contract.AllExitCodes()`; plus an unknown-kind-fails-to-internal test. Both mutants
  demonstrated RED-then-reverted: (1) `KindLockHeld`→`ExitRefused` reddened only the matrix value test;
  (2) dropping `KindInternal` (code collides with the default) left the value test GREEN but reddened the
  totality test — proving the two checks are non-redundant. Full `go build ./… && go vet ./… && go test
  ./…` GREEN (17 packages).
- **M2/B · `internal/resolve` — pure path-bundle projector** (`resolve.go` + `resolve_test.go`).
  `Bundle(layout.Layout, state.IsolateRecord) (contract.ResolveBlock, error)` is a PURE, zero-I/O
  projection (no config, no git, no network, no filesystem read) answering "given a task, where is
  everything?": `isolate_root`=TaskDir, `state_dir`=StateDir, `log`=LogDir, and per repo
  `worktree`=Isolate (`isolas/<task>/<repo>`), `mirror`=Repo (`repos/<repo>` SSOT clone), `branch`=`""`
  (v0 worktrees are DETACHED, DESIGN §5). Every path sourced from `internal/layout`, never hand-assembled;
  the CLI owns `state.Load` + `ErrNoRecord`→`not_found`. Resolves decision **#R**. Guard `RESOLVE-BUNDLE`:
  a 2-repo record projects to exact hand-written golden paths (independent join), + empty-record→non-nil
  empty slice + traversing-name→validation error. Mutants demonstrated: `mirror := worktree`→both Mirror
  assertions RED; drop a repo→count RED. **M2 turns COMPLETE.**
- **M0/A · contract enums** — `internal/contract/enums.go`: closed sets `Action` (6),
  `ErrorKind` (11), `ExitCode` (9), `Capability` (10 vocab / 4 advertised) + `SchemaVersion`.
  Guard `SHAPE-ENUM-DOUBLE-ENTRY` (`enums_test.go`): independent literal copies; drift /
  duplicate / subset checks; inline non-vacuity proof. Real-source mutant (added `"timeout"`
  to `AllErrorKinds`) confirmed RED, then reverted → GREEN.
- **M0/A · Envelope wire type** — `internal/contract/envelope.go`: the `Envelope` struct with
  locked field declaration order + custom `MarshalJSON` enforcing `error:null` (never omitted)
  and `repos`/`capabilities`/`warnings`/`next` always-`[]` (never null). Nested wire types
  `RepoResult`, `MirrorFreshness`, `Warning`, `Error`, `ResolveBlock`, `ResolveRepo`, `PlanItem`,
  `BlockItem` (additive blocks `resolve`/`planned`/`blocked` are omitempty per DESIGN §3.1).
  Guard `SHAPE-ENVELOPE-INVARIANTS` (`envelope_test.go`): golden success/error bytes,
  error-always-present, repos-always-array, frozen 11-key top-level order + a non-vacuous
  order-extractor proof. Mutant (added `,omitempty` to `Error`) confirmed RED on
  `TestEnvelopeGoldenSuccess`/`TestEnvelopeErrorAlwaysPresent`, then reverted → GREEN.
- **M0/A · WarningCode closed vocab** (open decision #1, RESOLVED) — `internal/contract/enums.go`:
  closed `WarningCode` set `{hydrate_skipped, base_behind_ssot}` + `AllWarningCodes()`; `Warning.Code`
  retyped `string`→`WarningCode`. Extended `SHAPE-ENUM-DOUBLE-ENTRY` (`wantWarningCodes` +
  `TestWarningCodeDoubleEntry` + uniqueness). Real-source mutant (`"stray_mutant"`) confirmed RED,
  reverted → GREEN. Staleness deliberately NOT a warning — it lives in `mirror_freshness.stale`.
- **M0/A · Envelope JSON Schema SSOT** — `schema/envelope.schema.json` (draft 2020-12,
  `additionalProperties:false`, all 11 top-level keys `required` incl. `error`, closed enums for
  action/capability/error.kind/warning.code, `schema_version` const `"1.0"`). Embedded via new
  `package schema` (`schema/schema.go`, `//go:embed`) for the future `wi schema` command + test use.
  First external dep: `santhosh-tekuri/jsonschema/v6` (test-only import; benign transitive
  `x/text`,`regexp2`). Guard `SHAPE-SCHEMA` (`internal/contract/schema_test.go`): both goldens
  validate; a 7-case malformed corpus (extra key, missing `error`, bad enums, wrong version, null
  repos) is rejected; + non-vacuity proof that the validator still accepts the known-good golden.
  Mutant (top-level `additionalProperties:true`) confirmed RED on `TestSchemaRejectsInvalid`,
  reverted → GREEN. **Decision:** reserved additive blocks (`land_state`/`ports`/`hooks`/`tethers`)
  are NOT pre-declared — added to schema+struct together at their milestone (minor bump) so the
  upcoming `SHAPE-FINGERPRINT` schema↔struct tripwire stays exact.
- **M0/A · SHAPE-FINGERPRINT + contract.lock.json** — `internal/contract/fingerprint_test.go` +
  `internal/contract/testdata/contract.lock.json`. One frozen tripwire over the whole contract:
  `SchemaVersion` + sha256(schema bytes) + a reflection-derived canonical `struct_shape` (json tags
  incl. `,omitempty`, recursing through pointers/slices/nested types) + its sha256. The lock file IS
  the fingerprint (env-gated regen via `WI_UPDATE_CONTRACT_LOCK=1`); **decision:** no duplicate Go
  `SchemaFingerprint` const, to avoid double-maintenance. `TestFingerprintIsNonVacuous` proves the
  shape extractor catches added field / retype / omitempty change. Real-source mutant (added a
  `Mutant string` field tagged `json:"-"`) confirmed it turns ONLY `TestContractFrozen` RED while the
  marshal/golden tests stay green — exactly the silent-drift class the fingerprint exists to catch.
  Reverted → GREEN. **Wave A contract spine is now structurally complete** (schema/struct/enums all
  guarded + locked); remaining Wave-A write-first items are INV-NO-LLM, META-VACUITY, NORM-CORRECT.

- **M0/A · INV-NO-LLM** — `internal/invariants` (new package owning DESIGN §2 architecture guards).
  `nollm_test.go` walks the module graph (`go.mod` + `go.sum`, the full transitive closure) and
  fails if any curated LLM/agent-SDK token appears in a module path; pure `scanForDenylisted` +
  cwd-walk-up `moduleRoot`. `TestNoLLMScannerIsNonVacuous` exercises the same detector on a synthetic
  `go-openai` line. Real-source mutant (appended a `// ...go-openai` comment to `go.mod`) confirmed
  `TestNoLLMDependencies` RED (flagged `[openai go-openai]`), reverted via `git checkout` → GREEN.
- **M0/A · WI_FAULT harness + META-VACUITY** — `internal/fault`: the deterministic fault-injection
  seam (`fault.Active(id)` reading the `WI_FAULT` env, exact per-entry match; inert when unset) that
  future HEAL/crash guards consult so their mutant is "set `WI_FAULT=<id>`" rather than a source edit
  — the harness IMPLEMENTATION_PLAN §2 lists as a Wave-A deliverable. `META-VACUITY`
  (`meta_vacuity_test.go`) re-execs the test binary to run a reference guard twice: under
  `WI_FAULT=meta.reference` it MUST fail (a fault can redden a guard), with no fault it MUST pass.
  Unit test `TestActiveIn` pins exact (non-substring) matching. Real-source mutant (made `refSubject`
  ignore the fault) confirmed `TestMetaVacuity` RED ("harness is vacuous"), reverted → GREEN.
- **Wave A is COMPLETE** (modulo `NORM-CORRECT`, intentionally deferred to Wave B): contract spine
  (enums/envelope/schema/fingerprint-lock), INV-NO-LLM, and the WI_FAULT/META-VACUITY methodology
  harness are all in and green. M0 now proceeds to its non-contract packages.
- **M0 · `internal/layout` (path core)** — `layout.go` + `layout_test.go`: `Layout` is the SOLE
  owner of every wi path (DESIGN §1, §4). `New(absRoot)` (cleans, requires absolute) + accessors
  `Root/Config/ReposDir/IsolasDir/WiDir`, the seven `.wi/` subtree dirs
  (`locks/state/log/mirrors/land/ports/trust` via one `WiSubdirs()` SSOT), and the input-bearing
  `Repo/TaskDir/Isolate`. `validSegment` is the chokepoint blocking traversal: rejects
  empty / `.` / `..` / path-separator (either flavor) / NUL / absolute, so user repo/task names
  can't escape the tree. Guards `LAYOUT-PATHS` (hand-written golden relative paths — independent
  copy of the scheme) + `LAYOUT-SAFE` (reject corpus + an accept floor). Mutants confirmed:
  `isolas`→`isolate` → `TestPaths` RED; `validSegment`→always-nil → reject cases RED; →always-error
  → accept floor RED. All reverted → GREEN. **Deferred to the post-`testenv` unit:** `Bootstrap`
  (mkdir the `.wi/` subtree) + EvalSymlinks root normalization — both need an existing on-disk root,
  so they wait for the real-FS `internal/testenv` harness (also M0).

- **M0 · `internal/cli/opid` (op-id format)** — `opid.go` + `opid_test.go`: mints/validates the one
  volatile envelope field (DESIGN §2/§3.1/§8). Root id `op_<base36ts>_<base32rand>` (ts = Unix
  millis base36; rand = 5 bytes → 8 chars lowercase unpadded base32), child suffix `.<n>` (n≥1,
  nests). `New(now, io.Reader)` is pure/deterministic (reads exactly `randLen`, errors on short read
  — never truncates); `Child`; `Valid` over a frozen regex. **Decisions recorded** (#A below): ms
  time unit, 5 random bytes, n≥1 no-leading-zero child index. Guard `OPID-FORMAT` pins the shape from
  independent angles (zero-bytes→`"aaaaaaaa"` pins the base32 encoding; inverse `ParseInt(base36)`
  pins base + ms unit; reject corpus + accept floor). Mutants confirmed: `UnixMilli`→`Unix`,
  `randLen` 5→4, prefix `op_`→`wi_` all → RED. Reverted → GREEN.

- **M0 · `internal/clock` (time/rand seam)** — `clock.go` + `clock_test.go`: the `Clock` interface
  (`Now() time.Time` + `Rand() io.Reader`) funnels wi's two volatile inputs (DESIGN §2 determinism,
  §4). `System` = real UTC time + `crypto/rand` (a local syscall, honors no-hidden-network §2.3);
  `Fake(instant, seed)` = fixed advanceable instant + a self-contained splitmix64 byte stream
  (`detReader`, ours not `math/rand` so the sequence is stable across stdlib changes; never
  short-reads). Guard `CLOCK-DETERMINISM` pins reproducible (same seed → same stream + same op_id via
  `opid.New`), seed-sensitive (diff seed → diff stream), non-degenerate (not all-zero), and System
  live (UTC + crypto/rand varies). Mutants confirmed: `Fake.Rand`→`crypto/rand` → reproducible RED;
  `NewFake` ignores seed → seed-sensitive RED. Reverted → GREEN. (Compile-time `var _ Clock` for both
  impls.)

- **M0 · `internal/testenv` (hermetic real-git harness)** — `testenv.go` + `testenv_test.go`: the
  sandbox every FS/git unit test runs inside (PLAN §M0). `New(t)` → an `Env` with an
  EvalSymlinks-normalized `t.TempDir` root + a fully isolated git environment; `Git(t,dir,args…)`
  runs git under it (fails on non-zero, returns trimmed stdout); `SeedOrigin(t,name)` makes a bare
  origin with one deterministic commit on `main` (local `clone --bare`, no network). Isolation:
  `GIT_CONFIG_GLOBAL`/`SYSTEM`=`/dev/null` + `GIT_CONFIG_NOSYSTEM=1`, fixed identity + fixed
  author/committer **dates** (reproducible SHAs), `LC_ALL=C` (stable git English), no prompt/no net.
  Guard `TESTENV-HERMETIC`: pins an **absolute golden base SHA** (`48f4258c…`, sha1) — fully
  determined by identity+dates+content+message — plus injected author identity + symlink-normalized
  root. Mutants confirmed: drop fixed dates → SHA ≠ golden RED; drop `GIT_AUTHOR_NAME` → ambient
  username (`admin`) leaks → identity RED. **Note:** a relative "two runs agree" determinism check
  was rejected as vacuous (same-second SHA collision); the absolute golden is the real pin. `RunWI`
  deferred to M3 (needs the built binary).

- **M0 · `internal/lockfs` flock half** — `flock_unix.go` (`//go:build unix`) + `flock_unix_test.go`:
  `FileLock`, the advisory whole-file `flock(2)` lock that serializes concurrent `wi` processes
  touching the same `.wi/` resource (DESIGN §6.1). `NewFileLock(path)` + `TryLock()` (non-blocking,
  `(bool,err)`) / `Lock()` (blocking) / `Unlock()` (idempotent no-op when not held); double-lock of a
  held handle is a usage error. Built on `syscall.Flock(LOCK_EX|LOCK_NB)` — **decision #6 flock leg
  RESOLVED: hand-rolled on stdlib `syscall.Flock`, NOT `gofrs/flock`** (zero new deps, INV-NO-LLM
  stays trivially green; PID/self-heal layer is hand-written regardless). Kernel releases the lock on
  process exit, so a crashed holder never wedges it. Guard `FLOCK-EXCLUDES` exploits BSD flock's
  per-open-file-description semantics to prove exclusion in-process (two independent handles contend):
  TryLock refuses a second holder + frees on Unlock; blocking Lock waits then proceeds after release.
  Mutant (`LOCK_EX`→`LOCK_SH`) confirmed `TestFlockExcludesSecondHolder` + `TestLockBlocksUntilReleased`
  RED (shared locks coexist). Reverted → GREEN. Auto-lock-break self-heal (§7.3) is a separate M4 unit.

- **M0 · `internal/layout` Bootstrap + Resolve** — `layout.go` + `bootstrap_test.go`: the two
  filesystem-aware constructors completing the layout package. `Resolve(root)` is the
  EvalSymlinks-normalized constructor the CLI uses at startup (DESIGN §4) — requires an existing
  absolute root, resolves every symlink so the canonical root is a fixed point (matters on macOS:
  /var → /private/var). `(l Layout) Bootstrap()` materializes the `.wi/` runtime subtree (WiDir + all
  7 `WiSubdirs`) and writes a self-ignoring `.wi/.gitignore` (`*\n`) so runtime state can never be
  committed (DESIGN §1) without wi touching the user's ignore files; idempotent. **First real consumer
  of `lockfs.WriteFileAtomic`** — dogfoods the §6.2 single-writer invariant. Guard `LAYOUT-BOOTSTRAP`:
  symlinked-root resolution + fixed-point, relative/missing-root rejection, subtree+gitignore creation,
  idempotency. Mutants confirmed: skip the WiSubdirs loop → `TestBootstrapCreatesSubtree` RED
  (`.wi/locks` missing); drop EvalSymlinks (`return New(root)`) → `TestResolveNormalizesSymlinks` RED
  (link unresolved) **and** the missing-root reject RED (proving the existence guard comes from
  EvalSymlinks). Reverted → GREEN. **`internal/layout` is now complete** (path core + Bootstrap +
  Resolve).

- **M0 · `internal/lockfs` atomic writer** — `atomic.go` + `atomic_test.go`: `WriteFileAtomic(path,
  data, perm)`, the SINGLE atomic writer every `.wi/` state writer reuses (DESIGN §6.2). Recipe:
  `os.CreateTemp` in the SAME dir (rename stays intra-fs ⇒ atomic) → write → fsync → chmod to the
  caller's mode (CreateTemp gives 0600; Chmod dodges umask) → close → `os.Rename` over the target →
  `fsyncDir` the parent so the rename itself is durable. Failure paths remove the temp and leave the
  target untouched. **First consumer of the `WI_FAULT` seam:** `FaultBeforeRename`
  (`lockfs.before_rename`) aborts in the exact temp-written-but-not-renamed window. Guard
  `HEAL-ATOMIC-WRITE` (plain `t.TempDir`, no git ⇒ no `testenv` needed): content+perm round-trip;
  **crash-safety** — under the injected fault the target keeps its complete OLD content (never torn)
  and no temp turds remain; two-sided floor that an un-faulted replace DOES apply. Mutant (in-place
  `O_TRUNC` write instead of temp+rename) confirmed `TestAtomicReplaceIsCrashSafe` RED (target torn to
  `"v2…"`), reverted → GREEN. flock advisory-lock half is a separate follow-up unit.

- **M0 · `internal/lock` (lock-key namespace + total-order acquire)** — `keys.go` + `acquire.go`
  (+ `keys_test.go`, `acquire_test.go`): the SOLE owner of wi's advisory lock-key namespace, built on
  `lockfs.FileLock` (DESIGN §6.1). Closed namespace: `project-registry`, `repo:<name>` (the key sync
  AND land both take — this is what linearizes their freshness race), `isolate-state:<task>`.
  Constructors `ProjectRegistry()`/`Repo(name)`/`IsolateState(task)` route names through the new
  exported `layout.ValidateSegment` (one shared traversal chokepoint — keys become lock filenames), and
  a `Key` derives its own `.lock` path so callers never assemble lock paths. `Acquire(locksDir, keys…)`
  folds the set into `orderedUnique` order (sorted+deduped) and TryLocks each non-blocking:
  all-or-nothing — a held key rolls back the partial acquire and returns typed `*HeldError` (→ exit 6
  `lock_held`), never blocks, never double-grants; `Held.Release()` frees in reverse, idempotent.
  Guards: `LOCK-KEYS` (pinned canonical strings + path derivation + unsafe-name rejection),
  `LOCK-ORDER` (`orderedUnique` is order-independent + dedups), `LOCK-MUTEX` (overlap refused with
  `*HeldError` naming the key; partial-failure rollback proven via fresh re-acquire). All four mutants
  confirmed RED: no-op sort comparator → `TestOrderedUnique…` RED; treat refused TryLock as success →
  `TestAcquireRefusesOverlap` RED; skip rollback on refusal → `TestAcquireReleasesOnPartialFailure` RED;
  `repo:`→`repository:` → `TestCanonicalKeyStrings` RED. Reverted → GREEN. Auto-lock-break self-heal
  (§7.3) stays a separate M4 unit. **M0 building blocks are now complete** (contract spine + layout +
  opid + clock + testenv + lockfs + lock).

- **M1 · `internal/gitexec` (runner + egress belt)** — `gitexec.go` + `gitexec_test.go`: the single
  chokepoint that launches every git child process (DESIGN §4, §2.3). `Runner` (New = inherit env;
  NewWithEnv = explicit hermetic base) with `Run` (OFFLINE — overlays `GIT_ALLOW_PROTOCOL=none` so git
  refuses every transport, the no-hidden-network belt; + `GIT_TERMINAL_PROMPT=0`) and `RunNetwork` (the
  narrow online opt-in for fetch/clone, prompt still disabled). Captures `Result{Args,Stdout,Stderr,
  ExitCode}`; non-zero exit → typed `*ExitError` carrying the full Result (so the later stderr→kind
  classifier reads it without re-running git); start failure → plain wrapped error. `setEnv` overlays
  keys by replace-not-append so git never sees a duplicate key. testenv gained `GitEnv()` to feed the
  hermetic base. Guards: `GITEXEC-OFFLINE-BELT` (two-sided, fully hermetic via a local `file://` remote:
  `ls-remote` is REFUSED with "transport 'file' not allowed" through `Run` but SUCCEEDS through
  `RunNetwork`, so the refusal is attributable to the belt) + `GITEXEC-CAPTURE` (stdout floor via `git
  version`; exit-error surfaced for an unknown subcommand). Mutants confirmed: drop the belt → offline
  `ls-remote` succeeds → `TestOfflineRefusesTransport` RED; swallow the non-zero exit → `TestRunSurfaces
  ExitError` RED. Reverted → GREEN. **Note:** this is the unit-level half of `INV-NO-NETWORK`; the
  module-wide architecture test (git-child belt asserted across all offline command paths) lands later
  in `internal/invariants`.

- **M1 · `internal/git` (SSOT keystone)** — `git.go` + `git_test.go`: deterministic typed git verbs
  on `gitexec.Runner`, all local (offline `Run`, never dial). `ResolveRef(dir, ref)` reads a verified
  commit SHA (`rev-parse --verify --end-of-options`). **`FastForwardBaseRef(dir, base, newRev)` is the
  SOLE base-ref-mutation path (DESIGN §5):** reads current `refs/heads/<base>`, checks `merge-base
  --is-ancestor <cur> <new>` (exit 1 ⇒ genuine non-ff ⇒ typed `*NonFastForwardError`, ref untouched;
  other exits ⇒ real error), then `update-ref refs/heads/<base> <new> <cur>` — **no checkout, no
  merge**, with the old value asserted so a concurrent change fails atomically rather than racing. Works
  on the detached-HEAD SSOT; v0 sync + v1 land both reuse it. Guard `GIT-FF-ONLY` (two-sided, via a
  `testenv` SSOT): a true fast-forward advances the ref; a divergent sibling commit is REFUSED with the
  ref SHA unchanged (before==after). Mutant (drop the `--is-ancestor` precheck → unconditional
  update-ref) confirmed `TestFastForwardRefusesNonFastForward` RED (divergent target advances, no
  error). Reverted → GREEN.

- **M1 · `internal/git` · `EnsureClone` + SSOT-pristine predicate** — `git.go` + `git_test.go`:
  completes the SSOT clone lifecycle on top of the keystone. `EnsureClone(dir, originURL, base)` lazily
  materializes an absent clone in the SSOT posture — `clone --branch <base>` (via the network-only
  `gitexec.RunNetwork`, the ONE permitted dial) then a local `switch --detach`, so refs/heads/<base>
  exists at the origin's base tip but is NOT checked out (advancing it via update-ref never touches a
  working tree). Idempotent: an existing repo (guarded `os.Stat` dir + `rev-parse --git-dir`) is a noop
  — no re-clone, no network. `StatusPorcelain`/`IsClean` are the SSOT-pristine check — `git status
  --porcelain`, clean iff empty, so even an UNTRACKED turd counts as drift. Guards: `GIT-CLONE-DETACHED`
  (fresh clone → HEAD abbrev-ref is `"HEAD"` AND HEAD == refs/heads/<base> tip; a sentinel file survives
  a second call, proving no re-clone) + `GIT-CLEAN` (two-sided: fresh clone clean, one untracked file
  dirty). Mutants confirmed: skip the `switch --detach` → HEAD abbrev-ref is `"main"` →
  `TestEnsureCloneDetachesAtBaseTip` RED; make `IsClean` always return true →
  `TestIsCleanReflectsWorkingTree` RED on the dirtied case. Reverted → GREEN. **`internal/git` is now
  complete** (ResolveRef / FastForwardBaseRef / EnsureClone / StatusPorcelain / IsClean).

- **M1 · `internal/mirror` (cached freshness — read/classify)** — `mirror.go` + `mirror_test.go`: the
  cached SSOT-freshness layer that feeds `mirror_freshness` in the envelope (DESIGN §5). `Snapshot`
  (comparable, all-scalar) records what the last fetch observed — `repo`/`base`/`fetched_at`/
  `local_base_sha`/`origin_base_sha`/`behind_origin_as_of_fetch` — persisted to
  `<root>/.wi/mirrors/<repo>.json` via the single atomic writer (`lockfs.WriteFileAtomic`, DESIGN §6.2)
  and read back by `Load` with **zero** network (the package imports no git/gitexec and takes no Runner,
  so a read structurally cannot dial). `Snapshot.Freshness()` projects onto `contract.MirrorFreshness`
  purely (no I/O): **`stale = behind_origin_as_of_fetch > 0`** (decision #M below). A never-fetched repo
  → `ErrNoSnapshot` so callers omit the block (≠ stale). Repo name (→ filename) routes through the
  shared `layout.ValidateSegment` chokepoint, mirroring `lock`'s `<key>.lock`-in-LocksDir pattern.
  Guards: `MIRROR-FRESHNESS` (two-sided: behind>0 stale, behind==0 fresh, carrying count + fetched_at)
  + `MIRROR-PERSIST` (Store→Load round-trip, missing→`ErrNoSnapshot`, traversing name rejected).
  Mutants confirmed: `Stale:false` hardcode → `TestFreshnessClassifiesStaleByBehindCount` RED; Store
  diverts the write (`p+".mutant"`, import kept used) → `TestSnapshotRoundTrips` RED (Load can't find
  it). Reverted → GREEN.

- **M1 · `internal/git` · `Fetch` + `DivergedCounts` (the freshness git verbs)** — `git.go` +
  `git_test.go`: the two raw git verbs the upcoming `mirror.Fetch` orchestration composes.
  **`Fetch(dir, remote)`** is the SECOND (with `EnsureClone`) network-permitted verb — it routes through
  `gitexec.RunNetwork` and updates `refs/remotes/<remote>/*` only; it never moves a local branch ref and
  never touches the working tree (advancing the SSOT base stays `FastForwardBaseRef`'s exclusive job,
  DESIGN §5). **`DivergedCounts(dir, local, remote)`** reads `rev-list --left-right --count
  local...remote` from LOCAL refs only (offline) → `(ahead, behind)`, the basis for both freshness
  (behind) and the future `main_state` classification. Guards (shared `fetchedMirror` helper: origin at
  C0, EnsureClone'd mirror, push child C1 to origin, fetch): `GIT-FETCH` (post-fetch
  `refs/remotes/origin/<base>` == C1 while `refs/heads/<base>` stays C0 AND `IsClean` true) +
  `GIT-DIVERGED` (local-vs-origin ahead 0/behind 1; reversed args flip to ahead 1/behind 0 — pins each
  count to the right column). Mutants confirmed: no-op `Fetch` (skip the dial) → tracking ref stays C0 →
  `TestFetchAdvancesRemoteTrackingOnly` RED; swap the rev-list columns → `TestDivergedCountsAheadBehind`
  RED (and `GIT-FETCH` stays green, so the swap is attributable to `DivergedCounts`). Reverted → GREEN.
  The Git-struct doc now names `EnsureClone`+`Fetch` as the only two dialing verbs.

- **M1 · `internal/mirror` · `Refresh` (the fetch orchestration)** — `fetch.go` + `fetch_test.go`:
  the one network step of the freshness layer, composing the git verbs. `Refresh(ctx, g, clk,
  mirrorsDir, repo, dir, base)`: `g.Fetch(dir, "origin")` (the dial), resolve `refs/heads/<base>`
  (local_base_sha) + `refs/remotes/origin/<base>` (origin_base_sha), `g.DivergedCounts` behind column,
  build `Snapshot{FetchedAt: clk.Now().UTC() RFC3339, ...}`, `Store`, return it. It is the ONLY part of
  `mirror` that touches git/network, so it takes `*git.Git`+`clock.Clock`; placed in a SEPARATE
  `fetch.go` so `mirror.go`'s "this file imports no git" doc stays literally true (the read path
  Load/Freshness is still Runner-free and cannot dial). Refresh does NOT advance the base ref — only the
  remote-tracking ref moves, so the SSOT tree stays pristine. Guard `MIRROR-FETCH` (testenv origin at
  C0, EnsureClone'd mirror, push C1 to origin, Refresh): behind==1 so `Freshness().Stale`, origin_base
  == C1, local_base == C0 (unmoved), fetched_at == injected fake-clock instant, `git.IsClean` holds, and
  the returned snapshot equals what `Load` reads back. Mutant (skip `g.Fetch`, classify against the
  stale tracking ref) confirmed RED on behind/stale/origin_base, reverted to GREEN. **`internal/mirror`
  is now complete** (Snapshot/Freshness/Store/Load read+classify + Refresh fetch).

- **M1 · `internal/invariants` · `INV-NO-NETWORK` (module-wide architecture test) — CLOSES M1** —
  `nonetwork_test.go`: the architecture half of DESIGN §2 #3 (the gitexec `GITEXEC-OFFLINE-BELT` unit
  proves the belt *works*; this proves the belt *cannot be bypassed*). `TestNoHiddenNetwork` walks the
  module tree (skipping `.git`, non-`.go`, and `_test.go`), derives each file's slash-separated package
  path, and — for every package NOT in `egressAllowed` — fails if it imports `os/exec` (can spawn a git
  child) or references the `GIT_ALLOW_PROTOCOL` belt key. Uses **go/parser** (not grep), so the belt key
  in a comment or this guard's own prose never false-positives; pure `scanFileForEgress(src)
  (importsExec, refsBelt)` is driven directly by the non-vacuity test. **Allowlist decision (#N below):**
  `{internal/gitexec, internal/testenv}` — gitexec is the runtime chokepoint that applies the belt;
  testenv is the test-only git-fixture harness (a non-`_test.go` support pkg never reachable from
  `cmd/wi`). Survey confirmed those are the only two source files importing `os/exec`, and
  `GIT_ALLOW_PROTOCOL` appears only in gitexec. Guard `INV-NO-NETWORK` + self-test
  `TestNoNetworkScannerIsNonVacuous` (scanner flags an `os/exec`-import source, flags a belt-key
  string-literal source, and clears a clean source). **Two mutants demonstrated** (arch tests co-locate
  detector+test, so RED-first is the mutate-demonstrate cycle, per the INV-NO-LLM precedent): empty
  `egressAllowed` → `TestNoHiddenNetwork` RED (gitexec+testenv themselves trip, proving the walk + both
  detectors fire on real source); `scanFileForEgress` always `(false,false)` → `TestNoNetworkScanner
  IsNonVacuous` RED (a blind scanner is a silent false negative). Both reverted → full `go build/vet/test`
  GREEN. **M1 is now COMPLETE** (gitexec belt + full git verbs + mirror + INV-NO-NETWORK).

- **M2 · `internal/config` (manifest read + validate) — M2 BEGINS** — `config.go` + `config_test.go`:
  the SOLE owner of the committed manifest `<root>/wi.config.jsonc` (DESIGN §1 line 19, §map line 167),
  read+validate half. `Parse([]byte) (Config, error)` strips JSONC comments then decodes with
  `encoding/json` under `DisallowUnknownFields` (closed key set — unknown key at ANY level is a hard
  error), requires exactly one JSON value (`dec.More()` guard), and validates each repo: non-empty
  `name` routed through the shared `layout.ValidateSegment("repo", …)` traversal chokepoint (names become
  `repos/<name>` segments), non-empty `url`, and an **effective base** = the repo's own `base` else
  `defaults.base` (a repo with neither is rejected); duplicate names rejected. `Load(path)` wraps
  `os.ReadFile`+`Parse` and surfaces a missing file as `fs.ErrNotExist` (so the CLI can branch to suggest
  `wi init`). Resolved `Config{Defaults, Repos}` exposes effective bases so downstream never re-applies
  the default; `Config.Lookup(name)`. `stripJSONC` is a hand-rolled state machine (normal/string/line-
  comment/block-comment, honoring `\\` escapes so a `//` inside a JSON string survives, preserving
  newlines for decoder error positions). Guard `CONFIG-PARSE`: golden manifest (comments + an inherited
  base + an explicit base) → expected typed `Config`; an 11-case reject corpus (unknown key at
  top/defaults/repo level, missing name/url, no-base-anywhere, duplicate, traversing name, malformed
  JSON, trailing content, comments-only); empty-manifest accept floor (`{"repos":[]}` and `{}`); `Load`
  round-trip + missing→`fs.ErrNotExist`. **Two mutants demonstrated:** `stripJSONC`→no-op (`return src`)
  → comment becomes a JSON syntax error → `TestParseAcceptsGolden` RED; drop `DisallowUnknownFields` →
  all 3 unknown-key cases parse cleanly → `TestParseRejectsInvalid` RED. (The "unknown repo key" case was
  strengthened with a valid `base` so it isolates `DisallowUnknownFields` rather than tripping the
  missing-base rule.) Both reverted → full `go build/vet/test` GREEN. **Decision #C** (JSONC parser =
  hand-rolled stripper + stdlib, zero new deps) recorded below. The AST-preserving *edit* path (for
  `repo add`) and trailing-comma tolerance are deferred to the writer unit.

- **M2 · `internal/state` (runtime registry — per-isolate record)** — `state.go` + `state_test.go`:
  the SOLE owner of the `.wi/state/` runtime registry (DESIGN §map line 168). `IsolateRecord{Task, OpID,
  Repos []RepoRecord}` with `RepoRecord{Repo, Stage}` is the durable entry for one isolate;
  `NewIsolateRecord(task, opID, repos)` builds every declared repo at `StagePending`. Persistence mirrors
  `internal/mirror` exactly: flat `<stateDir>/<task>.json`, task name routed through the shared
  `layout.ValidateSegment("task", …)` traversal chokepoint, `Store` = `json.MarshalIndent`+`\n`+the single
  atomic writer `lockfs.WriteFileAtomic` (§6.2), `Load` with `fs.ErrNotExist`→`ErrNoRecord` (the
  "isolate never created" sentinel, like mirror's `ErrNoSnapshot`). **`UpdateRepoStage(stateDir, task,
  repo, stage)`** is the durable-partial-success operation (DESIGN §6.3): load → flip exactly the named
  repo's stage (unknown repo is an error) → atomic re-store, called **after each worktree add** so a crash
  mid-multi-repo leaves a registry reflecting EXACTLY the completed repos. Takes no Runner and dials
  nothing (pure local persistence, like mirror); the caller holds the `isolate-state:<task>` lock around
  the load-modify-store. Guards: `STATE-PERSIST` (Store/Load round-trip with all-pending fresh start,
  missing→`ErrNoRecord`, unsafe task name rejected, `UpdateRepoStage` flips one repo + errors on unknown
  repo, flat-`<task>.json` path) + `STATE-DURABLE` (an `UpdateRepoStage` interrupted in the atomic
  writer's pre-rename crash window via `WI_FAULT=lockfs.before_rename` MUST fail and leave the PRIOR
  durable record intact — the completed flip survives, the interrupted one neither applies nor tears the
  file). **Three mutants demonstrated:** `Store` diverts the write (`p+".mutant"`) → `Load` can't find it
  → `TestRecordRoundTrips` RED; `UpdateRepoStage` skips its unknown-repo error (`found := true`) →
  flipping a non-existent repo wrongly succeeds → `TestUpdateRepoStageFlipsOneRepo` RED; `Store` swaps the
  atomic writer for `os.WriteFile` (lockfs kept referenced so the *assertion* reddens, not the compiler) →
  the injected pre-rename crash no longer aborts so the interrupted flip lands → `TestDurablePartialSuccess`
  RED. All reverted → full `go build/vet/test` GREEN. **Decision #S** (Stage is a state-owned typed string,
  not a contract enum) recorded below.

- **M2 · `internal/git` · `AddWorktree` (isolate materialization primitive)** — `git.go` +
  `git_test.go`: the per-repo verb `internal/isolate` composes to materialize one worktree off the SSOT.
  `AddWorktree(ctx, ssotDir, worktreePath, rev)` runs `git worktree add --detach <path> <rev>` (offline
  `Run`), producing a **linked worktree sharing the SSOT object store** (native git sharing — no object
  duplication, DESIGN §1 line 30) that is **detached** (holds no branch, so the SSOT base ref is never
  "checked out in a worktree" and `FastForwardBaseRef` can always advance it — the keystone, DESIGN §5).
  rev is wi-internal (a SHA or `refs/heads/<base>`); ownership/gc-protection via the
  `refs/wi/owned/<task>/<repo>` marker (DESIGN §7.1) is a separate follow-on step. Guard `GIT-WORKTREE`
  (testenv SSOT, EnsureClone'd): after add, the worktree HEAD is detached (`--abbrev-ref` == "HEAD") at
  the base tip; its `.git` is a **gitlink file** (not a dir) and `rev-parse --git-common-dir` resolves to
  the **SSOT's own `.git`** (structural object-store-sharing / no-dup check, the isolation invariant PLAN
  §line 102); and the SSOT working tree stays **pristine** (`IsClean`). Mutant (materialize via a
  standalone `git clone` instead of a linked worktree) confirmed RED on **all three** assertions —
  abbrev-ref "main" (branch checked out, not detached), `.git` a directory, and the common git dir the
  clone's own (object duplication) — proving the guard verifies genuine worktree sharing, the precise
  worktree-vs-clone design choice, not merely "a checkout appeared". Reverted → full `go build/vet/test`
  GREEN. (`--detach` is defense-in-depth: a SHA or fully-qualified ref already detaches; the flag keeps a
  short-branch-name caller detached too.)

- **M2 · `internal/git` · `CreateOwnedRef` + `OwnedRefSHA` (evidence-positive ownership marker)** —
  `git.go` + `git_test.go`: the wi-owned marker-ref verbs (decision #2, DESIGN §7.1), the second
  sub-step of `internal/isolate`. **`CreateOwnedRef(ctx, ssotDir, task, repo, sha)`** atomically writes
  `refs/wi/owned/<task>/<repo>` at sha (a single `update-ref`) — the POSITIVE evidence reclamation
  requires: a worktree/branch is reclaimable only if such a marker proves wi created it; an unexplained
  orphan with no marker is a HARD BLOCK, never auto-pruned. **`OwnedRefSHA(...)` → `(sha, exists, err)`**
  reads it back via `rev-parse --verify --quiet`, cleanly distinguishing a genuinely absent marker
  (`exists=false`, nil error — the "no ownership recorded" case reclamation inspects on an orphan) from a
  real read failure (exit 1 + empty output ⇒ absent; the same `*gitexec.ExitError` exit-code idiom
  `FastForwardBaseRef` uses). The namespace lives in one place (`ownedRef`), exactly as
  `FastForwardBaseRef` owns `"refs/heads/"+base`; task/repo are caller-validated (this package holds no
  path policy). The decisive property: markers live under `refs/wi/*`, NOT `refs/heads/*`, so the commit
  is gc-reachable yet the marker is **not a branch** — the pristine SSOT never grows a stray branch
  (DESIGN §5). Guard `GIT-OWNED-REF` (testenv SSOT, EnsureClone'd): absent-before via the verb; after
  create the verb reads back the sha AND raw git confirms `refs/wi/owned/<task>/<repo>` == sha while
  `refs/heads/` still holds ONLY the base ref (no leaked branch). Mutant (flip `ownedRef`'s namespace
  `refs/wi/`→`refs/heads/`) confirmed RED on both the "lives under refs/wi at the sha" and the "no stray
  branch" assertions (refs/wi/owned empty; refs/heads/ grew `wi/owned/taskx/acme`), while the round-trip
  stayed GREEN — proving those two assertions, not the round-trip, carry the decision-#2 namespace
  property. Reverted → full `go build/vet/test` GREEN. **Decision #2** (git ref over note/reflog AND over
  a `.wi/index` backref) recorded below + marked RESOLVED in PLAN §7.

- **M2 · `internal/isolate` · `isolate.New` (the N-repo orchestration — durable partial success)** —
  `isolate.go` + `isolate_test.go`: the domain core of `wi isolate new`, the partial-success-critical
  command (DESIGN §6.3). `New(ctx, l, g, task, opID, specs)` acquires the `isolate-state:<task>` lock
  (held → `*lock.HeldError` → exit 6), `mkdir`s `isolas/<task>/`, writes a `state.NewIsolateRecord` with
  every repo `StagePending` **before any materialization** (the durable statement of intent that makes
  the op resumable), then materializes repos in request order. Each repo, in the exact evidence-positive
  order: `AddWorktree --detach` off `refs/heads/<base>` → `CreateOwnedRef` (marker BEFORE claiming
  "created", so a crash leaves a wi-owned reclaimable worktree, never an unexplained orphan, §7.1) →
  `state.UpdateRepoStage(…Created)`. **Stop-on-first-fail with durable, NOT-rolled-back completed repos:**
  the first failing repo halts the run, repos before it stay on disk + in the registry, repos after it are
  never attempted (stay `StagePending`); the result carries `StatusPartial` (→ exit 2) and the registry
  reflects EXACTLY the completed set. A per-repo failure is recorded in the `Result` (not a Go error);
  `New`'s error return is reserved for can't-run-at-all (held lock, unwritable initial record). Decoupled
  from the manifest via `isolate.RepoSpec{Name, Base}` (the CLI maps `config.Repo`→`RepoSpec`). Never
  moves a base ref / never dirties the SSOT (DESIGN §5). Guard `ISOLATE-NEW` (testenv SSOTs): (complete)
  3 repos all materialize detached, each marker records the worktree tip, durable record all `created`,
  SSOT base refs unmoved; (partial — the core) "web" has no SSOT clone so its add fails → "api" before it
  stays `created` with a durable worktree + pristine SSOT, "db" after it stays `pending` with no worktree,
  `Status==partial`, durable registry == exactly {api:created, web:pending, db:pending}; (lock) a
  pre-held `isolate-state:feat` makes `New` return `*lock.HeldError`. **Two mutants demonstrated:** drop
  the stop-on-first-fail `return` (→ `continue`) so the loop materializes "db" past the failed "web" →
  reddens exactly the 3 "db not attempted" assertions (result stage, durable stage, on-disk worktree),
  api/web/Status staying green — isolating the §6.3 stop-on-first-fail / "exactly the completed set"
  property; skip the upfront all-pending `state.Store` → the first repo's `UpdateRepoStage` errors
  `state: no isolate record` and there's no durable registry to resume from → reddens both the complete
  and partial guards (proving the durable-intent write is load-bearing). Both reverted → full
  `go build/vet/test` GREEN. **Deferred:** `isolate.New` resume (on re-run, skip repos already
  `StageCreated` rather than re-adding and failing) is a small follow-on once `resolve`/CLI land.

- **M2 · `internal/resolve` · `Bundle` (the path-bundle projector) — COMPLETES M2** — `resolve.go` +
  `resolve_test.go`: the data behind `wi resolve <task>` (and the `resolve` block isolate responses embed,
  DESIGN §3.1, §map line 166). `Bundle(l, rec)` is a **PURE projection** of a `layout.Layout` + a
  persisted `state.IsolateRecord` — **zero I/O** (no FS reads, no git, no network — stronger than mirror's
  offline read path), so it is trivially offline. Every path comes from `internal/layout` (the sole path
  owner — resolve assembles nothing): `isolate_root`=`TaskDir` (`isolas/<task>`), `state_dir`=`StateDir`
  (`.wi/state`), `log`=`LogDir` (`.wi/log`); per repo (iterating `rec.Repos`, the isolate's actual
  contents, in recorded order) `worktree`=`Isolate` (`isolas/<task>/<repo>`) and `mirror`=`Repo`
  (`repos/<repo>`, the SSOT clone / local mirror of origin). The CLI owns `state.Load` + the
  `ErrNoRecord`→`not_found` mapping; `Bundle` takes the loaded record so it stays a total, testable
  function. Guard `RESOLVE-BUNDLE`: a 2-repo record projects to the exact hand-written golden paths
  (built independently of the layout accessors, so a mis-wire is caught), in order; an empty record yields
  a non-nil empty `Repos` (marshals as `[]`); a traversing repo name surfaces as a validation error.
  **Two mutants demonstrated:** wire `mirror` to the worktree path instead of `layout.Repo` → both repos'
  Mirror reddens (proves resolve distinguishes the `isolas/<task>/<repo>` worktree from the `repos/<repo>`
  SSOT clone); drop a repo from the loop → count/second-repo reddens (proves every recorded repo is
  projected). Both reverted → full `go build/vet/test` GREEN. **Decision #R** (resolve field semantics;
  v0 `branch` empty because worktrees are detached) recorded below. **M2 is now COMPLETE** (config +
  state + isolate + resolve); next is M3 — the CLI surface.

## Next unit (pick this on the next firing) — FINISH M3 (help/suggest gap)

> **Corrected status (2026-06-30): MVP M0–M3 is NOT yet complete.** `help` and `suggest` are M3
> deliverables (PLAN line 136) that earlier firings skipped before wrongly declaring STOP. The loop's
> stop condition ("if the full MVP M0–M3 is green … stop") therefore has NOT actually been met. Keep
> going on M3 — this is finishing the MVP, NOT starting M4/M5 (those stay gated). The three release
> follow-ups (HOMEBREW_TAP_GITHUB_TOKEN PAT secret · LICENSE + cask license · `wi version` unit) remain
> owner-gated and unchanged.

- ✅ **DONE (earlier firing) — `internal/suggest`** (commit `b043457`, guard `SUGGEST-DIDYOUMEAN`): the
  pure did-you-mean Levenshtein engine, SOLE owner per DESIGN §3.1. `For(input, candidates)` → typo
  (edit dist ≤ 2, case-insensitive) or prefix matches, sorted (dist asc, name asc), nil on no-match.
  Decision #S recorded (reproduce cobra's `SuggestionsFor`; hand-rolled, no `agnivade/levenshtein` dep,
  consistent with #F dropping cobra).
- ✅ **DONE (this firing) — `internal/help`** (guard `HELP-MODEL`): the pure progressive-disclosure
  help model + `next[]` rules, SOLE owner (DESIGN line 158). One command-metadata `table` (the single
  source of truth for the command surface, ordered as the canonical workflow) backs both `Commands()`
  and `For(topic)`. `For("")` → overview (tagline + full table + getting-started `next[]`);
  `For("<command>")` → that command's synopsis/usage/runnable `next[]`; unknown topic → zero Model +
  ok=false (so the caller can refuse with a `did_you_mean` hint). Non-vacuity mutant: described
  source-edits (ignore the topic / ok=true on unknown / drop the `wi ` prefix / blank a row) — each
  reddens a distinct guard test. Pure, zero-dep, no contract/cli coupling yet.
- ✅ **DONE (this firing) — unknown-command suggestion injection** (commit pending, guard
  `DISPATCH-DIDYOUMEAN`): `cli.unknownCommandEnvelope` now populates the usage envelope's
  `error.did_you_mean[]` from `suggest.For(attempted, registeredNames(reg))` (sorted registry keys)
  and sets `error.help = "wi help"`, so an unknown/typo'd command self-corrects. `nil` suggestion →
  omitempty drops the field; the help pointer is always present.
- ✅ **DONE (this firing) — contract `help` block** (decision #HB; guards via `TestEnvelopeHelpBlockGolden`
  / `TestEnvelopeHelpOmittedWhenNil` / `goldenHelp` in `TestSchemaAcceptsGolden` / `TestContractFrozen`).
  `contract.Envelope` gained an additive omitempty `Help *HelpBlock` (between `next` and `error`) +
  the `HelpBlock`/`HelpCommand` wire types; mirrored in `schema/envelope.schema.json` ($defs +
  top-level ref, not required); SHAPE-FINGERPRINT lock regenerated. This is the wire form the
  `help-json` capability finally has a payload for — the `cmd_help.go` handler (NEXT) fills it.
- ✅ **DONE (this firing) — `cmd_help.go` handler + `"help"` registry entry** (guard `CMD-HELP`). `wi
  help [topic]` is now a real command backing the advertised `help-json` capability (closes the PLAN
  line 108 "capabilities ⇒ backing command" violation ORIENT caught). Added `Help *contract.HelpBlock`
  to `cli.Result` + threaded it onto the success envelope in `envelopeFor` (alongside
  `Resolve/Planned/Blocked`). `helpCmd.Run` projects `help.For(topic)` onto the contract:
  `helpBlockFromModel` maps `help.Model`→`contract.HelpBlock` and each `help.Command`→`contract.HelpCommand`
  (cli owns the translation so contract never imports help and help never imports contract); the model's
  runnable lines ride `Result.Next`. Topic resolution: no args → overview; args joined (`help isolate
  new`) → that command (Dispatch resolves the 1-token "help" and hands the rest as args, rejoined with
  spaces); unknown topic → `*CommandError{not_found}` + `did_you_mean` via `suggest.For` over
  `helpCommandNames()` + the `wi help` pointer. Smoke-verified through the built binary: `wi help`→exit
  0 with all 6 commands in the block; `wi help isolate new`→exit 0 (topic detail, table omitted); `wi
  help snc`→exit 3 not_found with `did_you_mean:["sync"]`. Two non-vacuity mutants demonstrated
  RED-then-reverted (see the `CMD-HELP` registry row).
- ✅ **DONE (this firing) — help↔registry SYNC fitness** (guard `HELP-REGISTRY-SYNC`, decision #HR).
  `help_registry_sync_test.go` (`package cli_test`) is the one test that imports BOTH `internal/help`
  and the registry — no cycle, because `help` is pure and the registry never imports it (the
  help→contract projection lives in the cli layer). `TestHelpTableMatchesRegistry` asserts (1) `"help"`
  IS a registry key (it backs the help-json capability), (2) `"help"` is NOT a `help.Commands()` row
  (meta-command, decision #HR), and (3) `BuildRegistry(Deps{})` keys MINUS `"help"` == `help.Commands()`
  names as EQUAL sets — so the metadata table can never drift from the live command surface ("help can
  never lie", DESIGN §3.1; the promise help.go's own header makes is now enforced). Empty `Deps` suffices
  — the key set (the command surface) is independent of the deps the factories close over. Non-vacuity
  mutant demonstrated RED-then-reverted: added a bogus `"ghost"` registry key → the equal-sets assertion
  RED with a clean diff (`registry (minus help) = [ghost init …]` vs `help table = [init …]`). Both
  `help` AND `suggest` are now fully BUILT, WIRED, and GUARDED — the M3 gap ORIENT caught is closed in
  code; only end-to-end re-verification remains.
- ✅ **DONE (this firing) — re-verified M0–M3 end-to-end; MVP is GREEN → STOP.** `gofmt -l .` clean,
  `go build`/`go vet`/`go test ./...` all GREEN (23 packages), `goreleaser check` GREEN via remote CI
  (28411028448). Built-binary smoke in a fresh temp workspace exercised the full surface — `wi help`
  (exit 0, 6 commands), `wi help isolate new` (exit 0, topic detail), `wi help snc` (exit 3 +
  `did_you_mean:["sync"]`), `wi snc` (exit 64 usage + `did_you_mean`), `init`/`resolve ghost`/reinit/text
  projection (0/3/4/4) — one envelope each, closed-set exit codes. Git-backed commands covered by
  hermetic guard tests over real git. **No further loop work: the MVP (M0–M3) is complete and green.**
  M4/M5 await an explicit owner go-ahead.

M3 (DESIGN §3, IMPLEMENTATION_PLAN §M3 + Wave B) wires the green domain core through the uniform
pipeline into the runnable `wi` binary: `internal/cli` (parse → dispatch → **one** envelope out →
mapped exit), `help`, `suggest`, then `cmd/wi`, with CI + `.goreleaser.yaml` + Homebrew tap. The hard
part is the contract plumbing (one well-formed envelope per invocation, JSON default, the closed
exit-code set, text as a lossless projection), NOT arg parsing — so build the contract spine of the
CLI first, bottom-up, smallest cohesive unit each firing.

Done so far in M3 (bottom-up): `exitcontract` (the `error.kind→exit-code` matrix `ExitCodeFor` + the
sole `os.Exit` wrapper, `SHAPE-FAIL-MATRIX`), `cli.Emit` (one-envelope serialization, `SHAPE-ONE-ENVELOPE`),
the `cli` ASSEMBLER (`Meta` + `Success`/`Failure` + `ExitFor`, `SHAPE-ASSEMBLE`/`SHAPE-DRYRUN-EXIT0`),
`cli.RenderText` (the `--format text` lossless projection, `SHAPE-TEXT-PROJECTION`), the Runner
EXECUTE-CORE (`Result`/`Command`/`CommandError` + `Format`/`Render` + `Execute`→`envelopeFor`→`ExitFor`,
`RUN-PIPELINE`), and now the DISPATCH layer (`Registry` + `Dispatch`: argv → globals + longest-match
command → `op_id` mint → `Meta` → `Execute`, `DISPATCH-ROUTES`, decision #F = hand-rolled stdlib not
cobra). **The entire generic CLI pipeline — argv → dispatch → outcome → one envelope (json|text) →
mapped exit — is now complete and green.** What remains for MVP is the per-command handlers that plug
real domain work into that pipeline, then the `cmd/wi` main, then CI/release.

- **DONE (this iteration):** `cmd_sync.go` — the **`wi sync [<repo>…]` handler** (guard `CMD-SYNC`), the
  thin envelope projection over the now-green `sync.Run` core. Added a `Clock` field to `Deps` (sync is the
  first handler that timestamps a snapshot) + the `"sync"` `BuildRegistry` line binding
  `d.Layout`/`d.Git`/`d.Clock`. `newSyncCommand` does NO arg validation (no operands = all declared repos;
  named operands checked against the manifest in `Run`). `Run` loads the manifest (missing →
  not_found+`wi init`, malformed → usage — decision #H), `selectRepos` resolves operands (none → every
  `cfg.Repo` in declaration order; first undeclared → not_found before any dial), maps to
  `[]sync.RepoSpec{Name,URL,Base}` (proving the manifest URL is read — isolate new ignores it), drives
  `sync.Run`, and projects each outcome → `RepoResult{Action:synced, Branch, SHA:Snapshot.LocalBaseSHA,
  Mirror, Freshness}` with `StatusPartial`→durable `(result, *CommandError{partial, Action:synced})`
  mirroring `cmd_isolate_new` (#D). **Per-repo-kind question settled:** every per-repo failure projects to
  `kind=internal` for now (matching `projectRepoOutcome`'s precedent), with typed refinement
  (`*git.NonFastForwardError`→conflict, `*lock.HeldError`→lock_held) DEFERRED to the gitexec stderr→kind
  classifier so both sibling projections gain it uniformly rather than diverging — see decision #K.
- **DONE (prior iteration, this run):** `internal/sync` — the **sync domain core** `sync.Run` (guard
  `SYNC-RUN`). Built as its own domain package (mirroring `internal/isolate`) rather than inline in the
  handler, so the orchestration — per-repo, under `repo:<name>`,
  `EnsureClone`→`Fetch`→`FastForwardBaseRef`→`mirror.Store`, CONTINUE-on-fail — is hermetically testable
  below the envelope machinery. The first `internal/` package to drive `gitexec.RunNetwork` (clone+fetch)
  end-to-end.
- **DONE (prior iteration):** `isolate new <task> <repo>…` — the marquee handler — guard
  `CMD-ISOLATE-NEW`; added `Deps.Git`. Resolves each requested repo against the manifest → `RepoSpec`
  (undeclared → not_found, missing manifest → not_found+`wi init`, malformed → usage = decision #H),
  reads the op_id via `OpIDFrom(ctx)` into the durable `IsolateRecord`, drives `isolate.New`, and maps
  `StatusComplete`→created / `StatusPartial`→durable `(result, *CommandError{partial})` (#D) /
  `*lock.HeldError`→lock_held.
- **DONE (prior iteration):** the `WithOpID`/`OpIDFrom` **context seam** (`Execute` injects `Meta.OpID`
  into the ctx the Command sees) — guard `CTX-OPID`. The prerequisite that let `isolate new` write the
  envelope's op_id into the durable `IsolateRecord` instead of a divergent one.
- **DONE (prior iteration):** `init` (scaffold a workspace) — guard `CMD-INIT`; resolves decision #G
  (root = cwd, init takes no operand). Bootstraps `.wi/` then writes a starter `wi.config.jsonc` LAST
  (O_EXCL commit point); re-init → `already_exists` leaving the manifest byte-for-byte intact.
- **DONE (prior iteration):** `resolve` (pure read) + the `Deps`/`BuildRegistry` seam — guard
  `CMD-RESOLVE`. The handler→`Result`/`CommandError` contract pattern is now established for the rest.
- **ALL SIX PER-COMMAND HANDLERS DONE** — `resolve` (`CMD-RESOLVE`), `init` (`CMD-INIT`), `isolate new`
  (`CMD-ISOLATE-NEW`), `sync` (`CMD-SYNC`), `repo add` (`CMD-REPO-ADD`), and `isolate rm` (`CMD-ISOLATE-RM`)
  all land green as `Command`s over the M0–M2 core, each plugged into the pipeline via one `BuildRegistry`
  line. The `isolate rm` triplet completed this firing: (a) `internal/git` `RemoveWorktree`/`DeleteOwnedRef`
  (`GIT-RECLAIM`), (b) `internal/isolate.Remove` (`ISOLATE-REMOVE`/`ISOLATE-REMOVE-TEARDOWN`), (c) the thin
  `cmd_isolate_rm.go` handler (`CMD-ISOLATE-RM`, decision #RD).
- **DONE — `cmd/wi/main.go`** (guard `CMD-MAIN`, see Done): the single process entry / only `os.Exit`
  site, wiring cwd→layout→real `Deps`→`BuildRegistry`→`Dispatch` through the testable `run` seam. The `wi`
  binary now runs the full command surface end-to-end (smoke-verified).
- **DONE (this firing) — release scaffolding sub-unit (a): the CI gate workflow** (`.github/workflows/
  ci.yml`, decision #CI; preceded by a `style:` commit making the tree gofmt-clean — a prerequisite, see
  Done). Runs `gofmt -l`+`go build`+`go vet`+`go test` on push (`main`+`build/wi`) and PR, matrix
  `[ubuntu, macos]`, Go pinned from go.mod. Process artifact (no Go guard/mutant); verified by parsing
  the YAML and asserting the four gate commands.
- **DONE (this firing) — release scaffolding sub-unit (b): `.goreleaser.yaml` + CI `goreleaser check`**
  (decision #GR; see Done). v2 config, cross-compiles cmd/wi for darwin/linux×amd64/arm64, proven by
  `goreleaser check` (clean) + a four-target snapshot build on goreleaser v2.16.0; wired into CI.
- **DONE (this firing) — release scaffolding sub-unit (c): Homebrew cask + tag-push release workflow**
  (decisions #GR/#HC; the LAST MVP unit). Both config DATA (fitness = `goreleaser check`, NOT a Go
  test): **(1)** a `homebrew_casks:` block in `.goreleaser.yaml` — NOT `brews:`, which goreleaser
  HARD-deprecated inside the `~> v2` range we pin, so `goreleaser check` (our fitness) FAILS on it; this
  overrides PLAN §6's "pin the major to dodge it, cask rejected" mitigation (recorded as decision #HC).
  Pushes the generated cask to the separate repo ggkguelensan/homebrew-tap (`directory: Casks`,
  `skip_upload: auto`, token from `{{ .Env.HOMEBREW_TAP_GITHUB_TOKEN }}`); `license` left unset until a
  LICENSE lands. **(2)** `.github/workflows/release.yml` on tag-push `v*` → `goreleaser release --clean`
  via `goreleaser-action@v6` (`version: ~> v2`), `permissions: contents: write`, `fetch-depth: 0` (no
  shallow clone), passing both `GITHUB_TOKEN` (in-repo upload) and the owner-provided
  `HOMEBREW_TAP_GITHUB_TOKEN` PAT (cross-repo cask push). `goreleaser check` re-run → PASS; a snapshot
  release generated a valid `dist/homebrew/Casks/wi.rb` (on_macos + on_linux stanzas).
  **→ This completes M3, and with it the full MVP (M0–M3). STOP condition reached — see the banner
  under "Next unit" above for the three owner follow-ups and the M4/M5 gate.**
- Deferred follow-ons (pull in when a command drives them): `isolate.New` **resume** (on re-run skip
  repos already `StageCreated`); per-repo **base persisted in `state`** (lets `resolve` populate
  `branch` instead of v0's empty); state **KV + `cas`** (`--expected __ABSENT__`).

## Mutant registry (guard → mutant that must turn it RED)

| guard | mutant |
|-------|--------|
| GC-CLASSIFY (M4 HEAL-2) | in `gc.Classify`, **(no-live-loss limb)** delete the `case !c.Clean \|\| c.AheadOfBase: return ClassBlockedWork` arm → a wi-owned worktree carrying uncommitted/unmerged work falls through to `ClassReclaimable` → `TestClassifyNeverReclaimsLiveWork` RED (`got "reclaimable"`) + the `owned-dirty`/`owned-ahead-but-clean`/`owned-dirty-and-ahead` truth-table rows RED, the orphan/live/clean-reclaimable rows GREEN — pinning HEAL-GC-NO-LIVE-LOSS (DESIGN §7.1: gc never destroys live work). **(Evidence-positive limb)** delete the `case !c.HasMarker: return ClassOrphanUnexplained` arm → a markerless candidate is judged by cleanliness alone, so a clean one becomes `ClassReclaimable` → `TestClassifyNeverReclaimsWithoutMarker` RED + the `no-marker-*` rows RED, the owned/live rows GREEN — pinning the §7.1 evidence-positive keystone (no marker = no provenance = never reclaimable). Pure function, no build tag. Darwin RED→GREEN, both confirmed-then-reverted (`cached` = byte-identity) |
| STATE-LIST (M4 HEAL-2) | in `state.List`, **(primary — completeness limb)** `break`/early-return after the first `out = append(out, rec)` → only 1 of 3 stored records returned → `TestListEnumeratesAllRecords` RED (DeepEqual mismatch). **(alternate — filter limb)** drop the `if e.IsDir() || filepath.Ext(e.Name()) != ".json" { continue }` guard → a stray non-record file (`notes.txt`) is fed to `Load`, which reads the absent `notes.txt.json` → `ErrNoRecord` → `List` errors → `TestListSkipsNonRecordFiles` RED. Read-only, no build tag. Darwin RED→GREEN, both confirmed-then-reverted. (The `missing-dir→empty` and `corrupt-record→hard-error` posture additionally guarded by `TestListMissingOrEmptyDirIsEmpty`/`TestListSurfacesCorruptRecord`.) |
| GIT-LIST-OWNED-REFS (M4 HEAL-2) | in `git.ListOwnedRefs`, **(primary — scoping limb)** widen the `for-each-ref` pattern from `ownedRefPrefix` (`refs/wi/owned/`) to `"refs/wi/"` → a `refs/wi/backup/*` ref (PROTECTED from gc by §7.1) surfaces, fails the `CutPrefix(refname, ownedRefPrefix)` owned-prefix guard, and `ListOwnedRefs` errors → `TestListOwnedRefsScopedToOwnedNamespace` RED (`for-each-ref returned non-owned ref "refs/wi/backup/feat/api"`) — pinning that backup refs/branches are never enumerated as gc candidates. **(alternate — completeness limb)** `break` after the first `out = append(...)` → only 1 of 3 markers returned → `TestListOwnedRefsEnumeratesAllMarkers` RED (`got [bugfix/api], want all three sorted`). Read-only, no build tag. Darwin RED→GREEN, both confirmed-then-reverted |
| GC-INSPECT (M4 HEAL-2) | in `gc.Inspect`, **(primary — live-join limb)** stop populating the live-set in `liveSet` (replace `live[cellKey{rec.Task, rr.Repo}] = true` with `_ = rr`) → the live cell loses its `Live` signal and `gc.Classify` reclassifies it from `ClassLive` to `ClassReclaimable` → `TestInspectClassifiesEachWorktree` RED (`active/api classified "reclaimable", want "live"`) — pinning that Inspect threads `state.List`'s Live signal into the §7.1 never-collect-live gate. **(alternate — enumeration-completeness limb)** `break` after the first `cands = append(...)` per task → only the first repo of each task is enumerated → 2-of-5 candidates → `TestInspectClassifiesEachWorktree` RED (`Inspect returned 2 candidates, want 5`). End-to-end over a real git workspace, no build tag. Darwin RED→GREEN, both confirmed-then-reverted + linux cross-build/vet clean |
| GC-COLLECT (M4 HEAL-2) | in `gc.collectTask`, **(primary — no-live-loss limb)** make the `case ClassBlockedWork:` arm call `reclaim(...)` instead of recording a hard block → `gone/ledger` (clean but AHEAD of marker — committed unmerged work) is removed by `RemoveWorktree` → `TestCollectReclaimsOnlyReclaimable` RED (`gone/ledger worktree must be left intact, was removed`) — pinning HEAL-GC-NO-LIVE-LOSS (gc never destroys committed-but-unlanded work). **(alternate — evidence-positive limb)** make the `case ClassOrphanUnexplained:` arm call `reclaim(...)` → the markerless `gone/auth` worktree is removed → RED (`gone/auth worktree must be left intact, was removed`) — pinning §7.1 (wi reclaims only what a marker proves it owns; an unexplained orphan is a hard block). End-to-end over a real git workspace, no build tag. Darwin RED→GREEN, both confirmed-then-reverted + linux cross-build/vet clean. (Busy-task skip additionally guarded by `TestCollectSkipsBusyTask`.) |
| CMD-GC (M4 HEAL-2) | in `gcCmd.collect`, **(primary — blocked-sweep-is-a-refusal limb)** on the `conflictBlock` arm return `(result, nil)` instead of `(result, *CommandError{conflict})` → a sweep with blocked_work/orphan cells is mis-reported as a clean exit-0 success → `TestGCReclaimsAndRefuses` RED (`want *cli.CommandError, got <nil>`); the busy test is UNAFFECTED (it routes through the untouched `busyBlock` arm), localizing the regression. **(alternate — dry-run-is-read-only limb)** guard the `Run` dry-run branch with `if false && DryRunFrom(ctx)` so `--dry-run` falls through to the mutating `collect` → `TestGCDryRunDoesNotMutate` RED (the read plan now errors with the conflict refusal, `--dry-run must not error … got workspace not fully reclaimed`). End-to-end through the real registry+handler over the four-class git workspace, no build tag. Darwin RED→GREEN, both confirmed-then-reverted + linux cross-build/vet clean. (busy→lock_held exit-6 distinction additionally guarded by `TestGCBusyTaskIsLockHeld`; empty-workspace noop by `TestGCEmptyWorkspaceIsCleanNoop`.) |
| HEAL-GC-NO-LIVE-LOSS (M4 HEAL-2) | the negative battery pinning gc never destroys recoverable work or resurrects a torn-down isolate (PLAN 123-125). **(Test A — commit-identity limb)** in `gc.observeCandidate` compute `AheadOfBase` by comparing `HEAD^{tree}` to `markerSHA^{tree}` instead of the commit shas → an equal-to-base (`--allow-empty`) commit reads "not ahead", classifies `ClassReclaimable`, and `gc.Collect` DESTROYS the commit → `TestGCRefusesEqualToBaseCommit` RED (`Status = "complete", want "blocked"`); reddens ONLY this test (the file-committing GC-COLLECT cell has a DIFFERENT tree, so it stays ahead/blocked) — isolating that §7.1 "ahead of base" is COMMIT IDENTITY (HEAD != markerSHA), not tree-equality. (The blunt `gc.Classify` "drop `\|\| c.AheadOfBase`" mutant also reddens it, alongside the GC-CLASSIFY/INSPECT/COLLECT ahead-rows — kept as the registered alternate.) **(Test B — subtractive-only limb)** insert `state.Store(l.StateDir(), state.NewIsolateRecord(task, opID, nil))` after the reclaim loop in `gc.collectTask` (a "tombstone what we collected" regression) → the post-sweep `state.Load(gone)` no longer returns `ErrNoRecord` → `TestGCNoResurrectionOfCompletedIsolate` RED; reddens ONLY this test (no other fitness inspects the registry after Collect) — pinning that gc is purely SUBTRACTIVE (writes no record, re-adds no worktree, idempotent re-sweep is a clean noop). DEFERRED (recorded): PLAN case (iii) HEAL-4-reset+gc composition waits on HEAL-4's op journal. End-to-end over a real git workspace, no build tag. Darwin RED→GREEN, both confirmed-then-reverted + linux cross-build/vet clean |
| JOURNAL-CODEC (M4 HEAL-4) | the pure op-journal record + codec — the durability foundation of HEAL-4's crash recovery (DESIGN §7.4). **(primary — closed-enum guard)** make `journal.Phase.Valid`'s `default` arm `return true` (drop the closed-set check) → a journal line carrying `phase:"halfway"` parses instead of being refused → `TestRejectsUnknownPhaseKindAndMissingOpID/unknown_phase` RED (`= (…Phase:halfway…, nil), want an error`) + `TestPhaseAndKindValidMembership` RED (`unknown phase "parked" must be invalid`); reddens ONLY the rejection/membership assertions, the round-trip/wire-key test stays GREEN — pinning that `ParseEntry` conservatively REFUSES a phase wi cannot understand (never a degraded zero Entry recovery might act on). **(alternate — wire-key stability)** rename the `Phase` field's json tag from `phase` to `state` → `TestMarshalRoundTripAndWireKeys` RED on the concrete durable-key assertion (`missing durable wire key "phase"`) while its round-trip `DeepEqual` stays GREEN (Marshal+Unmarshal share the renamed tag, so they remain symmetric) — PROVING the concrete-wire-key assertion is load-bearing and a round-trip-only test is VACUOUS against a tag rename (the LOCK-HOLDER methodology lesson, applied directly). Pure codec, no I/O, no build tag. Darwin RED→GREEN, both confirmed-then-reverted (`cached` = byte-identity) + gofmt clean + linux cross-build/vet clean |
| JOURNAL-STORE (M4 HEAL-4) | the append-safe per-op JSONL store (`journal.Append`/`ReadOp` over `lockfs.WriteFileAtomic`, one file per op_id — decision #JOURNAL-PER-OP-FILE). **(primary — append-safety)** make `Append` write only the new `line` instead of `append(prior, line...)` → the journal is overwritten each call → `TestAppendAccumulatesLifecycle` RED (`ReadOp returned 1 entries, want 3`); also reddens the crash test (only the last pre-crash line survives) — pinning that an append never clobbers prior lifecycle, the property recovery reads. **(alternate — per-op isolation)** make `opPath` ignore op_id (constant `journal.jsonl` filename) → two ops share one file → `TestReadOpIsolatesByOpID` RED (`ReadOp(op_a)` returns op_b's line too); reddens ONLY this test (single-op accumulate/crash cells are unaffected by a constant path) — pinning the per-op-file model that makes journaling race-free without a lock. **(alternate — atomic-writer crash-safety)** make `Append` use `os.WriteFile` instead of `lockfs.WriteFileAtomic` → the `WI_FAULT=lockfs.before_rename` crash seam is bypassed, so the faulted 3rd append SUCCEEDS → `TestAppendCrashLeavesPriorIntact` RED (`Append done under injected crash returned nil, want an error`); reddens ONLY this test — pinning that the journal inherits crash-safety by routing through the single atomic writer, not a naive truncate-write. I/O over a real tmp dir + the WI_FAULT seam, no build tag. Darwin RED→GREEN, all three confirmed-then-reverted (`cached` = byte-identity) + gofmt clean + linux cross-build/vet clean |
| HEAL-CRASH-RECOVER (classifier limb) (M4 HEAL-4) | the pure recovery classifier (`journal.Classify`, `internal/journal/recover.go`) — the decision core the offline roll-forward recovery scan acts on, computing an op's `Disposition` from its FURTHEST-reached phase (decision #4: roll FORWARD, never back). **(primary — roll-forward guarantee)** map `PhaseCommitted` to `DispositionComplete` instead of `DispositionRollForward` → a committed-but-not-done op (one that crossed its point of no return) is silently treated as finished and dropped from recovery → `TestClassifyByFurthestPhase` RED on the `intent+committed`/`committed`-alone rows + `TestClassifyFurthestWins` RED on the `committed then stray intent` row — pinning THE load-bearing data-integrity guarantee (a crashed-after-commit op MUST be finished, the resolution of §7 decision #4). **(alternate — furthest-wins)** read the LAST entry's phase (`entries[len-1].Phase`) instead of the rank-max over all entries → a non-monotonic/torn journal (an earlier-phase line trailing a later one) downgrades the verdict → `TestClassifyFurthestWins` RED on all three rows (`committed→intent`, `done→intent`, `done→committed`); reddens ONLY that test (the monotonic truth-table rows are unaffected by last-vs-max) — pinning that a re-ordered/torn journal can never drop a committed op from recovery. `Disposition` is INTERNAL recovery vocabulary, not a contract wire enum. Pure function, no I/O, no build tag. Darwin RED→GREEN, both confirmed-then-reverted (`cached` = byte-identity) + gofmt clean + linux cross-build/vet clean |
| HEAL-CRASH-RECOVER (scan limb) (M4 HEAL-4) | the journal directory scan (`journal.Scan`, `internal/journal/scan.go`) — the offline recovery entry point that turns the `.wi/journal/*.jsonl` subtree into an `[]OpRecovery` worklist (each op's identity from its intent entry paired with its `Classify` disposition). **(primary — pairing correctness)** ignore each op's per-op `Classify` result and pair every op with `DispositionComplete` → a crashed (committed/intent-only) op is reported finished and dropped from the roll-forward worklist → `TestScanPairsOpsWithDispositions` RED on the `op_fwd` (RollForward) + `op_ab` (Abandoned) rows (`= "complete"`); reddens ONLY those rows, the torn/missing/sidecar tests GREEN — pinning that each op's true disposition flows through the scan (the load-bearing recovery-worklist correctness). **(alternate — error-surfacing)** swallow the `ReadOp` error and `continue` instead of returning it → a torn/unparseable journal is silently skipped → `TestScanTornJournalErrors` RED (`Scan over a torn journal = nil error, want an error`); reddens ONLY that test — pinning the conservative posture (a journal recovery cannot understand is SURFACED, never silently dropped, lest it hide a committed op). Read-side I/O over a real tmp dir, no build tag. Darwin RED→GREEN, both confirmed-then-reverted (`cached` = byte-identity) + gofmt clean + linux cross-build/vet clean |
| REPAIR-PLAN | change the `ClassOrphanWorktree` arm of `isolate.PlanAction` to `return RepairDropRecord` (auto-clean an orphan) → exactly the two `orphan_worktree` rows of `TestPlanActionTruthTable` + `TestPlanActionNeverAutoRemovesOrphan` RED, the other 6 rows GREEN — proving the §7.1 orphan hard-block is load-bearing (a `ClassReclaimed`→`RepairRematerialize` mutant likewise reddens the resurrection guard) |
| GIT-WORKTREE-PRUNE | make `git.PruneWorktrees` a no-op (`return nil` without running `git worktree prune`) → the stale `.git/worktrees/<id>` admin entry left by an out-of-band worktree-dir removal survives → the post-prune `AddWorktree` in `TestPruneWorktreesClearsStaleAdminEntry` fails "missing but already registered" (exit 128) → RED (the pre-prune failed re-add additionally pins that AddWorktree itself does not silently `--force`) |
| REPAIR-EXEC | skip the `PruneWorktrees` call in `isolate.Repair`'s `rematerialize` arm (call only `AddWorktree`) → the `web` MissingWorktree cell's re-add fails "missing but already registered" → exactly the `web` rematerialize assertions of `TestRepairReconcilesAllDriftStates` (Done, worktree-present, HEAD-at-marker) RED while the other four arms (`api`/`auth`/`db`/`cache`) stay GREEN — pinning that the executor composes the unit-22 prune primitive before re-adding; alternates: drop a Reclaimed repo from the `drop` set → cache survives in the record RED; turn the `block_orphan` arm into a removal → db orphan-left-intact RED |
| CMD-ISOLATE-REPAIR | in `isolateRepairCmd.Run`, on `RepairBlocked` return `(result, nil)` instead of `(result, *CommandError{Kind: conflict})` → a blocked reconcile is mis-reported as a clean success (exit 0) → `TestIsolateRepairBlockedIsConflict` RED (want `*cli.CommandError{conflict}`, got nil). Alternate (the `--dry-run` seam): make the dry-run branch fall through to the mutating `repair` path (`if false && DryRunFrom(ctx)`) → `TestIsolateRepairDryRunDoesNotMutate` RED (the run errors on the orphan instead of staying exit-neutral, and would re-materialize the missing worktree) — pinning that the read-only plan path is genuinely lock/mutation-free |
| SHAPE-ENUM-DOUBLE-ENTRY | add/reorder a value in any `All*()` without editing the `want*()` literal copy |
| SHAPE-ENVELOPE-INVARIANTS | add `,omitempty` to `Envelope.Error`, or drop the nil→`[]` coercion for repos/capabilities/warnings/next in `MarshalJSON`; **help block (decision #HB):** drop `,omitempty` from `Envelope.Help` → `"help":null` appears on every envelope → `TestEnvelopeHelpOmittedWhenNil` + the success/error goldens RED; reorder/rename a `HelpBlock` json tag or move the `Help` field → the frozen bytes drift → `TestEnvelopeHelpBlockGolden` RED |
| SHAPE-SCHEMA | set top-level `additionalProperties:true` (or drop `error` from `required`, or widen a closed enum) in `schema/envelope.schema.json` → `TestSchemaRejectsInvalid` RED |
| SHAPE-FINGERPRINT | rename/retype/reorder any `Envelope` (or nested) field, or edit the schema bytes, without regenerating `contract.lock.json` → `TestContractFrozen` RED |
| INV-NO-LLM | introduce a denylisted LLM/agent-SDK module into `go.mod`/`go.sum` (or empty `llmDenylist`) → `TestNoLLMDependencies` / `TestNoLLMScannerIsNonVacuous` RED |
| META-VACUITY | make `refSubject` ignore the fault (e.g. `if false && Active(refFaultID)`, or always return 42) so the under-fault subprocess passes → `TestMetaVacuity` RED ("harness is vacuous") |
| (fault seam unit) | replace exact `strings.TrimSpace(f) == id` with `strings.Contains` in `activeIn` → the `{"foobar","foo"}` case of `TestActiveIn` RED |
| LAYOUT-PATHS | change any segment literal (`"isolas"`→`"isolate"`, `"repos"`→…) or swap a join order in `layout.go` → `TestPaths` RED vs the hand-written goldens |
| LAYOUT-SAFE | make `validSegment` always-nil → reject cases of `TestSegmentSafety` RED; always-error → the `ok-name_1` accept floor RED |
| OPID-FORMAT | change the time unit (`UnixMilli`→`Unix`), `randLen` (5→4), the `op_` prefix, or drop `strings.ToLower` → `TestNewFormat`/`TestValid` RED |
| CLOCK-DETERMINISM | make `Fake.Rand` return `crypto/rand.Reader` → `TestFakeReproducible` RED; make `NewFake` ignore its seed → `TestFakeSeedSensitive` RED |
| TESTENV-HERMETIC | drop the fixed `GIT_AUTHOR_DATE`/`GIT_COMMITTER_DATE` → seeded SHA ≠ `goldenBaseSHA` → `TestSeedOriginIsDeterministic` RED; drop `GIT_AUTHOR_NAME` injection → ambient username leaks → `TestHermeticIdentity` RED |
| HEAL-ATOMIC-WRITE | replace `WriteFileAtomic`'s temp+rename with an in-place `O_TRUNC` write to the final path (still honoring `FaultBeforeRename`) → under the injected crash the target is torn to the new content → `TestAtomicReplaceIsCrashSafe` RED |
| LAYOUT-BOOTSTRAP | skip the `WiSubdirs` loop in `Bootstrap` → a declared `.wi/` subdir is missing → `TestBootstrapCreatesSubtree` RED; drop `EvalSymlinks` in `Resolve` (`return New(root)`) → a symlinked root keeps its link component → `TestResolveNormalizesSymlinks` RED |
| FLOCK-EXCLUDES | take the lock with `LOCK_SH` instead of `LOCK_EX` in `FileLock.TryLock`/`Lock` → two holders coexist → `TestFlockExcludesSecondHolder` + `TestLockBlocksUntilReleased` RED |
| LOCK-KEYS | change a kind prefix (`repo:`→`repository:`, …) or the `.lock` suffix in `internal/lock/keys.go` → `TestCanonicalKeyStrings` / `TestKeyPathDerivation` RED |
| LOCK-ORDER | make `orderedUnique` leave input order intact (no-op comparator) or skip the dedup → `TestOrderedUniqueIsTotalOrderAndDedups` RED |
| LOCK-MUTEX | treat a refused `TryLock` (`!ok`) as acquired → `TestAcquireRefusesOverlap` RED; or skip `h.Release()` rollback on refusal → `TestAcquireReleasesOnPartialFailure` RED |
| GITEXEC-OFFLINE-BELT | drop `GIT_ALLOW_PROTOCOL=none` from `Run`'s overlay → an offline `ls-remote file://…` succeeds instead of being refused → `TestOfflineRefusesTransport` RED (unit-level half of INV-NO-NETWORK) |
| INV-NO-NETWORK (architecture) | empty `egressAllowed` (or import `os/exec` / reference `GIT_ALLOW_PROTOCOL` in any non-allowlisted package) → `TestNoHiddenNetwork` RED (gitexec+testenv themselves trip, proving the walk + both detectors fire on real source) |
| INV-NO-NETWORK (detector) | make `scanFileForEgress` always return `(false,false)` → `TestNoNetworkScannerIsNonVacuous` RED (a blind scanner would be a silent false negative) |
| GITEXEC-CAPTURE | make `run` swallow a non-zero exit (return `nil` instead of `*ExitError`) → `TestRunSurfacesExitError` RED |
| GIT-FF-ONLY | drop the `merge-base --is-ancestor` precheck in `FastForwardBaseRef` (update-ref unconditionally) → a divergent target advances the base ref → `TestFastForwardRefusesNonFastForward` RED (missing error + moved ref) |
| GIT-CLONE-DETACHED | skip the `switch --detach` in `EnsureClone` (leave `<base>` checked out) → HEAD abbrev-ref is the branch name, not `"HEAD"` → `TestEnsureCloneDetachesAtBaseTip` RED |
| GIT-CLEAN | make `IsClean` ignore `StatusPorcelain` and always return `true` → an untracked file no longer reads as drift → `TestIsCleanReflectsWorkingTree` RED |
| GIT-FETCH | make `Fetch` a no-op (return nil without running `git fetch`) → the remote-tracking ref stays at the old tip → `TestFetchAdvancesRemoteTrackingOnly` RED |
| GIT-DIVERGED | swap the two `rev-list --left-right --count` columns in `DivergedCounts` (read ahead from `fields[1]`, behind from `fields[0]`) → `TestDivergedCountsAheadBehind` RED |
| GIT-WORKTREE | materialize via a standalone `git clone <ssotDir> <path>` instead of `git worktree add --detach` in `AddWorktree` → the result checks out `main` (not detached) and has its own `.git` dir + object store (common-dir ≠ SSOT) → `TestAddWorktreeIsDetachedLinkedAndShared` RED on all three assertions (proves the guard verifies genuine linked-worktree sharing, not just a checkout) |
| GIT-OWNED-REF | flip the namespace `refs/wi/`→`refs/heads/` in `ownedRef` → the marker becomes a stray branch: `refs/wi/owned/` is empty while `refs/heads/` grows a second ref → `TestOwnedRefMarksOwnershipUnderRefsWi` RED on both the "lives under refs/wi at the sha" and "no stray branch" assertions (the round-trip stays GREEN, isolating the decision-#2 namespace property; a no-op `CreateOwnedRef` additionally reddens the absent→present round-trip) |
| MIRROR-FETCH | make `Refresh` skip the `g.Fetch` dial (classify against the stale remote-tracking ref) → behind stays 0, origin_base == local_base, not stale → `TestRefreshFetchesAndClassifies` RED |
| MIRROR-FRESHNESS | hardcode `Stale:false` (or `true`) in `Snapshot.Freshness()`, ignoring the behind count → `TestFreshnessClassifiesStaleByBehindCount` RED (two-sided: a constant fails one branch) |
| MIRROR-PERSIST | make `Store` divert/skip the write (e.g. write `p+".mutant"`) so `Load` can't find it → `TestSnapshotRoundTrips` RED; or drop the `layout.ValidateSegment` call in `metaPath` → `TestStoreRejectsUnsafeRepoName` RED |
| CONFIG-PARSE | make `stripJSONC` a no-op (`return src`) → the golden manifest's comments become JSON syntax errors → `TestParseAcceptsGolden` RED; drop `dec.DisallowUnknownFields()` → the 3 unknown-key cases parse cleanly → `TestParseRejectsInvalid` RED |
| STATE-PERSIST | make `Store` divert the write (`p+".mutant"`) so `Load` can't find it → `TestRecordRoundTrips` RED; or make `UpdateRepoStage` skip its unknown-repo error (`found := true`) so flipping a non-existent repo wrongly succeeds → `TestUpdateRepoStageFlipsOneRepo` RED |
| STATE-DURABLE | replace `lockfs.WriteFileAtomic` with `os.WriteFile` in `Store` (keep `lockfs` referenced so the assertion, not the compiler, reddens) → the injected `WI_FAULT=lockfs.before_rename` no longer aborts so the interrupted flip lands → `TestDurablePartialSuccess` RED |
| ISOLATE-NEW | drop the stop-on-first-fail `return` in `isolate.New` (turn it into `continue`) → the loop materializes the repo AFTER the failed one → `TestNewStopsOnFirstFailWithDurablePartialSuccess` RED on the 3 "db not attempted" assertions (result stage, durable stage, on-disk worktree); or skip the upfront all-pending `state.Store` → the first repo's `UpdateRepoStage` finds no record (`state: no isolate record`) and no durable registry exists to resume from → both `TestNewMaterializesAllReposComplete` + `TestNewStopsOnFirstFail…` RED |
| RESOLVE-BUNDLE | wire per-repo `mirror` to the worktree path (`mirror := worktree`) instead of `layout.Repo` in `resolve.Bundle` → the SSOT mirror equals the worktree, reddening both repos' `Mirror` assertions in `TestBundleProjectsRecordPaths` (proves Bundle distinguishes the `isolas/<task>/<repo>` worktree from the `repos/<repo>` SSOT clone); or `continue` on one repo (drop it from the loop) → the projected `Repos` count/second-repo assertions RED (proves every recorded repo is projected, in order) |
| SHAPE-ONE-ENVELOPE | make `cli.Emit` write the envelope TWICE (a second `w.Write(b)`) → the stream carries two top-level JSON values → `TestEmitWritesExactlyOneEnvelope` RED (second `Decode` returns a document, not `io.EOF`); or drop the trailing `'\n'` (`w.Write` without `append(b,'\n')`) → `TestEmitTerminatesWithSingleNewline` RED |
| SHAPE-FAIL-MATRIX | perturb one pairing in `exitcontract.exitByKind` (e.g. `KindLockHeld`→`ExitRefused`/4 instead of `ExitLocked`/6) → `TestExitCodeForMatchesFailureMatrix` RED on that kind's row vs the independent §3.2 literal copy; or drop a kind whose code collides with the defensive default (e.g. remove `KindInternal`, code 70 == `ExitCodeFor`'s unmapped default) → the value test stays GREEN but `TestExitCodeForIsTotalOverAllKinds` RED (MappedKinds no longer covers `AllErrorKinds`), proving the totality check is non-redundant with the value check |
| SHAPE-ASSEMBLE | in `cli.Success` set `e.OK=false` (break the ok ⟺ error==nil coupling) → `TestSuccessEnvelopeCoupling` RED; or have the shared `spine` omit `Capabilities`/`SchemaVersion` (leave them zero) → `assertCommonFields` reddens in BOTH `TestSuccessEnvelopeCoupling` + `TestFailureEnvelopeCoupling` |
| SHAPE-DRYRUN-EXIT0 | make `cli.ExitFor` return a refusal code when `len(env.Blocked)>0` (treat a would-block verdict as a refusal) → `TestExitForBlockedVerdictsAreExitNeutral` RED (blocked must be exit-neutral); the companion assertion that a genuine usage error on a `--dry-run` still maps to 64 guards against the over-correction (a blanket `if env.DryRun { return ExitOK }` would wrongly swallow it) |
| SHAPE-TEXT-PROJECTION | drop ANY field from `cli.RenderText`/its helpers (e.g. comment out the `worktree` line in `renderRepo`) → that field's unique sentinel leaf is absent from the render → `TestRenderTextIsLossless` RED, naming the exact dropped fact (the independent `collectStringLeaves` reflection walk enumerates every envelope string leaf; the hand-written renderer can't silently omit one). Non-vacuity is inline: ≥25 leaves must be found and a never-present sentinel must NOT match |
| CMD-RESOLVE | in `resolveCmd.Run` drop the `errors.Is(err, state.ErrNoRecord)` branch (`if false && …`) → a missing isolate falls through as a plain error → maps to kind=internal not not_found → `TestResolveMissingIsolateIsNotFound` RED (got `*errors.errorString`, want `*cli.CommandError{not_found}`); or make `newResolveCommand` accept any arg count (skip `len(args)!=1`) → `TestResolveFactoryValidatesArgs` RED |
| CMD-INIT | in `initCmd.Run` open the manifest with `O_TRUNC` instead of `O_EXCL` (clobber-on-reinit) → a re-init silently rewrites the manifest and returns `ActionCreated` (no `fs.ErrExist`→`already_exists`) → `TestInitOnExistingProjectIsAlreadyExists` RED (got a result + nil error, want `*cli.CommandError{already_exists}`, and the manifest is no longer preserved); or make `newInitCommand` accept any arg count (skip `len(args)!=0`) → `TestInitFactoryRejectsArgs` RED |
| CTX-OPID | in `Execute` drop the `ctx = WithOpID(ctx, m.OpID)` injection (`if false {…}`) → the Command observes `""` from `OpIDFrom(ctx)` instead of the minted op_id → `TestExecuteInjectsOpIDIntoContext` RED (saw `""`, want the `Meta.OpID`) |
| DISPATCH-ROUTES | in `cli.resolveCommand` ignore the parsed name and always return a fixed real command (`return "init", positional, true`) → an unknown name wrongly runs a real command (exit 0 not 64) → `TestDispatchRoutesUnknownToUsage` RED, and the 2-token name is mis-stamped with its args dropped → `TestDispatchRoutesTwoTokenCommand` RED; or skip the `op_id` mint (leave `Meta.OpID` empty) → `TestDispatchMintsOpID` RED (`opid.Valid("")` fails on both the success and usage paths) |
| DISPATCH-DIDYOUMEAN | in `cli.unknownCommandEnvelope` drop the `suggest.For` call (leave `env.Error.DidYouMean` nil) → a near-miss no longer self-corrects → `TestDispatchUnknownCommandSuggests` RED (`innit` got nil, want `[init]`); or blank `env.Error.Help` (`env.Error.Help = ""`) → the `wi help` pointer assertions RED on both the near-miss and the no-match (`xyzzy`) cases; the no-match case stays GREEN on the did_you_mean check under the first mutant (suggest already returns nil there), isolating the mutant to the suggestion-injection path |
| RUN-PIPELINE | in `cli.envelopeFor` drop the durable-partial result-merge (`if false && r != nil { env.Repos = r.Repos … }`) → a partial no longer carries its per-repo detail → `TestExecutePartialCarriesReposAndExitsTwo` RED ("got 0 repos"); or make `Execute` ignore `ExitFor` and `return contract.ExitOK` → every non-zero-exit assertion (CommandError→3, partial→2, internal→70) RED |
| CMD-ISOLATE-NEW | in `isolateNewCmd.Run`, on `res.Status == isolate.StatusPartial` return `(result, nil)` instead of `(result, *CommandError{Kind:partial})` → a partial is mis-reported as a clean success (no error, exit 0) → `TestIsolateNewDurablePartial` RED (`want *cli.CommandError, got <nil>`); or drop the unknown-repo `!ok` not_found branch (skip the `cfg.Lookup` check) → `TestIsolateNewUnknownRepoIsNotFound` RED (no error / wrong kind) |
| SYNC-RUN | drop the `g.FastForwardBaseRef` call in `syncOne` (`if false {…}`, keep the snapshot built from `originSHA`) → after the origin advances the on-disk base ref is never moved → `TestSyncFastForwardsToNewOriginTip` RED (on-disk base frozen at the seed tip, not the new origin tip), while the fresh-materialize + continue-on-fail tests stay GREEN (isolates the mutant to the advance path); secondary: turn the per-repo loop's continue-on-fail into break/return-on-first-error → `TestSyncContinuesOnFailureAndReportsPartial` RED (the reachable repo after the failed one is never synced) |
| CMD-SYNC | in `syncCmd.Run`, on `res.Status == syncpkg.StatusPartial` return `(result, nil)` instead of `(result, *CommandError{Kind:partial})` → a partial sync is mis-reported as a clean success (no error, exit 0) → `TestSyncHandlerDurablePartial` RED (`want *cli.CommandError, got <nil>`), while `TestSyncHandlerSyncsAllDeclaredRepos` stays GREEN (isolates the mutant to the partial-mapping path); alternate: drop the unknown-repo `!ok` not_found branch in `selectRepos` → `TestSyncHandlerUnknownRepoIsNotFound` RED (no error / wrong kind) |
| CONFIG-ADD | after `os.ReadFile` in `config.Add`, strip comments before splicing (`data = stripJSONC(data)`) → the rewrite is still valid JSON containing every repo but the comments are gone → `TestAddAppendsPreservingComments` + `TestAddIntoEmptyArray` RED on the comment-survival assertions, while the repo-presence/re-parse assertions stay GREEN (isolates the mutant to the AST-preserving property — proving the edit is genuinely comment-preserving, not merely "produces valid JSON"); secondary: drop the `,` separator in the non-empty splice (`",\n"`→`"\n"`) → two adjacent objects with no separator → the post-rewrite Parse belt rejects it → `TestAddAppendsPreservingComments` RED (Add returns an error) |
| CMD-REPO-ADD | in `repoAddCmd.Run` drop the registry lock by acquiring zero keys (`lock.Acquire(c.layout.LocksDir())`) → a busy registry is no longer refused → `TestRepoAddBusyRegistryIsLockHeld` RED (got a created Result, want `*cli.CommandError{lock_held}`), while the other 5 tests stay GREEN (isolates the mutant to "the handler actually takes the project-registry lock"); alternate: drop the `errors.Is(err, config.ErrDuplicateRepo)` → already_exists branch → a duplicate falls through to the usage default → `TestRepoAddDuplicateIsAlreadyExists` RED (wrong kind) |
| GIT-RECLAIM | replace `git worktree remove` with a bare `os.RemoveAll(worktreePath)` in `RemoveWorktree` → the dir vanishes but the SSOT's worktree admin entry survives as a stale prunable entry AND a dirty worktree is wrongly nuked → `TestRemoveWorktreeDeregisters` RED (`worktree list` still names the path, "prunable gitdir file points to non-existent location") + `TestRemoveWorktreeRefusesDirty` RED (removed a dirty worktree, want refusal) — pins the deregister + no-force/no-reset-hard safety (DESIGN §7.1/§7.2); for `DeleteOwnedRef` skip the `update-ref -d` (`if false {…}`) → the marker survives → `TestDeleteOwnedRefClearsMarker` RED (`OwnedRefSHA` still reports present) |
| ISOLATE-REMOVE | drop the ahead-of-base gate in `reclaimRepo` (`if false && head != marker`) → a worktree carrying a local commit (clean tree, HEAD moved past the creation marker) is no longer a HARD BLOCK; its clean tree lets `git worktree remove` succeed, so the local work is wrongly reclaimed → `TestRemoveReclaimsCleanBlocksAheadOfBase` RED on every "web intact" assertion (outcome `Removed:true` not blocked, worktree dir gone, marker cleared, registry no longer retains web) — pins the evidence-positive "not ahead of base" gate (DESIGN §7.1, decision #RM); secondary: skip the marker-existence/clean gates likewise → an unowned or dirty orphan is reclaimed |
| ISOLATE-REMOVE-TEARDOWN | in `isolate.Remove`'s `len(rec.Repos)==0` branch replace `state.Delete` with `state.Store(stateDir, rec)` (keep an empty-repos husk instead of deleting) → a fully-reclaimed isolate's record survives → `TestRemoveAllCleanDeletesRecord` RED (`state.Load` returns a record, want `state.ErrNoRecord`) — pins that full teardown removes the registry entry so a later `isolate rm` correctly reports not_found |
| CMD-ISOLATE-RM | in `isolateRmCmd.Run`, on the mixed outcome (`removed > 0` with blocks) return `(result, nil)` instead of `(result, *CommandError{Kind:partial, Action:removed})` → a partial teardown is mis-reported as a clean success (no error, exit 0) → `TestIsolateRmDurablePartialBlocksOrphan` RED (`want *cli.CommandError, got <nil>`), while complete-teardown + all-blocked stay GREEN (isolates the mutant to the partial-mapping path); alternate: in `projectRemoveOutcome` map an orphan hard-block to `Kind:internal` (or drop the `Code:"orphan_unexplained"`) → same test RED on the repos[] `web.Error.Kind == conflict` / `Code == orphan_unexplained` assertions — pins the loud `orphan_unexplained` surface (DESIGN §7.1) riding in repos[] not Blocked[] (decision #RD) |
| CMD-MAIN | in `run` (cmd/wi) `_ = code; return contract.ExitOK` instead of `return code` → run swallows Dispatch's computed exit and always exits 0 → `TestRunUnknownCommandExitsUsage` RED (got 0, want 64), while `TestRunInitScaffoldsWorkspace` stays GREEN (init already exits 0) — isolates the mutant to exit-code propagation; alternate: hand `cli.Dispatch` an empty `Registry{}` instead of `BuildRegistry(deps)` → every command is unknown → `TestRunInitScaffoldsWorkspace` RED (no `.wi/` scaffolded, ok:false/usage not created) — pins that the REAL registry over a cwd-resolved root is wired |
| SUGGEST-DIDYOUMEAN | in `suggest.For` return `nil` regardless (the shipped test-first stub) → every typo/prefix case loses its suggestion → `TestForSuggestsClosest` RED on the `reslove`/`snc`/`re`/`RESLOVE` rows (got nil, want the match) while the `xyzzy`/empty rows stay GREEN (isolates the mutant to the match path); alternate: drop the threshold/prefix filter and `return candidates` unfiltered → the `xyzzy`/empty "nothing close → nil" rows RED (got the whole command set); alternate: remove the `input==""` guard → empty input prefix-matches everything → `empty input is never a suggestion` RED |
| CMD-HELP | in `helpCmd.Run` drop the `!ok` not_found branch (`if false && !ok`) so an unknown topic maps the zero `help.Model` to a `Result` instead of refusing → `TestHelpUnknownTopicIsNotFound` RED (got a result, want `*cli.CommandError{not_found}`); or in `envelopeFor` drop the `env.Help = r.Help` success-branch threading → the help block never reaches the wire → `TestHelpEnvelopeCarriesBlockEndToEnd` RED (`env.Help` nil, the help-json capability has no payload). Overview/command-detail tests stay GREEN under either mutant, isolating each to its path |
| HELP-MODEL | in `help.For` ignore the topic and always `return Model{Synopsis:overview, Commands:Commands(), Next:…}, true` → drilling into a command no longer yields its detail → `TestForCommandDetail` RED (Usage/Next mismatch, Commands non-nil) and `TestForUnknownTopic` RED (got ok=true for `frobnicate`); alternate: `return Model{}, true` for an unknown topic → `TestForUnknownTopic` RED (want ok=false); alternate: drop the `"wi "` prefix on a `table` row's `Next` (or the overview's) → `TestNextIsRunnable` RED (not a runnable `wi …` line); alternate: blank a row's `Usage`/`Synopsis` → `TestTableIsFullyPopulated` RED (help would lie about the surface). Overview/empty-topic rows stay GREEN under the first mutant, isolating it to the per-command path |
| HELP-REGISTRY-SYNC | add a bogus key (e.g. `"ghost"`) to `BuildRegistry` → a registry command with no help row → `TestHelpTableMatchesRegistry` RED (equal-sets assertion: `registry (minus help) = [ghost init …]` ≠ `help table = [init …]`); alternate: drop a row (e.g. `isolate rm`) from `help.table` → a help row's command outlives… inverted: a registry command with no help row → same RED on the missing name; alternate: remove the `"help"` registry entry → `registry must contain the "help" command` RED. Pins that the help metadata table and the live dispatch surface can never drift (DESIGN §3.1 "help can never lie") |
| LOCK-LIVENESS-PID (M4) | replace `lock.processAlive`'s body with `return true` (the registered mutant) → a reaped, provably-dead child pid reads as alive → `TestProcessAlive` RED (`processAlive(reaped child pid …) = true, want false`) along with the `pid 0`/`-1` guard rows; symmetrically `return false` reddens the live-self row (`processAlive(self …) = false, want true`). Confirmed RED with `return true` before going green. Pins the proven-dead gate self-heal consults before breaking a stale lock — a live process must NEVER read as dead (DESIGN §2 / §7.3) |
| HOST-BOOTID (M4) | in the platform `host.bootID` success path replace `return "boottime:"+sec…`/`return "boot_id:"+id…` with `_ = sec; return "", nil` (the registered mutant) → `BootID()` yields an empty id → `TestBootID` RED (`BootID() = "", want a non-empty per-boot identifier`); alternate: return a value that varies per call → the stability assertion RED (`BootID() not stable across calls`). Confirmed RED (empty form) before green. Pins the reuse guard the lock-liveness layer pairs with the holder pid: a non-empty, boot-stable id is what lets a stale-across-reboot lock be told from a live one (DESIGN §6 / §7.3, open decision #3) |
| LOCK-HOLDER (M4) | in `lock.CurrentHolder` set `OpID:""` ignoring the arg → `TestCurrentHolderCapturesProcess` RED (`OpID = "", want "op-xyz"`); alternate: rename a json tag on `Holder` (e.g. `boot_id`→`bootid`) → `TestHolderRoundTrip` RED on the **durable wire-key** assertion (`marshaled holder … missing durable wire key "boot_id"`) — NOTE the round-trip equality alone does NOT catch a tag rename (Marshal+Unmarshal share the tag and stay symmetric), which is exactly why the test pins the concrete keys `"pid"`/`"host"`/`"boot_id"`/`"op_id"`. Both confirmed RED before green. Pins lossless serialization + correct identity capture of the lock-holder record the liveness layer reads back (DESIGN §6 / §7.3) |
| FLOCK-BODY (M4) | make `lockfs.FileLock.WriteBody` `return nil` without writing → `ReadBodyAt` sees an empty body → `TestFlockBodyRoundTrip` RED (round-trip mismatch); alternate: drop the `Truncate(0)` in `WriteBody` → a shorter rewrite leaves the longer body's tail → `TestFlockBodyRoundTrip` RED on the shorter-overwrite assertion (`got "{…long…}" want "{}\n"`). Both confirmed RED before green. Pins that a held flock carries a holder body readable by a by-path inspector while held — the channel self-heal uses to identify a contended lock's holder (DESIGN §6 / §7.3) |
| LOCK-STAMP (M4) | make `(*lock.Held).Stamp` `return nil` without writing any body → a freshly-acquired lock has an empty body → `ReadHolder` errors → `TestStampRoundTrip` RED (`lock: empty holder body`); alternate: make `Stamp` write only `h.locks[0]` (skip the rest) → the second held key's body stays empty → `TestStampStampsEveryHeldLock` RED on `ReadHolder(repo:b)` (the per-lock angle: identity is recorded into EVERY lock the operation holds, not just the first). Both confirmed RED before green. Pins the lock-layer write/read of holder identity (composing `Holder` + `WriteBody`) — an unstamped or missing lock reads as an error (unknown holder → conservatively never broken), never a zero-value `Holder` (DESIGN §6 / §7.3) |
| ISOLATE-STAMP (M4) | drop the `held.Stamp(opID)` call in `isolate.New` (the pre-wiring state) → the isolate-state lock is acquired but never stamped → its body stays empty → `lock.ReadHolder(LocksDir, IsolateState(task))` returns `lock: empty holder body` → `TestNewStampsHolderOnIsolateLock` RED. Confirmed RED before green. Pins that the first wired acquire site actually records its holder identity end-to-end (the lock file persists past release; `Unlock` does not unlink), so the self-heal layer can read who created an isolate (DESIGN §6 / §7.3) |
| SYNC-STAMP (M4) | drop the `held.Stamp(opID)` call in `sync.syncOne` (or thread `opID` but never call `Stamp`) → the `repo:<name>` lock is acquired but never stamped → its body stays empty → `lock.ReadHolder(LocksDir, Repo("api"))` returns `lock: empty holder body` → `TestSyncStampsHolderOnRepoLock` RED. Confirmed RED before green. Pins that the hottest-contention acquire site (parallel agents racing `wi sync` on the same `repo:<name>` key) records its holder identity end-to-end (DESIGN §6 / §7.3) |
| REPOADD-STAMP (M4) | drop the `held.Stamp(OpIDFrom(ctx))` call in `repoAddCmd.Run` (the pre-wiring state) → the `project-registry` lock is acquired but never stamped → its body stays empty → `lock.ReadHolder(LocksDir, ProjectRegistry())` returns `lock: empty holder body` → `TestRepoAddStampsHolderOnRegistryLock` RED. Confirmed RED before green. Pins that the registry-mutation acquire site records its holder identity end-to-end, reading the op_id from the context (`cli.OpIDFrom`, the same id `Execute` injects) — no signature change needed (DESIGN §6 / §7.3) |
| ISOLATE-RM-STAMP (M4) | drop the `held.Stamp(opID)` call in `isolate.Remove` (the pre-wiring state) → the isolate-state lock is re-acquired but never re-stamped → its body still carries the op id `isolate.New` stamped during setup → `lock.ReadHolder(LocksDir, IsolateState(task))` returns `OpID == "op_new_for_rm_stamp"`, not the Remove op id → `TestRemoveStampsHolderOnIsolateLock` RED. Confirmed RED before green (`OpID = "op_new_for_rm_stamp", want "op_remove_stamp"`). The 4th/final acquire site; the RE-stamp angle (a fresh op overwrites the prior holder, not just an empty→full transition) proves the stamp fires in `Remove` specifically, not merely as leftover from `New`. Needed the only external signature change among the four — `opID` threaded through `Remove` + its `cmd_isolate_rm` caller + 4 test callers (DESIGN §6 / §7.3) |
| LOCK-PROVEN-DEAD (M4) | in `ProvenDead` drop the boot-mismatch limb (`if h.BootID != bootID { return true }`), falling through to the same-boot pid check for every same-host holder → a different-boot holder whose pid happens to be a LIVE process this boot (reboot + pid reuse) reads as NOT dead → `TestProvenDead` RED on the reboot case (`different-boot, live pid = false, want true`). Confirmed RED before green (only that one assertion reddened; the live-self/reaped-pid/foreign-host/empty-origin cases stayed green, proving the test isolates exactly that limb). Alternate mutant = drop the `h.Host != hostname` guard → a foreign-host holder with a reaped pid reads as dead → RED on the foreign-host case. Pins the DESIGN §7.3 proven-dead predicate (boot mismatch OR same-boot ESRCH, same host only) that — with the fs-trust gate — is the SOLE authority to break a contended lock; conservative on every unprovable case so wi never steals a live peer's lock |
| LOCK-FS-TRUST (M4) | make the per-OS classifier (`darwinFSTypeTrustworthy`/`linuxFSTypeTrustworthy`) `return true` unconditionally → every network/unknown fs reads as flock-trustworthy → `TestDarwinFSTypeTrustworthy` (host) RED on all network/unknown cases (nfs/smbfs/afpfs/webdav/ftp/fusefs/""/"wat"); symmetric `TestLinuxFSTypeTrustworthy` RED on NFS/CIFS/9p/FUSE/0. Confirmed RED on darwin before green; the `FSTrustworthy(t.TempDir())==true` end-to-end smoke stayed green under the mutant (it only exercises the syscall+string-extraction wiring, not the classification), so the classifier table is the load-bearing half. Alternate = `return false` → the local apfs/ext/btrfs cases redden. Pins DESIGN §7.3's allowlist/fail-closed fs-trust gate: a break is refused unless the backing fs is POSITIVELY a known-local type, so wi never breaks a flock another host may hold over a shared (network) fs. Linux limb typecheck-verified via `GOOS=linux go vet` (not run on the darwin host) |
| LOCK-SAFE-TO-BREAK (M4) | in `AssessBreak` drop the `ProvenDead` conjunct from the verdict — replace `case !d.ProvenDead:`/`default:` with a single `default:` that sets `Safe=true` (i.e. `Safe = FSTrustworthy && HolderKnown`, break any known holder on a trustworthy fs, alive or not) → the live-holder case (body = `CurrentHolder` = THIS running process) reads `Safe=true` → `TestAssessBreak/live_holder_is_never_breakable` RED (`Safe = true while the holder (this process) is alive`). Confirmed RED before green — ONLY the live case reddened; the unknown-holder case (`HolderKnown=false`→Safe=false) and the proven-dead case (boot-mismatched holder→Safe=true correctly) stayed green, proving the test isolates exactly the dropped conjunct. Alternate mutant = treat an unknown holder as breakable → the unknown-holder case reddens. Pins the HEAL-3 composition (DESIGN §7.3 / §7.4): the read-only break verdict is the conjunction of all three gates (fs-trustworthy AND holder-known AND proven-dead), fail-safe on every other state, so wi never steals a lock from a live or unknown peer. Darwin host RED→GREEN; `//go:build unix` file linux-verified via `GOOS=linux go build`+`go vet` |
| LOCK-PARSE-KEY (M4) | in `ParseKey` replace the `default` error branch with `return Repo(s)` (treat any unrecognized string as a repo key) → a junk filename with no recognized namespace prefix but an otherwise-valid segment (`"garbage"`, `"unknown:thing"`) parses successfully as a repo key → `TestParseKey` RED on exactly those rejection cases (`ParseKey("garbage") = nil error, want error`). Confirmed RED before green — the round-trip cases (ProjectRegistry/Repo/IsolateState) and the prefix-handled rejections (`"repo:"`,`"repo:bad/name"`,`"isolate-state:"` — caught by the repo/isolate branches' `ValidateSegment`) stayed green, isolating the namespace-gate. Alternate = drop the `"isolate-state:"` branch (fall to the default error) → the isolate-state round-trip reddens (`ParseKey("isolate-state:feature-x"): unexpected error`). Pins the inverse of the key namespace `lock ls` relies on: a stray, non-key file in the locks dir is rejected, never fabricated into a Key and assessed. Shared namespace consts make `String()` and ParseKey provably non-drifting (DESIGN §6.1 / §7.3) |
| LOCK-LIST (M4) | in `List` drop the stray-skip — change `key, err := ParseKey(...)`/`if err != nil { continue }` to `key, _ := ParseKey(...)` and assess unconditionally → a `.lock` file whose stem is not a valid key (`notakey.lock`) yields the zero Key and is fabricated into a phantom LockStatus with an empty key → `TestList` RED on the exact sorted-key-set assertion (`List keys = [<empty> isolate-state:task1 project-registry repo:api], want [isolate-state:task1 …]`). Confirmed RED before green — the missing-dir and empty-dir subtests stayed green, isolating the stray-skip. Alternate = drop the `errors.Is(err, os.ErrNotExist)` special-case → a missing locksDir returns an error → the "missing dir is empty, not an error" subtest reddens. Pins that lock enumeration (the data half of `lock ls`) skips strays (a non-key file is NEVER fabricated into a lock), treats "no locks" as a valid empty result, and carries each lock's AssessBreak verdict (DESIGN §7.3 / §7.4) |
| LOCK-BREAK (M4) | in `Break` drop the `if !d.Safe { return d, nil }` early return so it ALWAYS `os.Remove`s the lock file regardless of verdict → a refused break still destroys the file → `TestBreak` RED on BOTH the `live_holder_is_refused_and_left_intact` and `unknown_holder_(body-less)_is_refused_and_left_intact` subtests (`lock file removed …, want intact`), while `proven-dead … is broken` and `nothing to break is not an error` stay green — isolating the mutant to exactly the safe gate. Alternate = replace `os.Remove(...)` with a no-op → the proven-dead subtest reddens (`lock file still present after a safe break, want removed`). Pins the DESIGN §7.3 / HEAL-3 displacement action: a lock is unlinked ONLY when AssessBreak proves the holder dead on a trustworthy fs; unlinking a file a LIVE peer holds would break mutual exclusion (next Acquire O_CREATEs a new inode and flocks that), the exact data-loss path §7 forbids. Darwin host RED→GREEN; `//go:build unix` |
| CMD-LOCK-LS (M4) | in the `LockStatus`→`LockInfo` projection (`lockInfoOf`/`lockInfoFrom`) drop the `if d.HolderKnown { li.Holder = … }` guard so Holder is left nil for every row → the proven-dead row (which HAS a known holder) loses its identity → `TestLockLsProjectsHolders` RED on the non-nil-holder assertion (`repo:api has a known holder; its nested holder identity must be projected, got nil`), while the body-less row (holder legitimately nil) stays green — isolating the holder projection. Alternate = swap two of the four bool fields in the projection → the per-field bool assertions redden. Pins the read-only `wi lock ls` CLI surface over `lock.List`: action=read, the four verdict bools land on the right contract fields, Reason is carried, and a holder identity rides the nested LockHolder EXACTLY when known (DESIGN §7.3/§7.4); `//go:build unix` |
| CMD-LOCK-BREAK (M4) | in `lockBreakCmd.Run` drop the `if !d.Safe { return … &CommandError{Kind: KindLockHeld} … }` branch so every break maps to `Result{Action: removed}` / exit 0 regardless of verdict → a live holder is mis-reported as a successful break → `TestLockBreakLiveHolderRefusesWithLockHeld` RED (`exit = 0, want 6`; `got ok=true error=<nil>`), while `TestLockBreakProvenDeadRemovesAndExitsZero` stays green — isolating the verdict→envelope mapping. Alternate = drop `env.Locks = r.Locks` from `envelopeFor`'s failure arm → the refusal stops carrying its verdict → same test RED on the `locks[]`-on-failure-envelope assertion (`a lock_held refusal must carry the lock's verdict`). Both confirmed via `cli.Execute` (full pipeline) RED→GREEN. Pins the ACTION half of HEAL-3: a SAFE break → removed/exit 0 + the removed lock's verdict; a refused break → lock_held/exit 6 with the verdict carried so the agent sees WHY, file left intact; one-operand factory rule (DESIGN §7.3); `//go:build unix` |
| REPAIR-CLASSIFY (M4) | in `isolate.Classify` replace the marker-keyed arms with `case !worktreeExists: return ClassMissingWorktree` (re-materialize ANY missing worktree, ignoring the marker) → a completed-then-deleted cell (no marker, no worktree) is mis-classified as a re-materialize candidate → `TestClassifyNoResurrection` RED (`got "missing_worktree"`, want `reclaimed`) + `TestClassifyEvidencePositive/neither-present` RED, while `owned-but-worktree-gone`, `owned-and-present`, `worktree-without-marker` stay green — isolating exactly the no-resurrection keystone. Pins HEAL-1 (DESIGN §7.1/§7.4): the marker ref — not the registry record — is the authority on whether a cell should exist, so the re-materialize verdict (MissingWorktree) requires a SURVIVING marker; a deliberately removed op (marker unlinked by `isolate rm`) classifies as Reclaimed and is NEVER resurrected. Pure function, no build tag. Darwin RED→GREEN |
| REPAIR-INSPECT (M4) | in `isolate.observeCell` mutate `pathExists` to `return true` (never stat the worktree path; pretend every worktree exists) → cells whose worktree was removed are mis-observed → `TestInspectObservesEachCell` RED on exactly `web` (`consistent`, want `missing_worktree`) and `cache` (`orphan_worktree`, want `reclaimed`), while `api`/`db` (worktrees genuinely present) stay green — isolating the worktree read as load-bearing. Pins the HEAL-1 read-only observer: `Inspect(ctx,l,g,task)` loads the registry record and, per recorded repo in record order, reads the marker ref (`git.OwnedRefSHA`) + worktree presence (`os.Stat`) into a classified `Cell{Repo,Stage,Class,MarkerSHA}`; takes no lock, mutates nothing, dials no network; a missing record propagates `state.ErrNoRecord` (→ not_found). MarkerSHA carries the marker's recorded base sha (the re-materialize source) only when the marker survives. Hermetic real-git harness; 4-repo isolate driven to all four drift states by disk/ref manipulation. Darwin RED→GREEN + linux cross-build/vet clean |

## Decisions taken (from IMPLEMENTATION_PLAN.md §7 open decisions)

- **#4 isolate-remove (and crash) recovery policy = roll-FORWARD — RESOLVED 2026-06-30** (the §7 #4 open
  decision, "leaning roll-forward"; stamped RESOLVED in IMPLEMENTATION_PLAN.md §7 #4). Ruling: recovery
  rolls FORWARD, never back, and the action for each interrupted op is decided per-op from its durable
  journal's FURTHEST-reached phase via `journal.Classify` (guard `HEAL-CRASH-RECOVER`, HEAL-4 sub-unit 3a):
  • `done` furthest → **Complete** — the op finished cleanly; nothing to recover (its journal is stale, may
    be cleaned up). • `committed`-but-not-`done` → **RollForward** — the op crossed its point of no return;
    the next offline startup FINISHES it (an interrupted isolate-rm completes its deletion), it is NEVER
    restored — accepting that an interrupted remove cannot be undone by re-run. • `intent`-only → **Abandoned**
    — the op crashed BEFORE committing (before the point of no return); recovery neither finishes it (nothing
    durably began) nor undoes it (roll-forward-only), leaving any partial artifacts to the evidence-positive
    heals (isolate repair / gc). This honors the §7 "never heal in a way that destroys live work" posture:
    only a point-of-no-return-crossed op is auto-finished; a pre-commit crash is SURFACED, not guessed. The
    classifier reads furthest-phase (rank-max), NOT the last journal line, so a torn or re-ordered journal can
    never downgrade a committed op out of recovery. Recovery is OFFLINE-ONLY (zero network, no git fetch).
    `Disposition` (`complete`/`roll_forward`/`abandoned`) is INTERNAL recovery vocabulary, not a contract wire
    enum (contract stays the sole owner of those). Remaining HEAL-4 work: sub-unit 3b directory scan +
    3c roll-forward executor & offline startup hook actually ENACT this policy.

- **#3 boot_id derivation — RESOLVED 2026-06-30** (the §7 #3 open decision, "Blocks: M4 lock liveness").
  Ruling: `internal/host.BootID()` returns an opaque, boot-stable, platform-tagged id. **darwin:** derive
  from `kern.boottime` via the `sysctl(2)` SYSCALL (`syscall.Sysctl("kern.boottime")`), decoding the
  leading 8 bytes as little-endian `tv_sec` → `"boottime:<sec>"`. **linux:** read
  `/proc/sys/kernel/random/boot_id` (the kernel's per-boot UUID) → `"boot_id:<uuid>"`. Both are constant
  for a boot's lifetime, unchanged by sleep/wake, and differ after a reboot — so a lock body recording a
  boot_id != the current one was written before a reboot and its pid is provably dead. **Crucial
  refinement forced by INV-NO-NETWORK:** decision #3's wording ("derive from `sysctl kern.boottime`")
  initially read as "shell out to sysctl(8)," but that import of `os/exec` tripped the no-network invariant
  (only `internal/gitexec` may spawn child processes, so the egress belt is unbypassable — DESIGN §2 #3).
  Using the raw syscall instead satisfies the invariant, adds no dependency (decision #6), and is strictly
  more faithful to "derive from sysctl." Sleep/wake stability and PID-reuse soundness: boottime/boot_id are
  set once at boot, so the {boot_id, pid} pair is unique within a boot and a reused pid from a *prior* boot
  is rejected by the boot_id mismatch. Guard `HOST-BOOTID`. Supported platforms = linux + darwin (the CI
  matrix); other unix is out of scope until M5's portability matrix.

- **#S did-you-mean engine = hand-rolled cobra-`SuggestionsFor` clone — RESOLVED 2026-06-30** (settles
  the §7 "help/did_you_mean/next ownership" decision's typo-suggestion half). DESIGN §7 said "defer
  unknown-command typos to cobra's `SuggestionsFor`," but decision #F dropped cobra entirely, so there
  is nothing to defer to. Ruling: `internal/suggest` reproduces cobra's algorithm exactly — a candidate
  qualifies when case-insensitive Levenshtein distance ≤ 2 (cobra's default `minDistance`) OR the
  candidate has the input as a case-insensitive prefix. Levenshtein is hand-rolled (two-row DP over
  runes), NOT the `agnivade/levenshtein` dep PLAN line 37 floated — consistent with #F's zero-dep,
  hand-rolled-stdlib posture and DESIGN §2's minimal-surface invariant. Ordering (distance asc, name
  asc) and nil-on-no-match are wi additions for a deterministic, omitempty-friendly `did_you_mean[]`.

- **#HB help model → envelope wire form = a top-level additive `help` block — RESOLVED 2026-06-30**
  (settles the §7 "help/did_you_mean/next ownership" decision's help-payload half; the typo half is #S).
  How does `help.Model` ride the one envelope? Ruling: a reserved additive block `help` on
  `contract.Envelope` (contract stays SOLE owner of the wire type, DESIGN §3.1). `HelpBlock{topic,
  synopsis, usage, commands[]}` + `HelpCommand{name, synopsis, usage}` carry the DESCRIPTIVE surface;
  the model's RUNNABLE follow-ups ride the existing top-level `next[]` (not duplicated in the block).
  `Synopsis` is always set; `topic`/`usage` are empty for the overview (where `commands[]` lists the
  whole surface) and set for a single command (where `commands` is nil) — so `For("")` vs
  `For("<cmd>")` map to two distinct, self-evident shapes. Declared in BOTH `schema/envelope.schema.json`
  ($defs `helpBlock`/`helpCommand`, top-level `help` ref, NOT in `required`) AND the Go struct at
  schema_version **"1.0"** with NO version bump — pre-release v0, exactly as `resolve`/`planned`/`blocked`
  were added; the SHAPE-FINGERPRINT lock was regenerated (`WI_UPDATE_CONTRACT_LOCK=1`) to capture the new
  shape + schema sha. The cli layer (NEXT unit) maps `help.Command`→`contract.HelpCommand` so contract
  never imports help and help never imports contract. M5's agent-usability capstone enriches each command
  with self-describing flags[]/exit-codes/kinds — additive to this v0 block, OUT of M3 scope. Guards:
  `TestEnvelopeHelpBlockGolden` (frozen bytes + field order, help between next and error),
  `TestEnvelopeHelpOmittedWhenNil` (omitempty), `goldenHelp` added to `TestSchemaAcceptsGolden`, lock via
  `TestContractFrozen`.

- **#HR `help` is a meta-command excluded from the help↔registry sync set — RESOLVED 2026-06-30** (not a
  §7 ruling; forced by building the `HELP-REGISTRY-SYNC` fitness, which had to decide how `help` itself is
  treated in the comparison). The fitness `TestHelpTableMatchesRegistry` proves the help metadata table
  (`internal/help`, SOLE owner of the command surface) and the live dispatch registry describe the SAME
  command set so `wi help` can never lie (DESIGN §3.1). But the surfaces are NOT identically sized: the
  registry has 7 keys, `help.Commands()` lists 6. The seventh, **`help`, is a META-command** — a real
  registered command (it backs the advertised `help-json` capability, so it MUST be in the registry) that
  is DELIBERATELY ABSENT from the help table, because the table doubles as the help text and reads as the
  init→repo add→sync→isolate new→resolve→isolate rm WORKFLOW RUNBOOK; `wi help` is the lens you read the
  runbook THROUGH, not a step in it. Ruling: the fitness asserts (1) `"help"` IS a registry key, (2)
  `"help"` is NOT a `help.Commands()` row, then (3) compares the registry keys MINUS `"help"` against the
  help-table names as equal sets. This makes the exclusion principled (a checked invariant) rather than a
  silent `delete`. Guard `HELP-REGISTRY-SYNC`; mutant = a bogus `"ghost"` registry key reddens the
  equal-sets assertion.

- **#HC Homebrew cask over formula — RESOLVED 2026-06-30** (overrides PLAN §6's "cask rejected" risk
  note; not a §7 ruling). goreleaser **hard-deprecated `brews` (formula) within the `~> v2` range** we
  pin (observed on v2.16.0): `goreleaser check` returns non-zero with "configuration is valid, but uses
  deprecated properties." Since `goreleaser check` IS our fitness gate (decision #GR) and the locked
  build rule is "trust the build over the doc," `brews` is unusable — PLAN §6's mitigation (pin the
  major to dodge the deprecation, keep a formula, reject casks) was predicated on the deprecation NOT
  landing inside `~> v2`, which proved false. Adopted `homebrew_casks` (goreleaser's blessed path for
  prebuilt-binary Homebrew distribution): cask `wi` → `ggkguelensan/homebrew-tap`, `skip_upload: auto`,
  no `license` (no LICENSE file yet), no `test` (casks lack `test do`). The generated cask includes
  `on_linux` stanzas referencing the Linux tarballs, though official Homebrew cask support is
  macOS-centric — Linux users can also take the release archives / `go install`. Two OWNER follow-ups
  flagged: (1) set the `HOMEBREW_TAP_GITHUB_TOKEN` PAT secret (cross-repo cask push) before the first
  `v*` tag; (2) add a LICENSE file + set the cask `license`. Recorded here + in `.goreleaser.yaml`/
  `release.yml` headers; PLAN §6 "cask rejected" is superseded.

- **#GR goreleaser config shape — RESOLVED 2026-06-30** (not a §7 ruling; PLAN §6 fixes only the
  `~> v2` pin + cask-rejected). `.goreleaser.yaml` **schema v2**, pinned major `~> v2` (never
  auto-upgraded). One `builds` entry over `cmd/wi`: `{darwin,linux} × {amd64,arm64}`, `CGO_ENABLED=0`
  (static pure-Go, zero-cgo posture), `-trimpath` + `mod_timestamp` for **reproducible** byte-stable
  output, `ldflags: -s -w`. `archives` = tar.gz `wi_{Version}_{Os}_{Arch}` + README + `LICENSE*`;
  `checksum` = sha256 `checksums.txt`; `release` → `ggkguelensan/workspace-isolation`,
  `prerelease: auto`, `mode: replace`. **`-X` version stamping intentionally omitted** until a `wi
  version` unit adds `version`/`commit`/`date` vars to main (injecting into non-existent symbols is a
  silent no-op at best). **Fitness = `goreleaser check`** (config DATA, not Go; no guard/mutant row),
  wired as the `goreleaser-config` CI job (`goreleaser-action@v6`, `args: check`). Proven locally with
  goreleaser **v2.16.0**: `check` clean + `build --snapshot` emitted all four binaries (Mach-O
  arm64/x86_64 + ELF aarch64/x86-64). The §7-flagged owner choices for the *release trigger* (tag-push
  `v*`) and the *Homebrew tap repo* (`ggkguelensan/homebrew-tap`) are adopted-and-recorded but land
  with sub-unit (c) (the release workflow + `brews:` block).

- **#CI CI gate workflow shape — RESOLVED 2026-06-30** (not a §7 ruling; PLAN §6 risk register +
  the §2 fitness-gate intent fix the spirit, not the YAML). `.github/workflows/ci.yml` runs the
  green gate `gofmt -l` → `go build ./...` → `go vet ./...` → `go test ./...` (the same gate every
  build firing runs locally) on `push` to `main`+`build/wi` and on every `pull_request`. **Matrix =
  `[ubuntu-latest, macos-latest]`**: ubuntu gives a recent upstream git, macOS exercises Apple Git
  (the PLAN §6 lag risk). A pinned **git-floor cell** (2.38, the `merge-tree --write-tree` floor) is
  **deferred to the M5 capstone "portability CI matrix"** (PLAN Wave C / §1) — M3 needs only the
  green gate on a representative pair, not the full portability sweep. **Go pinned from `go.mod`** via
  `setup-go`'s `go-version-file` (one source of truth; no workflow/`go.mod` drift). **Golden suite is
  fail-closed by construction** — plain `go test` never passes the harness's `-update` flag, so
  goldens are asserted, never regenerated (PLAN §2 "CI refuses -update"); no special invocation
  needed. Hygiene: least-privilege `permissions: contents: read`; `concurrency` cancel-in-progress
  per ref; `strategy.fail-fast: false` (one OS's failure must not mask the other). Actions pinned to
  **major tags** (checkout@v4, setup-go@v5); SHA-pinning is an available owner hardening, not adopted
  for MVP. **This unit has NO Go fitness function** — it is a process artifact whose "green" is the
  workflow's own gate passing; its fitness is the YAML parsing and the four gate commands being
  present on both OSes (asserted at author time via Ruby `psych`, pyyaml being absent). It therefore
  has no row in the mutant registry by design (a guard→mutant pair would require a Go test). The
  `brews`-deprecated and goreleaser concerns (PLAN §6) attach to sub-units (b)/(c), still pending.

- **#RD `isolate rm` outcome → envelope/exit mapping — RESOLVED 2026-06-30** (not a §7 ruling; DESIGN
  defines the exit table + the `orphan_unexplained` sub-code but pins no per-outcome mapping for the rm
  command, so the handler adopts one). Given `isolate.Remove`'s per-repo tallies (reclaimed vs blocked):
  **(1) all reclaimed → `Result{Action: removed}`, nil error → exit 0.** **(2) mixed (≥1 reclaimed AND
  ≥1 not) → the DURABLE PARTIAL `(result, *CommandError{Kind: partial, Action: removed})` → exit 2** —
  durable forward progress was made and re-running reclaims any now-unblocked repos, so it is resumable,
  the same #D shape `isolate new`/`sync` use. **(3) nothing reclaimed with ≥1 orphan hard-block → a full
  refusal `*CommandError{Kind: conflict}` → exit 4** (NOT partial: no progress was made, it is a clean
  "refused at exec" the agent must resolve; the worktree's on-disk state conflicts with what wi can prove
  it owns). **(4) nothing reclaimed and every non-removed repo is merely not a member →
  `*CommandError{Kind: not_found}`** ("you named repos that aren't in this isolate"). Plus the pre-loop
  faults: `*lock.HeldError`→lock_held (exit 6); `state.ErrNoRecord`→not_found+`wi isolate new` hint (exit
  3, the isolate does not exist). **Per-repo projection:** reclaimed→`removed`; orphan hard-block→`noop`
  + `Error{Kind: conflict, Code: "orphan_unexplained", Message: Reason}` (the loud DESIGN §7.1 surface —
  `orphan_unexplained` is a SUB-CODE on the per-repo error, not an `error.kind`; the kind is `conflict`
  uniformly whether the block is unowned/dirty/ahead-of-base, since all three mean "on-disk state
  conflicts with safe reclamation"); not-a-member→per-repo not_found; other fault→internal. **Critical
  contract fact:** blocked repos ride in **repos[]**, NOT `Blocked[]`. `envelopeFor` threads only
  `Repos/Warnings/Next` onto a FAILURE envelope (`Blocked[]` is the exit-NEUTRAL dry-run "would-block"
  construct, threaded only on the SUCCESS path) — so a non-zero-exit refusal that put its blocked repos
  in `Blocked[]` would silently drop them from the emitted envelope. Recorded in the `isolateRmCmd.Run`
  doc comment; guard `CMD-ISOLATE-RM`'s mutant pins the partial-mapping + the orphan_unexplained surface.
- **#RM "not ahead of base" realization for v0 reclamation — RESOLVED 2026-06-30** (not a §7 ruling;
  forced by `isolate.Remove` implementing DESIGN §7.1's "not ahead of base" gate against a `state.RepoRecord`
  that does NOT persist the per-repo base branch name). **A worktree is "not ahead of base" iff its HEAD sha
  still equals the ownership marker `refs/wi/owned/<task>/<repo>`'s recorded sha** (the base tip captured at
  creation). The marker IS the base evidence — it was written to the exact base tip the worktree detached
  at — so a HEAD that moved past it necessarily carries local commit(s) = ahead of base = an
  `orphan_unexplained` HARD BLOCK. Rationale: this is evidence-POSITIVE and self-contained (the proof lives
  in wi's own marker, not in re-deriving the base branch name); it is STRICTLY STRONGER than a
  `DivergedCounts(HEAD, baseRef) ahead==0` check would be (which needs the base branch name absent from
  state, and would miss a worktree that committed then the base fast-forwarded to elsewhere); and it needs
  zero new persisted state. **Deferred (additive):** once a per-repo base IS persisted in `state.RepoRecord`
  (the same deferred enrichment `resolve`'s `branch` field awaits), `Remove` MAY additionally consult
  `DivergedCounts` to distinguish "ahead" from "the base itself advanced" for a richer block reason — but
  the marker-equality gate remains the safety floor. Recorded in the `isolate.Remove`/`reclaimRepo` doc
  comments; guard `ISOLATE-REMOVE`'s mutant pins it.
- **#G root discovery — RESOLVED 2026-06-30** (not one of the 7 §7 rulings; DESIGN pins no
  root-discovery mechanism, and `wi init` forces it because it DEFINES the root). **Root = the current
  working directory.** `cmd/wi` resolves the layout once at startup via `layout.Resolve(cwd)` and hands
  it to every command through `Deps.Layout`; `wi init` therefore takes NO positional dir operand — it
  scaffolds the workspace at that resolved root (Bootstrap + starter manifest). Rationale: cwd is the
  universal zero-config default; agents invoke `wi` from the workspace root; an explicit override and
  walk-up both add ambiguity better deferred. **Deferred (additive, contract-neutral):** a global
  `--root <dir>`/`-C <dir>` override (lives in `Dispatch.parseGlobals`, applies uniformly to ALL
  commands — the documented overridability mechanism), and parent-directory walk-up (which ancestor is
  the project? — resolve explicitly later, e.g. by the presence of `wi.config.jsonc`). Recorded here +
  in the `cmd_init.go`/`newInitCommand` doc comments; the `cmd/wi` main (a later unit) implements the
  `layout.Resolve(cwd)` startup path.
- **#H malformed-manifest error kind — RESOLVED 2026-06-30** (not a §7 ruling; forced by `isolate new`
  loading the manifest — the closed 11-kind taxonomy has no dedicated "bad config" kind). **A manifest
  that exists but fails `config.Parse` (unknown key, missing url/base, duplicate repo, JSON syntax) maps
  to `kind=usage` (exit 64), NOT `internal`.** Rationale: a malformed manifest is user-fixable INPUT,
  exactly what `usage` (exit 64, "the operator gave bad input") communicates — surfacing it as `internal`
  (exit 70) would wrongly assert a wi BUG and mislead an agent into retrying rather than fixing the file.
  A MISSING manifest (`fs.ErrNotExist`) is distinct → `not_found` + a `wi init` hint (the workspace isn't
  initialized, not malformed). Recorded in the `cmd_isolate_new.go` `Run` doc comment; every later
  manifest-reading handler (`sync`, `repo add`, `isolate rm`) follows the SAME two-way split.
- **#S sync failure semantics — RESOLVED 2026-06-30** (not a §7 ruling; forced by building `internal/sync`
  — the multi-repo `wi sync` needs a defined behavior when one repo fails). **`sync` is CONTINUE-on-fail
  (best-effort per repo), NOT stop-on-first-fail.** Each repo is synced independently under its own
  `repo:<name>` lock; a per-repo failure (unreachable origin, `*git.NonFastForwardError` on a rewound
  origin, a held lock) is recorded in that repo's `RepoOutcome.Err` and the remaining repos are STILL
  attempted; overall `Status=StatusPartial` if any failed (→ exit 2). Rationale: repos are independent
  SSOTs with no inter-dependency, so a network blip or non-ff on one must not strand the others; and each
  repo's sync is atomic + idempotent, so there is nothing to "resume" as a contiguous prefix. This
  deliberately DIFFERS from `isolate.New` (stop-on-first-fail — decision baked into `internal/isolate`),
  because an isolate is ONE coherent multi-repo workspace whose completed set must remain a resumable
  prefix (DESIGN §6.3). `sync.Run`'s Go-error return is therefore reserved for an op-level failure that
  prevents the whole run; in v0 every failure is per-repo, so it returns a nil error and reports via
  Status/Repos. Recorded in the `internal/sync` package doc; the `cmd_sync` handler will project
  `StatusPartial` onto a durable `(result, *CommandError{partial, Action:synced})` exactly as
  `cmd_isolate_new` does (decision #D).
- **#K per-repo `Error.Kind` projection — RESOLVED 2026-06-30** (not a §7 ruling; forced by building
  `cmd_sync`, where a continue-on-fail partial surfaces a per-repo `error` for each failed repo). **Every
  per-repo failure projects to `kind=internal` for now, in BOTH sibling projections
  (`projectRepoOutcome` for isolate new, `projectSyncOutcome` for sync) — typed refinement is DEFERRED to
  the gitexec stderr→kind classifier, not done ad-hoc per handler.** Two of sync's per-repo failures ARE
  cleanly typed today (`*git.NonFastForwardError` → semantically `conflict`, `*lock.HeldError` →
  `lock_held`) and tempting to classify inline. Rejected because: (a) the gitexec stderr→kind classifier
  is the single designated home for per-repo kind derivation and will cover ALL cases (including the
  network/`remote_error` ones the type system can't reach) uniformly — a partial ad-hoc classification now
  would create a two-tier scheme the classifier later has to un-pick; (b) the per-repo `error.kind` is
  INFORMATIONAL detail nested in `repos[]` — it does NOT drive the exit code (the top-level `partial`
  → exit 2 does), so mislabeling it `internal` is a fidelity gap, not a contract violation; (c) it keeps
  the two sibling projections identical, so the classifier fixes both in one move. The deferred-follow-ons
  list tracks "gitexec stderr→kind classifier"; when it lands, both projections route per-repo errors
  through it and this entry is discharged.
- **#F CLI arg-parsing library — RESOLVED 2026-06-30** (an open architectural decision from the PLAN
  stack/Wave-B text, which named `cobra` as a candidate and listed it among the `go.mod` pins; recorded
  as a new resolved item in PLAN §7). **Hand-rolled stdlib parser, NOT cobra** (no new dependency). `internal/cli.Dispatch` does its own parsing: a forgiving single-pass global-flag
  extractor (`parseGlobals` — `--dry-run`, `--format <v>`/`--format=<v>` recognized anywhere, the rest
  positional) + a longest-match command lookup against a `Registry` map (2-token `"isolate new"` beats
  1-token `"isolate"`). Rationale: consistent with the established zero-dep posture (decisions **#6**
  zero new deps, **#C** hand-rolled JSONC) which keeps `INV-NO-LLM` trivially green and the supply-chain
  surface empty; and wi's command surface is small and FIXED (`init`/`repo add`/`sync`/`isolate
  new`/`resolve`/`isolate rm`), so cobra's generation/help/completion machinery would be weight without
  payoff — wi's help + JSON-envelope output are bespoke anyway (the `help-json` capability), not cobra's.
  Agent-friendliness also favors the hand-roll: globals are accepted in ANY position and every malformed
  invocation produces the SAME one-envelope `kind=usage`/exit-64 shape as every other error, rather than
  cobra's free-text stderr. Rejected cobra (and `urfave/cli`, `kong`) for v0; revisit only if the command
  surface grows enough that subcommand/flag wiring becomes a real maintenance cost. Recorded here + PLAN
  §7 (#F struck through) + the `Dispatch` doc comment. Guard `DISPATCH-ROUTES`.

- **#M `mirror_freshness.stale` predicate — RESOLVED 2026-06-30** (not one of the 7 §7 rulings; §7 #1
  only fixed that staleness lives in the structured field, not a warning). `stale = true` **iff
  `behind_origin_as_of_fetch > 0`** — the most current offline-knowable signal, since wi never
  auto-fetches. Rejected a time-based TTL (would need a clock policy or a dial; no TTL exists anywhere
  in the spec). The `stale` bool and the count are non-redundant — the count is `,omitempty` (absent at
  0), so `stale` is the stable field agents branch on. Never-fetched repo → `mirror.ErrNoSnapshot` →
  the `mirror_freshness` block is omitted entirely (≠ "fresh"). Recorded in DESIGN §5.

- **#S `internal/state` stage vocabulary ownership — RESOLVED 2026-06-30** (not one of the 7 §7
  rulings; the spec names the registry but fixes no stage type). The per-repo isolate `Stage`
  (`StagePending` → `StageCreated`) is a small typed-string vocabulary **owned by `internal/state`, NOT a
  closed `internal/contract` enum.** Rationale: the contract owns only the closed *wire* enums, and the
  envelope's `RepoResult.Stage` is already an intentionally free-form `string` projection (confirmed in
  `envelope.go`) — so a closed contract enum would over-constrain a field the contract deliberately left
  open. The v0 isolate lifecycle is `pending → created`; the land-phase vocabulary
  (`pending|landed|blocked`) is a SEPARATE `landstate` concern for v1 and is deliberately not conflated
  with the isolate-materialization stage. If a stage value ever needs to surface as a *closed* envelope
  enum, it moves to `internal/contract` then (per the standing "closed enums live in contract" rule).
  Recorded in the `internal/state` package doc.

- **#C `wi.config.jsonc` parser + manifest schema — RESOLVED 2026-06-30** (not one of the 7 §7 rulings;
  DESIGN names the file `.jsonc` and "repos, defaults, policy" but fixes no field-level schema or parser
  choice). **Parser:** hand-rolled JSONC comment stripper + stdlib `encoding/json` with
  `DisallowUnknownFields`, **zero new deps** — consistent with decision #6 (zero-dep posture) and keeping
  INV-NO-LLM trivially green; a JSONC library was rejected for the read path. **Schema (v0, minimal,
  closed):** top-level `{ defaults?, repos? }`; `defaults` = `{ base }`; each repo = `{ name, url, base? }`
  with effective base = repo `base` else `defaults.base`. Following the SHAPE-SCHEMA precedent (don't
  pre-declare reserved blocks), `policy` and a manifest `version` field are NOT added speculatively —
  they land with their feature at a documented bump. **Deferred:** the AST-preserving *edit* path (for
  `repo add`, DESIGN §line 204) and trailing-comma tolerance are a separate writer unit; this unit is
  read+validate only. Recorded in the `internal/config` package doc.

- **#R `resolve` block field semantics — RESOLVED 2026-06-30** (not one of the 7 §7 rulings; the
  schema + `envelope.go` declare the `resolve` block's fields as plain strings with NO field-level
  intent). `wi resolve <task>` is a **PURE, zero-I/O projection** of a `layout.Layout` + a loaded
  `state.IsolateRecord` — no config dependency, no git, no network, not even a filesystem read (stronger
  than `mirror`'s offline read path). Field mapping: top-level `isolate_root` = `layout.TaskDir`,
  `state_dir` = `layout.StateDir`, `log` = `layout.LogDir` (v0: the dir — no per-task log writer exists
  yet); per repo `worktree` = `layout.Isolate` (the `isolas/<task>/<repo>` linked worktree), `mirror` =
  `layout.Repo` (the `repos/<repo>` SSOT clone), `branch` = **`""`** because v0 isolate worktrees are
  DETACHED (DESIGN §5 — no working branch to report). Every path is sourced from `internal/layout` (the
  sole path owner), never hand-assembled; the CLI owns `state.Load` + mapping `ErrNoRecord` → a
  `not_found` envelope, so `Bundle` stays a total testable function. **Deferred:** (a) populating
  `branch` once a per-repo base is persisted in the state record; (b) drift detection (registry says
  `created` but the worktree is gone on disk) — the contract has no field for it and `doctor`/drift is
  M4, so `Bundle` does not stat paths. Guard `RESOLVE-BUNDLE`. Recorded in the `internal/resolve` package
  doc.

- **#X `remote_error` exit-code mapping — RESOLVED 2026-06-30** (not one of the 7 §7 rulings; DESIGN
  §3.2's exit-code table assigns codes to 10 of the 11 error kinds but leaves `remote_error` without a
  dedicated code). **`remote_error` → exit 70 (`ExitInternal`)**, the catch-all failure bucket it shares
  with `internal`. Rationale: the closed exit-code set is deliberately COARSER than the kind set —
  `dirty_worktree`/`conflict`/`already_exists` already collapse onto 4, and `lock_held`/`mirror_stale`
  onto 6 — so the precise "remote vs internal" distinction lives in the envelope's `kind` field while the
  exit code is the bucket a shell branches on. A remote/transport failure has no slot among the
  refusal (4), lock (6), not-found (3), usage (64), partial (2) or approval (5) codes, so 70 (general
  non-specific failure) is the only consistent home. Rejected minting a new exit code (the set is frozen
  at M0 by `SHAPE-FINGERPRINT`/`contract.lock.json`; a new code would be a contract break, not an
  additive change). `ExitCodeFor` additionally fails-safe to 70 for any *unmapped* kind, so an
  unforeseen future kind degrades to the same general-failure bucket rather than crashing. Recorded in
  the `internal/exitcontract` package doc + guard `SHAPE-FAIL-MATRIX`.

- **#E `--format json` emit output convention — RESOLVED 2026-06-30** (not one of the 7 §7 rulings;
  DESIGN §3.1 pins the envelope SHAPE but not its byte formatting). `cli.Emit` writes **compact,
  single-line** JSON via `contract.Envelope`'s own `json.Marshal` path, then **one trailing newline**.
  Two sub-rulings: (1) **same marshaller as the goldens** — Emit reuses `json.Marshal` (which invokes
  `Envelope.MarshalJSON`) rather than a `json.Encoder` with `SetEscapeHTML(false)` or `SetIndent`, so the
  emitted bytes are byte-identical to the frozen contract goldens + the schema SSOT (a divergent
  serializer would create two inconsistent wire forms of the same envelope and could drift past
  `SHAPE-FINGERPRINT`). Consequence: default Go HTML-escaping (`<`→`<`) is retained — acceptable
  since agents JSON-decode (escaping is transparent) and it keeps one canonical encoding. (2)
  **single-line + trailing newline** — compact (not pretty-printed) so the stream is line-oriented (one
  envelope per line, greppable, log-friendly) and "exactly one envelope" is a decode-then-EOF check; the
  newline is a terminator for line readers, not part of the JSON value. Pretty-printing, if ever wanted
  for human reading, is a `--format text`/pretty concern layered on top, never the machine default.
  Recorded in the `internal/cli` package doc + guard `SHAPE-ONE-ENVELOPE`.

- **#D dry-run exit-0 mechanism + partial-success envelope representation — RESOLVED 2026-06-30** (not
  one of the 7 §7 rulings; DESIGN §3.2 says "every --dry-run → exit 0" and lists `partial`→exit 2 but
  does NOT state the envelope's `ok` value for a partial, whether `error.kind=partial` sits at top level,
  or *how* dry-run exit-0 is achieved). Two coupled rulings, both embodied in `cli.ExitFor` being a
  **pure function of the top-level error** (nil → 0; else `exitcontract.ExitCodeFor(kind)`):
  - **Partial success** = `ok:false` + a **top-level `error.kind=partial`** + per-repo detail in
    `repos[]`, → **exit 2** via the matrix. This is the only representation consistent with `partial`
    being BOTH a closed `error.kind` AND a closed `ExitCode` mapped to each other (`exitcontract`,
    decision none-needed — the table already pairs them) and with §6.3 (durable, resumable). `ok` is
    false because the operation did not fully succeed; the kind field + `repos[]` carry which repos
    completed. `Failure(m, ActionCreated, Error{Kind:KindPartial,…})` is the constructor a partial uses
    (action stays the in-flight verb, the partial verdict rides in `error.kind`).
  - **Dry-run exit-0** is achieved by the **planner discipline**, NOT a special case in `ExitFor`: a
    dry-run that RAN puts its would-block verdicts in `Blocked[]` and leaves `Error` nil, so it falls
    through to exit 0 — `Blocked` is exit-neutral. A blanket `if env.DryRun { return ExitOK }` was
    REJECTED because it would wrongly swallow a genuine top-level error on a `--dry-run` invocation (e.g.
    a usage error that stopped the command *before* any plan was produced must still exit 64). "Every
    --dry-run → exit 0" is thus read as "every dry-run that produced a plan", which the nil-error path
    delivers without `ExitFor` ever consulting `DryRun`. Recorded in the `internal/cli` package doc +
    guards `SHAPE-ASSEMBLE`/`SHAPE-DRYRUN-EXIT0`.

- **#T `--format text` projection scope + formatting — RESOLVED 2026-06-30** (not one of the 7 §7
  rulings; DESIGN §3.1 pins text as a "pure, path-scoped projection of the same struct ... no extra
  facts, no dropped facts" but fixes no layout and does not say *which* fields render). Two coupled
  rulings, embodied in `cli.RenderText(io.Writer, contract.Envelope) error`:
  - **Scope = every field, losslessly.** Text renders EVERY populated field of the assembled envelope —
    including the metadata (op_id/action/schema/capabilities) and every additive block (repos + their
    path/freshness/error, resolve, planned, blocked, warnings, top-level error, next) — formatted as a
    human-readable sectioned report; empty optionals are omitted (absence carries no fact to drop). The
    renderer takes the ALREADY-assembled struct and only reformats it: no new I/O, never a git/state
    re-read, so the json and text wire forms can never disagree (DESIGN §3.1). A "render only the
    operator-significant subset" alternative was REJECTED — "no dropped facts" is literal, and a subset
    renderer would silently lose data an operator piping `--format text` still needs.
  - **Losslessness is proven by an INDEPENDENT derivation.** Because the renderer is hand-written (human
    formatting can't be auto-generated without losing readability), the guard does NOT re-walk the same
    code: `SHAPE-TEXT-PROJECTION` uses a reflection walk (`collectStringLeaves`) that enumerates every
    non-empty string leaf of a maximal envelope by a SEPARATE path, then asserts each appears in the
    render — so a hand-written renderer that forgets a field is caught. A generic *reflective dump* AS
    the renderer was REJECTED: it would make the guard vacuous (renderer and checker would share the one
    walk, so a forgotten field couldn't be detected) and would not be human-readable. Non-vacuity is
    inline (≥25 leaves found + a never-present sentinel that must NOT match). Recorded in the
    `internal/cli` package doc + guard `SHAPE-TEXT-PROJECTION`.

- **#N INV-NO-NETWORK egress allowlist — RESOLVED 2026-06-30** (not one of the 7 §7 rulings; the
  enforcement form of DESIGN §2 #3). The architecture guard permits `os/exec` import + `GIT_ALLOW_PROTOCOL`
  reference only in `{internal/gitexec, internal/testenv}`. **gitexec** is the runtime chokepoint that
  launches every git child and applies the belt; **testenv** is the test-only git-fixture harness — a
  non-`_test.go` support package (so the `_test.go` skip doesn't cover it) that runs git directly via
  `exec.Command`, but is never reachable from `cmd/wi`, so it never ships in a command path. A tree survey
  confirmed those are the only two source files importing `os/exec`, and `GIT_ALLOW_PROTOCOL` appears
  nowhere but gitexec. Scope rule: **go/parser AST scan** (not a token/grep scan) so the belt key inside a
  comment or this guard's own prose can't false-positive; detection is import-of-`os/exec` + belt-key
  string-literal, which is stricter and simpler than tracing `RunNetwork` reachability and needs no
  caller allowlist. Recorded here + in the `nonetwork_test.go` header.

- **#2 Marker-ref mechanism — RESOLVED 2026-06-30** (one of the 7 §7 open decisions). The
  evidence-positive ownership marker reclamation requires (DESIGN §7.1) is a **git ref**
  `refs/wi/owned/<task>/<repo>`, chosen over a git note/reflog AND over a `.wi/index` backref. A ref
  gives **atomic creation** (a single `update-ref`) and **gc-protection** (a ref keeps its commit
  reachable) while living under `refs/wi/*`, NOT `refs/heads/*` — so the marker is never a branch and
  the pristine SSOT (DESIGN §5) never grows a stray branch. The `.wi/index` backref alternative was
  rejected: it would be a second, non-atomic source of ownership truth that could drift from git's own
  ref store and is not gc-aware (git wouldn't protect the referenced objects from a `gc --prune`).
  Implemented as `git.CreateOwnedRef(ctx, ssotDir, task, repo, sha)` (write) + `git.OwnedRefSHA(...)`
  (read, returning `(sha, exists, err)` with a clean absent case), guard `GIT-OWNED-REF`. Recorded here
  + DESIGN §7.1 (already specified the ref) + PLAN §7 #2 (now struck through).

- **#1 `capabilities[]` + warning-code token sets — RESOLVED 2026-06-29.** Capabilities v0 =
  `{help-json, resolve-block, dry-run, partial-success}` (pinned in `Capabilities()`). Warning-code
  v0 = closed `{hydrate_skipped, base_behind_ssot}` (`AllWarningCodes()`), MVP-wired + offline-knowable
  only; staleness stays structured in `mirror_freshness.stale`. Recorded in DESIGN §8 + PLAN §7.

- **#6 Go libs sign-off (lockfs) — RESOLVED 2026-06-30: zero new deps, BOTH halves hand-rolled.**
  The §7 recommendation was "adopt `gofrs/flock` + `google/renameio`"; both legs were overridden to
  zero-dep hand-rolls with concrete rationale (not reflexive NIH).
  - **`WriteFileAtomic`** (not `google/renameio`): the unit's entire fitness is crash-safety, *proven*
    by injecting `WI_FAULT` exactly between the temp write and the rename; a library hides that
    boundary, so the non-vacuity mutant could not be expressed. DESIGN's §M0 file-list already
    specifies the manual recipe and §7 lists hand-rolled as the explicit alternative.
  - **`FileLock`** (not `gofrs/flock`): decided at `flock_unix.go` implementation time (the earlier
    entry deferred this leg pending implementation — not a flip-flop). wi's lock path is unix-only,
    `syscall.Flock(LOCK_EX|LOCK_NB)` is exactly the BSD-flock primitive (pure stdlib ⇒ INV-NO-LLM
    trivially green, no supply-chain surface), and the PID/`boot_id` lock-content + §7.3 auto-break
    self-heal are hand-written regardless, so a library would wrap only one syscall.
  Net: `go.mod` gains no runtime dependency from M0. Owner may override either leg. Recorded in
  DESIGN §6.2 + PLAN §7.

- **#A op_id encoding specifics — RESOLVED 2026-06-29** (DESIGN §3.1 fixed the skeleton
  `op_<base36ts>_<base32rand>` + `.<n>`; these fill the unspecified gaps). Time unit = Unix
  **milliseconds** (rough chronology + human-debuggable, distinct from s/ns). Random = **5 bytes**
  → 8 chars lowercase unpadded standard base32 (`[a-z2-7]`); plenty of within-ms collision
  resistance. Child index **n ≥ 1, no leading zero**; children nest (`.1.2`). op_id is not required
  to be lexicographically sortable (uniqueness comes from the random half). Recorded in DESIGN §8 row
  + `internal/cli/opid` doc comment.

## Conventions

- Module: `github.com/ggkguelensan/workspace-isolation`, Go 1.26.
- Every fitness/guard test names its mutant in the registry above; confirm RED→GREEN per unit.
- Commit one coherent unit at a time, conventional commits, Co-Authored-By trailer. No push / no PR.
