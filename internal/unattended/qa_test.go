package unattended

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The seven QA-001 acceptance criteria, one test each. They are deliberately
// mechanical: every one of them decides its result from recorded evidence and a
// revision, never from a claim that the work is fine.

const (
	shaOld = "1111111111111111111111111111111111111111"
	shaNew = "2222222222222222222222222222222222222222"
)

// passing builds PASS evidence for a gate against a revision.
func passing(gate, sha string) GateEvidence {
	return GateEvidence{
		GateID: gate, Tool: "go", ToolVersion: "go1.26.5", Result: GatePass,
		ObservedAt: time.Unix(1_700_000_000, 0).UTC(), TargetSHA: sha,
		Reproduce: []string{"go", "test", "./..."},
	}
}

// ledger builds an evidence map from individual records.
func ledger(evs ...GateEvidence) map[string]GateEvidence {
	out := map[string]GateEvidence{}
	for _, e := range evs {
		out[e.GateID] = MergeEvidence(out[e.GateID], e)
	}
	return out
}

func blockedGates(d ProgressionDecision) map[string]BlockReason {
	out := map[string]BlockReason{}
	for _, b := range d.Blocking {
		out[b.GateID] = b.Reason
	}
	return out
}

// ACCEPTANCE 1 — a required gate FAIL blocks progression, and no assertion of
// success can lift it.
func TestRequiredGateFailureBlocksProgression(t *testing.T) {
	evidence := ledger(
		passing(GateBuild, shaNew),
		GateEvidence{
			GateID: GateUnitTest, Tool: "go", Result: GateFail,
			TargetSHA: shaNew, Detail: "--- FAIL: TestThing",
		},
	)

	d := EvaluateProgression(QAPolicy{}, RiskQ1, shaNew, evidence)
	if d.Allowed {
		t.Fatalf("progression allowed with a failing required gate: %s", d.Reason())
	}
	if got := blockedGates(d)[GateUnitTest]; got != BlockFailed {
		t.Fatalf("unit-test blocked as %q, want %q", got, BlockFailed)
	}

	// The authoring agent now asserts the gate passed, for the identical code.
	// The ledger keeps the mechanical verdict: a failure stands until the code
	// changes, so there is no assertion that clears it.
	asserted := GateEvidence{
		GateID: GateUnitTest, Tool: "claude", Result: GatePass, TargetSHA: shaNew,
		Detail: "reviewed the failure and it is not a real problem",
	}
	evidence[GateUnitTest] = MergeEvidence(evidence[GateUnitTest], asserted)

	after := EvaluateProgression(QAPolicy{}, RiskQ1, shaNew, evidence)
	if after.Allowed {
		t.Fatalf("an assertion overrode a mechanical failure: %s", after.Reason())
	}
	if got := evidence[GateUnitTest].Result; got != GateFail {
		t.Fatalf("ledger result = %q after an asserted pass, want %q", got, GateFail)
	}
}

// ACCEPTANCE 2 — valid PASS evidence for every mandatory gate permits
// progression.
func TestValidPassEvidenceForEveryMandatoryGatePermitsProgression(t *testing.T) {
	risk := RiskQ3
	required := RequiredGates(QAPolicy{}, risk)

	evs := make([]GateEvidence, 0, len(required))
	for _, id := range required {
		evs = append(evs, passing(id, shaNew))
	}

	d := EvaluateProgression(QAPolicy{}, risk, shaNew, ledger(evs...))
	if !d.Allowed {
		t.Fatalf("progression refused with every gate passing: %s", d.Reason())
	}
	if len(d.Satisfied) != len(required) {
		t.Fatalf("satisfied = %v, want all of %v", d.Satisfied, required)
	}
	if len(d.Blocking) != 0 {
		t.Fatalf("blocking = %v, want none", d.Blocking)
	}
}

// ACCEPTANCE 3 — missing mandatory evidence blocks progression. Nothing
// recorded is not the same as nothing wrong.
func TestMissingMandatoryEvidenceBlocksProgression(t *testing.T) {
	// Every gate but static-analysis has passed, at the right revision.
	d := EvaluateProgression(QAPolicy{}, RiskQ2, shaNew, ledger(
		passing(GateBuild, shaNew),
		passing(GateUnitTest, shaNew),
	))
	if d.Allowed {
		t.Fatalf("progression allowed with a gate that never ran: %s", d.Reason())
	}
	if got := blockedGates(d)[GateStaticAnalysis]; got != BlockMissing {
		t.Fatalf("static-analysis blocked as %q, want %q", got, BlockMissing)
	}

	// An empty ledger blocks every required gate rather than vacuously passing.
	empty := EvaluateProgression(QAPolicy{}, RiskQ2, shaNew, nil)
	if empty.Allowed || len(empty.Blocking) != len(empty.Required) {
		t.Fatalf("an empty ledger produced %+v, want every required gate blocking", empty)
	}

	// A decision nobody took must not read as a decision that found nothing
	// wrong — those are opposite claims and the report says so.
	if got := (ProgressionDecision{}).Reason(); got != "no progression decision was recorded" {
		t.Fatalf("the zero decision reads as %q", got)
	}
}

// ACCEPTANCE 4 — PASS evidence bound to an older revision cannot certify
// changed code.
func TestPassEvidenceForAnOlderRevisionCannotCertifyChangedCode(t *testing.T) {
	evidence := ledger(passing(GateBuild, shaOld), passing(GateUnitTest, shaOld))

	// At the revision it examined, the evidence certifies.
	if at := EvaluateProgression(QAPolicy{}, RiskQ1, shaOld, evidence); !at.Allowed {
		t.Fatalf("evidence did not certify the revision it examined: %s", at.Reason())
	}

	// The code then changes. The same evidence must certify nothing.
	moved := EvaluateProgression(QAPolicy{}, RiskQ1, shaNew, evidence)
	if moved.Allowed {
		t.Fatalf("stale evidence certified changed code: %s", moved.Reason())
	}
	for _, gate := range []string{GateBuild, GateUnitTest} {
		if got := blockedGates(moved)[gate]; got != BlockStale {
			t.Fatalf("%s blocked as %q, want %q", gate, got, BlockStale)
		}
	}

	// Evidence with no revision of its own certifies nothing either — otherwise
	// an unbound pass would certify every revision there has ever been.
	unbound := ledger(passing(GateBuild, ""), passing(GateUnitTest, ""))
	if d := EvaluateProgression(QAPolicy{}, RiskQ1, shaNew, unbound); d.Allowed {
		t.Fatalf("evidence bound to no revision certified code: %s", d.Reason())
	}
}

// ACCEPTANCE 5 — a Q0 packet does not require or execute Q3-only gates.
func TestQ0DoesNotAcquireHigherRiskGates(t *testing.T) {
	q0 := RequiredGates(QAPolicy{}, RiskQ0)
	if len(q0) != 0 {
		t.Fatalf("Q0 requires %v, want no mandatory gate", q0)
	}
	for _, id := range q0 {
		if id == GateControlSafety {
			t.Fatal("Q0 acquired the Q3-only control-safety gate")
		}
	}

	// A Q0 packet with no gate evidence at all progresses, and a Q0 packet is
	// valid without declaring a single gate-producing task.
	if d := EvaluateProgression(QAPolicy{}, RiskQ0, shaNew, nil); !d.Allowed {
		t.Fatalf("a Q0 packet was refused progression: %s", d.Reason())
	}
	spec, plan := qaFixture(RiskQ0)
	if err := ValidateQAPacket(spec, plan); err != nil {
		t.Fatalf("a Q0 packet with no gate tasks was refused: %v", err)
	}

	// Every lower class is a subset of every higher one, so risk never removes
	// a gate on the way up.
	for i, lower := range RiskClasses() {
		for _, higher := range RiskClasses()[i:] {
			high := map[string]bool{}
			for _, id := range RequiredGates(QAPolicy{}, higher) {
				high[id] = true
			}
			for _, id := range RequiredGates(QAPolicy{}, lower) {
				if !high[id] {
					t.Fatalf("%s requires %q but %s does not", lower, id, higher)
				}
			}
		}
	}
}

// ACCEPTANCE 6 — a Q3 packet cannot avoid its mandatory baseline gates, either
// by omitting them from the plan or by declaring a policy that leaves them out.
func TestQ3CannotAvoidItsMandatoryGates(t *testing.T) {
	required := RequiredGates(QAPolicy{}, RiskQ3)
	var hasControlSafety bool
	for _, id := range required {
		if id == GateControlSafety {
			hasControlSafety = true
		}
	}
	if !hasControlSafety {
		t.Fatalf("Q3 required gates %v omit the control-safety baseline", required)
	}

	// A policy is additive. Declaring an unrelated gate does not narrow the set
	// to it — there is no field by which a packet lowers its own bar.
	narrowed := RequiredGates(QAPolicy{RequireGates: []string{GateBuild}}, RiskQ3)
	if len(narrowed) < len(required) {
		t.Fatalf("policy narrowed the Q3 gate set to %v, want at least %v", narrowed, required)
	}

	// A Q3 packet that declares no task producing the baseline gate is refused
	// before a worker starts.
	spec, plan := qaFixture(RiskQ3)
	var kept []Task
	for _, task := range plan.Tasks {
		if task.QAGate != GateControlSafety {
			kept = append(kept, task)
		}
	}
	plan.Tasks = kept
	err := ValidateQAPacket(spec, plan)
	if !errors.Is(err, ErrQAPacketInvalid) {
		t.Fatalf("a Q3 packet with no control-safety task = %v, want ErrQAPacketInvalid", err)
	}
	if !strings.Contains(err.Error(), GateControlSafety) {
		t.Fatalf("the refusal does not name the missing gate: %v", err)
	}

	// And even a complete Q3 plan does not progress until the gate has actually
	// passed against the code in hand.
	partial := ledger(
		passing(GateBuild, shaNew), passing(GateUnitTest, shaNew), passing(GateStaticAnalysis, shaNew),
	)
	if d := EvaluateProgression(QAPolicy{}, RiskQ3, shaNew, partial); d.Allowed {
		t.Fatalf("a Q3 packet progressed without control-safety evidence: %s", d.Reason())
	}
}

// ACCEPTANCE 7 — malformed or materially incomplete QA packet data fails before
// a worker starts.
func TestMalformedQAPacketFailsBeforeWorkerStart(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Spec, *Plan)
		wantSub string
	}{
		{
			name:    "no risk class declared",
			mutate:  func(_ *Spec, p *Plan) { p.Risk = "" },
			wantSub: "risk unset",
		},
		{
			name:    "risk class outside the declared vocabulary",
			mutate:  func(_ *Spec, p *Plan) { p.Risk = "Q9" },
			wantSub: `risk "Q9"`,
		},
		{
			name:    "task claims a gate that is not in the catalog",
			mutate:  func(_ *Spec, p *Plan) { p.Tasks[0].QAGate = "vibes" },
			wantSub: `qaGate "vibes"`,
		},
		{
			name:    "policy requires a gate that is not in the catalog",
			mutate:  func(s *Spec, _ *Plan) { s.QA.RequireGates = []string{"vibes"} },
			wantSub: `requireGates names "vibes"`,
		},
		{
			name: "two tasks claim the same gate",
			mutate: func(_ *Spec, p *Plan) {
				p.Tasks = append(p.Tasks, Task{
					ID: "second-build", Title: "build again", Band: BandValidation,
					Argv: []string{"true"}, QAGate: GateBuild,
				})
			},
			wantSub: "ambiguous ledger",
		},
		{
			name: "a required gate has no producing task",
			mutate: func(_ *Spec, p *Plan) {
				p.Tasks = p.Tasks[:1]
			},
			wantSub: "and no task declares it",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, plan := qaFixture(RiskQ2)
			tc.mutate(&spec, &plan)

			err := ValidateQAPacket(spec, plan)
			if !errors.Is(err, ErrQAPacketInvalid) {
				t.Fatalf("ValidateQAPacket = %v, want ErrQAPacketInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("refusal %q does not explain the problem (want %q)", err, tc.wantSub)
			}

			// The same packet must be refused by the run's own entry point, and
			// refused before it claims anything.
			if _, berr := Begin(context.Background(), spec, plan); !errors.Is(berr, ErrQAPacketInvalid) && !errors.Is(berr, ErrPlanInvalid) && !errors.Is(berr, ErrSpecInvalid) {
				t.Fatalf("Begin = %v, want the packet refused before a worker starts", berr)
			}
		})
	}

	// The fixture itself must be valid, or the cases above prove nothing.
	spec, plan := qaFixture(RiskQ2)
	if err := ValidateQAPacket(spec, plan); err != nil {
		t.Fatalf("the well-formed fixture was refused: %v", err)
	}
}

// qaFixture is a well-formed packet at the given risk: one task producing each
// gate that risk makes mandatory.
//
// Its gate tasks are `true` rather than real tools on purpose. QA-001 is the
// orchestration contract — classify, select, invoke, record, decide — and
// which binary satisfies a gate is the subject of the tool packets that plug
// into it later.
func qaFixture(risk RiskClass) (Spec, Plan) {
	spec := Spec{
		ProjectID: "corsolv-delivery-engine",
		StateDir:  "/tmp/does-not-need-to-exist",
		Ownership: Ownership{
			ProjectID: "corsolv-delivery-engine",
			Worktree:  "/tmp/does-not-need-to-exist",
			Role:      RoleWriter,
			Session:   "qa-fixture",
		},
	}
	plan := Plan{RunID: "qa-fixture", Risk: risk, Tasks: []Task{
		{ID: "work", Title: "the packet's work", Band: BandPrimary, Argv: []string{"true"}},
	}}
	for _, id := range RequiredGates(spec.QA, risk) {
		def, _ := LookupGate(id)
		plan.Tasks = append(plan.Tasks, Task{
			ID: "gate-" + id, Title: def.Title, Band: BandValidation,
			Argv: []string{"true"}, QAGate: id,
		})
	}
	return spec, plan
}

// TestGateEvidenceCarriesReproducibleDetail proves the evidence a gate records
// is structured enough to be acted on rather than merely believed.
func TestGateEvidenceCarriesReproducibleDetail(t *testing.T) {
	ev := passing(GateUnitTest, shaNew)
	switch {
	case ev.GateID == "":
		t.Fatal("evidence must name the gate it is for")
	case ev.Tool == "":
		t.Fatal("evidence must name the tool that produced the verdict")
	case ev.ObservedAt.IsZero():
		t.Fatal("evidence must be dated")
	case len(ev.Reproduce) == 0:
		t.Fatal("evidence that cannot be reproduced is testimony")
	case ev.TargetSHA == "":
		t.Fatal("evidence must name the revision it examined")
	}
}

// TestUnknownGateResultDoesNotCertify proves the fail-closed direction of the
// result vocabulary: a value this package does not recognize is not a pass.
func TestUnknownGateResultDoesNotCertify(t *testing.T) {
	ev := passing(GateBuild, shaNew)
	ev.Result = "probably-fine"
	if ev.Certifies(shaNew) {
		t.Fatal("an unrecognized result certified code")
	}
	if d := EvaluateProgression(QAPolicy{}, RiskQ1, shaNew, ledger(ev, passing(GateUnitTest, shaNew))); d.Allowed {
		t.Fatalf("an unrecognized result licensed progression: %s", d.Reason())
	}
}

// TestProgressionWithoutATargetRevisionBlocks proves that a packet with gates
// but no revision in hand certifies nothing, rather than certifying everything.
func TestProgressionWithoutATargetRevisionBlocks(t *testing.T) {
	d := EvaluateProgression(QAPolicy{}, RiskQ1, "", ledger(
		passing(GateBuild, shaNew), passing(GateUnitTest, shaNew),
	))
	if d.Allowed {
		t.Fatalf("progression allowed against no revision: %s", d.Reason())
	}
	for _, b := range d.Blocking {
		if b.Reason != BlockUnknownTarget {
			t.Fatalf("%s blocked as %q, want %q", b.GateID, b.Reason, BlockUnknownTarget)
		}
	}
}

// TestShippedPacketsSatisfyTheQAContract holds the repository's own work
// packets to the contract.
//
// Without it the contract is only proved against fixtures, and the packets that
// actually run unattended could drift out of compliance until the next run
// refused to start — at the one moment nobody is watching.
func TestShippedPacketsSatisfyTheQAContract(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "corsolv", "unattended", "*.plan.toml"))
	if err != nil {
		t.Fatalf("globbing the shipped packets: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no shipped work packets were found to check")
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			plan, err := LoadPlan(path)
			if err != nil {
				t.Fatalf("LoadPlan: %v", err)
			}
			if !plan.Risk.Valid() {
				t.Fatalf("risk %q is not a declared class", plan.Risk)
			}
			// The empty policy is the weakest one a project can apply, so a
			// packet that satisfies the contract under it satisfies it under
			// any policy that only adds.
			if err := ValidateQAPacket(Spec{}, plan); err != nil && errors.Is(err, ErrQAPacketInvalid) {
				t.Fatalf("the shipped packet does not satisfy the QA contract: %v", err)
			}
		})
	}
}

// TestRunRecordsGateEvidenceAndPermitsProgression drives the whole contract
// through a real run: a packet's gate tasks execute, their verdicts are bound
// to the revision in hand, and the run reports completion because the gates
// passed — not because its commands exited zero.
func TestRunRecordsGateEvidenceAndPermitsProgression(t *testing.T) {
	f := newRunFixture(t)
	s := f.begin(t, Plan{RunID: "run-qa-pass", Risk: RiskQ1, Tasks: []Task{
		{ID: "work", Title: "the packet's work", Band: BandPrimary, Argv: sh("true")},
		{ID: "gate-build", Title: "build", Band: BandValidation, Argv: sh("true"), QAGate: GateBuild},
		{ID: "gate-test", Title: "test", Band: BandValidation, Argv: sh("true"), QAGate: GateUnitTest},
	}})

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if event.Outcome != RunCompleted {
		t.Fatalf("outcome = %s (%s), want completed", event.Outcome, event.Reason)
	}
	if !event.QA.Allowed {
		t.Fatalf("progression refused after both gates passed: %s", event.QA.Reason())
	}
	build, ok := s.Runner.Evidence[GateBuild]
	if !ok {
		t.Fatal("the run recorded no evidence for a gate it executed")
	}
	if build.TargetSHA != s.Fence.Head {
		t.Fatalf("evidence bound to %s, want the revision in hand %s", build.TargetSHA, s.Fence.Head)
	}
	if build.TaskID != "gate-build" || len(build.Reproduce) == 0 {
		t.Fatalf("evidence is not reproducible: %+v", build)
	}

	// The evidence is durable, not merely in memory.
	records, _, rerr := ReadJournal(stateDirPath(f.stateDir, JournalName))
	if rerr != nil {
		t.Fatalf("ReadJournal: %v", rerr)
	}
	if got := Replay(records, "run-qa-pass").Gates; len(got) != 2 {
		t.Fatalf("journal replayed %d gate(s), want 2", len(got))
	}
}

// TestRunRefusesToCompleteWhenAGateFailsOrGoesStale is the fail-closed half of
// the same loop, proved twice over: a required gate that fails blocks, and a
// required gate that passed before the run's own commit no longer certifies the
// code that commit produced.
func TestRunRefusesToCompleteWhenAGateFailsOrGoesStale(t *testing.T) {
	t.Run("a required gate that fails blocks the run", func(t *testing.T) {
		f := newRunFixture(t)
		s := f.begin(t, Plan{RunID: "run-qa-fail", Risk: RiskQ1, Tasks: []Task{
			{ID: "gate-build", Title: "build", Band: BandValidation, Argv: sh("true"), QAGate: GateBuild},
			{
				ID: "gate-test", Title: "test", Band: BandValidation, MaxAttempts: 1,
				Argv: sh(`echo "--- FAIL: TestThing"; exit 1`), QAGate: GateUnitTest,
			},
		}})

		event, err := s.Runner.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if event.QA.Allowed {
			t.Fatalf("progression allowed with a failing gate: %s", event.QA.Reason())
		}
		if event.Outcome == RunCompleted {
			t.Fatalf("outcome = completed with a failing required gate: %s", event.Reason)
		}
		if got := s.Runner.Evidence[GateUnitTest].Result; got != GateFail {
			t.Fatalf("recorded result = %q, want %q", got, GateFail)
		}
	})

	t.Run("a gate that passed before the run's own commit goes stale", func(t *testing.T) {
		f := newRunFixture(t)
		s := f.begin(t, Plan{RunID: "run-qa-stale", Risk: RiskQ1, Tasks: []Task{
			{ID: "gate-build", Title: "build", Band: BandValidation, Argv: sh("true"), QAGate: GateBuild},
			{ID: "gate-test", Title: "test", Band: BandValidation, Argv: sh("true"), QAGate: GateUnitTest},
			{
				// Runs last, and moves the code the gates examined.
				ID: "amend", Title: "commit after the gates ran", Band: BandNextStage, Mutates: true,
				Needs: []string{"gate-build", "gate-test"},
				Argv:  sh(`echo late > late.txt && git add late.txt && git commit -qm "feat: late"`),
			},
		}})

		before := s.Fence.Head
		event, err := s.Runner.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if s.Fence.Head == before {
			t.Fatal("the fixture did not actually move the code")
		}
		if event.Tasks[TaskSucceeded] != 3 {
			t.Fatalf("succeeded = %d, want every task to have succeeded", event.Tasks[TaskSucceeded])
		}
		// Every task succeeded, and the packet still may not progress: the
		// evidence certifies the code as it was before the last commit.
		if event.QA.Allowed {
			t.Fatalf("stale evidence certified the run's own later commit: %s", event.QA.Reason())
		}
		if event.Outcome == RunCompleted {
			t.Fatalf("outcome = completed on stale evidence: %s", event.Reason)
		}
		for _, b := range event.QA.Blocking {
			if b.Reason != BlockStale {
				t.Fatalf("%s blocked as %q, want %q", b.GateID, b.Reason, BlockStale)
			}
		}
	})
}

// TestEvidenceLedgerSurvivesReplay proves the ledger is durable: it is folded
// out of the journal, so a resumed run inherits the verdicts — including the
// failures it might otherwise clear by restarting.
func TestEvidenceLedgerSurvivesReplay(t *testing.T) {
	fail := GateEvidence{GateID: GateUnitTest, Result: GateFail, TargetSHA: shaNew, Detail: "--- FAIL"}
	pass := passing(GateUnitTest, shaNew)
	records := []Record{
		{Seq: 1, RunID: "r", Kind: RecordGateEvidence, Gate: &fail},
		{Seq: 2, RunID: "r", Kind: RecordGateEvidence, Gate: &pass},
		{Seq: 3, RunID: "other", Kind: RecordGateEvidence, Gate: &pass},
	}

	st := Replay(records, "r")
	got, ok := st.Gates[GateUnitTest]
	if !ok {
		t.Fatal("replay lost the gate evidence")
	}
	if got.Result != GateFail {
		t.Fatalf("replayed result = %q, want the failure to survive a later pass for the same code", got.Result)
	}

	// A pass at a *new* revision does replace it: the code moved, so the old
	// verdict is about code that no longer exists.
	moved := passing(GateUnitTest, shaOld)
	st = Replay(append(records, Record{Seq: 4, RunID: "r", Kind: RecordGateEvidence, Gate: &moved}), "r")
	if st.Gates[GateUnitTest].Result != GatePass || st.Gates[GateUnitTest].TargetSHA != shaOld {
		t.Fatalf("evidence for a new revision did not replace the old verdict: %+v", st.Gates[GateUnitTest])
	}
}
