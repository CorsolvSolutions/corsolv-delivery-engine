# Delivery state: who owns which fact

Written after the GUK BPM pilot refused its own authorised mutation. It is
generic on purpose: the collision it prevents is not specific to GUK BPM, and
the next project will have a different layout.

## The failure this exists to prevent

A pilot was authorised to write `delivery/PROJECT-STATE.yml`. The assessment
that produced the authorisation read a checkout whose HEAD predated the file's
creation and reported the path as free. By execution time the project's own
`origin/main` carried a hand-maintained canonical governance record there, whose
header reads:

> a new Claude chat (or any human) must be able to determine current phase,
> active task, active branch/PR, completed work, evidence, blockers, remaining
> estimate, and next action from THIS FILE

Publishing a run-scoped projection over it would have destroyed a governance
document and replaced it with something untruthful about the project — **inside
a path the run was genuinely authorised to write**.

The lesson is one sentence: **an authorised path is not an authorised act.**

## The four authorities

No fact has two authoritative owners. Where one plausibly could, the precedence
rule below decides, once.

### 1. Project governance — the project's own record

Whatever path the project already keeps it at, commonly
`delivery/PROJECT-STATE.yml`. Maintained by the project: by hand, by its own
tooling, or by its own agents. Gas City **never writes here**.

Owns what the programme *intends*: current phase, milestone, strategy, RAG and
its reason, planned work, governance decisions, human-approved next actions.

A project may have no governance layer at all. That is normal, and such a
project may configure the Gas City projection at `delivery/PROJECT-STATE.yml`
directly — there is nothing to collide with. **Do not impose the GUK BPM layout
on projects that do not have one.**

### 2. Gas City execution projection — generated, disposable

Default `delivery/gascity/PROJECT-STATE.yml`, configurable per project. Written
by the delivery projector, overwritten every run, never edited by hand.

Owns what a run *did*: run identity, worker/session, worktree, actual start and
finish, attempts, execution blockers, fallback work taken, completion gates,
execution status and evidence pointers.

Every such file begins with:

```
# generated-by: gas-city-delivery-projector
```

That marker is the whole ownership mechanism. The publisher writes it and
refuses any target that lacks it — see `internal/unattended/projectionpublish.go`
and its regression suite. Ownership is established by **reading the target**,
never by trusting configuration, because configuration is exactly what was wrong
in the pilot.

### 3. GitHub — live, at query time

Owns pull-request state, check and CI state, the tested SHA, and merge state and
SHA. Neither YAML file is consulted for these, **and neither is believed if it
contains them**: a file written minutes ago cannot know what CI did since.

### 4. Dashboard — presentation, not authority

Reads all three and renders them. It owns no facts. It attributes each rendered
field to its source so a reader can audit where a value came from.

## The precedence rule

> For any field both documents can carry, **governance wins for intent and
> execution wins for outcome.**

Made executable in `src/lib/stateAuthority.ts` as two disjoint field maps, with a
test asserting they do not overlap — a precedence rule with an ambiguous case is
not a rule.

Consequences worth stating:

- An execution projection carries empty `strategy` and `currentPhase`, because a
  run has no opinion about them. Those empties must never blank real governance
  values.
- A run never introduces tasks the governance record does not know about. A run
  is evidence about planned work, not a way to plan more of it.
- Execution blockers are added to governance blockers, not substituted for them.
- A file at the execution path without the marker is reported and **not
  consumed**. One bad execution file must not blank the dashboard for a project
  whose governance record is fine.

## Configuring a project

Two independent choices:

**Where Gas City publishes.** The publication path is an argument to the
publish step, so it is per-project configuration rather than engine behaviour:

```toml
[[task]]
id      = "publish-projection"
argv    = [".../publish-projection.sh", "delivery/gascity/PROJECT-STATE.yml"]
mutates = true
```

A project with no governance layer may pass `delivery/PROJECT-STATE.yml`
instead. The publisher's refusal makes that safe to try: if something is already
there that it did not write, it stops and says what it would have taken.

**Where the dashboard looks.** `EXECUTION_STATE_PATH` in `stateAuthority.ts` is
the default; a project overrides it through the existing per-project
source-paths mechanism rather than by changing dashboard code.

## Choosing a publication path for a new project

1. Read the project's authoritative ref — **not a local checkout**, which may be
   on an old branch. That single mistake is what produced the pilot's finding.
2. If `delivery/PROJECT-STATE.yml` is absent, it is available.
3. If it is present, check for the generated marker. With it, it is a previous
   Gas City artifact and may be reused. Without it, it belongs to the project:
   publish to `delivery/gascity/PROJECT-STATE.yml`.
4. Never resolve the question by deleting or overwriting. That is a human
   decision, and the publisher will refuse it.
