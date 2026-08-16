# Pilot events — first managed-delivery exercise

The events worth recording from the first live portal → Gas City delivery, in
the shape a pilot review needs: what happened, whether a person or the system
did it, and what it cost.

This is a record, not a telemetry subsystem. The durable machine-readable copy
lives beside the run it describes, at
`<deliveryRoot>/<projectId>/pilot-events.jsonl`, written in the same directory
the run layer already owns. Nothing reads it automatically yet, and nothing
should until there is a consumer that needs it.

Project: `corsolv-managed-delivery-test-08141643`
Date: 2026-08-14

| Time (UTC) | Event | Package | Actor | Reason | Outcome | Mins |
| --- | --- | --- | --- | --- | --- | --- |
| 16:44 | `MANUAL_EXTERNAL_STEP` | — | human | Delivery host profile written; planner and forge CLI declared by absolute path | host ready | 3 |
| 16:47 | `COMPLETION_EVIDENCE` | — | automatic | Seeded CI ran on bootstrap `cba2139` | required check green from the project's first commit | 1 |
| 16:48 | `PR_RECONCILIATION` | — | automatic | Start accepted; duplicate Start returned the same delivery; changed terms refused 409 | idempotency and conflict detection held | 1 |
| 16:49 | `PROVIDER_BLOCKED` | — | automatic | `claude -p` → "You've hit your monthly spend limit" | planning could not run | 2 |
| 16:53 | `WORK_PACKAGE_REPLAN` | `wp-slugify` | human | Plan installed by hand via `delivery plan -from`, through the same validator an agent's plan faces | 1 bounded package accepted | 4 |
| 16:55 | `WORKER_RETRY` | — | automatic | `city-up` failed: `gc init` non-zero because a machine-wide supervisor already owned the port | driver corrected to judge from state, not exit code | 3 |
| 16:57 | `WORKER_RETRY` | — | automatic | `dispatch` failed: objective exceeded the 500-char bead title cap | bead reshaped — title is a label, objective is the description | 2 |
| 16:59 | `RECOVERY` | `wp-slugify` | automatic | City, rig and worker agent created; beads `r2-ghj` / `r2-b4s` created and routed | dispatch complete | 2 |
| 17:10 | `HUMAN_INTERVENTION` | — | human | Deliberate interruption test: SIGKILL of pid 1966225, matched against the recorded `writerPid` | only the owned process was signalled | 1 |
| 17:10 | `RECOVERY` | — | automatic | Restart through the normal path; `city-up` and `dispatch` each completed in 0s from the runtime ledger | resumed without replaying completed work | 2 |
| 17:12 | `WORKER_FAILURE` | `wp-slugify` | automatic | No worker could execute (provider blocked); required artifact `src/slugify.ts` absent | publication refused — no commit, branch, PR or merge | 2 |
| 17:12 | `COMPLETION_EVIDENCE` | — | automatic | Evidence gate assessed: 1 of 1 packages incomplete, `ac-1` unmet | delivery correctly NOT reported complete | — |
| 22:1x | `APPROVAL_REQUIRED` | — | human | Engine branch push blocked by nine expired `runtime.Provider` waivers (`ga-80po0c.3`, expired 2026-08-12) | publication lane blocked; branch preserved, no gate weakened | — |

## What the events say

Two of them are the point of the exercise.

**`WORKER_FAILURE` → publication refused.** The worker produced nothing, and
every downstream stage declined rather than degrading. No empty commit, no
branch, no pull request, no merge, and above all no Completed. A system that
reports success when its worker did nothing is worse than one that fails.

**`HUMAN_INTERVENTION` → `RECOVERY` in 0s.** The interruption was a hard kill of
the exact recorded writer, and the restart resumed from the runtime ledger
without replaying a single completed stage. That is the property that makes
unattended delivery survivable.

The two `WORKER_RETRY` rows are both driver defects found by running rather than
reading, and both are fixed. Neither would have been found by review.

## Correction: the 17:10 recovery was not a recovery

The row above reads as a success and was recorded as one. Re-reading the same
run against the driver showed it was not: `dispatch` completed in 0s *because it
did nothing*. Bead `r2-ghj` was still open, its worktree had survived the kill,
and no worker existed — but the runtime ledger's `dispatched` timestamp was
being read as "a worker exists and owns every open bead", so the stage returned
immediately and never started one.

`await` then polled that orphaned bead for its full 30 minutes and reported
**success** when its deadline expired, because an expired deadline was treated
as a survivable outcome rather than a failed wait. Publication refused on the
missing `src/slugify.ts` — the right refusal, for the wrong stated reason, half
an hour after the real event.

So the `WORKER_FAILURE` row's reason is incomplete. The provider block is why no
worker could have produced the artifact; it is not why no worker was started.
Two defects were, and both are now fixed in `driver.sh`: resume re-derives
worker liveness per package instead of trusting `dispatched`, and an expired
await deadline fails the stage. `driver_recovery_test.go` holds the regression
coverage, and every one of those tests fails against the driver as it stood
during this run.

## Second pilot — Website Status Checker Pilot v2

Project: `website-status-checker-pilot-v2`
Date: 2026-08-15

| Time (UTC) | Event | Package | Actor | Reason | Outcome | Mins |
| --- | --- | --- | --- | --- | --- | --- |
| 15:52 | `PR_RECONCILIATION` | — | automatic | Start accepted from the portal; intent recorded with digest `865680d4` | delivery admitted | — |
| 15:54 | `COMPLETION_EVIDENCE` | — | automatic | Planner produced 4 bounded work packages through the validator | plan accepted | — |
| 15:54 | `WORKER_FAILURE` | — | automatic | `city-up` cloned the rig and then died: `driver.sh: line 174: gc: command not found` | run failed in 1s; 1 failed, 8 held | — |
| 16:43 | `WORKER_RETRY` | — | automatic | With gc declared, `city-up` reached `gc rig add` and died there: `bd: not found`, from a script Gas City shells out to | second binary, same cause | — |
| 16:48 | `WORKER_RETRY` | — | automatic | With beads declared, `gc init` halted at provider readiness: `provider "claude" command "claude"` not found, leaving the city built but its pack imports uninstalled | third binary, same cause; late failure read as "the rig's bead store never became ready" | — |
| 17:10 | `RECOVERY` | — | automatic | With all three declared, `city-up` completed: city built, rig cloned, imports installed, 4 bounded worker agents declared | city-up green | — |
| 17:12 | `RECOVERY` | all 4 | automatic | `dispatch` created 8 beads (4 work, 4 controller merge), wired dependencies and slung each package to its worker | dispatch complete | — |
| 17:12 | `APPROVAL_REQUIRED` | all 4 | human | No worker session ever started: the machine-wide supervisor (pid 388, up 5d) holds the API port with no control socket and no children, so `gc supervisor reload` reports it not running | delivery cannot proceed; restarting a shared process is its owner's decision | — |
| 18:01 | `HUMAN_INTERVENTION` | — | human | **Manual operational intervention**: stale shared Gas City supervisor pid 388 restarted by authorised human decision, after a read-only check confirmed zero children, sole ownership of 8372, absent control socket, and only this pilot's city registered | supervisor replaced (pid 3449733); control socket present; `reload` answers | 3 |
| 18:02 | `RECOVERY` | all 4 | automatic | Delivery resumed through the supported path; `city-up` and `dispatch` completed and the supervisor reconciled the city | `worker-wp-scaffold` session active in its own worktree | 1 |

### The defect the second pilot found

**A detached run has no PATH, and two binaries were still relying on one.** The
first pilot found this for the planner and for the forge CLI, and both were
moved into the host profile as declared absolute paths. `gc` was left taking its
chances on PATH, and it is installed under the operator's home rather than in a
system directory, so the very first stage of every delivery on this host was one
line away from failing.

Naming `gc` absolutely got the run one step further, to `gc rig add`, and into
the second half of the same defect: Gas City resolves `bd` by PATH lookup from a
script it shells out to, and no flag can tell it otherwise. So the driver now
exposes the declared binaries — and only those, one symlink each in a directory
the run owns — on the PATH its children inherit. Declared, not inferred, and
without dragging in whatever else shares a folder with them.

Two things made it worse than it needed to be. Preflight reported READY over 27
checks without looking for either, because the spec declared `git`, `tmux` and
`gh` as its tools and not the two that build the city. And the driver sourced
its shared controller primitives from a hard-coded checkout rather than from its
own, so a fix touching both halves could arrive half-applied.

All of it is corrected: both CLIs are declared in the host profile and reach
every stage as `-gc` and `-bd`, preflight requires both by name (29 checks, not
27), and the driver reads its library from the tree it ships in.

### What stopped it after city-up

**A supervisor that exists is not a supervisor that works.** `gc init` registers
the city and then declines to start a second supervisor, correctly, because only
one may own the port — and leaves the city registered for the running one to
pick up. The driver checked that such a process existed and called the city up.

The process that existed had been running since 2026-08-10, held
`127.0.0.1:8372`, and had **no control socket and no children**. `gc supervisor
status` reports it running on an API liveness probe while admitting the control
socket is unreachable; `gc supervisor reload` — the request that makes a
supervisor learn about a city registered after it started — answers *"supervisor
is not running"*. So the city was registered with a process that would never
reconcile it. Dispatch routed all four packages to worker agents that were never
spawned, and the run sat in `await` for its full ninety-minute deadline waiting
for workers that did not exist.

The driver now takes the supervisor's own answer as the verdict, and asks before
dispatch rather than discovering it after: reconciling is both the check and the
step. The failure is declared to the run layer as a `human-decision`, because
restarting a machine-wide process other work may depend on is a judgement this
run is not entitled to make.

**The one action required, and it is a person's:** the stale supervisor must be
replaced —

```
gc supervisor stop      # will not reach it; the control socket is gone
kill 388                # the process started 2026-08-10 that owns 127.0.0.1:8372
gc supervisor start
```

No new supervisor can take over while pid 388 holds the port, which is why
`gc init` was right to refuse and why nothing this run could do would fix it.

### MANUAL OPERATIONAL INTERVENTION

**Stale shared Gas City supervisor pid 388 restarted by authorised human
decision**, 2026-08-15 18:01 UTC, after a read-only check confirmed it still had
zero children, still solely owned `127.0.0.1:8372`, still had no control socket,
and that `~/.gc/cities.toml` held only this pilot's city. `gc supervisor stop`
could not reach it; the process was signalled directly and the service manager
brought up a replacement (pid 3449733) which answers on
`/run/user/1000/gc/supervisor.sock` and honours `gc supervisor reload`.

This is recorded rather than folded into the narrative because of what it costs
the claim being tested. **The remainder of this pilot is not evidence of
zero-intervention autonomy.** It is evidence about the rest of the journey —
worker execution, publication, CI, merge, acceptance — measured on a host a
person had to fix first. The engine change above is what makes the same
condition self-reporting next time, in seconds and by name, instead of ninety
minutes of silence; it does not make it self-healing, and it should not.

### What the worker run then found

Two defects, both reached only because everything before them worked.

**A bounded worker cannot run the gates its own bead requires.** The scaffold
package's objective told its worker to prove itself with `npm install && npm run
verify`. `bounded-project` grants exactly three project commands — `npm run
typecheck`, `npm run build`, `npm test` — and nothing named `install` or
`verify`, so both were refused with *"Permission to use Bash has been denied
because Claude Code is running in don't ask mode"*. The worker wrote all eight
authorised files and closed `blocked` with a precise account of what it could
not do, which is the right answer to an impossible instruction and a useless
outcome for the delivery. There was no path from the plan into the worker's
permissions at all.

A package now declares its `gates`, validated in Go to be bare project runners,
and the run grants exactly those — in that package's own worktree, so nothing is
widened for any other worker. The controller re-runs the same declared gates
before publishing, so "the worker verified it" and "the controller verified it"
mean the same thing.

**One `await` for the whole plan cannot be satisfied by a dependency chain.**
The bead graph is `work → merge → work → merge`: a package waits for its
upstream to be *merged*, not merely finished, and that merge bead is closed by
the controller inside `publish`. A single plan-wide await therefore waited on
three work beads that could not open until a publication that could not start
until the await finished. The run had exactly one ready bead — the controller's
own merge bead — and sat out its full ninety-minute deadline before publishing
anything, then reported "deadline reached with work outstanding" about a plan
that was in fact progressing correctly.

Waiting is now per package, interleaved with publication, and the tree a
dependent package needs is cut where the waiting happens rather than after it.
Nothing new schedules any of this: it is the same queue over the same bead
graph, with the task order finally matching the dependency order it always had.

### The rerun on the fixed engine, and where it stopped

Same project, same repository, same durable state, replanned so the packages
could declare gates. Attempt 1's plan, city, rig and evidence are archived
beside the run in `attempt-1-blocked-worker-gates/` rather than deleted.

| Time (UTC) | Event | Package | Actor | Reason | Outcome | Mins |
| --- | --- | --- | --- | --- | --- | --- |
| 19:34 | `RECOVERY` | — | automatic | `city-up` on the fixed engine; preflight 30 checks | city built, supervisor reconciled | 1 |
| 19:35 | `RECOVERY` | all 4 | automatic | `dispatch` created the beads and granted each package its declared gates in its own worktree | 4 packages routed | 1 |
| 19:44 | `COMPLETION_EVIDENCE` | `wp-scaffold` | automatic | The worker ran the gates it was granted: `npm run lint` clean, `npm run typecheck` clean, `npm test` 11/11 | **a worker verified its own work for the first time in this pilot** | 9 |
| 19:44 | `WORKER_FAILURE` | `wp-scaffold` | automatic | Publication refused: two probe files outside `gc.authorised_paths` that the worker could not delete | containment held; delivery stopped | — |

Both fixes did what they were written for. `await-wp-scaffold` completed in
9m23s rather than burning a ninety-minute deadline, and the gate grant reached
the worker and was used.

### The defect it stopped on: a worker can create a file but not remove one

The scaffold worker wrote two probe files — `public/__lintprobe.js` and
`scripts/__typeprobe.js` — to prove its toolchain really did support the
runtimes the package needs, then found it could not clean them up. In its own
close reason:

> They were meant to be transient, but `rm` and `mv` are both blocked in this
> session, so they remain in the worktree. Neither is in `gc.authorised_paths`.
> Delete them, or publish authorised paths only.

`bounded-project` grants Read, Write, Edit, Glob and Grep. Nothing removes a
file, and nothing can: a cleanup command is not a project build or test runner,
so it cannot be declared as a gate either. Any transient file a worker creates
is therefore permanent, and permanently makes its package unpublishable.

The controller was right to refuse — an unauthorised file is exactly what the
scope check exists to catch — but the worker had no way to comply.

**A second defect is proved alongside it.** `publication_scope_violations` reads
`git status --porcelain` without `-uall`, so untracked directories collapse to a
single entry. The refusal named four violations — `public/ scripts/ src/ tests/`
— when only two files offended; every file under `src/` and `tests/` was
authorised. On that evidence a package that creates any new directory cannot
publish even when it is entirely compliant. Here a real violation and a phantom
one happened to coincide, which is the kind of luck that hides a defect rather
than revealing it.

**And a third thing has no mechanism at all.** The worker raised two rulings it
genuinely needed: a type-only `@types/node` without which `tsc --noEmit` cannot
pass, and `node --test tests` failing with `MODULE_NOT_FOUND` on the Node 24
that `ci.yml` pins — so the test script the plan specified can never go green in
CI. It reported `gc mail send` denied, leaving the bead's close reason as its
only channel to say so.

None of these three is fixed here. They are recorded as found.

### Portal defect observed, not fixed here

**Managed Delivery's "Check now" gives no visible feedback when reconciliation
finds nothing changed.** A user who presses it sees the page as it was and
cannot tell whether the request was made, whether it is still in flight, or
whether it completed and found the state unchanged — which is indistinguishable
from a dead button, and invites the repeated pressing that idempotency exists to
survive rather than to encourage.

That is a portal presentation defect, not an engine one: the engine's answer
("unchanged") is correct and is what the portal read. It is recorded here
because the pilot found it, and it belongs to the portal's own backlog.

## The second rerun: what the publication boundary was actually reading

Two of the three defects above are fixed in `eb904cf0a`. Nothing about the
project changed — same intent, same plan, same city, same rig, same beads, same
worktree with the scaffold worker's completed work still in it. The only change
was to what the controller reads before it publishes.

The phantom violations were a reading error. `git status --porcelain` reports an
untracked DIRECTORY as the directory alone, so the four names in the refusal —
`public/ scripts/ src/ tests/` — were never files any bead could have
authorised. Read with `-uall`, the same worktree at the same moment says:

```
?? public/__lintprobe.js
?? scripts/__typeprobe.js
?? src/config.js
?? tests/config.test.js
```

Two offenders, not four directories, and every file under `src/` and `tests/`
authorised exactly as the bead said. The refusal had been reporting a category
the boundary cannot express.

The two real offenders are handled by giving the removal to whoever can perform
it. A worker under `bounded-project` can create a file and cannot delete one,
and a cleanup command cannot be declared as a verification gate — so a transient
probe is permanent for the worker. It is not permanent for the controller, which
owns the worktree. And an untracked out-of-scope file was never going to be
published in any case: `controller_commit` stages the bead's named paths and
nothing else. The only harm such a file can still do is contaminate the gate run
the controller performs before publishing, so the controller moves it into
evidence, gates the clean tree, and commits that.

A TRACKED file changed out of scope is deliberately not touched. That is
content the project already had; quarantining it would be the controller
silently reverting the worker. It still refuses publication, and the authority
split is unchanged — the worker mutates a working tree, only the controller
publishes, and the controller still looks at what changed first.

### What the second rerun produced

| Time (UTC) | Event | Package | Actor | Reason | Outcome | Mins |
| --- | --- | --- | --- | --- | --- | --- |
| 20:42 | `RECOVERY` | — | automatic | `city-up`, `dispatch` and `await-wp-scaffold` all resumed from durable state | three stages re-entered and returned in 1s | 0 |
| 20:43 | `RECOVERY` | `wp-scaffold` | automatic | Controller quarantined `public/__lintprobe.js` and `scripts/__typeprobe.js` into `evidence/quarantined-wp-scaffold/` | the tree gated and committed contained authorised paths only | — |
| 20:44 | `COMPLETION_EVIDENCE` | `wp-scaffold` | automatic | Declared gates re-run under controller identity; `npm ci`, lint, typecheck and 11 tests clean | committed, pushed, PR #1 opened at `ba1282ee` | 1 |
| 20:44 | `COMPLETION_EVIDENCE` | `wp-scaffold` | automatic | Required job `validate` passed on the exact PR head in 9s | **PR #1 merged — the pilot's first package shipped** | — |
| 20:44 | `RECOVERY` | `wp-status-core` | automatic | `await-wp-status-core` cut the next worktree and launched session `c2-2kl` | **package 2 started because package 1 merged** | — |

`publish-wp-scaffold` took 1m0s end to end, from a stage that had previously
exhausted its attempts. The sequencing the previous fix built is now observable
rather than inferred: work1 → verify1 → publish1 → merge1 → work2, with the
second worker's session created 3 seconds after the first package's merge.

### The third defect stands, and was ruled on by hand

The worker still has no escalation channel; `gc mail send` remains denied to a
bounded worker, and the bead close reason remains its only way to raise a
question. Both rulings it raised that way were correct and are visible in the
shipped package:

- `@types/node` is a devDependency of the merged `package.json`. Without it
  `tsc --noEmit` cannot resolve `node:` builtins and the typecheck gate cannot
  pass.
- the test script ships as `node --test tests/*.test.js`, not the
  `node --test tests` the plan specified. On the Node 24 that `ci.yml` pins,
  the directory form fails; verified directly in the worker's worktree, it
  reports `pass 0 fail 1` against `test at tests:1:1`.

The worker was right twice, and had to spend a bead close reason to say so.
That is recorded as remaining work, not fixed here.

## Website Status Checker Pilot v2 — completed

Same project, same intent, same plan, same city, same rig, same beads. No v3,
no replan, no hand-written application code, and no package closed by hand.

| Time (UTC) | Event | Package | Actor | Reason | Outcome | Mins |
| --- | --- | --- | --- | --- | --- | --- |
| 20:43 | `RECOVERY` | `wp-scaffold` | automatic | Two transient probes quarantined into evidence; declared gates re-run by the controller | PR #1 green on `ba1282ee`, merged | 1 |
| 20:44 | `RECOVERY` | `wp-status-core` | automatic | Worker launched 3s after package 1 merged | worker `c2-2kl` active in its own worktree | — |
| 20:59 | `COMPLETION_EVIDENCE` | `wp-status-core` | automatic | Domain core and its deterministic tests; controller re-ran `npm ci`, lint, typecheck, tests | PR #2 merged as `df182efd9` | 15 |
| 21:16 | `COMPLETION_EVIDENCE` | `wp-web-app` | automatic | HTTP API, browser UI, entry point and README | PR #3 merged as `512367c82` | 17 |
| 21:17 | `WORKER_FAILURE` | `wp-acceptance` | **controller (deliberate)** | Worker session `worker-wp-acceptance-c2-zp8` killed to exercise recovery | session record survived; Gas City read it `asleep` | — |
| 21:19 | `RECOVERY` | `wp-acceptance` | automatic | Replacement worker `c2-jz7` woke on the same bead, branch and worktree | **a killed worker was replaced without human repair** | 1 |
| 21:35 | `COMPLETION_EVIDENCE` | `wp-acceptance` | automatic | `npm run acceptance` drove the real application over HTTP against a local fixture server | PR #4 merged as `72dc547bd` | 16 |
| 21:41 | `COMPLETION_EVIDENCE` | — | automatic | 12 of 12 tasks succeeded; projection rendered and published to `delivery/gascity/PROJECT-STATE.yml` | **delivery completed** | — |

### The recovery, and which mechanism performed it

The interruption was deliberate and pilot-owned: one worker session on the
pilot's own tmux socket, killed with `kill-session` rather than by stopping a
server. Nothing outside this pilot was touched.

What the kill proved first is the defect class the liveness fix was written
for. The session RECORD survived it. `gc session list` reported the killed
session as `asleep`, because it asks the runtime provider whether the process
is really there rather than trusting what the record remembers — so a reader
that trusted the record would have seen a worker that did not exist.

The replacement then arrived **before the delivery run was resumed**. Its
worktree setup ran at 22:19:31 local and the session woke at 22:19:59, against
a resume issued at 22:19:57; the city's standing `nudge-on-route` order is what
put a worker back on routed, still-open work. The driver's own `recover_worker`
ran a second later, found a live worker holding the bead, and declined to route
it again — `route-wp-acceptance.txt` is untouched from 22:18:11, which is the
mechanical proof it did not act. Two independent observers, converging on the
same state without duplicating the work, is the intended behaviour rather than
a coincidence.

Recovery is therefore attributed to Gas City's own reconciliation, not to the
delivery driver and not to a human. `driver_recovery_test.go` holds the
regression coverage for the driver's half.

### The last two defects, both found by finishing rather than by reading

**The projection could never be rendered.** `projector-gen` derives each
package's completion gate from a control ledger and never from its status, and
the delivery driver never wrote one. Every delivery run therefore reached
`project` and died on `open .../evidence/controls.tsv: no such file or
directory` — after all four packages had merged. The driver already adjudicated
all three controls; it now records them, derived from durable runtime state so
a resumed run produces the same ledger as an uninterrupted one.

**The assessment read the wrong document.** A delivery renders two, and both
were called the projection: the run publisher's view of the queue, keyed by run
task id, and the driver's delivery projection, keyed by package id and carrying
each package's gate. `Assess` only ever understood the second and was being
handed the first, so no row could match by construction and a fully merged,
fully gated delivery reported "4 of 4 work packages are not complete". Not an
error — just every package reported outstanding, which is indistinguishable
from work that has not been done.

### What the finished delivery is

```
state: completed
packagesComplete: 4 of 4
acceptanceMet: ac-1 ac-2 ac-3 ac-4 ac-5 ac-6 ac-7 ac-8 ac-9
mergedMainSha: 72dc547bdbce72830ef226e40021280f5b6bf91d
```

Verified independently of the engine, from a fresh clone of the authoritative
branch rather than from any worker's tree: 77 tests pass, lint and typecheck
are clean, and the application starts and serves the journey — add a website,
list it, Check now (`healthy`, HTTP 200, 85ms), restart the process, list it
again with its last check still there, remove it, list empty. Persistence is
proven across a real restart rather than asserted.

The `wp-acceptance` worker proved the same journey its own way, spawning the
application as a child process and driving it over HTTP against a local fixture
server answering 200 and 500, so the result depends on no public internet
service.

### Still unfixed, and deliberately so

The worker still has no escalation channel. Its prompt tells it to run
`gc mail send mayor` when blocked, and `gc mail send` is not in the bounded
worker's allowlist — so the instruction cannot be followed. Both rulings the
scaffold worker raised through a bead close reason were correct and are visible
in the shipped `package.json`: the `@types/node` it added, and the test script
it corrected because the plan's spelling cannot pass on the Node its own CI
pins. It was right twice and had to spend a close reason to say so.
