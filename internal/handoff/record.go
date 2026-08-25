package handoff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RecordSchemaVersion is the delivery-record version this engine writes.
const RecordSchemaVersion = 1

// RecordName is the delivery record's filename inside a project's delivery
// directory.
const RecordName = "delivery.json"

// Record is the durable link between a portal project and the delivery run
// that owns it.
//
// It is deliberately NOT a progress store. Progress already has an authority —
// the run's heartbeat — and task outcome already has one — the projection the
// engine publishes into the project's repository. A record that also carried
// them would be a third answer to questions that already have two, and the
// first time they disagreed nobody would know which to believe.
//
// What this file holds is what nothing else does: which project this is, what
// exactly was asked for, where the run that answers it keeps its state, and
// which runs have been made. Everything else is a pointer, and State resolves
// those pointers rather than caching what they say.
type Record struct {
	SchemaVersion int    `json:"schemaVersion"`
	ProjectID     string `json:"projectId"`

	// Intent is exactly what was accepted, kept verbatim so a later run can be
	// checked against it rather than against a summary of it.
	Intent Intent `json:"intent"`
	// IntentDigest fingerprints that intent. A second Start carrying different
	// terms is a contradiction, and this is what detects it.
	IntentDigest string `json:"intentDigest"`

	// StateDir is the run's durable state directory — the heartbeat,
	// completion event and journal authority.
	StateDir string `json:"stateDir"`
	// Worktree is the host-resolved working copy the run owns.
	Worktree string `json:"worktree"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// Runs is append-only. A restart after an interruption adds an entry; it
	// never rewrites the one before it, because what the interrupted run did
	// is part of this project's history.
	Runs []RunRef `json:"runs"`

	// Acceptances are the answers people gave to the criteria reserved for
	// them. Append-only for the same reason Runs is: an acceptance is an event
	// that happened, and the record of who said yes and when is the only thing
	// standing between a human boundary and a machine that walked through it.
	Acceptances []Acceptance `json:"acceptances,omitempty"`

	// Invalidations are the findings that a criterion reported met was not
	// actually satisfied. Append-only, and for a stronger reason than the two
	// above: this is the list that makes a finished project unfinished, so an
	// entry that could be edited or dropped would let the un-completion be
	// quietly undone by whatever runs next.
	//
	// Absent on every project that has never had one, which is every project
	// that existed before this field did — a record written without it reads
	// back identically.
	Invalidations []Invalidation `json:"invalidations,omitempty"`

	// Remediations journals every remediation the ENGINE authorized: its
	// sequence, a digest of the document as installed, and who authorized it
	// when. Append-only, for the strongest reason yet: the remediation files
	// beside this record are what un-completion is repaired THROUGH, and a
	// directory listing is not an authorization. A remediation document with
	// no entry here was never authorized by the engine, whatever its file
	// name claims — it was planted, and reading it as corrective work would
	// let anything that can write a file authorize its own repairs with
	// whatever provenance it liked. LoadRemediations refuses exactly that.
	//
	// Absent on every project whose remediations all predate this field; those
	// documents are adopted into the journal by the next authorization, which
	// is the first moment an operator attests to the directory's contents.
	Remediations []RemediationRef `json:"remediations,omitempty"`
}

// RemediationRef is the record's own memory of one authorization event.
type RemediationRef struct {
	// Seq is the remediation's position, matching its file name.
	Seq int `json:"seq"`
	// Digest is the SHA-256 of the remediation document exactly as installed.
	// A document that no longer matches was edited after authorization.
	Digest string `json:"digest"`
	// AuthorizedBy and AuthorizedAt duplicate the document's provenance ON
	// PURPOSE: the copy in the record is the one the document's author cannot
	// reach, which is what makes disagreement between the two detectable.
	AuthorizedBy string    `json:"authorizedBy"`
	AuthorizedAt time.Time `json:"authorizedAt"`
}

// Acceptance is one person's answer to one criterion only they could give.
type Acceptance struct {
	// CriterionID is the acceptance criterion answered. It must be one the
	// intent declared with AcceptedByHuman.
	CriterionID string `json:"criterionId"`
	// By names the person. It is required: an unattributed acceptance is
	// indistinguishable from the machine accepting on its own behalf, which is
	// the exact thing this record exists to rule out.
	By string `json:"by"`
	// Note is what they said, if anything.
	Note string `json:"note,omitempty"`
	// At is when they said it.
	At time.Time `json:"at"`
}

// Accept records a person's acceptance of a criterion reserved for them.
//
// It is idempotent and keeps the FIRST answer. A second call is a person
// re-confirming, not a later authority overwriting the earlier one — and an
// acceptance that could be rewritten is one that could be rewritten by whatever
// runs next.
func (r Record) Accept(criterionID, by, note string, now time.Time) (Record, error) {
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
	case !found.IsHuman():
		return r, fmt.Errorf("%w: %s is delivery's to satisfy and prove — accepting it by hand would forge the evidence the completion gate reads",
			ErrRecordConflict, criterionID)
	case strings.TrimSpace(by) == "":
		return r, fmt.Errorf("%w: accepting %s requires the name of the person accepting it",
			ErrRecordConflict, criterionID)
	}

	for _, a := range r.Acceptances {
		if a.CriterionID == criterionID {
			return r, nil
		}
	}
	out := r
	out.Acceptances = append(append([]Acceptance(nil), r.Acceptances...), Acceptance{
		CriterionID: criterionID,
		By:          strings.TrimSpace(by),
		Note:        strings.TrimSpace(note),
		At:          now.UTC(),
	})
	out.UpdatedAt = now.UTC()
	return out, nil
}

// RunRef identifies one execution attempt.
type RunRef struct {
	RunID     string    `json:"runId"`
	StartedAt time.Time `json:"startedAt"`
	// Reason says why this run exists — the first start, or a reconciliation
	// after an interruption.
	Reason string `json:"reason"`
}

// The reasons a run is started.
const (
	// ReasonInitial is the first run for a delivery.
	ReasonInitial = "initial"
	// ReasonResumed is a run started to reconcile after an interruption.
	ReasonResumed = "resumed"
)

// ErrRecordConflict is returned when durable state contradicts the request.
var ErrRecordConflict = errors.New("handoff: delivery record conflicts with this request")

// ErrRecordCorrupt is returned when a record exists but cannot be trusted.
var ErrRecordCorrupt = errors.New("handoff: delivery record is unreadable")

// Digest fingerprints the delivery terms of an intent.
//
// It covers what the run must honor and nothing else. Who pressed the button
// and when are provenance, not terms: re-pressing Start tomorrow is the same
// request as pressing it today, and a digest that disagreed would turn every
// retry into a conflict.
func Digest(in Intent) string {
	terms := struct {
		SchemaVersion int         `json:"schemaVersion"`
		ProjectID     string      `json:"projectId"`
		Repository    Repository  `json:"repository"`
		Checkout      string      `json:"checkout"`
		Objective     string      `json:"objective"`
		Lifecycle     []string    `json:"lifecycle"`
		Acceptance    []Criterion `json:"acceptance"`
		Policy        Policy      `json:"policy"`
	}{
		SchemaVersion: in.SchemaVersion,
		ProjectID:     in.ProjectID,
		Repository:    in.Repository,
		Checkout:      in.Checkout,
		Objective:     in.Objective,
		Lifecycle:     append([]string(nil), in.Lifecycle...),
		Acceptance:    sortedCriteria(in.Acceptance),
		Policy:        in.Policy,
	}
	// json.Marshal is deterministic for these types: struct fields keep
	// declaration order and every slice is explicitly ordered above.
	data, err := json.Marshal(terms)
	if err != nil {
		// Unreachable for this closed type graph, and a digest that silently
		// became "" would make every intent look identical.
		panic(fmt.Sprintf("handoff: fingerprinting intent: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sortedCriteria(in []Criterion) []Criterion {
	out := append([]Criterion(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RecordPath is where a project's delivery record lives.
func RecordPath(deliveryRoot, projectID string) string {
	return filepath.Join(deliveryRoot, projectID, RecordName)
}

// LoadRecord reads a project's delivery record.
//
// A missing record is reported as absence, not as an error: no delivery has
// started yet is the normal state of most projects.
func LoadRecord(deliveryRoot, projectID string) (Record, bool, error) {
	path := RecordPath(deliveryRoot, projectID)
	data, err := os.ReadFile(path) //nolint:gosec // a path this process composed
	if os.IsNotExist(err) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("reading delivery record %q: %w", path, err)
	}

	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, false, fmt.Errorf("%w: %s: %w", ErrRecordCorrupt, path, err)
	}
	if r.SchemaVersion != RecordSchemaVersion {
		return Record{}, false, fmt.Errorf("%w: %s: schemaVersion %d, this engine writes %d",
			ErrRecordCorrupt, path, r.SchemaVersion, RecordSchemaVersion)
	}
	// A record found under one project's directory that names another is the
	// cross-project failure in its most dangerous form: acting on it would let
	// one project's Start drive another project's repository.
	if r.ProjectID != projectID {
		return Record{}, false, fmt.Errorf("%w: %s holds a record for project %q",
			ErrRecordConflict, path, r.ProjectID)
	}
	if r.IntentDigest != Digest(r.Intent) {
		return Record{}, false, fmt.Errorf("%w: %s: the recorded intent does not match its own digest",
			ErrRecordCorrupt, path)
	}
	return r, true, nil
}

// SaveRecord writes a record atomically.
func SaveRecord(deliveryRoot string, r Record) error {
	if r.ProjectID == "" {
		return fmt.Errorf("%w: refusing to write a record with no project id", ErrRecordConflict)
	}
	path := RecordPath(deliveryRoot, r.ProjectID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating delivery record directory: %w", err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding delivery record: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil { //nolint:gosec // read by the portal
		return fmt.Errorf("writing delivery record: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("installing delivery record %q: %w", path, err)
	}
	return nil
}

// Admission is what Admit decided about a Start request.
type Admission struct {
	// Record is the delivery record to act under, whether newly created or
	// already present.
	Record Record
	// Created is true when this call established the delivery.
	Created bool
	// AlreadyStarted is true when a delivery for this project already existed
	// and this request added nothing. The caller reports success and starts
	// nothing — that is what makes Start idempotent.
	AlreadyStarted bool
}

// Admit decides whether a Start request may proceed, and returns the record it
// proceeds under.
//
// Idempotency is the point. A portal button can be pressed twice, a browser can
// resubmit, and a user who sees no immediate change will click again. None of
// those may produce a second delivery, so an identical request against an
// existing record is a no-op that reports the existing state.
//
// A request whose terms differ is not a retry, and is refused. Silently
// adopting the new terms would leave a run executing a plan built from the old
// ones; silently keeping the old would tell the user their change took effect
// when it did not.
func Admit(deliveryRoot string, in Intent, now time.Time) (Admission, error) {
	if err := in.Validate(); err != nil {
		return Admission{}, err
	}

	existing, found, err := LoadRecord(deliveryRoot, in.ProjectID)
	if err != nil {
		return Admission{}, err
	}
	digest := Digest(in)

	if !found {
		r := Record{
			SchemaVersion: RecordSchemaVersion,
			ProjectID:     in.ProjectID,
			Intent:        in,
			IntentDigest:  digest,
			CreatedAt:     now.UTC(),
			UpdatedAt:     now.UTC(),
		}
		return Admission{Record: r, Created: true}, nil
	}

	if existing.IntentDigest != digest {
		return Admission{}, fmt.Errorf(
			"%w: delivery for %q already started under different terms (recorded digest %s, this request %s) — "+
				"stop the existing delivery and archive its record before starting different work",
			ErrRecordConflict, in.ProjectID, short(existing.IntentDigest), short(digest))
	}
	return Admission{Record: existing, AlreadyStarted: true}, nil
}

// AppendRun records a new execution attempt on the record.
func (r Record) AppendRun(runID, reason string, now time.Time) Record {
	out := r
	out.Runs = append(append([]RunRef(nil), r.Runs...), RunRef{
		RunID:     runID,
		StartedAt: now.UTC(),
		Reason:    reason,
	})
	out.UpdatedAt = now.UTC()
	return out
}

// LatestRun returns the most recent run reference.
func (r Record) LatestRun() (RunRef, bool) {
	if len(r.Runs) == 0 {
		return RunRef{}, false
	}
	return r.Runs[len(r.Runs)-1], true
}

func short(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

// PlanPath is where a delivery's validated plan is kept.
func PlanPath(deliveryRoot, projectID string) string {
	return filepath.Join(deliveryRoot, projectID, "plan.json")
}

// IntentPath is where a delivery's accepted intent is kept, standing alone.
//
// The record already holds the intent, and this is not a second copy for the
// Go layer's benefit — nothing here reads it. It exists because the driver that
// executes the run is a separate program in a separate language, and its whole
// contract is "the two validated documents": what to deliver, and the work
// packages. Handing it the intent inside a record it would have to know how to
// unwrap would make it a reader of this package's internal shape.
func IntentPath(deliveryRoot, projectID string) string {
	return filepath.Join(deliveryRoot, projectID, "intent.json")
}

// SaveIntent writes the accepted intent where the driver reads it.
//
// It is written from the RECORD's copy rather than from whatever the caller
// happens to hold, so the document the driver acts on is provably the one that
// was admitted — not a later request that was refused for differing from it.
func SaveIntent(deliveryRoot string, in Intent) error {
	if err := in.Validate(); err != nil {
		return err
	}
	path := IntentPath(deliveryRoot, in.ProjectID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating delivery directory: %w", err)
	}
	data, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding delivery intent: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil { //nolint:gosec // run evidence
		return fmt.Errorf("writing delivery intent: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("installing delivery intent %q: %w", path, err)
	}
	return nil
}

// SavePlan writes a validated delivery plan atomically.
//
// It refuses to change one that is already there. The plan is what this
// delivery's merged work was measured against, and the evidence that judged it
// is only meaningful beside the document that asked for it — so an edited plan
// would silently re-date every completion gate that has already passed.
// Corrective work is ADDED, through a remediation, and never written here;
// rewriting the same bytes is allowed because that is not a change.
func SavePlan(deliveryRoot string, p DeliveryPlan) error {
	path := PlanPath(deliveryRoot, p.ProjectID)
	if existing, err := os.ReadFile(path); err == nil { //nolint:gosec // a path this process composed
		fresh, merr := json.MarshalIndent(p, "", "  ")
		if merr != nil {
			return fmt.Errorf("encoding delivery plan: %w", merr)
		}
		if string(existing) != string(append(fresh, '\n')) {
			return fmt.Errorf("%w: %q already has a plan, and a plan is not rewritten — the merged work of "+
				"this delivery was measured against it. Authorize corrective work as a remediation instead",
				ErrRecordConflict, p.ProjectID)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking for an existing delivery plan %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating plan directory: %w", err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding delivery plan: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil { //nolint:gosec // read by the portal
		return fmt.Errorf("writing delivery plan: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("installing delivery plan %q: %w", path, err)
	}
	return nil
}

// LoadPlan reads a delivery's plan, joins any remediations to it, and
// re-validates the whole thing against the intent.
//
// Re-validating on read is not paranoia about the disk. The plan is executed by
// a later process than the one that wrote it, possibly after an interruption
// and a restart, and the intent it must agree with is read from a separate
// file. Checking the pair at the moment of use is the only place that
// disagreement can actually be caught.
//
// THE REMEDIATIONS ARE JOINED HERE, ONCE. Every caller that asks for "the plan"
// gets the original packages plus whatever corrective work has since been
// authorized — because that union is what the delivery is now being measured
// against, and a caller that had to remember to ask for the second half would
// eventually forget. The two halves stay separate in the returned value
// (`Packages` is still exactly what was planned first), so nothing has to trust
// a merge to tell them apart.
//
// A plan file that is absent is absence. Remediations without one would be
// corrective work against a plan that does not exist, which is why they are
// read only after the plan is found.
func LoadPlan(deliveryRoot string, in Intent) (DeliveryPlan, bool, error) {
	path := PlanPath(deliveryRoot, in.ProjectID)
	data, err := os.ReadFile(path) //nolint:gosec // a path this process composed
	if os.IsNotExist(err) {
		return DeliveryPlan{}, false, nil
	}
	if err != nil {
		return DeliveryPlan{}, false, fmt.Errorf("reading delivery plan %q: %w", path, err)
	}

	remediations, err := LoadRemediations(deliveryRoot, in.ProjectID)
	if err != nil {
		return DeliveryPlan{}, false, err
	}
	p, err := DecodePlanWithRemediations(data, in, remediations)
	if err != nil {
		return DeliveryPlan{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return p, true, nil
}

// SanitizeProjectID rejects a project id that could address anything but its
// own directory.
//
// Every path in this package is composed from a project id supplied over the
// wire, so this is the one place traversal has to be stopped.
func SanitizeProjectID(id string) error {
	if !IDPattern.MatchString(id) {
		return fmt.Errorf("%w: project id %q is not a valid identifier", ErrIntentInvalid, id)
	}
	if strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return fmt.Errorf("%w: project id %q addresses a path", ErrIntentInvalid, id)
	}
	return nil
}
