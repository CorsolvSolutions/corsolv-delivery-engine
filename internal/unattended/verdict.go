// Package unattended is the fork-owned control layer that makes a Gas City run
// safe to leave alone.
//
// It exists because the first-runner program kept dying the same way: a
// blocker that was knowable before execution started — a competing writer, a
// dirty tree, a missing credential, a merge permission — was discovered
// serially, mid-run, after work had already begun. Every member of that
// population is mechanically detectable, so this package detects them, up
// front, once, and refuses to start a long unattended run that cannot finish.
//
// The package holds no judgement. It decides nothing about *what* work is
// worth doing; it decides only whether the ground is safe to stand on, who owns
// it, and whether the ground moved. What to run is supplied as configuration.
package unattended

// Outcome is the result of one individual readiness check.
type Outcome string

// The four outcomes a check may report.
const (
	// OutcomePass — the check executed and the condition holds.
	OutcomePass Outcome = "pass"
	// OutcomeHumanBoundary — the check executed and found a real limit that
	// only a human can lift. This is knowledge, not failure: the run may still
	// proceed if the boundary does not gate the work it plans to do.
	OutcomeHumanBoundary Outcome = "human-boundary"
	// OutcomeNotReached — the check did not execute, so nothing is known. It is
	// deliberately more severe than a human boundary: an unrun check is an
	// unexamined risk, whereas a boundary is an examined one.
	OutcomeNotReached Outcome = "not-reached"
	// OutcomeFail — the check executed and the condition does not hold.
	OutcomeFail Outcome = "fail"
)

// severity orders outcomes so that aggregation is a maximum.
//
// An outcome this function does not recognize scores as OutcomeNotReached
// rather than as a pass. A constant added later without a severity therefore
// degrades to "unproven" instead of silently licensing a run.
func (o Outcome) severity() int {
	switch o {
	case OutcomePass:
		return 0
	case OutcomeHumanBoundary:
		return 1
	case OutcomeNotReached:
		return 2
	case OutcomeFail:
		return 3
	default:
		return 2
	}
}

// WorstOutcome aggregates constituent outcomes by severity.
//
// An empty set returns OutcomeNotReached. A preflight that ran no checks has
// proved nothing, and must not read as though it proved everything.
func WorstOutcome(outcomes []Outcome) Outcome {
	worst := OutcomeNotReached
	if len(outcomes) == 0 {
		return worst
	}
	worst = OutcomePass
	for _, o := range outcomes {
		if o.severity() > worst.severity() {
			worst = normalizeOutcome(o)
		}
	}
	return worst
}

// normalizeOutcome maps an unrecognized outcome onto the canonical constant its
// severity denotes, so aggregation never propagates an invented vocabulary.
func normalizeOutcome(o Outcome) Outcome {
	switch o {
	case OutcomePass, OutcomeHumanBoundary, OutcomeNotReached, OutcomeFail:
		return o
	default:
		return OutcomeNotReached
	}
}

// Readiness is the single consolidated verdict a preflight produces.
type Readiness string

// The three readiness verdicts.
const (
	// Ready — every check passed.
	Ready Readiness = "READY"
	// ReadyWithKnownHumanBoundary — every check either passed or found a named
	// human boundary. The run may proceed; the boundary is published so the work
	// plan can route around it rather than colliding with it later.
	ReadyWithKnownHumanBoundary Readiness = "READY-WITH-KNOWN-HUMAN-BOUNDARY"
	// NotReady — at least one check failed, or at least one did not run.
	NotReady Readiness = "NOT-READY"
)

// ReadinessOf reduces constituent check outcomes to one verdict.
func ReadinessOf(outcomes []Outcome) Readiness {
	switch WorstOutcome(outcomes) {
	case OutcomePass:
		return Ready
	case OutcomeHumanBoundary:
		return ReadyWithKnownHumanBoundary
	default:
		return NotReady
	}
}

// PermitsUnattendedRun reports whether a run may begin under this verdict.
//
// A named human boundary permits the run on purpose. The point of discovering
// boundaries early is to plan around them, not to refuse all work whenever one
// exists; whether a specific boundary gates a specific task is decided by the
// work queue, which knows what the tasks need.
func (r Readiness) PermitsUnattendedRun() bool {
	return r == Ready || r == ReadyWithKnownHumanBoundary
}
