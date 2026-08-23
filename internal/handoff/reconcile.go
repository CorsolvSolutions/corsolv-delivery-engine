package handoff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// THE DEFECT THIS FILE EXISTS FOR.
//
// A delivery-owned criterion is scored met from evidence: every work package
// that claimed it merged with its completion gate met. That is a sound rule and
// it is still the rule. What it cannot do is survive being wrong.
//
// Evidence proves what it proves at the moment it is read. A gate that passed
// proves the plan was carried out; it does not prove the plan was right. So a
// criterion can be met, the project can reach Complete on the strength of it,
// and later evidence — a person opening the product, a bug report, a second
// look at the brief — can establish that the criterion was never satisfied at
// all.
//
// Before this file the engine had no way to say that. `delivery accept` refused
// (correctly: a delivery-owned criterion signed off by hand is forged
// evidence). Replacing the plan refused (correctly: merged work must not have
// its history rewritten). What was left was hand-editing durable state,
// rewriting history, or leaving a false completion standing — and a system
// whose only honest move is outside itself has a hole in it.
//
// So this adds one operation and one additive planning route, and both are
// append-only. Nothing here deletes an acceptance, a package record, a run, a
// projection or a plan. What it adds is a later fact that outranks an earlier
// one, and the history keeps both:
//
//	at time A, the engine believed ac-3 was met on evidence X
//	at time B, evidence Y proved that conclusion false
//	at time C, remedial work authorized for ac-3 merged, and ac-3 is met again
//
// A reader who cannot see all three has been told a story instead of a record.

// Invalidation is one recorded finding that a criterion reported met was not
// actually satisfied.
//
// Every field is required and none is derived. This is the one operation in the
// engine that makes a finished project unfinished, so it must never be possible
// to find one in the record and not know who raised it, why, or against what.
// An invalidation with no actor is indistinguishable from the machine deciding
// its own work was wrong — which is precisely the authority this must not have.
type Invalidation struct {
	// Seq is this invalidation's position in the record, from 1. It is what a
	// remediation names when it says which finding it answers, and it exists
	// because a criterion can be disproved more than once: "the invalidation
	// of ac-3" is ambiguous by the second time, and a remediation that answered
	// an ambiguous thing would clear a finding it never addressed.
	Seq int `json:"seq"`

	// CriterionID is the acceptance criterion whose earlier result is withdrawn.
	CriterionID string `json:"criterionId"`

	// By names the actor. Required — see the type comment.
	By string `json:"by"`
	// Reason is why the earlier result is wrong, in the actor's words.
	Reason string `json:"reason"`
	// Evidence is what proves it: an issue, a commit, a report, a file. This
	// engine does not read it and does not judge it. It is recorded because an
	// assertion with nothing behind it is an opinion, and an opinion may not
	// un-complete a project.
	Evidence string `json:"evidence"`

	// PreviousState is what the assessment said about this criterion at the
	// moment it was withdrawn. It is always "met" today, because withdrawing
	// anything else is refused, and it is stored rather than assumed so a later
	// reader is not left inferring what the engine believed.
	PreviousState string `json:"previousState"`

	At time.Time `json:"at"`
}

// The criterion states an invalidation can withdraw.
const (
	// CriterionMet is the assessment an invalidation withdraws. It is the only
	// one that can be withdrawn: there is no earlier conclusion to correct
	// about a criterion that was never scored met.
	CriterionMet = "met"
)

// Repair names one invalidated criterion a remediation is authorized to repair.
//
// Both halves are carried and neither is derivable from the other here. The
// criterion id is what a remedial work package claims, and the plan validator
// checks scope against it without ever seeing the record. The sequence is WHICH
// invalidation of that criterion this answers, and the assessment needs it
// because a criterion disproved a second time must not be cleared by the
// remediation that answered the first.
type Repair struct {
	CriterionID string `json:"criterionId"`
	// Invalidation is the Seq of the invalidation this repairs.
	Invalidation int `json:"invalidation"`
}

// RemediationSchemaVersion is the remediation version this engine accepts.
const RemediationSchemaVersion = 1

// Remediation is an additive plan revision: the work authorized to repair one
// or more invalidated criteria.
//
// IT IS NOT A NEW PLAN, AND THAT IS THE WHOLE POINT. The original plan stays
// exactly as it was written, on disk, unedited — it is the historical evidence
// of what this delivery set out to do and what its merged work was measured
// against. A remediation is a separate document that adds packages to it. The
// two are read together and neither is rewritten, so "what was planned" and
// "what was planned afterwards, and why" stay separately answerable forever.
//
// Everything a normal work package must satisfy, a remedial one must satisfy
// too: validated ids, declared phases, allowlisted gates, a required artifact,
// containment away from the paths that judge the run, and writer isolation from
// anything it could run beside. None of that is relaxed. What is ADDED is two
// restrictions that exist only here:
//
//   - SCOPE. A remedial package may claim only the criteria this remediation
//     repairs. Corrective work is authorized against a specific finding, and a
//     package that also claimed an untouched criterion would let a repair
//     quietly widen into delivery nobody asked for.
//
//   - FIDELITY, AGAIN AND ALONE. Where the invalidated criterion declares the
//     behaviors it requires, THIS remediation's own packages must carry all of
//     them. The original plan carried them too, and that is exactly why the
//     rule cannot lean on it: the original work was disproved, so its claim to
//     have covered the criterion is the thing under dispute. A repair that
//     inherited credit from the work it is repairing would be no repair at all.
type Remediation struct {
	SchemaVersion int    `json:"schemaVersion"`
	ProjectID     string `json:"projectId"`

	// Seq is this remediation's position among the project's remediations, from
	// 1. It orders them and names its file.
	Seq int `json:"seq"`

	// Repairs names the invalidated criteria this remediation answers.
	Repairs []Repair `json:"repairs"`

	// AuthorizedBy names who authorized this corrective work. Required, for the
	// same reason an invalidation's actor is: additive work against a finished
	// project is a decision, and a decision with no author is a mutation.
	AuthorizedBy string    `json:"authorizedBy"`
	AuthorizedAt time.Time `json:"authorizedAt"`

	Packages []WorkPackage `json:"packages"`

	// Supersedes names corrective work an EARLIER remediation authorized that
	// this one replaces.
	//
	// WHY CORRECTIVE WORK NEEDS THIS AND THE ORIGINAL PLAN DOES NOT. A plan is
	// written before anything is known to be wrong. A remediation is written
	// about something that IS wrong, from a diagnosis that can itself turn out to
	// be mistaken — and scorm-course-studio's was. Two remedial packages were
	// authorized to produce evidence that, it emerged, had already been merged,
	// so no worker could ever produce a diff and publication refused for the
	// right reason every time. A criterion is met only when EVERY package
	// claiming it completes, so one mis-shaped authorization held five criteria
	// hostage with no route back.
	//
	// Superseding is how a delivery changes its mind without pretending it never
	// held the earlier one. The superseded package stays in its own remediation
	// document exactly as authorized — nothing is rewritten and the sequence
	// stays readable — but it is no longer work this delivery waits for: not
	// compiled into the run, not required for completion, and not counted among
	// the packages repairing its criterion.
	//
	// It reaches BACKWARDS only, and only into corrective work. A remediation may
	// not supersede the original plan, whose work merged and is the history
	// everything since was measured against; nor its own packages, which would be
	// an authorization arguing with itself.
	Supersedes []string `json:"supersedes,omitempty"`
}

// remediationFilePattern matches a remediation document in a project's delivery
// directory, and captures its sequence.
var remediationFilePattern = regexp.MustCompile(`^remediation-(\d{3,})\.json$`)

// RemediationPath is where one remediation lives.
func RemediationPath(deliveryRoot, projectID string, seq int) string {
	return filepath.Join(deliveryRoot, projectID, fmt.Sprintf("remediation-%03d.json", seq))
}

// Criteria are the criterion ids this remediation repairs, in the order given.
func (rm Remediation) Criteria() []string {
	out := make([]string, 0, len(rm.Repairs))
	for _, r := range rm.Repairs {
		out = append(out, r.CriterionID)
	}
	return out
}

// RepairsInvalidation reports whether this remediation answers the given finding.
func (rm Remediation) RepairsInvalidation(criterionID string, seq int) bool {
	for _, r := range rm.Repairs {
		if r.CriterionID == criterionID && r.Invalidation == seq {
			return true
		}
	}
	return false
}

// PackagesFor are this remediation's packages that claim a criterion.
func (rm Remediation) PackagesFor(criterionID string) []string {
	var out []string
	for _, wp := range rm.Packages {
		if containsPath(wp.Satisfies, criterionID) {
			out = append(out, wp.ID)
		}
	}
	return out
}

// LatestInvalidation returns the most recent invalidation raised against a
// criterion.
//
// Most recent rather than only: a criterion can be disproved, repaired, and
// disproved again, and the earlier findings stay in the record because they
// happened. Only the last one can still be standing.
func (r Record) LatestInvalidation(criterionID string) (Invalidation, bool) {
	var found Invalidation
	var ok bool
	for _, inv := range r.Invalidations {
		if inv.CriterionID == criterionID {
			found, ok = inv, true
		}
	}
	return found, ok
}

// Invalidate records that a criterion reported met was not actually satisfied.
//
// `prior` is the assessment being withdrawn, and it is a parameter rather than
// something this method computes because computing it needs the published
// projection — a file read, which a method on a value type must not do. It also
// makes the precondition explicit at every call site: an invalidation withdraws
// a conclusion, so the conclusion has to be in hand.
//
// The refusals, in the order they are checked and for the reasons they exist:
//
//	unknown criterion    — nothing to withdraw, and a typo that silently created
//	                       a finding against a criterion the project does not
//	                       have would be a finding nothing could ever answer.
//	a person's criterion — unchanged by this packet. A human acceptance is the
//	                       person's own answer; revising it is theirs, not this
//	                       operation's, and letting delivery withdraw one would
//	                       hand the machine a veto over a human boundary.
//	no actor / reason / evidence
//	                     — the three things that make this a governed act rather
//	                       than an invisible mutation.
//	already standing     — deterministic and inert. A second finding while the
//	                       first is open would need a second remediation to
//	                       clear, so a criterion could be repaired and still be
//	                       unmet with nothing saying why.
//	not currently met    — there is no earlier conclusion to correct.
func (r Record) Invalidate(criterionID string, prior Evidence, by, reason, evidence string, now time.Time) (Record, error) {
	var found *Criterion
	for i := range r.Intent.Acceptance {
		if r.Intent.Acceptance[i].ID == criterionID {
			found = &r.Intent.Acceptance[i]
			break
		}
	}
	switch {
	case found == nil:
		return r, fmt.Errorf("%w: %s declares no acceptance criterion %q",
			ErrRecordConflict, r.ProjectID, criterionID)
	case found.IsHuman():
		return r, fmt.Errorf("%w: %s is a person's to accept — this operation withdraws a result delivery "+
			"proved, and a person's answer is theirs to give and theirs to revise",
			ErrRecordConflict, criterionID)
	case strings.TrimSpace(by) == "":
		return r, fmt.Errorf("%w: invalidating %s requires the name of the actor invalidating it — "+
			"an unattributed invalidation is an invisible mutation of a finished project",
			ErrRecordConflict, criterionID)
	case strings.TrimSpace(reason) == "":
		return r, fmt.Errorf("%w: invalidating %s requires a reason — what a later reader needs is why the "+
			"earlier conclusion was wrong, not that somebody decided it was",
			ErrRecordConflict, criterionID)
	case strings.TrimSpace(evidence) == "":
		return r, fmt.Errorf("%w: invalidating %s requires evidence — an assertion with nothing behind it is "+
			"an opinion, and an opinion may not un-complete a project",
			ErrRecordConflict, criterionID)
	}

	met := containsPath(prior.AcceptanceMet, criterionID)
	if inv, standing := r.LatestInvalidation(criterionID); standing && !met {
		return r, fmt.Errorf("%w: %s is already invalidated — invalidation %d, raised by %s on %s: %s (evidence: %s). "+
			"Authorize remediation for it rather than raising a second finding",
			ErrRecordConflict, criterionID, inv.Seq, inv.By,
			inv.At.UTC().Format(time.RFC3339), inv.Reason, inv.Evidence)
	}
	if !met {
		return r, fmt.Errorf("%w: %s is not currently met, so there is no result to withdraw: %s",
			ErrRecordConflict, criterionID, joinOr(prior.Reasons, "the assessment reports it outstanding"))
	}

	out := r
	out.Invalidations = append(append([]Invalidation(nil), r.Invalidations...), Invalidation{
		Seq:           len(r.Invalidations) + 1,
		CriterionID:   criterionID,
		By:            strings.TrimSpace(by),
		Reason:        strings.TrimSpace(reason),
		Evidence:      strings.TrimSpace(evidence),
		PreviousState: CriterionMet,
		At:            now.UTC(),
	})
	out.UpdatedAt = now.UTC()
	return out, nil
}

// StandingInvalidations are the findings this record has raised that the given
// assessment does not show answered, keyed by criterion.
//
// It is derived from the assessment rather than stored, for the reason every
// other verdict in this engine is: a stored "open" flag is a second answer to a
// question the evidence already answers, and the first time the two disagreed
// nobody would know which to believe.
func (r Record) StandingInvalidations(prior Evidence) map[string]Invalidation {
	out := map[string]Invalidation{}
	for _, c := range r.Intent.Acceptance {
		inv, has := r.LatestInvalidation(c.ID)
		if !has || containsPath(prior.AcceptanceMet, c.ID) {
			continue
		}
		out[c.ID] = inv
	}
	return out
}

// DecodeRemediation parses a remediation and refuses anything it cannot fully
// account for, for the same reason DecodeIntent does.
func DecodeRemediation(data []byte) (Remediation, error) {
	var rm Remediation
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rm); err != nil {
		return rm, fmt.Errorf("%w: %w", ErrPlanInvalid, err)
	}
	return rm, nil
}

// Validate refuses a remediation this engine will not add to a plan.
//
// It checks what a remediation can be judged on ALONE: its own shape, its own
// scope, and its own fidelity to the criteria it repairs. Everything that is a
// property of the whole plan — unique ids, writer isolation, dependency cycles,
// containment — is checked by DeliveryPlan.Validate over every generation at
// once, because those questions have no answer for one document in isolation.
func (rm Remediation) Validate(in Intent) error {
	if rm.SchemaVersion != RemediationSchemaVersion {
		return fmt.Errorf("%w: remediation schemaVersion %d, this engine speaks %d",
			ErrPlanInvalid, rm.SchemaVersion, RemediationSchemaVersion)
	}

	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if rm.ProjectID != in.ProjectID {
		add("remediation projectId %q does not match the intent's %q", rm.ProjectID, in.ProjectID)
	}
	if rm.Seq < 1 {
		add("remediation seq %d is not a position in the project's remediations", rm.Seq)
	}
	if strings.TrimSpace(rm.AuthorizedBy) == "" {
		add("remediation authorizedBy is required — additive work against a finished project is a decision, " +
			"and a decision with no author is a mutation")
	}
	if len(rm.Repairs) == 0 {
		add("a remediation repairs nothing — corrective work is authorized against a finding, never in general")
	}
	if len(rm.Packages) == 0 {
		add("a remediation with no work packages repairs nothing")
	}

	declared := map[string]Criterion{}
	for _, c := range in.Acceptance {
		declared[c.ID] = c
	}
	repaired := map[string]bool{}
	for i, rep := range rm.Repairs {
		c, known := declared[rep.CriterionID]
		switch {
		case !known:
			add("repairs[%d] names %q, which the intent does not declare as an acceptance criterion", i, rep.CriterionID)
			continue
		case c.IsHuman():
			add("repairs[%d] names %q, which only a person may accept — delivery does not remediate a person's answer", i, rep.CriterionID)
			continue
		case rep.Invalidation < 1:
			add("repairs[%d] (%s) names invalidation %d, which is not a recorded finding", i, rep.CriterionID, rep.Invalidation)
			continue
		case repaired[rep.CriterionID]:
			add("repairs[%d] names %q twice", i, rep.CriterionID)
			continue
		}
		repaired[rep.CriterionID] = true
	}

	// SCOPE. Corrective work is authorized against a specific finding.
	for _, wp := range rm.Packages {
		for _, s := range wp.Satisfies {
			if !repaired[s] {
				add("remedial package %s claims %q, which this remediation does not repair — "+
					"a repair may not widen into delivery nobody authorized", wp.ID, s)
			}
		}
	}

	// FIDELITY, over this remediation's own packages and no others. See the
	// type comment for why the original plan's coverage cannot be inherited.
	for _, rep := range rm.Repairs {
		c, known := declared[rep.CriterionID]
		if !known || c.IsHuman() || len(c.MustCover) == 0 {
			continue
		}
		covering := coveringPackages(rm.Packages, c.ID)
		if len(covering) == 0 {
			add("%s is repaired by no work package in this remediation", c.ID)
			continue
		}
		var gated bool
		for _, wp := range covering {
			if len(wp.Gates) > 0 {
				gated = true
				break
			}
		}
		if !gated {
			add("%s requires behavior the remediation must prove, and no remedial package claiming it declares "+
				"a gate — the work being repaired passed every gate it declared, so a repair that declares none "+
				"cannot be evidence of anything", c.ID)
		}
		if missing := missingBehaviours(coveringText(covering), c.MustCover); len(missing) > 0 {
			add("%s requires %s, which no remedial package claiming it plans to deliver — the original work "+
				"claimed those behaviors and was disproved, so a repair has to carry them itself",
				c.ID, strings.Join(quoteAll(missing), ", "))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrPlanInvalid, strings.Join(problems, "; "))
	}
	return nil
}

// LoadRemediations reads a project's remediations in sequence order.
//
// A project with none — every project that existed before this packet — reads
// as an empty slice and no error. That is the normal state, not an absence to
// be reported.
func LoadRemediations(deliveryRoot, projectID string) ([]Remediation, error) {
	dir := filepath.Join(deliveryRoot, projectID)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading delivery directory %q: %w", dir, err)
	}

	var out []Remediation
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := remediationFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, rerr := os.ReadFile(path) //nolint:gosec // a path this process composed
		if rerr != nil {
			return nil, fmt.Errorf("reading remediation %q: %w", path, rerr)
		}
		rm, derr := DecodeRemediation(data)
		if derr != nil {
			return nil, fmt.Errorf("%s: %w", path, derr)
		}
		seq, cerr := strconv.Atoi(m[1])
		if cerr != nil || rm.Seq != seq {
			return nil, fmt.Errorf("%w: %s holds remediation seq %d — a remediation's file name is its sequence",
				ErrPlanInvalid, path, rm.Seq)
		}
		if rm.ProjectID != projectID {
			return nil, fmt.Errorf("%w: %s holds a remediation for project %q",
				ErrRecordConflict, path, rm.ProjectID)
		}
		out = append(out, rm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// SaveRemediation writes one remediation atomically.
//
// It REFUSES to overwrite. A remediation is a record of work authorized against
// a finding, and rewriting one would let the authorization for merged remedial
// work be edited after the fact — the same history rewrite this whole mechanism
// exists to avoid.
func SaveRemediation(deliveryRoot string, rm Remediation) error {
	if rm.ProjectID == "" {
		return fmt.Errorf("%w: refusing to write a remediation with no project id", ErrRecordConflict)
	}
	path := RemediationPath(deliveryRoot, rm.ProjectID, rm.Seq)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: remediation %d for %q already exists at %s — a remediation is the durable "+
			"authorization for work that may already have merged, and is never rewritten",
			ErrRecordConflict, rm.Seq, rm.ProjectID, path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking for an existing remediation %q: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating delivery directory: %w", err)
	}
	data, err := json.MarshalIndent(rm, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding remediation: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil { //nolint:gosec // read by the portal
		return fmt.Errorf("writing remediation: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("installing remediation %q: %w", path, err)
	}
	return nil
}

// AuthorizeRemediation validates corrective work against the record's standing
// findings and installs it.
//
// This is the only place plan-shaped input meets the RECORD, and the check it
// makes is the one the plan validator structurally cannot: that every finding
// this remediation claims to answer is a finding that is actually standing. A
// remediation authorized against a criterion nobody disproved is additive work
// with no authority behind it; one authorized against an already-repaired
// finding would clear nothing and leave the current finding open, with the
// packages to make it look otherwise.
//
// `prior` is the assessment the authorization is made against, for the same
// reason Invalidate takes one.
// The document supplies what a person decided: which findings this repairs, and
// the work that repairs them. The ENGINE supplies `seq`, `authorizedAt` and the
// actor — a durable authorization whose position, timestamp or author could be
// dictated by the document being authorized is one whose provenance means
// nothing. A document that declares them is refused rather than silently
// overwritten.
func AuthorizeRemediation(deliveryRoot string, record Record, plan DeliveryPlan, prior Evidence, rm Remediation, by string, now time.Time) (Remediation, error) {
	var problems []string
	if rm.SchemaVersion != RemediationSchemaVersion {
		return rm, fmt.Errorf("%w: remediation schemaVersion %d, this engine speaks %d",
			ErrPlanInvalid, rm.SchemaVersion, RemediationSchemaVersion)
	}
	if rm.ProjectID != record.ProjectID {
		problems = append(problems, fmt.Sprintf("remediation projectId %q does not match the delivery's %q",
			rm.ProjectID, record.ProjectID))
	}
	if rm.Seq != 0 {
		problems = append(problems, "a remediation does not declare its own seq — the engine assigns it from "+
			"how many findings this project has already had remediated")
	}
	if !rm.AuthorizedAt.IsZero() {
		problems = append(problems, "a remediation does not declare its own authorizedAt — a timestamp the "+
			"authorized document chose is not evidence of when it was authorized")
	}
	if strings.TrimSpace(rm.AuthorizedBy) != "" {
		problems = append(problems, "a remediation does not declare its own authorizedBy — who authorizes "+
			"corrective work is stated by the person doing it, not by the document asking for it")
	}
	if strings.TrimSpace(by) == "" {
		problems = append(problems, "authorizing remediation requires the name of the actor authorizing it")
	}
	if len(problems) > 0 {
		return rm, fmt.Errorf("%w: %s", ErrPlanInvalid, strings.Join(problems, "; "))
	}

	standing := record.StandingInvalidations(prior)

	existing, err := LoadRemediations(deliveryRoot, record.ProjectID)
	if err != nil {
		return rm, err
	}
	rm.Seq = len(existing) + 1
	rm.AuthorizedBy = strings.TrimSpace(by)
	rm.AuthorizedAt = now.UTC()

	for _, rep := range rm.Repairs {
		inv, open := standing[rep.CriterionID]
		switch {
		case !open:
			problems = append(problems, fmt.Sprintf(
				"%s has no standing invalidation — remediation repairs a recorded finding, and there is none to repair",
				rep.CriterionID))
		case inv.Seq != rep.Invalidation:
			problems = append(problems, fmt.Sprintf(
				"%s names invalidation %d, but the standing finding against it is invalidation %d",
				rep.CriterionID, rep.Invalidation, inv.Seq))
		}
	}
	if len(problems) > 0 {
		return rm, fmt.Errorf("%w: %s", ErrPlanInvalid, strings.Join(problems, "; "))
	}

	if err := rm.Validate(record.Intent); err != nil {
		return rm, err
	}

	// And the whole plan, every generation at once: unique ids, declared
	// phases, allowlisted gates, containment, writer isolation. Validating the
	// union rather than the addition is what stops a remediation being safe on
	// its own and impossible beside what it is added to.
	combined := plan
	combined.Remediations = append(append([]Remediation(nil), existing...), rm)
	if err := combined.Validate(record.Intent); err != nil {
		return rm, err
	}

	if err := SaveRemediation(deliveryRoot, rm); err != nil {
		return rm, err
	}
	return rm, nil
}
