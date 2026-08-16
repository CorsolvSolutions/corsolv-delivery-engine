package unattended

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// This file is the mechanical QA contract: a work packet declares the risk of
// the change it makes, the risk plus the packet's policy resolves to an
// explicit set of mandatory gates, each gate produces structured evidence bound
// to the exact revision it examined, and progression is permitted only when
// every mandatory gate has valid PASS evidence for the revision in hand.
//
// The property the contract exists to hold is that an authoring agent cannot
// certify its own output. Nothing here reads an assertion. EvaluateProgression
// takes recorded evidence and a target revision and returns a decision; there
// is no field, flag or override by which a claim of success can substitute for
// a gate that failed, never ran, or ran against different code.

// RiskClass is how much the packet's change can hurt if it is wrong.
//
// It is declared by the packet rather than inferred, because inferring it would
// put a judgement call in Go — and the judgement in question ("is this
// orchestration or is it application code") is exactly the one a person or an
// agent is better placed to make than a pattern match over file paths.
type RiskClass string

// The four risk classes.
const (
	// RiskQ0 — trivial. Documentation, comments, formatting: changes that
	// cannot alter behavior.
	RiskQ0 RiskClass = "Q0"
	// RiskQ1 — normal application code.
	RiskQ1 RiskClass = "Q1"
	// RiskQ2 — scripts, infrastructure, orchestration: code that runs other
	// code, where a defect is not contained by the process that has it.
	RiskQ2 RiskClass = "Q2"
	// RiskQ3 — autonomous or production-affecting control: code that acts
	// without a person in the loop, or on production state.
	RiskQ3 RiskClass = "Q3"
)

var riskRank = map[RiskClass]int{RiskQ0: 0, RiskQ1: 1, RiskQ2: 2, RiskQ3: 3}

// Valid reports whether the class is one of the declared four.
func (r RiskClass) Valid() bool { _, ok := riskRank[r]; return ok }

// Rank orders risk classes. Higher is riskier.
//
// An unrecognized class ranks above every declared one. A class added to a
// packet but not to this table therefore acquires every gate rather than none,
// which is the fail-closed direction to be wrong in.
func (r RiskClass) Rank() int {
	if n, ok := riskRank[r]; ok {
		return n
	}
	return len(riskRank)
}

// RiskClasses returns the declared classes in ascending order of risk.
func RiskClasses() []RiskClass {
	return []RiskClass{RiskQ0, RiskQ1, RiskQ2, RiskQ3}
}

// The gate catalogue's identifiers.
//
// It is deliberately small. A gate here is a *class* of mechanical evidence,
// not a tool: which tool satisfies GateStaticAnalysis for a PowerShell packet
// and which satisfies it for a Go one is a later, bounded question, and the
// packet answers it by declaring a task that produces the gate.
const (
	// GateBuild — the packet's code compiles or otherwise loads.
	GateBuild = "build"
	// GateUnitTest — the packet's own automated tests pass.
	GateUnitTest = "unit-test"
	// GateStaticAnalysis — the packet passes static examination: vet, lint,
	// analyzer, whatever the language's equivalent is.
	GateStaticAnalysis = "static-analysis"
	// GateControlSafety — the packet's autonomous or production-affecting
	// behavior has been mechanically examined. It is the baseline a Q3 packet
	// cannot be without.
	GateControlSafety = "control-safety"
)

// GateDefinition is one catalog entry: a gate and the risk at which it stops
// being optional.
//
// The catalog is a table rather than a chain of conditionals on purpose.
// Required-gate selection is then a filter over data, so adding a gate is
// adding a row, and no packet can acquire a gate through a special case written
// somewhere else.
type GateDefinition struct {
	// ID is the gate's stable identifier, used by evidence and by policy.
	ID string
	// Title says what the gate proves, for reports.
	Title string
	// MinRisk is the lowest risk class at which the gate is mandatory. A packet
	// classified below it does not require the gate at all.
	MinRisk RiskClass
}

// gateCatalogue is the whole of the mandatory-gate policy.
var gateCatalogue = []GateDefinition{
	{ID: GateBuild, Title: "the packet's code builds", MinRisk: RiskQ1},
	{ID: GateUnitTest, Title: "the packet's automated tests pass", MinRisk: RiskQ1},
	{ID: GateStaticAnalysis, Title: "the packet passes static examination", MinRisk: RiskQ2},
	{ID: GateControlSafety, Title: "the packet's autonomous behavior is mechanically examined", MinRisk: RiskQ3},
}

// GateCatalogue returns the declared gates, in catalog order.
func GateCatalogue() []GateDefinition {
	out := make([]GateDefinition, len(gateCatalogue))
	copy(out, gateCatalogue)
	return out
}

// LookupGate returns a catalog entry by ID.
func LookupGate(id string) (GateDefinition, bool) {
	for _, d := range gateCatalogue {
		if d.ID == id {
			return d, true
		}
	}
	return GateDefinition{}, false
}

// QAPolicy is the packet- or project-level half of gate selection.
//
// It can only add. There is deliberately no field by which a packet lowers the
// bar its own risk class sets: a policy that could remove a mandatory gate
// would make the risk classification advisory, and an authoring agent that can
// edit the packet could then license its own work.
type QAPolicy struct {
	// RequireGates names catalog gates this packet requires regardless of
	// risk. Every entry must be a known gate; an unknown one is a malformed
	// packet rather than a silently ignored line.
	RequireGates []string `toml:"requireGates,omitempty" json:"requireGates,omitempty"`
}

// RequiredGates resolves risk plus policy into the explicit mandatory set.
//
// The result is sorted and deduplicated so that two packets with the same risk
// and policy produce byte-identical decisions, which is what makes a blocked
// progression reproducible rather than a matter of map iteration order.
func RequiredGates(policy QAPolicy, risk RiskClass) []string {
	seen := map[string]bool{}
	for _, d := range gateCatalogue {
		if risk.Rank() >= d.MinRisk.Rank() {
			seen[d.ID] = true
		}
	}
	for _, id := range policy.RequireGates {
		seen[id] = true
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// GateResult is what a gate's mechanical execution observed.
type GateResult string

// The gate results.
const (
	// GatePass — the gate ran to completion and the condition holds. It is the
	// only result that can certify anything.
	GatePass GateResult = "pass"
	// GateFail — the gate ran and the condition does not hold.
	GateFail GateResult = "fail"
	// GateError — the gate could not be run to a verdict. It is not a pass and
	// it is not a failure of the code; it is an absence of knowledge, and it
	// blocks for the same reason missing evidence does.
	GateError GateResult = "error"
)

// Passed reports whether the result certifies.
//
// It is an equality against GatePass rather than an inequality against the
// failures, so a result value this package does not recognize is not a pass. A
// vocabulary extended later without touching this function therefore blocks
// rather than licenses.
func (r GateResult) Passed() bool { return r == GatePass }

// severity orders results so that folding two verdicts for the same revision is
// a maximum. A pass is the weakest claim; anything else outranks it.
func (r GateResult) severity() int {
	switch r {
	case GatePass:
		return 0
	case GateFail:
		return 2
	case GateError:
		return 1
	default:
		return 3
	}
}

// GateEvidence is one gate's structured, durable record of what was examined,
// by what, against which code, and what came back.
//
// Every field exists to answer a question that a bare pass/fail cannot: what
// tool said so, at what version, over which revision, and how a reader
// reproduces it. Evidence that cannot be reproduced is testimony.
type GateEvidence struct {
	// GateID names the catalog gate this evidence is for.
	GateID string `json:"gateId"`
	// TaskID names the packet task whose execution produced it.
	TaskID string `json:"taskId,omitempty"`
	// Tool is the executable that produced the verdict.
	Tool string `json:"tool,omitempty"`
	// ToolVersion is what that executable reported about itself, when known.
	ToolVersion string `json:"toolVersion,omitempty"`
	// Result is the mechanical verdict.
	Result GateResult `json:"result"`
	// ObservedAt is when the gate ran.
	ObservedAt time.Time `json:"observedAt"`
	// TargetSHA is the exact revision the gate examined. Evidence with no
	// target certifies nothing: see Certifies.
	TargetSHA string `json:"targetSha,omitempty"`
	// Reproduce is the argv a reader runs to get this verdict again.
	Reproduce []string `json:"reproduce,omitempty"`
	// Detail is the failure evidence — the reason, redacted.
	Detail string `json:"detail,omitempty"`
	// EvidencePath points at the captured output of the run, when one was kept.
	EvidencePath string `json:"evidencePath,omitempty"`
}

// Certifies reports whether this evidence licenses progression for a revision.
//
// Three conditions, all mechanical: the result is a pass, the evidence names
// the revision it examined, and that revision is the one in hand. The middle
// condition is not redundant — evidence with no target would otherwise certify
// every revision, which is precisely the failure this contract exists to
// prevent.
func (e GateEvidence) Certifies(targetSHA string) bool {
	if !e.Result.Passed() {
		return false
	}
	if strings.TrimSpace(e.TargetSHA) == "" || strings.TrimSpace(targetSHA) == "" {
		return false
	}
	return e.TargetSHA == targetSHA
}

// Stale reports whether this evidence examined code other than the revision in
// hand.
func (e GateEvidence) Stale(targetSHA string) bool {
	return strings.TrimSpace(targetSHA) != "" &&
		strings.TrimSpace(e.TargetSHA) != "" &&
		e.TargetSHA != targetSHA
}

// MergeEvidence folds a newly observed verdict into the one already held for a
// gate, and returns what the ledger should keep.
//
// The rule has two halves and neither is negotiable:
//
// A verdict for a *different* revision replaces what came before, because the
// ground moved and the old verdict is about code that no longer exists.
//
// A verdict for the *same* revision keeps the worse of the two. This is what
// makes a mechanical failure impossible to talk out of. Without it, a gate that
// genuinely failed could be followed by a second record claiming a pass for the
// identical code — from a re-run, from an agent asserting it, from anything —
// and the ledger would forget the failure. Same code, same answer: a failure
// stands until the code changes.
func MergeEvidence(existing, observed GateEvidence) GateEvidence {
	if existing.GateID == "" {
		return observed
	}
	if existing.TargetSHA != observed.TargetSHA {
		return observed
	}
	if existing.Result.severity() >= observed.Result.severity() {
		return existing
	}
	return observed
}

// BlockReason is why one required gate does not permit progression.
type BlockReason string

// The reasons a gate blocks. Each is distinct because the action a reader takes
// differs: missing evidence means run the gate, stale means run it again here,
// failed means fix the code.
const (
	// BlockMissing — the gate is mandatory and nothing has been recorded for it.
	BlockMissing BlockReason = "missing"
	// BlockFailed — the gate ran against this revision and did not pass.
	BlockFailed BlockReason = "failed"
	// BlockStale — the gate passed, but against a different revision, so it
	// certifies code other than the code in hand.
	BlockStale BlockReason = "stale"
	// BlockUnknownTarget — there is no revision to certify against, so no
	// evidence can be bound to anything.
	BlockUnknownTarget BlockReason = "unknown-target"
)

// GateBlock is one blocking gate, in enough detail to act on without reading
// the ledger.
type GateBlock struct {
	GateID string      `json:"gateId"`
	Reason BlockReason `json:"reason"`
	// Result is the recorded verdict, when there is one.
	Result GateResult `json:"result,omitempty"`
	// EvidenceSHA is the revision the recorded evidence examined, which for a
	// stale block is the whole story.
	EvidenceSHA string `json:"evidenceSha,omitempty"`
	// Detail is the failure evidence carried forward from the gate.
	Detail string `json:"detail,omitempty"`
}

// String renders one block the way a report reads it.
func (b GateBlock) String() string {
	switch b.Reason {
	case BlockMissing:
		return fmt.Sprintf("%s: required, and no evidence has been recorded", b.GateID)
	case BlockStale:
		return fmt.Sprintf("%s: passed against %s, which is not the code in hand", b.GateID, shortSHA(b.EvidenceSHA))
	case BlockUnknownTarget:
		return fmt.Sprintf("%s: there is no target revision to certify against", b.GateID)
	default:
		detail := b.Detail
		if detail != "" {
			detail = " — " + firstLine(detail)
		}
		return fmt.Sprintf("%s: recorded %s against %s%s", b.GateID, b.Result, shortSHA(b.EvidenceSHA), detail)
	}
}

// ProgressionDecision is the whole answer to "may this packet progress".
type ProgressionDecision struct {
	// Allowed is the decision. It is true only when every required gate has
	// PASS evidence bound to TargetSHA.
	Allowed bool `json:"allowed"`
	// Risk is the class the decision was taken under.
	Risk RiskClass `json:"risk"`
	// TargetSHA is the revision the decision certifies.
	TargetSHA string `json:"targetSha,omitempty"`
	// Required is the mandatory gate set, sorted.
	Required []string `json:"required,omitempty"`
	// Satisfied names the required gates with valid PASS evidence, sorted.
	Satisfied []string `json:"satisfied,omitempty"`
	// Blocking names every required gate that does not permit progression, in
	// required-set order.
	Blocking []GateBlock `json:"blocking,omitempty"`
}

// Reason renders why progression was refused, or why it was permitted.
func (d ProgressionDecision) Reason() string {
	if d.Allowed {
		if len(d.Required) == 0 {
			return fmt.Sprintf("risk %s requires no mechanical gate", d.Risk)
		}
		return fmt.Sprintf("every gate required at risk %s has passing evidence for %s: %s",
			d.Risk, shortSHA(d.TargetSHA), strings.Join(d.Required, ", "))
	}
	parts := make([]string, 0, len(d.Blocking))
	for _, b := range d.Blocking {
		parts = append(parts, b.String())
	}
	return fmt.Sprintf("%d of %d gate(s) required at risk %s do not permit progression: %s",
		len(d.Blocking), len(d.Required), d.Risk, strings.Join(parts, "; "))
}

// EvaluateProgression decides whether a packet may progress.
//
// It is a pure function of the required-gate set and the recorded evidence, and
// it takes no other input. There is no reviewer argument, no override, no
// confidence — nothing an authoring agent could set to make a failing gate
// license its work. A packet progresses because the gates passed against the
// code in hand, or it does not progress.
func EvaluateProgression(policy QAPolicy, risk RiskClass, targetSHA string, evidence map[string]GateEvidence) ProgressionDecision {
	d := ProgressionDecision{
		Risk:      risk,
		TargetSHA: targetSHA,
		Required:  RequiredGates(policy, risk),
	}

	// A packet whose required set is empty progresses on the strength of its
	// declared risk alone. That is not a hole: Q0 is a claim the packet makes
	// about itself in a file under review, and the gates it skips are the ones
	// that would examine behavior it asserts it does not change.
	if len(d.Required) == 0 {
		d.Allowed = true
		return d
	}

	// Without a revision, nothing can be bound to anything, and every piece of
	// evidence in the ledger is evidence about unknown code.
	if strings.TrimSpace(targetSHA) == "" {
		for _, id := range d.Required {
			d.Blocking = append(d.Blocking, GateBlock{GateID: id, Reason: BlockUnknownTarget})
		}
		return d
	}

	for _, id := range d.Required {
		ev, recorded := evidence[id]
		switch {
		case !recorded:
			d.Blocking = append(d.Blocking, GateBlock{GateID: id, Reason: BlockMissing})
		case ev.Certifies(targetSHA):
			d.Satisfied = append(d.Satisfied, id)
		case !ev.Result.Passed():
			d.Blocking = append(d.Blocking, GateBlock{
				GateID: id, Reason: BlockFailed, Result: ev.Result,
				EvidenceSHA: ev.TargetSHA, Detail: ev.Detail,
			})
		case ev.Stale(targetSHA):
			d.Blocking = append(d.Blocking, GateBlock{
				GateID: id, Reason: BlockStale, Result: ev.Result, EvidenceSHA: ev.TargetSHA,
			})
		default:
			// A pass that neither certifies nor is stale is a pass with no
			// target of its own. It proves nothing about this revision.
			d.Blocking = append(d.Blocking, GateBlock{
				GateID: id, Reason: BlockUnknownTarget, Result: ev.Result,
			})
		}
	}
	d.Allowed = len(d.Blocking) == 0
	return d
}

// ErrQAPacketInvalid is returned when a packet's QA declarations cannot govern
// a run.
var ErrQAPacketInvalid = fmt.Errorf("unattended: the packet's QA declarations are invalid")

// ValidateQAPacket refuses a packet whose QA information is malformed or
// materially incomplete.
//
// It runs before the worktree is claimed and before any task executes, because
// every problem it catches is one whose symptom at run time is a run that does
// work and then cannot certify it — the most expensive moment to discover that
// the packet never declared how it would be examined.
func ValidateQAPacket(spec Spec, plan Plan) error {
	var problems []string

	if !plan.Risk.Valid() {
		declared := string(plan.Risk)
		if strings.TrimSpace(declared) == "" {
			declared = "unset"
		}
		problems = append(problems, fmt.Sprintf(
			"risk %s is not a declared class (%s) — a packet that does not classify its own risk cannot resolve a gate set",
			quoteOrBare(declared), joinRiskClasses()))
	}

	for _, id := range spec.QA.RequireGates {
		if _, ok := LookupGate(id); !ok {
			problems = append(problems, fmt.Sprintf("qa.requireGates names %q, which is not a catalog gate (%s)",
				id, joinGateIDs()))
		}
	}

	producers := map[string][]string{}
	for _, t := range plan.Tasks {
		if t.QAGate == "" {
			continue
		}
		if _, ok := LookupGate(t.QAGate); !ok {
			problems = append(problems, fmt.Sprintf("task %q declares qaGate %q, which is not a catalog gate (%s)",
				t.ID, t.QAGate, joinGateIDs()))
			continue
		}
		producers[t.QAGate] = append(producers[t.QAGate], t.ID)
	}
	for _, id := range sortedKeys(producers) {
		if len(producers[id]) > 1 {
			problems = append(problems, fmt.Sprintf(
				"gate %q is claimed by %d tasks (%s) — two verdicts for one gate is an ambiguous ledger",
				id, len(producers[id]), strings.Join(producers[id], ", ")))
		}
	}

	// Only a packet whose risk is known can be asked to cover its gates. When
	// the class itself is malformed the required set is meaningless, and
	// reporting a list of uncovered gates on top of it would bury the one
	// problem that has to be fixed first.
	if plan.Risk.Valid() {
		for _, id := range RequiredGates(spec.QA, plan.Risk) {
			if len(producers[id]) == 0 {
				def, _ := LookupGate(id)
				problems = append(problems, fmt.Sprintf(
					"risk %s requires gate %q (%s) and no task declares it — the packet cannot produce the evidence it needs to progress",
					plan.Risk, id, def.Title))
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrQAPacketInvalid, strings.Join(problems, "; "))
	}
	return nil
}

func quoteOrBare(s string) string {
	if s == "unset" {
		return s
	}
	return fmt.Sprintf("%q", s)
}

func joinRiskClasses() string {
	out := make([]string, 0, len(riskRank))
	for _, r := range RiskClasses() {
		out = append(out, string(r))
	}
	return strings.Join(out, ", ")
}

func joinGateIDs() string {
	out := make([]string, 0, len(gateCatalogue))
	for _, d := range gateCatalogue {
		out = append(out, d.ID)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
