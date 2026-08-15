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
