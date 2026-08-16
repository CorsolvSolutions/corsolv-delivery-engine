# Managed delivery

The seam a management portal starts delivery through, and the mechanics that
answer it.

## The split

A portal knows **what** a project is for: its identity, its repository, the
registered checkout, the business objective, the lifecycle, what would count as
done, and which delivery authorities it is willing to grant. It does not know,
and must not decide, **how** delivery happens — which city gets built, which
worktree a worker is given, when a branch is cut, what proves a gate.

So the wire between them carries a *delivery intent* and nothing else. There is
no field on it that becomes a command line, and a reflection test in
`internal/handoff` walks the type graph to keep it that way. A portal that can
post a command is a remote shell with a project id attached.

## The pieces

| Layer | Owns | Where |
| --- | --- | --- |
| Contract | intent, plan schema, durable record, evidence, state | `internal/handoff` |
| Command | the portal's entry point | `corsolv/delivery/main.go` |
| Host profile | machine-specific facts | `~/.corsolv/delivery-host.toml` |
| Driver | execution mechanics | `corsolv/delivery/driver.sh` |
| Run layer | journal, writer lock, resume | `internal/unattended` |
| Projection | what the portal reads | `delivery/gascity/PROJECT-STATE.yml` |

## Starting delivery

```bash
delivery preflight -intent intent.json     # could it start? starts nothing
delivery start     -intent intent.json     # admit, plan, compile, begin
delivery status    -project <id>           # the canonical state, as JSON
delivery plan      -project <id>           # show the plan
delivery plan      -project <id> -from f   # install one written by hand
```

`start` is idempotent. Pressing it twice reports the existing delivery and
starts nothing; pressing it with *different* terms is refused rather than
silently adopting either reading.

## The host profile

Every machine-specific fact is declared here, never inferred. Moving managed
delivery to another machine is a change to this file and to nothing else.

```toml
deliveryRoot       = "/home/you/corsolv-delivery"
driver             = "/path/to/corsolv-delivery-engine/corsolv/delivery/driver.sh"
githubCommand      = "/mnt/c/Program Files/GitHub CLI/gh.exe"
gascityCommand     = "/home/you/.local/bin/gc"
beadsCommand       = "/home/you/.local/bin/bd"
provider           = "claude"
windowsMountPrefix = "/mnt"
plannerCommand     = "claude"
plannerArgs        = ["-p", "--permission-mode", "plan"]
```

`githubCommand` points into `/mnt/c` because the engine runs under WSL while
the only authenticated `gh` is a Windows install. That is exactly the kind of
fact this file exists to hold.

Every executable a run needs is named here for the same reason. A delivery
detaches into its own process group and does not inherit an interactive shell's
PATH, so a binary installed under a home directory — `gc`, `bd` and the planner
all are — is not there to be found. Preflight checks each of them by the name
given here, which is why an undeclared one is refused before a run starts rather
than discovered by the stage that needed it.

`beadsCommand` is the one the driver never runs. Gas City runs it, by PATH
lookup from a script it shells out to, and will not build a city without it. So
the driver exposes the declared binaries — and only those — through a directory
the run owns, and puts that on PATH for its children.

Planning is normally an agent's job. `delivery plan -from` exists for when a
person already knows the answer, or when the agent runtime is unavailable —
which on a real machine is a Tuesday rather than a hypothetical. An installed
plan faces exactly the same validator an agent's would: the containment rules
are not a property of who wrote the plan. It will not replace an existing plan,
because a delivery part-way through has merged work against the plan it started
with.

There is deliberately no equivalent fallback for worker execution. A controller
that wrote the code itself would be forging the evidence it later checks.

## What happens after Start

1. **Admit** — the intent is validated and recorded. Idempotent.
2. **Plan** — a planning agent turns the objective and acceptance criteria into
   bounded work packages. Go validates the result and refuses anything unsafe;
   it never decides what the work is.
3. **Compile** — intent plus plan become a declarative run spec and work plan
   for `internal/unattended`, which already owns the journal, the writer lock
   and resume.
4. **Execute** — the driver builds a city, clones a working rig, declares one
   bounded worker per package, creates the beads, wires the dependencies and
   routes them. Each package is then waited for and published in turn:
   `await-A → publish-A → await-B → …`, because a dependent package's work bead
   only opens when its upstream's *merge* bead closes, and that happens during
   publication. Workers write code; only the controller commits, pushes, opens
   pull requests and merges.
5. **Project** — the run's own evidence is rendered into a delivery projection
   and published to `delivery/gascity/PROJECT-STATE.yml`, which the portal
   reads from the authoritative ref.

## Resuming an interrupted delivery

Every stage is idempotent, but idempotent does not mean "skip what is already
recorded". The run's `runtime.json` is scratch memory, and its `dispatched`
timestamp is durable *history*: it says routing happened once, never that a
worker still exists. So a resumed `dispatch` re-derives, per package, from state
it re-reads rather than remembers:

| Bead | Worker | What resume does |
| --- | --- | --- |
| closed | — | nothing; the work is done |
| open | a live Gas City session holds it | nothing; a second sling would duplicate it |
| open | none | routes it again, through the same sling that dispatched it |
| open | no worktree yet (upstreams unmerged) | nothing; `publish` cuts its base and starts it there |

Liveness is Gas City's own answer, not a file: `gc session list` asks the runtime
provider whether the session is really running and reports a killed worker's
surviving record as `asleep`.

`await` waits only for the packages it is responsible for — the ones dispatch
gave a worktree. If its deadline expires while any of those is still open, the
stage says so: **CONTINUE**, the work is unfinished. A deadline is not an
outcome, and reporting one as success is how a run reached publication with work
that had never been done — but nor is it a failure, because nothing was proved
wrong. The run re-offers the stage under a bounded resume budget, and holds it
for a person if it never converges.

## What a stage says happened

A stage started by a run is **supervised**: the run exports a path, the stage
states its outcome in a structured document there, and *that statement is the
verdict*. The exit status is not consulted. Both directions of that residue were
produced by the pilot — `gc init` exits non-zero for a condition that is correct
on a host that already has a supervisor, and a wrapper exits zero over work that
was cut off part-way through.

| The stage says | What the run does |
| --- | --- |
| `COMPLETE` | the stage finished its work |
| `CONTINUE` | unfinished, not failed: re-offered without spending a retry, bounded by the task's resume budget |
| `HUMAN_BLOCKED` | stops safely for a judgement this run is not entitled to make — a machine-wide supervisor it may not restart |
| `FAILED` + `authentication_failed` / `permission_denied` | stops safely for a credential, reported apart from a judgement because it is usually seconds of a person's time |
| `FAILED` + `network_timeout` / `rate_limited` | bounded retry under the external-service policy |
| `FAILED` | classified from the output, exactly as an unsupervised command is |
| nothing at all | an absence of knowledge — never a pass. A stage killed at its timeout leaves exactly this |

The vocabulary lives in `corsolv/powershell/controller-result.contract.json`, the
one document all three implementations are checked against: the Go consumer
(`internal/unattended/controller.go`), the PowerShell producer, and this driver's
producer (`controller-contract.sh`). `driver_parity_test.go` drives every outcome
in that contract through the driver's own writer and asks the run's own
interpreter what it would do, so the two cannot decide differently about the same
result.

The driver never decides. It states what happened, and it reads what the run
decided — including whether the packet's mandatory QA gates permit progression,
which caps what the delivery projection is allowed to claim.

## Why the driver is bash

The controller primitives it needs already exist, proven, in
`corsolv/p2-smoke/lib/sa-lib.sh`: worktree provisioning, a ready-set predicate a
sibling bead's title cannot satisfy, the final-state re-read, publication scope
adjudication, required-job CI verdicts. Each encodes a specific failure that was
diagnosed once. Porting them would mean rewriting the lessons, so the driver
generalizes the harness that already carries them.

The Go layer keeps everything that benefits from types and tests: the contract,
validation, the durable record, the evidence assessment and the compiler.
`driver_test.go` runs every command line the compiler emits through the driver's
own parser, because nothing else connects the two.

## Gates

A work package declares the commands its worker must run to verify itself, and
those are exactly the commands that worker is permitted to run.

Both halves matter. A bounded worker is deny-by-default, so a package whose
objective says "verify with `npm run verify`" and whose gates say nothing has
told its worker to do something the platform forbids — which is what happened,
and the worker correctly closed `blocked` rather than claiming unverified work.
And a gate list that widened permissions for the fleet would trade one package's
convenience for every other package's containment.

So the grant is installed in that package's own worktree, the one directory
belonging to exactly one package, and `handoff.ValidateGate` refuses anything
that is not a bare project runner: no shell syntax, no paths, no traversal, and
never `git`, `gh`, `npx`, `curl` or a shell. Publication authority stays with
the controller, which re-runs the same declared gates before it publishes.

## Containment

- A work package may only change the paths it declared. The controller compares
  the change set before publishing; anything else stops publication.
- No package may authorize `.git/`, `.github/workflows/` or `delivery/gascity/`.
  A package that could edit the workflow judging it could make its own gate pass.
- No two packages may authorize the same path.
- Workers run `bounded-project`: read, write, edit, and the project's named
  gates. No git, no forge CLI, no shell family. Publication authority is the
  controller's alone.
- The rig is a working clone, never the registered checkout. The user's own
  working copy is read for identity and never mutated.

## Completion is evidence-based

A project does not complete because its tasks are ticked. `handoff.Assess`
requires, per package, a merge *and* a met completion gate — required CI on the
exact head, independent assurance, and a governed merge — plus every acceptance
criterion covered, no blocking work, and an accepted commit on the authoritative
branch when merge authority was granted.

A run that finishes cleanly over a projection showing unproven work reports
**blocked**, and says which clause failed. That is the case the whole design
exists for: an agent can close its own bead, so closing beads must not be what
decides delivery is done.
