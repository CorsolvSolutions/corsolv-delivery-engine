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
provider           = "claude"
windowsMountPrefix = "/mnt"
plannerCommand     = "claude"
plannerArgs        = ["-p", "--permission-mode", "plan"]
```

`githubCommand` points into `/mnt/c` because the engine runs under WSL while
the only authenticated `gh` is a Windows install. That is exactly the kind of
fact this file exists to hold.

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
   routes them. Workers write code; only the controller commits, pushes, opens
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
stage **fails**. A deadline is not an outcome, and reporting one as success is
how a run reached publication with work that had never been done.

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
