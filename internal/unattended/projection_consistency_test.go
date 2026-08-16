package unattended

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// These are the regressions for the projection-consistency defect: the delivery
// projection used to project a task's declared terminal status, and a met
// completion gate with it, the moment the task's command exited zero — with no
// reference to whether the packet's mandatory QA gates permitted progression.
//
// The run's own completion event refused such a packet. The projection beside
// it said the work had shipped. A reader gets the projection.

// refusedProgression is the decision a Q1 packet gets when its mandatory gates
// have recorded nothing: build and unit-test are required, neither has
// evidence, so nothing may progress.
func refusedProgression(head string) ProgressionDecision {
	return EvaluateProgression(QAPolicy{}, RiskQ1, head, nil)
}

func TestProjectionRefusesCOMPLETEWhenMandatoryQAHasNotPassed(t *testing.T) {
	spec, plan, _ := publishFixture(t)
	plan.Tasks[0].DeliveryStatus = "complete"
	q := NewQueue(plan, nil)
	ship, _ := q.Task("ship")
	q.RecordAttempt(ship, TaskAttempt{Succeeded: true, StartedAt: time.Now().UTC(), Duration: "2s"})

	fence := &Fence{Branch: "main", Head: "abc123def456"}
	qa := refusedProgression(fence.Head)
	if qa.Allowed {
		t.Fatal("the fixture's premise is wrong: this decision must refuse")
	}

	data, err := PublishDelivery(spec, q, fence, qa, time.Now())
	if err != nil {
		t.Fatalf("PublishDelivery: %v", err)
	}
	body := string(data)
	if strings.Contains(body, `status: "complete"`) {
		t.Fatalf("a successful task projected COMPLETE with no passing mandatory gate:\n%s", body)
	}
	if strings.Contains(body, `completionGateStatus: "met"`) {
		t.Fatalf("a completion gate was met with no passing mandatory gate:\n%s", body)
	}
	// not-met is the field handoff.Assess keys on when it decides whether a
	// package is complete, so this is where the refusal reaches the delivery's
	// own completion assessment rather than stopping at the run's report.
	if !strings.Contains(body, `completionGateStatus: "not-met"`) {
		t.Fatalf("the refused packet must render an explicitly unmet gate:\n%s", body)
	}
	if !strings.Contains(body, "may not progress") {
		t.Fatalf("the projection must say why the claim was refused:\n%s", body)
	}
}

func TestProjectionRefusesEveryTerminalDeliveryStatusUnderARefusedPacket(t *testing.T) {
	// The ceiling is over the whole terminal vocabulary, not over the word
	// "complete": a packet that may not progress has not merged, deployed or
	// verified anything either.
	for _, status := range []string{"merged", "deployed-uat", "applied-uat", "verified", "complete"} {
		t.Run(status, func(t *testing.T) {
			spec, plan, _ := publishFixture(t)
			plan.Tasks[0].DeliveryStatus = status
			q := NewQueue(plan, nil)
			ship, _ := q.Task("ship")
			q.RecordAttempt(ship, TaskAttempt{Succeeded: true, StartedAt: time.Now().UTC(), Duration: "1s"})

			fence := &Fence{Branch: "main", Head: "abc123def456"}
			data, err := PublishDelivery(spec, q, fence, refusedProgression(fence.Head), time.Now())
			if err != nil {
				t.Fatalf("PublishDelivery: %v", err)
			}
			if strings.Contains(string(data), `status: "`+status+`"`) {
				t.Fatalf("terminal status %q survived a refused packet:\n%s", status, data)
			}
		})
	}
}

func TestProjectionIsUnchangedWhenTheGatesPermitProgression(t *testing.T) {
	// The ceiling can only ever lower a claim. A permitted packet is left
	// exactly as the run described it.
	spec, plan, _ := publishFixture(t)
	q := NewQueue(plan, nil)
	ship, _ := q.Task("ship")
	q.RecordAttempt(ship, TaskAttempt{Succeeded: true, StartedAt: time.Now().UTC(), Duration: "1s"})

	fence := &Fence{Branch: "main", Head: "abc123def456"}
	evidence := map[string]GateEvidence{
		GateBuild:    {GateID: GateBuild, Result: GatePass, TargetSHA: fence.Head},
		GateUnitTest: {GateID: GateUnitTest, Result: GatePass, TargetSHA: fence.Head},
	}
	qa := EvaluateProgression(QAPolicy{}, RiskQ1, fence.Head, evidence)
	if !qa.Allowed {
		t.Fatalf("the fixture's premise is wrong: %s", qa.Reason())
	}

	data, err := PublishDelivery(spec, q, fence, qa, time.Now())
	if err != nil {
		t.Fatalf("PublishDelivery: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, `status: "merged"`) {
		t.Fatalf("a certified task lost the status its success established:\n%s", body)
	}
	if !strings.Contains(body, `completionGateStatus: "met"`) {
		t.Fatalf("a certified task lost its met completion gate:\n%s", body)
	}
}

func TestProjectionRefusesStaleGateEvidence(t *testing.T) {
	// Evidence that passed against a different revision certifies code that is
	// not the code in hand, and the projection must not launder it.
	spec, plan, _ := publishFixture(t)
	q := NewQueue(plan, nil)
	ship, _ := q.Task("ship")
	q.RecordAttempt(ship, TaskAttempt{Succeeded: true, StartedAt: time.Now().UTC(), Duration: "1s"})

	fence := &Fence{Branch: "main", Head: "abc123def456"}
	stale := map[string]GateEvidence{
		GateBuild:    {GateID: GateBuild, Result: GatePass, TargetSHA: "0000000000"},
		GateUnitTest: {GateID: GateUnitTest, Result: GatePass, TargetSHA: "0000000000"},
	}
	data, err := PublishDelivery(spec, q, fence, EvaluateProgression(QAPolicy{}, RiskQ1, fence.Head, stale), time.Now())
	if err != nil {
		t.Fatalf("PublishDelivery: %v", err)
	}
	if strings.Contains(string(data), `status: "merged"`) {
		t.Fatalf("stale gate evidence licensed a merged status:\n%s", data)
	}
}

func TestAProjectionPublishedDuringARunNeverOutrunsItsGates(t *testing.T) {
	// The projection is refreshed alongside the heartbeat, so mid-run it is
	// read while the gates are still running. It must not claim completion in
	// that window either.
	f := newControllerFixture(t)
	publishPath := stateDirPath(f.stateDir, "PROJECT-STATE.yml")
	f.spec.PublishPath = publishPath

	build := supervisedTask("build", BandPrimary)
	build.QAGate = GateBuild
	build.DeliveryStatus = "complete"
	build.CompletionGate = "the packet's mandatory gates"
	tests := supervisedTask("tests", BandValidation)
	tests.QAGate = GateUnitTest

	agent := newScriptedAgent().
		on("build", structured(true, ControllerResult{State: StateComplete}, "built")).
		on("tests", structured(false, ControllerResult{State: StateFailed, TerminalReason: "assertion"}, "--- FAIL"))

	s := f.begin(t, Plan{RunID: "run-projection", Risk: RiskQ1, Tasks: []Task{build, tests}}, agent)
	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if event.QA.Allowed {
		t.Fatalf("the fixture's premise is wrong: %s", event.QA.Reason())
	}

	data, rerr := os.ReadFile(publishPath)
	if rerr != nil {
		t.Fatalf("the run published no projection: %v", rerr)
	}
	body := string(data)
	if strings.Contains(body, `status: "complete"`) {
		t.Fatalf("the projection the run published claims completion its gates refused:\n%s", body)
	}
	if strings.Contains(body, `completionGateStatus: "met"`) {
		t.Fatalf("the projection the run published met a gate its packet did not:\n%s", body)
	}
	// The two documents a reader compares must agree.
	if !strings.Contains(event.Reason, "may not progress") && event.Outcome == RunCompleted {
		t.Fatalf("the completion event and the projection disagree: %s", event.Reason)
	}
}

// THE DECISION HAS TO BE READABLE WHILE THE RUN IS STILL RUNNING.
//
// The run caps its own projection with it. The delivery driver renders the OTHER
// projection — the one an acceptance assessment reads — and renders it from
// inside a task of this run, so it can apply the same cap only if the decision
// is published before the run ends. Publishing it in the completion event alone
// would leave the document a person treats as the answer written by a stage with
// no way of knowing what its packet's gates permitted.
func TestTheHeartbeatPublishesTheProgressionDecisionWhileTheRunIsStillRunning(t *testing.T) {
	f := newControllerFixture(t)

	build := supervisedTask("build", BandPrimary)
	build.QAGate = GateBuild
	tests := supervisedTask("tests", BandValidation)
	tests.QAGate = GateUnitTest

	agent := newScriptedAgent().
		on("build", structured(true, ControllerResult{State: StateComplete}, "built")).
		on("tests", structured(false, ControllerResult{State: StateFailed, TerminalReason: "assertion"}, "--- FAIL"))

	s := f.begin(t, Plan{RunID: "run-heartbeat", Risk: RiskQ1, Tasks: []Task{build, tests}}, agent)
	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	progress, found, err := ReadProgress(f.stateDir)
	if err != nil || !found {
		t.Fatalf("the run published no heartbeat: found=%v err=%v", found, err)
	}
	if progress.QA.Allowed {
		t.Fatalf("the heartbeat says the packet may progress and its own gates say %s", event.QA.Reason())
	}
	if progress.QA.Reason() != event.QA.Reason() {
		t.Fatalf("the heartbeat and the completion event carry different decisions:\n heartbeat:  %s\n completion: %s",
			progress.QA.Reason(), event.QA.Reason())
	}
	if len(progress.QA.Blocking) == 0 {
		t.Fatal("a refusal that names no gate is one a reader cannot act on")
	}
}
