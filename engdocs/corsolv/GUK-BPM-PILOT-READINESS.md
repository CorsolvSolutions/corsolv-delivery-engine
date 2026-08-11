# GUK BPM pilot readiness assessment

Read-only. Nothing in `D:\Development\guk-bpm-platform` was modified to produce
this, and nothing in it should be modified until the pilot is approved.

Collected 2026-08-11 from the repository, the forge and the working tree.

## Observed state

| Fact | Value |
| --- | --- |
| Repository | `https://github.com/CorsolvSolutions/guk-bpm-platform.git` (private) |
| Permission held | `ADMIN` |
| Default branch | `main`, **not** protected — no required approving review |
| Main checkout | `D:\Development\guk-bpm-platform` |
| Its branch | `docs/uat-packet-b-recompile-rc2` |
| Its HEAD | `67dacf7822ed7c259068881eed7505693ad12cae` |
| Its worktree state | dirty, 8 paths |
| Registered worktrees | **37** |
| Open pull requests | 8 (one explicitly titled MERGE BLOCKED) |
| Stack | Vite / React / TypeScript, Vitest, Supabase, Azure Container Apps |
| Deployment environments | one — `uat`, carrying a branch policy |
| Workflows | 18, including `deploy-live`, `deploy-uat`, `deploy-readonly-pilot`, `live-emergency-rollback`, `live-traffic-promotion` |
| Delivery projection | `delivery/` holds ten Markdown documents; there is **no** `delivery/PROJECT-STATE.yml` |

## What this means for a first unattended run

### The worktree population is the dominant risk

Thirty-seven registered worktrees, one of them `locked`, several on detached
HEADs, and eighteen of them under `.claude/worktrees/` — i.e. created by previous
agent sessions. This is the exact population that produced "two writers in one
worktree" and "a branch switched underneath a live writer" on the Delivery
Engine, at roughly ten times the scale.

The control layer handles it, but only if the pilot declares **one** worktree
and owns it. The writer lock is per-worktree by construction (it lives in each
worktree's own git directory), so a pilot in its own worktree is unaffected by
the other thirty-six — and any other session that tries to enter the pilot's
worktree is refused with the owner named.

**Do not run the pilot in `D:\Development\guk-bpm-platform` itself.** That
checkout is dirty, is on a documentation branch, and is the one a person is most
likely to be using interactively.

### The main checkout is dirty and on a feature branch

Preflight would refuse it, correctly. The pilot needs a purpose-made worktree
created from a known ref.

### Merge authority exists; deployment approval is the real boundary

`main` is unprotected and the account is ADMIN, so a pilot could merge. The
boundary that actually matters is deployment: `deploy-uat`, `deploy-live` and
`live-traffic-promotion` reach real infrastructure, and the `uat` environment
carries a branch policy. A pilot must declare `needMerge = false` and treat every
deploy workflow as a human boundary until a person says otherwise.

The `uat` environment's only protection rule is a *branch policy* — it restricts
which refs may deploy. It is **not** a required-reviewer gate, so it will not
stop an unattended deployment on an allowed branch. That is a reason to withhold
deployment in the plan rather than to rely on the forge to withhold it.

### The dashboard projection is not yet established there

`delivery/` is rich but is all prose. There is no `delivery/PROJECT-STATE.yml`,
so the dashboard does not read this project yet. The pilot's `publishPath` would
establish it — and that is a good first delivery milestone, because it is
additive, reversible and immediately visible.

## Proposed initial work package

Safe, useful, entirely non-deploying, and drawn from scripts the project already
has rather than invented for the pilot:

| Band | Task | Command |
| --- | --- | --- |
| primary | typecheck and build | `npm run build` |
| primary | unit suite | `npm run test` |
| validation | lint | `npm run lint` |
| validation | read-only guard check | `npm run check:readonly-guards` |
| validation | mutation audit | `npm run audit:mutations` |
| assurance | assurance suite | `npm run test:assurance` |
| assurance | DAB contract check | `npm run check:dab-contract` |
| assurance | controlled-write coverage | `npm run check:controlled-write-coverage` |
| assurance | mutation-count consistency | `npm run check:mutation-count-consistency` |
| evidence | establish `delivery/PROJECT-STATE.yml` | the projector, committed — the one mutating task |
| next-stage | UAT environment validation, read-only | `npm run validate:uat-environment` |

Every one of these already exists in `package.json`. None deploys, none writes to
Supabase, none touches live traffic.

## Required before the pilot runs

| Item | Status | Who |
| --- | --- | --- |
| A dedicated pilot worktree from a known ref | not created | can be created by the pilot's operator |
| `node` and `npm` on the execution host | present on Windows; **absent in WSL** | needs installing, or the pilot runs on the Windows side |
| `npm ci` completed in the pilot worktree | not done | mechanical |
| Deployment credentials | **not held** — `GUK_BPM_DEPLOY_TOKEN` unset | platform owner, and only if deployment is in scope |
| Decision on merge authority | recommend `needMerge = false` | delivery owner |
| Decision on deployment | recommend excluded from the first pilot entirely | delivery owner |

The node-in-WSL point is the one real setup task, and it is the kind this
milestone exists to surface before a run rather than nine minutes into one.

## Estimated unattended duration

45–90 minutes for the package above: the build and Vitest suites dominate, the
assurance scripts are each a few minutes, and the evidence commit runs the
project's own pre-commit hooks.

## Success criteria for the pilot

1. Preflight returns READY or READY-WITH-KNOWN-HUMAN-BOUNDARY, and every boundary
   it names is one of the ones predicted above.
2. Exactly one writer holds the pilot worktree for the whole run; a deliberate
   second writer is refused and names the owner.
3. The run continues past at least one ordinary failure rather than stopping.
4. `delivery/PROJECT-STATE.yml` is established and the dashboard reads it.
5. No deployment workflow is triggered.
6. `D:\Development\guk-bpm-platform` and the other 36 worktrees are byte-identical
   before and after.

## The next human decision

Everything above is either already true or is a mechanical setup step. The
outstanding decisions are two, and they are genuinely decisions:

- **APPROVE GUK BPM PILOT** with the work package above, and
- whether deployment is in scope at all for the first run (recommended: no).
