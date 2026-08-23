# Autonomous technical decision policy

**Status:** standing policy for supervised managed-delivery runs.
**Authorised by:** Jon Pratten, 2026-08-23.
**Scope:** SCORM Course Studio, the Corsolv Delivery Engine, the Project
Management Portal, and the local development and test infrastructure supporting
them. **GUK BPM is out of scope.**

## The failure this exists for

A supervised run hit three Delivery Engine defects in sequence — remedial worker
agents never declared on a resumed city, corrective worktrees cut from the run's
historical base, and remediation having no lifecycle for a criterion whose
evidence was already merged. Each was diagnosed correctly. Each was then brought
back to a person as a numbered menu of reversible technical options, and the
delivery stopped dead until an answer came.

Every one of those was inside already-approved scope, testable, and governable
through branch → PR → CI → merge. Stopping for them did not add safety; it
added latency and moved engineering judgement onto someone who had already
delegated it. **The interruptions were themselves a delivery-control failure**,
and they are what this policy forbids.

## The rule

A supervisory agent **must continue without asking** when a problem is *all* of:

- technical;
- reversible;
- inside already-approved product or delivery scope;
- testable;
- governable through branch/worktree → PR → CI → merge;
- and free of human, business, security or legal judgement.

In that case it must: diagnose it, choose the smallest durable **systemic**
solution, prove it with a regression test where applicable, implement it through
normal governance, merge only on green required checks, synchronise any
installed copy, re-run the exact previously-failing path, and carry on.

The decision and its evidence are reported **after** execution, not before.

## What is explicitly NOT a boundary

- a recoverable technical defect;
- a new Delivery Engine defect discovered by a real project;
- a failing regression test;
- a stale worktree holding no unique work;
- an architectural correction needed to support an already-approved lifecycle.

## The only real boundaries

Stop, and ask, when one of these is genuinely reached:

| | boundary |
| --- | --- |
| A | a required secret or credential is unavailable |
| B | an external paid or live action is required and not already authorised |
| C | a production, live or destructive mutation would occur |
| D | continuing would irreversibly destroy or overwrite user or business data |
| E | an unresolved writer collision exists and safe ownership cannot be established |
| F | requirements are materially ambiguous in a way that changes product scope or business behaviour |
| G | explicit human acceptance is required — RC2 / a human-reserved criterion |
| H | a security, legal or business decision genuinely requires the owner |

A human-reserved acceptance criterion is never self-approved. The engine already
enforces that structurally: `handoff.Criterion.IsHuman` criteria may be claimed
by no work package, and `delivery accept` refuses anything delivery is expected
to satisfy and prove itself. This policy does not weaken that and cannot.

## Why it belongs here rather than in a conversation

A rule that lives only in one session's memory is a rule the next run does not
have. This file is the durable location, versioned with the engine whose
behaviour it governs, and changed the same way anything else here is changed.
