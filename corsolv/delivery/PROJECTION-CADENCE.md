# The projection is published once, at the end

A field report from a portal integration, and a proposed contract change. This
document changes no behaviour; it records a defect with its evidence so the
engine's owners can decide what to do about it.

## What the contract says today

The README names the projection as the portal's source:

| Layer | Owns | Where |
| --- | --- | --- |
| Projection | what the portal reads | `delivery/gascity/PROJECT-STATE.yml` |

and step 5 of a run:

> **Project** — the run's own evidence is rendered into a delivery projection
> and published to `delivery/gascity/PROJECT-STATE.yml`, which the portal reads
> from the authoritative ref.

Elsewhere, describing remediation, it says "the portal sees the change
immediately".

## What a compiled run actually schedules

From `run-plan.toml` for a sixteen-package delivery:

```toml
[[task]]
  id = "project"
  needs = ["publish-wp-discovery-brief", "publish-wp-architecture-data-design",
           ... all sixteen publish-* tasks ... ]

[[task]]
  id = "publish-projection"
  needs = ["project"]
```

`project` depends on **every** package's publish stage. So the projection is
rendered once, after the last package succeeds, and `publish-projection` runs
after that.

The consequence: **a delivery that does not finish publishes no projection at
all.** Not a partial one — none. One held package anywhere in the plan and the
portal's documented source is never written.

## The field report

castle-habits-app, 29 September to 4 October 2026. Sixteen planned packages.

| | |
| --- | --- |
| Packages published and merged | **14 of 16** |
| Pull requests merged to `main` | 16 |
| Acceptance criteria whose plan-declared package merged | 33 of 42 |
| `delivery/gascity/PROJECT-STATE.yml` on the authoritative ref | **never written** |
| `delivery status` → `completePackages` | `null` |
| `delivery status` → `packagesComplete` | `0` |

The run ended with one package exhausting its attempts, so `project` never
became eligible and the projection was never rendered.

For six days the portal's project page read:

```
OVERALL PROGRESS   0%   of recorded deliverables
WORK REMAINING     40   of 40 deliverables outstanding
Discovery 0% · Design 0% · Build 0% · Test 0% · UAT 0% · Live 0%
```

Every figure there was correct against its source. The portal reads the
project's own records, those records said nothing had started, and nothing in
the handoff contradicted them — `completePackages` was `null` and there was no
projection to read. A fortnight of delivered, merged, working software was
invisible to the system whose job is to report it.

The portal eventually compensated by reading merged pull requests and crossing
them with the plan's `satisfies` map. That works, but it is a portal inferring
delivery outcomes from a forge, which is exactly the inference the handoff
contract exists to make unnecessary.

## Why this is a contract question and not a plan question

A plan is immutable once work has merged against it, correctly — `delivery plan
-from` refuses a replacement, and remediation requires a recorded invalidation.
So an integrator who hits this cannot schedule the projection differently for
their own run. The cadence is the engine's to choose.

It also cannot be worked around by reading `delivery status` more often. That
command reports `completePackages: null` for the same reason: nothing has
rendered the run's evidence yet.

## Proposed contract

**After each package's publish stage succeeds, render and publish the
projection.**

Three properties that matter to a portal, offered as the acceptance for any
change here:

1. **Monotonic within a run.** A later projection never shows less delivered
   than an earlier one. A portal that polls must not watch progress go
   backwards.
2. **Partial is published.** A run that stops at fourteen of sixteen leaves a
   projection describing fourteen. "Incomplete" and "nothing happened" are
   different states and must not render alike.
3. **Terminal state is still distinguishable.** Whatever marks a delivery
   finished today must remain distinguishable from a partial projection, so a
   portal cannot read fourteen-of-sixteen as a completed delivery.

Point 3 is the one that makes this more than a scheduling tweak, and is why this
is a proposal rather than a patch: only the engine knows how its projection
distinguishes "this is the final word" from "this is where we have got to".

## What this is not asking for

- Not a change to what the projection contains.
- Not a change to what `complete` means. Delivery's acceptance of its own
  evidence stays the engine's word; a portal has no business inferring it.
- Not a plan or scheduling hook for integrators. The cadence should stay the
  engine's decision — that is the point of the split.

## Smaller alternative

If per-package publication is too expensive — it is a commit and a push to the
project's repository each time — a projection rendered once when a run
**terminates for any reason**, rather than only on success, would fix most of
this. A portal would see the truth within one run rather than within one
successful run, and the castle-habits-app page would have shown fourteen of
sixteen instead of nought.

---

*Reported from the claude-project-management portal integration, 4 October 2026.
Happy to supply the run journal, compiled plan and status output behind any of
the figures above.*
