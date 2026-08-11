# GUK BPM Gas City pilot — result

First pilot of the unattended control layer against a real production project.
Authorised as a bounded package: one worktree, one tracked file, no deployment,
no merge.

## Verdict

**PASS** — with the pilot's central finding being that it **refused its own
authorised mutation**, correctly.

## What happened

| | |
| --- | --- |
| Run ID | `guk-bpm-pilot-004` (session `guk-bpm-pilot-001`) |
| Duration | 1m05s |
| Baseline | `cde8d350f0c81f59b7b652b60cd6896d54aed2a6` — GUK BPM `origin/main` re-resolved at execution time |
| Worktree | `/home/corsolvtech/guk-bpm-pilot`, branch `pilot/gascity-001` |
| Tasks | 12 planned — 9 succeeded, 2 failed, 1 held |
| Human interventions during the run | **0** |

## The central finding

The approval authorised one tracked mutation: `delivery/PROJECT-STATE.yml` on
the pilot branch, described in the approval packet as *additive* — "there is no
`delivery/PROJECT-STATE.yml`, so the pilot's `publishPath` would establish it".

**That was wrong by the time it executed.** The assessment read the primary
checkout, which sits on a documentation branch at `67dacf782`. The project's
actual `origin/main` had moved to `cde8d350f`, and at that commit
`delivery/PROJECT-STATE.yml` exists and is a hand-maintained canonical record
whose own header reads:

> a new Claude chat (or any human) must be able to determine current phase,
> active task, active branch/PR, completed work, evidence, blockers, remaining
> estimate, and next action from THIS FILE

Publishing over it would have replaced a governance document with a projection
of a single one-minute run — destructive, and untruthful about the project —
**inside a path the run was genuinely authorised to write**.

An authorised path is not an authorised act. The publisher now refuses to
overwrite a file it did not author, identified by a generated marker rather than
by guesswork, and the refusal classifies as `human-decision`: recorded once, not
retried, and it does not stop the run.

The first attempt at this pilot, before the guard existed, staged that overwrite
and was stopped only because an unrelated failure prevented the commit. The
worktree was restored to baseline with a targeted `git restore`; no reset, no
clean.

## The other findings

1. **A stale binary was used for the first attempt.** `/var/tmp/unattended-run`
   predated the failure-capture change, so that run produced no failure output —
   which is exactly the diagnosis gap that change had closed. Rebuilt and re-run.
2. **The delivery projection did not exist while the run was still running.** It
   was published only after `Run` returned, so the evidence task that exists to
   commit it could never find it. It is now refreshed alongside the heartbeat,
   which also means the dashboard sees delivery state *during* a long run rather
   than only once it is over.
3. **Git identity is not configured for GUK BPM in WSL.** The commit failed with
   "empty ident name". A linked worktree shares `.git/config` with all
   forty-one other worktrees, so setting it would have changed shared state the
   run was not authorised to touch; identity is now passed per-invocation with
   `-c`.
4. **`pwsh` is absent from this host's WSL.** `scripts/validate-uat-environment.ps1.test.mjs`
   shells out to it deliberately — it runs the real PowerShell gate rather than a
   re-implementation, so it cannot drift from CI. Five of 1130 tests fail here;
   1125 pass, and GUK BPM's CI runs `ubuntu-latest` where pwsh is preinstalled.
   Classified `environment`, retried once, run continued. **This is not a defect
   in GUK BPM.**
5. **A projector artifact.** `build` projects with `blocker: "waiting on install"`
   although it succeeded, because `install` declares no delivery status and so is
   outside the projected set. Cosmetic, derived, and recorded rather than fixed —
   fixing it was outside this pilot's scope.

## Isolation proof

Before and after censuses fingerprint every registered worktree by path, HEAD,
branch, dirty-count and tracked-tree hash. The complete diff is:

```
worktrees   41  ->  42
+ /home/corsolvtech/guk-bpm-pilot | cde8d350f… | pilot/gascity-001 | 0 | d6955176…
```

Nothing else. All forty-one pre-existing worktrees are identical in every
fingerprinted field. The primary checkout's nine dirty paths are byte-identical
before and after — deliberately not "corrected", because their unchanged state is
part of the proof.

| Claim | Evidence |
| --- | --- |
| Primary checkout untouched | branch `docs/uat-packet-b-recompile-rc2`, HEAD `67dacf782`, 9 dirty paths — all identical |
| Other 40 worktrees untouched | identical tracked-tree hashes |
| Pilot worktree | `cde8d350f`, **0 dirty paths** — the authorised mutation was refused, so none was made |
| No deployment | no deploy workflow invoked; the deployment credential is deliberately absent and the one deploy-shaped task was held before the run began |
| No SQL, Azure, secret or permission change | none attempted |
| No merge, no push to main, no force push, no reset, no clean | none |

## Process hygiene

| | Before | After |
| --- | --- | --- |
| tmux | 129 | 129 |
| dolt | 1 | 1 |
| gc supervisor | 1 | 1 |
| `convoy control --serve` | 5 | 5 |
| unattended-run | 0 | 0 |

**Zero pilot-created process leaks.** The pre-existing leftovers are the ones
already recorded in the readiness result; the pilot added none and cleaned none,
because none were its own.

## Success criteria

| Criterion | Result |
| --- | --- |
| Preflight still valid | `READY-WITH-KNOWN-HUMAN-BOUNDARY`, 28 checks |
| Exactly one writer | yes — lock held by `guk-bpm-pilot-004`, no competing writer |
| Dedicated worktree only | yes |
| Current `origin/main` baseline | yes — re-resolved to `cde8d350f`, which had moved since the assessment |
| Approved task package executed | yes, from `package.json` scripts; none substituted |
| Ordinary failure/retry exercised | yes, naturally — `unit` classified `environment`, retried, run continued |
| Fallback work continued | yes — six assurance tasks ran after `unit` failed |
| PROJECT-STATE truthful | yes — `publish-projection` projects `planned`/`not-met` with its refusal as the blocker, not the `pr-open` it would have earned |
| Dashboard-consumable state | yes, at the run's publish path, in the projector's schema |
| No deployment | yes |
| No merge | yes |
| Primary and other worktrees untouched | yes |
| Zero human interventions | yes |
| No hidden or false PASS | the one authorised mutation did not happen, and this document says so first |

## What a person must now decide

The pilot proved Gas City can safely manage the real GUK BPM repository. It also
proved the specific publication target in the approval was the wrong one.

**The decision is where the Gas City delivery projection should live in GUK BPM**,
given that `delivery/PROJECT-STATE.yml` is already taken by the project's own
canonical record. The obvious candidate is a distinct path — for example
`delivery/GASCITY-RUN-STATE.yml` — which would be genuinely additive. That is a
new authorisation, not an extension of this one.

No production work package follows automatically from this pilot.
