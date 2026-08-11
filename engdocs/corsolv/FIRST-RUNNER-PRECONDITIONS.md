# First-runner configuration preconditions

Conditions the promoted first-runner route depends on. Each is a precondition
rather than a defect: the route is safe **while** it holds, and becomes unsafe
if the configuration changes underneath it.

## 1. The promoted route must remain `--no-formula --no-convoy`

**Status:** holds today. Enforced by every sling in `corsolv/p2-smoke/sb-run.sh`
and `shadow-run.sh`.

**Why it matters.** The Bd-backed graph binding does not yet satisfy the
single-winner metadata compare-and-swap conformance case
(`CompareAndSetMetadataKeyHasOneWinner`), and two sibling graph cases
(`UpdateIfMatchRejectsStaleRevision`, `ReadyContextHonorsCancellation`) are
likewise outside the proven set. G7 proved single-winner CAS for **bead claim
ownership** — `ClaimIsCompareAndSwap` against a real bd + Dolt store — and that
is the ownership primitive the promoted route actually uses. It did not prove
uniform single-winner semantics for every metadata CAS in that binding.

Metadata CAS *is* a genuine fencing primitive elsewhere in the engine —
exclusive drain reservations, `gc.control_epoch` fencing, molecule attach
fencing, `gc.attach_fence_pending`. Those paths apply to formulas, molecules,
convoys and control beads. The promoted route routes raw beads with
`--no-formula --no-convoy`, so it never creates the subjects that exercise them.

**What the promoted route relies on instead:**

1. `BdStore.Claim` compare-and-swap — proven by G7 on the real Bd/Dolt path.
2. ClaimFence — monotonic owner generation, stale-owner fencing.
3. Fail-closed worktree ownership evidence — a stale attempt's `work_dir`
   cannot license a new attempt.
4. Controller-only publication authority — `gc.authorised_paths` plus
   controller-side publication.

**When this becomes safety-relevant.** If formulas or convoys are enabled in a
future configuration, the metadata-CAS gap moves onto the safety path and must
be closed *before* that configuration is promoted. Enabling them without closing
it would silently remove a fencing guarantee the route currently never needs.

**Do not** treat this as a licence to broaden into a full graph-binding
remediation programme; the exact dependency is the one named above.

## 2. Criterion 11 attribution

The Delivery Engine's S-B run proved PR-only governed entry to **its own
run-scoped integration target**. `main`'s W1/W2/W3 state came from the earlier
PowerShell-controller run. S-B did not re-prove PR-only entry to `main`, and no
work was invented to replay already-merged changes so that it could. See
`S-B-RESULT.md` for the full attribution.

## 3. The generated delivery projection is generated

`delivery/PROJECT-STATE.yml` in a target project is written by the Delivery
Engine projector. Hand edits are overwritten and are not a source of truth. The
dashboard reads it from the target repository's authoritative ref
(`origin/main`), never from a working tree.
