// Package handoff defines the bounded contract by which a management portal
// hands a project to the delivery engine, and compiles that intent into the
// run the engine already knows how to execute.
//
// The contract exists because of one asymmetry. A portal knows WHAT a project
// is for — its identity, its repository, the business objective, what would
// count as done. It does not know, and must not decide, HOW delivery happens:
// which city to build, which worktree a worker gets, when a branch is cut, what
// command proves a gate. Those are execution mechanics, and they belong to the
// engine that owns the machines they run on.
//
// So the wire between them carries intent and nothing else. There is no field
// on Intent that becomes a command line, and TestIntentCarriesNoExecutable
// enforces that by walking the type graph: the day someone adds a `script` or
// `argv` field to make one awkward project work, the build fails. That guard is
// the whole security model of this package. A portal that can post a command is
// a remote shell with a project id attached, and no amount of validation
// downstream recovers from having built one.
package handoff

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SchemaVersion is the only intent version this engine accepts.
//
// An unrecognized version is refused rather than coerced. A portal one release
// ahead of the engine is a real deployment state, and the failure mode of
// guessing at its meaning is a delivery run that does something subtly other
// than what was asked for — which is worse than not starting.
const SchemaVersion = 1

// IDPattern is the shape of every identifier crossing this contract.
//
// It is deliberately narrower than the portal's own project ids need to be.
// These strings reach branch names, directory names and bead titles, so the
// character set is restricted to what is safe in all three, on both platforms
// this fork runs on.
var IDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)

// Repository is the forge identity of the project being delivered.
type Repository struct {
	// Slug is `owner/name`.
	Slug string `json:"slug"`
	// Origin is the clone URL the registered checkout must actually have. It is
	// carried separately from Slug so the two can be cross-checked: a portal
	// that has drifted will usually disagree with itself before it disagrees
	// with the world, and catching that here costs nothing.
	Origin string `json:"origin"`
	// DefaultBranch is the branch delivery integrates into.
	DefaultBranch string `json:"defaultBranch"`
}

// Criterion is one acceptance statement, in the words of whoever wants the
// work. It is prose on purpose: turning it into a verdict is a judgement, and
// judgement does not belong in Go.
type Criterion struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`

	// AcceptedBy names who is entitled to say this criterion is met. Empty
	// means AcceptedByDelivery.
	//
	// WHY THE CONTRACT NEEDS THIS WORD. Without it every criterion is delivery's
	// to claim, the plan validator below demands that each one be claimed by a
	// work package, and a project whose last criterion is "a person accepts
	// this" has only one plan it can express: the one where an agent claims the
	// acceptance. It then merges, and every rule downstream reads that merge as
	// evidence — so the machine approves its own release, and the boundary the
	// project stated is the one thing the contract made unstateable.
	AcceptedBy string `json:"acceptedBy,omitempty"`

	// MustCover is the authoritative acceptance detail: the behaviors this
	// criterion requires, in the words of whoever wants the work, stated so a
	// machine can check a plan against them. Empty means the statement stands
	// alone, exactly as every criterion did before this existed.
	//
	// Each entry is one required behavior. Alternatives the author considers
	// the same thing are separated by `|` — "decimal|number" says those two
	// words mean one requirement here, because whether they do is the author's
	// call and nobody else's.
	//
	// THE DEFECT THIS EXISTS FOR. A pilot's brief required a per-column
	// inferred type drawn from text / integer / decimal / boolean / date /
	// mixed. What reached the engine was the sentence "the required overall and
	// per-column data-quality measures", and the planner wrote itself a
	// vocabulary without `mixed` in it. Every gate downstream was derived from
	// that plan and every one of them passed, because each was asking whether
	// the plan had been carried out — and it had. The project reported 8 of 8
	// over a product missing a required behavior.
	//
	// The coverage rule that should have caught it only asked whether SOME
	// package named this criterion's id. Naming a criterion is not delivering
	// it, and there was nothing here for a plan to be compared against.
	//
	// This does NOT make Go judge prose; the statement above stays prose for
	// exactly that reason. It lets the author say which words are load-bearing,
	// and `DeliveryPlan.Validate` then refuses a plan whose covering work does
	// not carry them. A planner may still split, reorder and reword the work —
	// it may not quietly drop a requirement.
	MustCover []string `json:"mustCover,omitempty"`
}

// Who may accept a criterion.
const (
	// AcceptedByDelivery is a criterion managed delivery is expected to satisfy
	// and prove. It is the default, and the empty string means it.
	AcceptedByDelivery = "delivery"
	// AcceptedByHuman is a criterion only a person may satisfy. Delivery may
	// prepare what that person reads and may never claim their answer.
	AcceptedByHuman = "human"
)

// IsHuman reports whether only a person may accept this criterion.
func (c Criterion) IsHuman() bool { return c.AcceptedBy == AcceptedByHuman }

// Policy is the delivery authority the portal grants this run.
//
// Every field is a permission, never an instruction. The engine may do less
// than the policy allows — a run that opens no PR because it produced no change
// is fine — but it may never do more.
type Policy struct {
	NeedPush   bool `json:"needPush"`
	NeedPR     bool `json:"needPr"`
	NeedChecks bool `json:"needChecks"`
	NeedMerge  bool `json:"needMerge"`

	// MergeHumanAction names what a person must do when merge authority is
	// withheld. Naming it converts an eventual hard stop into a boundary the
	// work queue can plan around.
	MergeHumanAction string `json:"mergeHumanAction,omitempty"`

	// MaxWorkers bounds concurrent agent sessions. Zero means the engine's
	// default.
	MaxWorkers int `json:"maxWorkers,omitempty"`
	// WorkDeadlineSeconds bounds how long the run waits for workers. Zero means
	// the engine's default.
	WorkDeadlineSeconds int `json:"workDeadlineSeconds,omitempty"`
}

// Intent is the whole of what a portal sends to start managed delivery.
type Intent struct {
	SchemaVersion int `json:"schemaVersion"`

	// ProjectID is the portal's identity for the project, and the key every
	// durable artifact this run writes is filed under.
	ProjectID  string     `json:"projectId"`
	Repository Repository `json:"repository"`

	// Checkout is the registered local working copy on the delivery host. The
	// portal supplies it because the portal is what registered it; the engine
	// proves it before touching it.
	Checkout string `json:"checkout"`

	// Objective is the business brief in prose — what the project is for.
	Objective string `json:"objective"`

	// Lifecycle is the project's phases, in order.
	Lifecycle []string `json:"lifecycle"`

	// Acceptance is what would make this delivery done.
	Acceptance []Criterion `json:"acceptance"`

	Policy Policy `json:"policy"`

	RequestedBy string    `json:"requestedBy"`
	RequestedAt time.Time `json:"requestedAt"`
}

// ErrIntentInvalid is returned for an intent this engine will not act on.
var ErrIntentInvalid = errors.New("handoff: delivery intent is invalid")

// ErrSchemaUnsupported is returned for an intent written against a different
// version of this contract.
var ErrSchemaUnsupported = errors.New("handoff: delivery intent schema is not supported")

// DecodeIntent parses an intent and refuses anything it cannot fully account
// for.
//
// Unknown fields are an error rather than an ignorable extra. A portal that
// sends a field this engine drops silently believes it asked for something it
// did not get, and "delivery ran without the constraint you set" is precisely
// the class of failure this contract exists to make impossible.
func DecodeIntent(data []byte) (Intent, error) {
	var in Intent
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("%w: %w", ErrIntentInvalid, err)
	}
	if err := in.Validate(); err != nil {
		return in, err
	}
	return in, nil
}

// Validate refuses an intent that cannot safely govern a delivery run.
//
// It reports every problem at once. A portal fixing one field at a time across
// five round trips is a portal whose user gives up and bootstraps by hand.
func (in Intent) Validate() error {
	if in.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: schemaVersion %d, this engine speaks %d",
			ErrSchemaUnsupported, in.SchemaVersion, SchemaVersion)
	}

	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if !IDPattern.MatchString(in.ProjectID) {
		add("projectId %q is not 2-64 characters of lowercase letters, digits and hyphens", in.ProjectID)
	}

	owner, name, slugOK := splitSlug(in.Repository.Slug)
	if !slugOK {
		add("repository.slug %q is not owner/name", in.Repository.Slug)
	}
	if strings.TrimSpace(in.Repository.DefaultBranch) == "" {
		add("repository.defaultBranch is required — delivery has nothing to integrate into")
	}
	switch {
	case strings.TrimSpace(in.Repository.Origin) == "":
		add("repository.origin is required — the checkout's remote cannot be proved against nothing")
	case slugOK:
		originSlug, err := slugFromOrigin(in.Repository.Origin)
		if err != nil {
			add("repository.origin %q is not a readable git remote URL: %v", in.Repository.Origin, err)
		} else if !strings.EqualFold(originSlug, owner+"/"+name) {
			// The portal contradicting itself. Refused here rather than
			// downstream, because by the time a clone has happened the wrong
			// repository is already on disk.
			add("repository.origin %q names %q but repository.slug says %q", in.Repository.Origin, originSlug, owner+"/"+name)
		}
	}

	if strings.TrimSpace(in.Checkout) == "" {
		add("checkout is required — managed delivery does not choose where a project lives")
	} else if !filepath.IsAbs(in.Checkout) && !isWindowsAbs(in.Checkout) {
		add("checkout %q is not an absolute path", in.Checkout)
	}

	if strings.TrimSpace(in.Objective) == "" {
		add("objective is required — a run with no stated purpose cannot be planned or judged")
	}

	if len(in.Lifecycle) == 0 {
		add("lifecycle is required — at least one phase")
	}
	for i, phase := range in.Lifecycle {
		if strings.TrimSpace(phase) == "" {
			add("lifecycle[%d] is empty", i)
		}
	}

	if len(in.Acceptance) == 0 {
		add("acceptance is required — delivery that cannot be judged done cannot complete")
	}
	seen := map[string]bool{}
	for i, c := range in.Acceptance {
		switch {
		case !IDPattern.MatchString(c.ID):
			add("acceptance[%d].id %q is not a valid identifier", i, c.ID)
		case seen[c.ID]:
			add("acceptance[%d].id %q is duplicated", i, c.ID)
		default:
			seen[c.ID] = true
		}
		if strings.TrimSpace(c.Statement) == "" {
			add("acceptance[%d] (%s) has no statement", i, c.ID)
		}
		switch c.AcceptedBy {
		case "", AcceptedByDelivery, AcceptedByHuman:
		default:
			add("acceptance[%d] (%s) has acceptedBy %q — it is %q or %q, and empty means %q",
				i, c.ID, c.AcceptedBy, AcceptedByDelivery, AcceptedByHuman, AcceptedByDelivery)
		}
	}
	// Delivery that may claim nothing has been asked to start a run whose only
	// possible outcome is a boundary. Refusing here beats reaching it.
	if len(in.Acceptance) > 0 && !anyDelivered(in.Acceptance) {
		add("every acceptance criterion is reserved to a person — managed delivery has nothing it may satisfy")
	}

	problems = append(problems, in.Policy.problems()...)

	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrIntentInvalid, strings.Join(problems, "; "))
	}
	return nil
}

// anyDelivered reports whether at least one criterion is delivery's to satisfy.
func anyDelivered(cs []Criterion) bool {
	for _, c := range cs {
		if !c.IsHuman() {
			return true
		}
	}
	return false
}

// problems reports policy grants that cannot all be true at once.
//
// These are contradictions, not preferences. A run authorized to merge but not
// to push has been handed an authority it cannot reach, and the honest moment
// to say so is before anything starts — not three hours later at a merge step
// that was never reachable.
func (p Policy) problems() []string {
	var out []string
	if p.NeedPR && !p.NeedPush {
		out = append(out, "policy.needPr requires policy.needPush — a pull request needs a pushed branch")
	}
	if p.NeedMerge {
		if !p.NeedPR {
			out = append(out, "policy.needMerge requires policy.needPr — this engine merges through pull requests, never by pushing to the default branch")
		}
		if !p.NeedChecks {
			out = append(out, "policy.needMerge requires policy.needChecks — merging without required checks is publication without acceptance")
		}
	}
	if !p.NeedMerge && strings.TrimSpace(p.MergeHumanAction) == "" {
		out = append(out, "policy.mergeHumanAction is required when merge authority is withheld — name what a person must do, so the run reports a boundary rather than a failure")
	}
	if p.MaxWorkers < 0 {
		out = append(out, "policy.maxWorkers cannot be negative")
	}
	if p.WorkDeadlineSeconds < 0 {
		out = append(out, "policy.workDeadlineSeconds cannot be negative")
	}
	return out
}

// splitSlug splits `owner/name`, rejecting anything with extra segments or
// path traversal in it.
func splitSlug(slug string) (owner, name string, ok bool) {
	parts := strings.Split(strings.TrimSpace(slug), "/")
	if len(parts) != 2 {
		return "", "", false
	}
	owner, name = parts[0], parts[1]
	if owner == "" || name == "" || owner == "." || owner == ".." || name == "." || name == ".." {
		return "", "", false
	}
	return owner, name, true
}

// slugFromOrigin reduces a git remote URL to `owner/name`.
//
// Both forms this fork encounters are handled: the https URL a portal records,
// and the scp-like syntax git writes for ssh remotes.
func slugFromOrigin(origin string) (string, error) {
	origin = strings.TrimSpace(origin)
	origin = strings.TrimSuffix(origin, ".git")

	if at := strings.Index(origin, "@"); at >= 0 && !strings.Contains(origin, "://") {
		// git@github.com:owner/name
		colon := strings.Index(origin[at:], ":")
		if colon < 0 {
			return "", errors.New("no path after host")
		}
		return strings.Trim(origin[at+colon+1:], "/"), nil
	}

	u, err := url.Parse(origin)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", errors.New("no host")
	}
	trimmed := strings.Trim(path.Clean("/"+u.Path), "/")
	if owner, name, ok := splitSlug(trimmed); ok {
		return owner + "/" + name, nil
	}
	return "", fmt.Errorf("path %q is not /owner/name", u.Path)
}

// isWindowsAbs recognizes a Windows absolute path on a non-Windows host.
//
// The delivery host runs Linux while the portal that registered the checkout
// runs on Windows, so an intent legitimately carries `D:\...`. Rejecting it as
// relative would fail every real handoff on this machine; the path is resolved
// for the host later, by the resolver that knows the mount layout.
func isWindowsAbs(p string) bool {
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		c := p[0]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return strings.HasPrefix(p, `\\`)
}
