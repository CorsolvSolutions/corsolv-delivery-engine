package unattended

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func publishFixture(t *testing.T) (Spec, Plan, *Queue) {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	spec := Spec{
		ProjectID:   "corsolv-delivery-engine",
		StateDir:    stateDir,
		PublishPath: filepath.Join(t.TempDir(), "delivery", "PROJECT-STATE.yml"),
		Ownership: Ownership{
			ProjectID: "corsolv-delivery-engine", Worktree: "/tmp/wt",
			ExpectedOrigin: testOrigin, ExpectedBranch: "main",
			Role: RoleWriter, Session: "s",
		},
	}
	plan := Plan{RunID: "run-publish", Risk: RiskQ0, Tasks: []Task{
		{
			ID: "ship", Title: "ship the control layer", Band: BandPrimary, Argv: sh("true"),
			DeliveryStatus: "merged", CompletionGate: "CI green at the exact merged SHA", Phase: "delivery",
		},
		{
			ID: "review", Title: "await review", Band: BandPrimary, Argv: sh("true"),
			DeliveryStatus: "pr-open", CompletionGate: "an approving review", Needs: []string{"ship"},
		},
		{ID: "internal", Title: "an internal step", Band: BandValidation, Argv: sh("true")},
	}}
	return spec, plan, NewQueue(plan, nil)
}

func TestPublishRendersTheProjectorSchema(t *testing.T) {
	spec, _, q := publishFixture(t)
	ship, _ := q.Task("ship")
	q.RecordAttempt(ship, TaskAttempt{Succeeded: true, StartedAt: time.Now().UTC(), Duration: "2s"})

	data, err := PublishDelivery(spec, q, &Fence{Branch: "main", Head: "abc123def456"}, time.Now())
	if err != nil {
		t.Fatalf("PublishDelivery: %v", err)
	}
	body := string(data)
	for _, want := range []string{"schemaVersion", "projectId", "corsolv-delivery-engine", "ship"} {
		if !strings.Contains(body, want) {
			t.Fatalf("projection omits %q:\n%s", want, body)
		}
	}
	if _, err := os.Stat(spec.PublishPath); err != nil {
		t.Fatalf("the projection was not written to the publish path: %v", err)
	}
}

func TestPublishOmitsInternalMachinery(t *testing.T) {
	// Projecting every green command as delivery progress would overstate what
	// actually shipped.
	spec, _, q := publishFixture(t)
	internal, _ := q.Task("internal")
	q.RecordAttempt(internal, TaskAttempt{Succeeded: true, StartedAt: time.Now().UTC(), Duration: "1s"})

	data, err := PublishDelivery(spec, q, nil, time.Now())
	if err != nil {
		t.Fatalf("PublishDelivery: %v", err)
	}
	if strings.Contains(string(data), "an internal step") {
		t.Fatal("a task with no declared delivery status must not appear in the projection")
	}
}

func TestPublishNeverClaimsADeliveryStatusTheWorkDidNotEarn(t *testing.T) {
	spec, _, q := publishFixture(t)
	data, err := PublishDelivery(spec, q, nil, time.Now())
	if err != nil {
		t.Fatalf("PublishDelivery: %v", err)
	}
	// Match the status field itself. The word "merged" also occurs inside the
	// declared completion-gate text, which is not a status claim.
	if strings.Contains(string(data), `status: "merged"`) {
		t.Fatalf("an unattempted task was projected with the status its success would have established:\n%s", data)
	}
	if !strings.Contains(string(data), "planned") {
		t.Fatalf("an unattempted task must project as planned:\n%s", data)
	}
}

func TestPublishRefusesAStatusOutsideTheConsumerVocabulary(t *testing.T) {
	// A near-miss status is worse than none: the consumer holds an unknown
	// status at 0% and reports it, so a typo silently stalls a real project.
	spec, plan, _ := publishFixture(t)
	plan.Tasks[0].DeliveryStatus = "mostly-merged"
	q := NewQueue(plan, nil)

	if _, err := PublishDelivery(spec, q, nil, time.Now()); err == nil {
		t.Fatal("an invented delivery status must be refused, not rendered")
	}
}

func TestPublishProjectsAHeldTaskAsAwaitingHuman(t *testing.T) {
	spec, _, q := publishFixture(t)
	ship, _ := q.Task("ship")
	q.Hold(ship, "a human reviewer must approve the pull request")

	data, err := PublishDelivery(spec, q, nil, time.Now())
	if err != nil {
		t.Fatalf("PublishDelivery: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "awaiting-human-action") {
		t.Fatalf("a held task must project as awaiting-human-action:\n%s", body)
	}
	if !strings.Contains(body, "a human reviewer must approve") {
		t.Fatal("the projection must carry the blocker's own words")
	}
}

func TestPublishRecordsRealAttemptTimings(t *testing.T) {
	spec, _, q := publishFixture(t)
	ship, _ := q.Task("ship")
	when := time.Date(2026, 8, 11, 6, 30, 0, 0, time.UTC)
	q.RecordAttempt(ship, TaskAttempt{Succeeded: false, StartedAt: when, Duration: "5s", Class: FailureRetryable, Reason: "a transient failure"})
	q.RecordAttempt(ship, TaskAttempt{Succeeded: true, StartedAt: when.Add(time.Minute), Duration: "3s"})

	data, err := PublishDelivery(spec, q, nil, time.Now())
	if err != nil {
		t.Fatalf("PublishDelivery: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "2026-08-11T06:30:00Z") {
		t.Fatalf("the projection must place attempts on the timeline they happened on:\n%s", body)
	}
	if !strings.Contains(body, "a transient failure") {
		t.Fatal("a failed attempt's reason must reach the projection")
	}
}

func TestPublishDoesNotMeetAGateThatWasNeverDeclared(t *testing.T) {
	// "met" against no gate would score an unexamined task at 100%.
	spec, plan, _ := publishFixture(t)
	plan.Tasks[0].CompletionGate = ""
	q := NewQueue(plan, nil)
	ship, _ := q.Task("ship")
	q.RecordAttempt(ship, TaskAttempt{Succeeded: true, StartedAt: time.Now().UTC(), Duration: "1s"})

	data, err := PublishDelivery(spec, q, nil, time.Now())
	if err != nil {
		t.Fatalf("PublishDelivery: %v", err)
	}
	if strings.Contains(string(data), "met") && !strings.Contains(string(data), "not-met") {
		t.Fatalf("a task with no declared gate must not report a met gate:\n%s", data)
	}
}

func TestPublishWithNoPathStillRenders(t *testing.T) {
	// A run that publishes nowhere must still be able to produce its projection
	// for evidence, rather than silently doing nothing.
	spec, _, q := publishFixture(t)
	spec.PublishPath = ""
	data, err := PublishDelivery(spec, q, nil, time.Now())
	if err != nil {
		t.Fatalf("PublishDelivery: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("the projection must be rendered even when it is not written")
	}
}
