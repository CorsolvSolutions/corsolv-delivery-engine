package unattended

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/projector"
)

// QA-003 — CRITICAL DELIVERY INVARIANTS, part 1 of 2: QA PROGRESSION INTEGRITY
// and REPLAY/RECOVERY DETERMINISM.
//
// The question the existing suite does not answer. Every test beside this one
// asks whether one operation is correct in isolation: does a failed gate block,
// does stale evidence block, is Replay idempotent. Each of them is a single
// example. The question a delivery engine that runs unattended has to answer is
// a different one — can a SEQUENCE of individually valid operations arrive
// somewhere none of them would go alone.
//
// So these tests do not assert an example. They generate event orderings —
// thousands of them, from fixed seeds so every failure reproduces exactly — fold
// each one through the real journal replay and the real progression evaluation,
// and check the invariant after the whole sequence AND against an oracle that
// is computed a different way from the code under test. An oracle that reuses
// MergeEvidence to predict MergeEvidence proves only that the function equals
// itself.

// --- deterministic generation ------------------------------------------------

// prng is a small xorshift generator.
//
// It is written out rather than taken from math/rand so that the sequences in
// this file are pinned to this source: a failure reported against seed 7 is
// reproducible from seed 7 forever, including on a toolchain whose global
// generator was reseeded or whose algorithm changed underneath the test.
type prng struct{ s uint64 }

func newPRNG(seed uint64) *prng {
	return &prng{s: seed*6364136223846793005 + 1442695040888963407}
}

func (p *prng) next() uint64 {
	p.s ^= p.s << 13
	p.s ^= p.s >> 7
	p.s ^= p.s << 17
	return p.s
}

func (p *prng) intn(n int) int { return int(p.next() % uint64(n)) }

// The two revisions every generated sequence moves between. Two is enough: the
// property under test is "evidence certifies the code it examined and no
// other", and a third revision exercises no rule a second one does not.
const (
	qa003SHAOld = "1111111111111111111111111111111111111111"
	qa003SHANew = "2222222222222222222222222222222222222222"
)

// qa003GateResults is the result vocabulary a generated gate may report,
// INCLUDING a value this package does not recognize. An unrecognized result is
// generated on purpose: GateResult.Passed is an equality against pass precisely
// so that a vocabulary extended later blocks rather than licenses, and a
// generator that only ever produced the three known values would never test it.
var qa003GateResults = []GateResult{GatePass, GateFail, GateError, GateResult("indeterminate")}

// generateEvidenceSequence builds one journal-shaped ordering of gate evidence
// for a Q3 packet, interleaved with ordinary task records so the fold is proved
// against a realistic journal rather than a stream of nothing but gates.
func generateEvidenceSequence(p *prng, runID string, gates []string) []Record {
	n := 1 + p.intn(12)
	records := make([]Record, 0, n*2)
	seq := 0
	appendRecord := func(r Record) {
		seq++
		r.Seq = seq
		r.RunID = runID
		r.At = time.Unix(int64(1700000000+seq), 0).UTC()
		records = append(records, r)
	}

	for i := 0; i < n; i++ {
		// Ordinary run traffic between gates. It must not affect the ledger.
		switch p.intn(4) {
		case 0:
			appendRecord(Record{Kind: RecordTaskStarted, TaskID: fmt.Sprintf("t%d", p.intn(3))})
		case 1:
			appendRecord(Record{Kind: RecordFenceVerified, TaskID: fmt.Sprintf("t%d", p.intn(3))})
		}

		gate := gates[p.intn(len(gates))]
		sha := qa003SHAOld
		if p.intn(2) == 0 {
			sha = qa003SHANew
		}
		ev := GateEvidence{
			GateID:     gate,
			TaskID:     "task-" + gate,
			Result:     qa003GateResults[p.intn(len(qa003GateResults))],
			TargetSHA:  sha,
			ObservedAt: time.Unix(int64(1700000000+seq), 0).UTC(),
			Reproduce:  []string{"go", "test", "./..."},
		}
		// One ordering in eight records evidence with no revision at all. It is
		// the shape a gate that ran outside a fence produces, and it must
		// certify nothing rather than everything.
		if p.intn(8) == 0 {
			ev.TargetSHA = ""
		}
		appendRecord(Record{Kind: RecordGateEvidence, TaskID: ev.TaskID, Outcome: string(ev.Result), Gate: &ev})
	}

	// A third of the corpus CONVERGES. Without it the generator would only ever
	// produce refusals — four independent gates rarely land on a pass at one
	// revision by chance — and a test that never sees a permitted decision
	// proves nothing about the half of the contract that lets work through.
	//
	// Converging is not simply "append passes". A verdict for the same revision
	// keeps the worse of the two, so a pass appended after a failure at that
	// revision changes nothing: the code has to move first. The closing run
	// therefore records each gate against the old revision and then passes it
	// against the new one, which is what a real re-run after a commit looks
	// like.
	if p.intn(3) == 0 {
		for _, gate := range shuffled(p, gates) {
			ev := GateEvidence{
				GateID: gate, TaskID: "task-" + gate,
				Result:    qa003GateResults[p.intn(len(qa003GateResults))],
				TargetSHA: qa003SHAOld, ObservedAt: time.Unix(1700009000, 0).UTC(),
			}
			appendRecord(Record{Kind: RecordGateEvidence, TaskID: ev.TaskID, Outcome: string(ev.Result), Gate: &ev})
		}
		for _, gate := range shuffled(p, gates) {
			ev := GateEvidence{
				GateID: gate, TaskID: "task-" + gate,
				Result: GatePass, TargetSHA: qa003SHANew, ObservedAt: time.Unix(1700009500, 0).UTC(),
				Reproduce: []string{"go", "test", "./..."},
			}
			appendRecord(Record{Kind: RecordGateEvidence, TaskID: ev.TaskID, Outcome: string(ev.Result), Gate: &ev})
		}
	}
	return records
}

// shuffled returns a permutation, so a converging run's gates do not always
// arrive in catalog order.
func shuffled(p *prng, in []string) []string {
	out := append([]string(nil), in...)
	for i := len(out) - 1; i > 0; i-- {
		j := p.intn(i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// expectedGateEvidence is the oracle: what the ledger must hold for one gate,
// derived from the contract's own words rather than from MergeEvidence.
//
// The contract says a verdict for a different revision replaces what came
// before, and a verdict for the same revision keeps the worse of the two. Read
// backwards that is: the ledger holds the LAST record's revision, and the worst
// result among the unbroken trailing run of records that share it.
func expectedGateEvidence(records []Record, gate string) (GateEvidence, bool) {
	var forGate []GateEvidence
	for _, r := range records {
		if r.Kind == RecordGateEvidence && r.Gate != nil && r.Gate.GateID == gate {
			forGate = append(forGate, *r.Gate)
		}
	}
	if len(forGate) == 0 {
		return GateEvidence{}, false
	}
	last := forGate[len(forGate)-1]
	worst := last
	for i := len(forGate) - 2; i >= 0; i-- {
		if forGate[i].TargetSHA != last.TargetSHA {
			break
		}
		if forGate[i].Result.severity() > worst.Result.severity() {
			worst = forGate[i]
		}
	}
	// The revision is always the last record's; only the verdict is the worst
	// of the trailing run.
	out := worst
	out.TargetSHA = last.TargetSHA
	return out, true
}

// --- INVARIANT 2: QA PROGRESSION INTEGRITY -----------------------------------

func TestNoOrderingOfGateEvidenceLicensesAPacketItsGatesRefuse(t *testing.T) {
	// INVARIANT 2. Missing, failed or stale mandatory evidence must make
	// progression impossible, under EVERY ordering — not merely the one each
	// single-example test happens to write down.
	gates := RequiredGates(QAPolicy{}, RiskQ3)
	if len(gates) != 4 {
		t.Fatalf("the premise moved: Q3 requires %v, want four gates", gates)
	}

	const sequences = 3000
	var permitted, refused, assertions int

	for seed := uint64(0); seed < sequences; seed++ {
		p := newPRNG(seed)
		records := generateEvidenceSequence(p, "run-q3", gates)
		ledger := Replay(records, "run-q3").Gates

		// The replayed ledger must be exactly what the contract's words say it
		// is, computed the other way round.
		for _, gate := range gates {
			want, wanted := expectedGateEvidence(records, gate)
			got, got0 := ledger[gate]
			assertions++
			if wanted != got0 {
				t.Fatalf("seed %d gate %s: ledger presence = %v, want %v", seed, gate, got0, wanted)
			}
			if wanted && (got.Result != want.Result || got.TargetSHA != want.TargetSHA) {
				t.Fatalf("seed %d gate %s: ledger holds %s@%s, the contract says %s@%s",
					seed, gate, got.Result, shortSHA(got.TargetSHA), want.Result, shortSHA(want.TargetSHA))
			}
		}

		for _, target := range []string{qa003SHANew, qa003SHAOld, ""} {
			d := EvaluateProgression(QAPolicy{}, RiskQ3, target, ledger)

			// The independent predicate. Progression is permitted exactly when
			// every mandatory gate holds a PASS bound to the revision in hand.
			wantAllowed := strings.TrimSpace(target) != ""
			for _, gate := range gates {
				ev, ok := ledger[gate]
				if !ok || ev.Result != GatePass || ev.TargetSHA != target {
					wantAllowed = false
				}
			}
			assertions++
			if d.Allowed != wantAllowed {
				t.Fatalf("seed %d target %s: Allowed = %v, want %v — %s",
					seed, shortSHA(target), d.Allowed, wantAllowed, d.Reason())
			}
			if d.Allowed {
				permitted++
				continue
			}
			refused++
			// A refusal must name every gate it refused on, in the mandatory
			// set. A decision that refuses without saying which gate refused is
			// one a reader cannot act on and a later change can silently empty.
			if len(d.Blocking) == 0 {
				t.Fatalf("seed %d target %s: refused with no blocking gate named", seed, shortSHA(target))
			}
			for _, b := range d.Blocking {
				if _, known := LookupGate(b.GateID); !known {
					t.Fatalf("seed %d: blocked on %q, which is not a catalog gate", seed, b.GateID)
				}
			}
			if len(d.Satisfied)+len(d.Blocking) != len(d.Required) {
				t.Fatalf("seed %d target %s: %d satisfied + %d blocking != %d required",
					seed, shortSHA(target), len(d.Satisfied), len(d.Blocking), len(d.Required))
			}
		}
	}

	// A generator that only ever produced refusals would pass this test while
	// proving nothing about the permitting half of the decision.
	if permitted == 0 || refused == 0 {
		t.Fatalf("the generated corpus is degenerate: %d permitted, %d refused", permitted, refused)
	}
	t.Logf("%d sequences, %d assertions: %d decisions permitted, %d refused",
		sequences, assertions, permitted, refused)
}

func TestNoOrderingOfGateEvidenceProjectsATerminalDeliveryStatusItCannotEarn(t *testing.T) {
	// INVARIANT 2, at the document a person actually reads. No event ordering
	// may result in complete, merged or verified — or in completionGateStatus
	// "met" — when mandatory QA does not permit progression.
	gates := RequiredGates(QAPolicy{}, RiskQ3)
	terminal := []string{"merged", "deployed-uat", "applied-uat", "verified", "complete"}

	const sequences = 400
	var refusedProjections int

	for seed := uint64(0); seed < sequences; seed++ {
		p := newPRNG(seed)
		records := generateEvidenceSequence(p, "run-q3", gates)
		ledger := Replay(records, "run-q3").Gates
		target := qa003SHANew
		if seed%3 == 0 {
			target = qa003SHAOld
		}
		d := EvaluateProgression(QAPolicy{}, RiskQ3, target, ledger)

		status := terminal[int(seed)%len(terminal)]
		spec, plan, _ := publishFixture(t)
		plan.Tasks[0].DeliveryStatus = status
		q := NewQueue(plan, nil)
		// Every projected task has SUCCEEDED. That is the whole point: a task's
		// command exiting zero is exactly the evidence that used to be enough.
		for _, qt := range q.Tasks() {
			q.RecordAttempt(qt, TaskAttempt{Succeeded: true, StartedAt: time.Unix(1700000000, 0).UTC(), Duration: "1s"})
		}

		data, err := PublishDelivery(spec, q, &Fence{Branch: "main", Head: target}, d, time.Unix(1700000000, 0).UTC())
		if err != nil {
			t.Fatalf("seed %d: PublishDelivery: %v", seed, err)
		}
		body := string(data)

		if d.Allowed {
			continue
		}
		refusedProjections++
		for _, s := range terminal {
			if strings.Contains(body, `status: "`+s+`"`) {
				t.Fatalf("seed %d: a refused packet projected terminal status %q\ndecision: %s\n%s",
					seed, s, d.Reason(), body)
			}
		}
		if strings.Contains(body, `completionGateStatus: "met"`) {
			t.Fatalf("seed %d: a refused packet met its completion gate\ndecision: %s\n%s", seed, d.Reason(), body)
		}
	}
	if refusedProjections == 0 {
		t.Fatal("no generated sequence produced a refused packet — the corpus proves nothing")
	}
	t.Logf("%d projections rendered under a refused packet, none reached a terminal status", refusedProjections)
}

func TestTheProgressionCeilingCoversEveryStatusTheConsumerScoresAsDone(t *testing.T) {
	// INVARIANT 2, against drift. publish.go keeps its own copy of the terminal
	// vocabulary because the consumer's is unexported and exists for different
	// arithmetic. A copy is only safe while something proves it is still a copy:
	// a status added to the consumer's terminal set and not to the ceiling's is
	// a status a refused packet could still claim.
	statuses := canonicalStatusesFromTheConsumer(t)
	if len(statuses) < 10 {
		t.Fatalf("only %d canonical statuses were recovered from the consumer; the census would be vacuous", len(statuses))
	}

	for _, s := range statuses {
		terminalToConsumer := consumerTreatsStatusAsTerminal(s)
		if got := terminalDeliveryStatuses[s]; got != terminalToConsumer {
			t.Fatalf("status %q: the ceiling treats it as terminal = %v, the consumer does = %v — "+
				"a refused packet can claim a status the ceiling does not lower",
				s, got, terminalToConsumer)
		}
	}
	t.Logf("census over %d canonical statuses: the ceiling's terminal set matches the consumer's", len(statuses))
}

// canonicalStatusesFromTheConsumer recovers the consumer's own status
// vocabulary rather than restating it here.
//
// A list retyped into this test would drift in exactly the way the test exists
// to catch. The vocabulary is not exported, but the consumer names it in full
// when it refuses one, so the refusal is where it is read from.
func canonicalStatusesFromTheConsumer(t *testing.T) []projector.TaskStatus {
	t.Helper()
	err := projector.ValidateTaskStatus("qa-003-not-a-status")
	if err == nil {
		t.Fatal("the consumer accepted a status outside its vocabulary")
	}
	msg := err.Error()
	const marker = "(canonical: "
	i := strings.Index(msg, marker)
	if i < 0 {
		t.Fatalf("the consumer no longer names its vocabulary when it refuses one: %s", msg)
	}
	rest := msg[i+len(marker):]
	j := strings.LastIndex(rest, ")")
	if j < 0 {
		t.Fatalf("the consumer's vocabulary list is unterminated: %s", msg)
	}
	var out []projector.TaskStatus
	for _, name := range strings.Split(rest[:j], ", ") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if verr := projector.ValidateTaskStatus(projector.TaskStatus(name)); verr != nil {
			t.Fatalf("recovered %q from the consumer's own message and it refuses it: %v", name, verr)
		}
		out = append(out, projector.TaskStatus(name))
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}

// consumerTreatsStatusAsTerminal asks the consumer, through its own behavior,
// whether a status satisfies a dependency.
//
// The consumer's isTerminalStatus is unexported; its effect is not. A dependency
// that is terminal stops blocking its dependent, so the blocker arithmetic
// answers the question without the test asserting anything about how.
func consumerTreatsStatusAsTerminal(s projector.TaskStatus) bool {
	state := projector.NewState("census")
	state.Tasks["dep"] = &projector.Task{TaskID: "dep", Status: s}
	state.Tasks["child"] = &projector.Task{TaskID: "child", Status: projector.StatusPlanned, Dependencies: []string{"dep"}}
	state.RecomputeBlockers()
	return !strings.Contains(state.Tasks["child"].Blocker, "waiting on dep")
}

func TestNoPolicyOrRiskCombinationRemovesAMandatoryGate(t *testing.T) {
	// INVARIANT 2. A packet declares its own risk and its own policy, and an
	// authoring agent can edit both. Neither may lower the bar: policy adds and
	// only adds, and a higher risk class never requires less than a lower one.
	var assertions int
	for _, risk := range RiskClasses() {
		base := RequiredGates(QAPolicy{}, risk)
		for _, policy := range []QAPolicy{
			{},
			{RequireGates: []string{GateBuild}},
			{RequireGates: []string{GateControlSafety}},
			{RequireGates: []string{GateBuild, GateBuild, GateUnitTest}},
			{RequireGates: []string{GateStaticAnalysis, GateControlSafety, GateUnitTest, GateBuild}},
		} {
			got := RequiredGates(policy, risk)
			assertions++
			for _, id := range base {
				if !contains(got, id) {
					t.Fatalf("risk %s with policy %v dropped mandatory gate %q", risk, policy.RequireGates, id)
				}
			}
			for _, id := range policy.RequireGates {
				if !contains(got, id) {
					t.Fatalf("risk %s: policy asked for %q and did not get it", risk, id)
				}
			}
			if !sort.StringsAreSorted(got) {
				t.Fatalf("risk %s policy %v: required set %v is not sorted — decisions would not be reproducible",
					risk, policy.RequireGates, got)
			}
			for i := 1; i < len(got); i++ {
				if got[i] == got[i-1] {
					t.Fatalf("risk %s: required set %v repeats a gate", risk, got)
				}
			}
		}
	}
	// Risk is monotone: every gate a class requires is required by every
	// riskier class. A ladder that is not monotone lets a packet acquire FEWER
	// gates by declaring itself more dangerous.
	classes := RiskClasses()
	for i := 1; i < len(classes); i++ {
		lower := RequiredGates(QAPolicy{}, classes[i-1])
		higher := RequiredGates(QAPolicy{}, classes[i])
		assertions++
		for _, id := range lower {
			if !contains(higher, id) {
				t.Fatalf("risk %s requires %q and the riskier %s does not", classes[i-1], id, classes[i])
			}
		}
	}
	// An undeclared class ranks above every declared one, so it acquires every
	// gate rather than none.
	unknown := RequiredGates(QAPolicy{}, RiskClass("Q9"))
	if len(unknown) != len(GateCatalogue()) {
		t.Fatalf("an unrecognized risk class required %v, want the whole catalog", unknown)
	}
	t.Logf("%d risk/policy combinations checked; the mandatory set only ever grows", assertions)
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

// --- INVARIANT 6: REPLAY / RECOVERY DETERMINISM ------------------------------

func TestReplayReconstructsTheSameAuthoritativeStateEveryTime(t *testing.T) {
	// INVARIANT 6. Given the same durable journal, replay must reconstruct the
	// same authoritative state — and the same progression decision with it.
	gates := RequiredGates(QAPolicy{}, RiskQ3)
	const sequences = 1500
	var assertions int

	for seed := uint64(0); seed < sequences; seed++ {
		records := generateEvidenceSequence(newPRNG(seed), "run-det", gates)

		first := Replay(records, "run-det")
		second := Replay(records, "run-det")
		assertions++
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("seed %d: two replays of one journal disagree\n%#v\n%#v", seed, first, second)
		}
		// Replaying a replayed run is normal operation, so the decision it
		// yields must be identical too — not merely equivalent.
		d1 := EvaluateProgression(QAPolicy{}, RiskQ3, qa003SHANew, first.Gates)
		d2 := EvaluateProgression(QAPolicy{}, RiskQ3, qa003SHANew, second.Gates)
		assertions++
		if !reflect.DeepEqual(d1, d2) {
			t.Fatalf("seed %d: the same journal produced two progression decisions:\n%s\n%s", seed, d1.Reason(), d2.Reason())
		}
	}
	t.Logf("%d journals replayed twice each, %d assertions", sequences, assertions)
}

func TestDuplicateEventProcessingDoesNotAlterProgression(t *testing.T) {
	// INVARIANT 6. A crash between writing a record and acting on it, or a
	// retried delivery, presents the same fact twice. Processing it twice must
	// not change what the run is entitled to do.
	gates := RequiredGates(QAPolicy{}, RiskQ3)
	const sequences = 600
	var duplicated int

	for seed := uint64(0); seed < sequences; seed++ {
		records := generateEvidenceSequence(newPRNG(seed), "run-dup", gates)
		baseline := EvaluateProgression(QAPolicy{}, RiskQ3, qa003SHANew, Replay(records, "run-dup").Gates)

		for i, r := range records {
			if r.Kind != RecordGateEvidence {
				continue
			}
			withDup := make([]Record, 0, len(records)+1)
			withDup = append(withDup, records[:i+1]...)
			withDup = append(withDup, r)
			withDup = append(withDup, records[i+1:]...)
			duplicated++

			got := EvaluateProgression(QAPolicy{}, RiskQ3, qa003SHANew, Replay(withDup, "run-dup").Gates)
			if !reflect.DeepEqual(baseline, got) {
				t.Fatalf("seed %d: repeating the gate-evidence record at index %d changed the decision\nwas:  %s\nnow: %s",
					seed, i, baseline.Reason(), got.Reason())
			}
		}
	}
	t.Logf("%d duplicated gate-evidence records, none moved a progression decision", duplicated)
}

func TestACrashBoundaryReplaysToTheStateThatWasDurablyTrue(t *testing.T) {
	// INVARIANT 6. A run that dies between writing and syncing its last record
	// leaves a torn line. That record never became durable, and the resumed run
	// must reconstruct EXACTLY the state of the records that did — no more and
	// no less. Anything else and a crash silently changes what the run believes.
	gates := RequiredGates(QAPolicy{}, RiskQ3)
	const journals = 120
	var tears int

	for seed := uint64(0); seed < journals; seed++ {
		p := newPRNG(seed)
		records := generateEvidenceSequence(p, "run-tear", gates)
		if len(records) < 2 {
			continue
		}
		dir := t.TempDir()
		path := filepath.Join(dir, JournalName)

		j, err := OpenJournal(path, "run-tear")
		if err != nil {
			t.Fatalf("seed %d: OpenJournal: %v", seed, err)
		}
		for _, r := range records {
			r.Seq = 0
			if _, err := j.Append(r); err != nil {
				t.Fatalf("seed %d: Append: %v", seed, err)
			}
		}
		if err := j.Close(); err != nil {
			t.Fatalf("seed %d: Close: %v", seed, err)
		}

		intact, _, err := ReadJournal(path)
		if err != nil {
			t.Fatalf("seed %d: ReadJournal: %v", seed, err)
		}
		if len(intact) != len(records) {
			t.Fatalf("seed %d: wrote %d records and read %d back", seed, len(records), len(intact))
		}
		wantAfterTear := Replay(intact[:len(intact)-1], "run-tear")

		// Tear the final line at an arbitrary point inside it, which is what a
		// process dying mid-write leaves behind.
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("seed %d: reading journal: %v", seed, err)
		}
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		last := lines[len(lines)-1]
		cut := 1 + p.intn(len(last)-1)
		lines[len(lines)-1] = last[:cut]
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("seed %d: tearing the journal: %v", seed, err)
		}

		torn, truncated, err := ReadJournal(path)
		if err != nil {
			t.Fatalf("seed %d: a torn tail must be recoverable, not an error: %v", seed, err)
		}
		if !truncated {
			// The cut happened to leave valid JSON — a shorter document, not a
			// torn one. That is not the case under test.
			continue
		}
		tears++
		got := Replay(torn, "run-tear")
		if !reflect.DeepEqual(wantAfterTear, got) {
			t.Fatalf("seed %d: a torn tail replayed to a different state than the records that were durable\nwant %#v\ngot  %#v",
				seed, wantAfterTear, got)
		}
		// And the decision the resumed run would take is the durable one.
		wantD := EvaluateProgression(QAPolicy{}, RiskQ3, qa003SHANew, wantAfterTear.Gates)
		gotD := EvaluateProgression(QAPolicy{}, RiskQ3, qa003SHANew, got.Gates)
		if !reflect.DeepEqual(wantD, gotD) {
			t.Fatalf("seed %d: a torn tail changed the progression decision\nwant %s\ngot  %s", seed, wantD.Reason(), gotD.Reason())
		}
	}
	if tears < 20 {
		t.Fatalf("only %d journals were genuinely torn; the corpus proves too little", tears)
	}
	t.Logf("%d torn journals each replayed to exactly the durable state", tears)
}

func TestAProgressionDecisionNeverImprovesWithoutNewPassingEvidence(t *testing.T) {
	// INVARIANT 6, in the direction that matters. Replay must not be able to
	// turn a refused packet into a permitted one. Extending a journal may only
	// permit progression when the extension itself records a PASS bound to the
	// revision in hand — never by re-ordering, re-reading or re-processing what
	// was already there.
	gates := RequiredGates(QAPolicy{}, RiskQ3)
	const sequences = 800
	var improvements int

	for seed := uint64(0); seed < sequences; seed++ {
		records := generateEvidenceSequence(newPRNG(seed), "run-mono", gates)
		for i := 1; i <= len(records); i++ {
			prev := EvaluateProgression(QAPolicy{}, RiskQ3, qa003SHANew, Replay(records[:i-1], "run-mono").Gates)
			cur := EvaluateProgression(QAPolicy{}, RiskQ3, qa003SHANew, Replay(records[:i], "run-mono").Gates)
			if !cur.Allowed || prev.Allowed {
				continue
			}
			improvements++
			r := records[i-1]
			if r.Kind != RecordGateEvidence || r.Gate == nil {
				t.Fatalf("seed %d: progression became permitted at record %d, which is a %s and certifies nothing",
					seed, i, r.Kind)
			}
			if !r.Gate.Certifies(qa003SHANew) {
				t.Fatalf("seed %d: progression became permitted on evidence that does not certify %s: %s@%s",
					seed, shortSHA(qa003SHANew), r.Gate.Result, shortSHA(r.Gate.TargetSHA))
			}
		}
	}
	if improvements == 0 {
		t.Fatal("no generated sequence ever reached a permitted decision — the corpus proves nothing")
	}
	t.Logf("%d transitions into a permitted decision, every one carried certifying evidence", improvements)
}

func FuzzJournalReplayIsDeterministic(f *testing.F) {
	// INVARIANT 6 against arbitrary bytes. A journal is a file on disk that
	// outlives the process that wrote it; a resumed run must reconstruct one
	// answer from it or refuse it, and must never panic or answer twice.
	f.Add("")
	f.Add("{\"seq\":1,\"kind\":\"run-started\",\"runId\":\"r\"}\n")
	f.Add("{\"seq\":1,\"kind\":\"gate-evidence\",\"runId\":\"r\",\"gate\":{\"gateId\":\"build\",\"result\":\"pass\",\"targetSha\":\"" + qa003SHANew + "\"}}\n")
	f.Add("{\"seq\":1,\"kind\":\"gate-evidence\",\"runId\":\"r\",\"gate\":{\"gateId\":\"build\",\"result\":\"pass\",\"targetSha\":\"" + qa003SHANew + "\"}}\n" +
		"{\"seq\":2,\"kind\":\"gate-evidence\",\"runId\":\"r\",\"gate\":{\"gateId\":\"build\",\"result\":\"fail\",\"targetSha\":\"" + qa003SHANew + "\"}}\n")
	f.Add("{\"seq\":1,\"kind\":\"run-finished\",\"runId\":\"r\",\"outcome\":\"completed\"}\nnot json")

	f.Fuzz(func(t *testing.T, body string) {
		path := filepath.Join(t.TempDir(), JournalName)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Skip()
		}
		records, _, err := ReadJournal(path)
		if err != nil {
			// A refusal is a valid answer; it must be a stable one.
			if _, _, again := ReadJournal(path); again == nil {
				t.Fatalf("the same journal was refused once and accepted once")
			}
			return
		}
		a := Replay(records, "r")
		b := Replay(records, "r")
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("two replays of one journal disagree:\n%#v\n%#v", a, b)
		}
		da := EvaluateProgression(QAPolicy{}, RiskQ3, qa003SHANew, a.Gates)
		db := EvaluateProgression(QAPolicy{}, RiskQ3, qa003SHANew, b.Gates)
		if !reflect.DeepEqual(da, db) {
			t.Fatalf("one journal produced two progression decisions:\n%s\n%s", da.Reason(), db.Reason())
		}
		// Evidence with no revision, or a verdict that is not a pass, can never
		// license anything however it was spelled on disk.
		for id, ev := range a.Gates {
			if ev.Certifies(qa003SHANew) && (ev.Result != GatePass || ev.TargetSHA != qa003SHANew) {
				t.Fatalf("gate %s certified %s on %s@%q", id, shortSHA(qa003SHANew), ev.Result, ev.TargetSHA)
			}
		}
	})
}
