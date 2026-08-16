# Managed delivery — first live run

What the first end-to-end exercise of the portal → Gas City handoff proved, what
it found, and the one thing it could not reach.

Run date: 2026-08-14. Disposable project:
`CorsolvSolutions/corsolv-managed-delivery-test-08141643` (private, created for
this exercise and safe to delete).

## What was proved

Every stage below ran for real against a real GitHub repository.

| Stage | Evidence |
| --- | --- |
| Portal provisioning | repo created, workspace cloned, scaffold committed `cba2139`, project registered |
| Seeded CI gate | workflow `CI` ran and passed on the bootstrap commit — a required check exists from the project's first commit |
| Portal → engine handoff | `POST /api/projects/<id>/delivery` → `planning`, run detached |
| Idempotency | an identical Start returned 200 and the same delivery; no second run |
| Contradictory terms | a Start with a changed objective was refused 409, naming both intent digests |
| Cross-project isolation | a Start against an unregistered id was refused 409 before the engine was asked |
| Preflight | READY — ownership, origin, forge authority (including merge), tools, durable state |
| Durable record | intent kept verbatim with its digest; run history append-only |
| `city-up` | city built, working rig cloned, one bounded worker agent declared per package, `bounded-project` confirmed present in the resolved config |
| `dispatch` | real beads created in the rig's own store (`r2-ghj` work, `r2-b4s` controller merge bead), dependency wired, work slung to `worker-wp-slugify` |
| Portal projection | `running` with `live: true`, the real run id, real package counts and real evidence reasons |
| **Interruption** | the run was identified by its recorded `writerPid` and killed with SIGKILL — nothing else was touched |
| **Recovery** | the portal immediately reported `queued`/`live: false` rather than trusting the stale heartbeat; restarting through the normal path resumed with `city-up` and `dispatch` completing in 0s each, and did not replay them |
| Containment | with no artifact produced, publication refused — no commit, no branch, no pull request, no merge |

## What it could not reach

**The agent runtime has no budget.** `claude -p` on this host answers:

```
You've hit your monthly spend limit · raise it at claude.ai/settings/usage
```

That blocks the two stages that need an agent:

- **planning** — turning the business brief into work packages;
- **worker execution** — the coding agent that produces the change.

Neither is a defect in this code, and neither can be worked around from here:
raising a spend limit is a billing decision. Until it is raised, a managed
delivery reaches `dispatch` and then correctly fails at publication because no
work was produced. It does **not** report Completed, which is the whole point of
evidence-based completion.

Planning has a second author while that is true: `delivery plan -project <id>
-from <file>` installs a plan written by hand, through the same validator an
agent's plan faces. Worker execution has no such fallback and should not: a
controller that wrote the code itself would be forging the evidence.

## What the run found and fixed

Five real defects, each found by running rather than by review.

1. **The detached run had no PATH.** A run detached into its own process group
   does not inherit an interactive shell's PATH, so `claude` was not found. The
   host profile now names the planner by absolute path, as it already did for
   the forge CLI. Machine-specific facts are declared, not inferred.

2. **The planner's failure said nothing.** An agent CLI reports the problems a
   person must act on — an expired login, an exhausted spend limit — on
   **stdout**, and only stderr was reported. The failure read `exited 1:` with
   nothing after it. Both streams are now considered, which is how the spend
   limit above became visible at all.

3. **The driver could not read its own documents.** It reads `intent.json` and
   `plan.json`; the Go layer wrote the intent only inside the delivery record.
   The first stage of every run failed with "no delivery intent". The Go layer
   now writes the intent standing alone, from the record's copy, and a test
   asserts the two halves agree on the filenames — the same class of test that
   already guards the command line.

4. **The forge CLI never reached the driver.** The host profile declared it and
   the compiler did not pass it, so the driver fell back to `gh` on PATH, which
   does not exist under WSL. The clone failed with an authentication error that
   named nothing. It is now passed to every stage.

5. **`gc init`'s exit code is not its verdict.** On a machine that already has a
   supervisor, `gc init` reports non-zero because it could not start a second
   one — which is not merely benign but correct, since only one supervisor may
   run per machine and the city is registered with the one already there. The
   driver now decides from the state on disk and a live supervisor, not from the
   status code.

Plus one design correction: a work bead's **title** carried the whole objective
and hit the 500-character cap. A real objective is several sentences, because
the worker reading it has no other context. The title is now a label and the
objective goes in the bead's description.

## Not touched

The machine-wide supervisor (pid 388, up 3d22h) was left alone throughout. It
is shared, and the correct response to "a supervisor already owns this port" is
to use it, not to restart it. No process outside this disposable project was
signalled.
