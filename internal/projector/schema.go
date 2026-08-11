package projector

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// This file is the CONSUMER CONTRACT: the exact document shape the delivery
// dashboard parses. The authority is that parser, not this file — field names,
// the status vocabulary and the compatibility rules are transcribed from it.
//
// The producer bends to the consumer here on purpose. An earlier version of
// this projector emitted its own shape (`tasks:`, `id:`, snake_case
// throughout) and the dashboard correctly refused it with "Project state not
// understood" rather than rendering a project with no work — which would have
// been indistinguishable from a real empty project. Teaching the dashboard a
// second schema would have created two ways to be right; changing the producer
// keeps one.

// SchemaVersion is the document version the dashboard reads. A document with
// any other value is refused outright, so this is not decoration.
const SchemaVersion = 1

// TaskStatus is the canonical status vocabulary, transcribed in ladder order
// from the consumer's KNOWN_TASK_STATUSES.
//
// An unrecognized status is NOT remapped by the consumer: it is held at 0% and
// reported. So emitting a near-miss is worse than emitting nothing, and this
// producer refuses to render one at all.
type TaskStatus string

// The canonical ladder.
const (
	StatusPlanned           TaskStatus = "planned"
	StatusActive            TaskStatus = "active"
	StatusAwaitingHuman     TaskStatus = "awaiting-human-action"
	StatusBlocked           TaskStatus = "blocked"
	StatusPROpen            TaskStatus = "pr-open"
	StatusChecksPassing     TaskStatus = "checks-passing"
	StatusPackagePrepared   TaskStatus = "package-prepared"
	StatusTestsReviewPassed TaskStatus = "tests-review-passed"
	StatusMerged            TaskStatus = "merged"
	StatusDeployedUAT       TaskStatus = "deployed-uat"
	StatusAppliedUAT        TaskStatus = "applied-uat"
	StatusVerified          TaskStatus = "verified"
	StatusComplete          TaskStatus = "complete"
)

var knownTaskStatuses = map[TaskStatus]bool{
	StatusPlanned: true, StatusActive: true, StatusAwaitingHuman: true,
	StatusBlocked: true, StatusPROpen: true, StatusChecksPassing: true,
	StatusPackagePrepared: true, StatusTestsReviewPassed: true,
	StatusMerged: true, StatusDeployedUAT: true, StatusAppliedUAT: true,
	StatusVerified: true, StatusComplete: true,
}

// ValidateTaskStatus refuses anything outside the canonical vocabulary.
func ValidateTaskStatus(s TaskStatus) error {
	if !knownTaskStatuses[s] {
		return fmt.Errorf("%w: %q (canonical: %s)", ErrUnknownStatus, string(s), strings.Join(canonicalStatusList(), ", "))
	}
	return nil
}

func canonicalStatusList() []string {
	out := make([]string, 0, len(knownTaskStatuses))
	for s := range knownTaskStatuses {
		out = append(out, string(s))
	}
	sort.Strings(out)
	return out
}

// CompletionGateStatus is the evidence gate the consumer reserves 100% for.
//
// The consumer scores `merged` at 70% deliberately: merged is a publication
// fact, not an acceptance one. Only "met" reaches 100%, and only when the
// completion gate genuinely passed. This producer therefore never derives
// "met" from status — see CompletionGate below.
type CompletionGateStatus string

// The three gate states the consumer accepts. Anything else it reads as
// not-met, so an unknown gate degrades safely rather than scoring.
const (
	GateMet          CompletionGateStatus = "met"
	GatePartiallyMet CompletionGateStatus = "partially-met"
	GateNotMet       CompletionGateStatus = "not-met"
)

// Attempt is one dated execution attempt.
//
// The consumer plots correction history from these, so a count is not enough:
// an attempt without a date cannot be placed on a timeline. Every attempt here
// carries the timestamp of the execution event that produced it — never an
// invented one, and a count is never expanded into synthetic dated entries.
type Attempt struct {
	Date     time.Time
	Outcome  string // "failed" | "succeeded"
	Summary  string
	Evidence string
}

// Blocker is one top-level blocker record.
type Blocker struct {
	BlockerID     string
	Summary       string
	HumanBoundary bool
	Evidence      string
}

// CompletedOutcome is one recent completed-outcome narrative entry.
type CompletedOutcome struct {
	Date     time.Time
	Summary  string
	Evidence string
}

// ProjectMeta is the document's project object.
type ProjectMeta struct {
	ProjectID             string
	Strategy              string
	AuthoritativeRef      string
	CurrentPhase          string
	CurrentMilestone      string
	OverallRag            string
	OverallRagReason      string
	LastUpdateTimestamp   time.Time
	LatestAcceptedMainSha string
}

// yamlOpt renders an optional scalar: a real value quoted, an absent one as an
// explicit null.
//
// null rather than "" is deliberate. The consumer treats `?? null` as absent
// and renders it as unknown; an empty string is a present-but-blank value that
// can read as a real answer. Absence must look like absence.
func yamlOpt(v string) string {
	if strings.TrimSpace(v) == "" {
		return "null"
	}
	return yamlScalar(v)
}

// yamlOptTime renders an absent timestamp as null rather than as an epoch. A
// date the producer has no authority for must never look like a real one.
func yamlOptTime(t time.Time) string {
	if t.IsZero() {
		return "null"
	}
	return `"` + t.UTC().Format(time.RFC3339) + `"`
}

func yamlOptInt(v int) string {
	if v == 0 {
		return "null"
	}
	return fmt.Sprintf("%d", v)
}
