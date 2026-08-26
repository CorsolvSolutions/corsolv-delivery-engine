package handoff

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// PlanSchemaVersion is the delivery-plan version this engine accepts.
const PlanSchemaVersion = 1

// WorkPackage is one bounded piece of delivery.
//
// It is what a planning agent produces and what a worker is given: a stated
// objective, the artifact it must leave behind, and the exact set of paths it
// is allowed to touch. The last of these is the containment boundary — the
// controller compares the change set against it before publishing, so a worker
// that wanders outside its package is caught before anything reaches the forge.
type WorkPackage struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Phase is the lifecycle phase this package belongs to. It must be one of
	// the phases the intent declared; a plan cannot invent a lifecycle.
	Phase string `json:"phase"`
	// Objective is what the worker is asked to achieve, in prose. It reaches
	// the worker as its bead text.
	Objective string `json:"objective"`
	// Artifact is the repository-relative file this package must produce. Its
	// absence after a worker finishes is a failed package, regardless of what
	// the worker claimed.
	Artifact string `json:"artifact"`
	// AuthorizedPaths is every repository-relative path this package may
	// create or change.
	AuthorizedPaths []string `json:"authorizedPaths"`
	// Gates are the verification commands this package's worker is expected to
	// run before closing, and is therefore permitted to run.
	//
	// They exist because a bounded worker is deny-by-default: the first pilot's
	// scaffold package told its worker to prove itself with `npm install && npm
	// run verify`, and the worker was structurally forbidden from executing
	// either, so it closed `blocked` with correct but unverified work. An
	// instruction a worker cannot obey is not a gate.
	//
	// Declaring them here — per package, validated, and nowhere else — is what
	// lets the run grant exactly these and nothing more. The grant is installed
	// in that package's own worktree, so one package's gates are not another's,
	// and no permission is widened for the fleet.
	Gates []string `json:"gates,omitempty"`
	// DependsOn names packages that must be MERGED before this one starts.
	DependsOn []string `json:"dependsOn,omitempty"`
	// Satisfies names the acceptance criteria this package contributes to.
	Satisfies []string `json:"satisfies"`
	// Verifies, when set, makes this package a VERIFICATION rather than a
	// MUTATION. See Verification.
	Verifies *Verification `json:"verifies,omitempty"`
}

// Verification turns a package from work-to-be-done into evidence-to-be-checked.
//
// WHY THIS EXISTS. A criterion reported met can later be disproved — by an
// audit, by a contract that grew a requirement, by evidence nobody had gathered.
// Usually the repair is work: someone changes the product, and the ordinary
// remediation lifecycle carries it (worker → branch → PR → CI → merge).
//
// But sometimes the evidence the criterion now needs is ALREADY on the
// authoritative branch. Nothing is missing from the product; what was missing
// was the checking. Asked to repair such a criterion, the ordinary lifecycle
// cannot: a worker handed a tree that already contains the evidence produces no
// diff, and publication correctly refuses — "nothing was produced". The only way
// through would be to manufacture a change nobody needs, open a pull request
// that repairs nothing, and merge it so the shape fits. That is a lie told to a
// state machine, and it is exactly the false completion this engine's controls
// exist to prevent.
//
// So a verification package states what it is: the commit whose evidence is
// being checked, and the gates that check it. The run cuts a clean tree at that
// exact commit, grants those gates, runs them, and requires the artifact to be
// really there. It creates no bead, no worker, no branch, no pull request and no
// merge, because it changes nothing — and it reconciles the criterion only if
// the checking passes.
//
// It is auditable as verification rather than mutation at every layer: the run
// task is `verify-<id>` rather than `await`/`publish`, the projected status is
// `verified` rather than `merged`, and the completion gate is derived from
// controls that name what was actually done — gates run against a named merged
// commit — instead of from a pull request that never existed.
type Verification struct {
	// MergedSha is the authoritative merged commit whose evidence is verified.
	//
	// Named, not inferred. "Whatever main happens to be" would make the record
	// of what was verified drift the moment anything else merged, and the whole
	// value of this package is that a later reader can go and look at the exact
	// tree the gates ran against.
	MergedSha string `json:"mergedSha"`
}

// IsVerification reports whether this package checks existing evidence rather
// than producing new work.
func (wp WorkPackage) IsVerification() bool {
	return wp.Verifies != nil
}

// ShaPattern matches a full git commit sha. Abbreviations are refused: what a
// verification package records is where a later reader must go to look, and an
// abbreviation is a prefix that a busy repository can grow to share.
var ShaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// DeliveryPlan is the initial plan for a project: the business brief turned
// into bounded work packages.
//
// Go does not author it. Deciding what work a brief implies is a judgement,
// and this fork's standing rule is that judgement lives in a prompt rather than
// in Go. What Go does is refuse a plan that cannot be executed safely, which is
// a structural question with a structural answer.
type DeliveryPlan struct {
	SchemaVersion int    `json:"schemaVersion"`
	ProjectID     string `json:"projectId"`
	// PlannedBy records who produced this plan — an agent session id, so the
	// plan's provenance survives in the run's evidence.
	PlannedBy string        `json:"plannedBy"`
	PlannedAt time.Time     `json:"plannedAt"`
	Packages  []WorkPackage `json:"packages"`

	// Remediations are the additive plan revisions authorized after a criterion
	// this plan claimed to satisfy was later disproved.
	//
	// NOT SERIALIZED, deliberately. Each remediation is its own document on
	// disk, and this field is filled by LoadPlan from those files. A remediation
	// that could travel inside plan.json would be a remediation that could be
	// installed by writing the plan — which is the history rewrite the whole
	// mechanism exists to prevent, arriving through the front door.
	Remediations []Remediation `json:"-"`

	// Provisional marks a plan that is a SHAPE rather than a proposal.
	//
	// Preflight compiles one: a placeholder package per delivery-owned
	// criterion, no gates, an objective saying it will never run — so that the
	// questions which do not depend on what the work turns out to be
	// (ownership, tools, the forge, durable state) can be asked before anyone
	// waits on a planner.
	//
	// THE DEFECT THIS EXISTS FOR. Fidelity measures a plan against what its
	// criteria require, and a placeholder carries none of those behaviors
	// because it promises nothing. So the moment a project declared required
	// behaviors, preflight refused it at the front door — listing behaviors
	// nobody had yet been given a chance to deliver, before the planner that
	// would have written a lawful plan was ever asked.
	//
	// This is the second time that trap has been sprung; `preflightPackages`
	// carries the note from the first, when a placeholder claiming a person's
	// criterion refused every intent with a human boundary. Same shape,
	// different rule.
	//
	// NOT SERIALIZED, for a sharper reason than Remediations: a plan file that
	// could declare itself provisional would be a plan file that could opt out
	// of the fidelity rule. This is only ever this process's word about a plan
	// it built itself, and every plan read from disk faces the rule.
	Provisional bool `json:"-"`
}

// AllPackages is every package this delivery must complete: what was planned
// first, then each remediation's corrective work in the order it was
// authorized.
//
// The generations are flattened here and nowhere else. Callers that ask "what
// work does this delivery have" want the union — the assessment, the compiler
// and the driver all do — while `Packages` on its own stays exactly what was
// planned before anything was disproved, so the two questions never collapse
// into one answer.
func (p DeliveryPlan) AllPackages() []WorkPackage {
	gone := p.Superseded()
	out := append([]WorkPackage(nil), p.Packages...)
	for _, rm := range p.Remediations {
		for _, wp := range rm.Packages {
			if gone[wp.ID] {
				continue
			}
			out = append(out, wp)
		}
	}
	return out
}

// Superseded is every corrective package a later remediation has replaced.
//
// They stay in their own remediation documents — the record of what was once
// authorized is not rewritten — but they are not work this delivery is waiting
// for, so every question about what must run, what must complete, and what is
// repairing a criterion is asked without them.
func (p DeliveryPlan) Superseded() map[string]bool {
	out := map[string]bool{}
	for _, rm := range p.Remediations {
		for _, id := range rm.Supersedes {
			out[id] = true
		}
	}
	return out
}

// generations groups this plan's packages by the revision that introduced them:
// index 0 is the original plan, and each remediation is its own generation.
//
// The grouping is load-bearing for exactly one rule — writer isolation — and
// pointless for every other, which is why it is computed here rather than
// carried on WorkPackage.
func (p DeliveryPlan) generations() [][]WorkPackage {
	gone := p.Superseded()
	gens := [][]WorkPackage{p.Packages}
	for _, rm := range p.Remediations {
		live := make([]WorkPackage, 0, len(rm.Packages))
		for _, wp := range rm.Packages {
			if gone[wp.ID] {
				// Superseded work claims nothing, so it collides with nothing —
				// and holding a replacement to the writer isolation of the
				// package it replaces would make replacing one impossible.
				continue
			}
			live = append(live, wp)
		}
		gens = append(gens, live)
	}
	return gens
}

// ErrPlanInvalid is returned for a plan this engine will not execute.
var ErrPlanInvalid = errors.New("handoff: delivery plan is invalid")

// protectedPrefixes are paths no work package may ever be authorized to change.
//
// This is not a style rule. Completion in this system is evidence-based, and
// the evidence is the project's own required CI running on the exact head. A
// package authorized to edit the workflow that judges it can make its own gate
// pass, at which point "CI green" stops being evidence of anything. The same
// reasoning covers the git directory and the engine's own delivery projection:
// a run must not be able to rewrite the record of what it did.
var protectedPrefixes = []string{
	".git",
	".github/workflows",
	"delivery/gascity",
}

// MaxGatesPerPackage bounds how much verification surface one package may ask
// for. A package needing more than this is not describing gates.
const MaxGatesPerPackage = 8

// gateRunners are the programs a work package may name as a gate.
//
// An allowlist, not a denylist, because the plan is written by a language model
// and the question "is this command safe" has no general answer. These are
// project build and test runners: they do what the project's own configuration
// says, which is exactly what a gate is for.
//
// What is deliberately absent is as much of the point as what is present.
// `git` and `gh` are publication authority, which belongs to the controller
// alone. `npx`, `curl`, `wget` and `pip` fetch and execute code the plan never
// declared. `bash`, `sh`, `env` and `xargs` are general execution surfaces that
// would make the rest of this list decorative. `sudo`, `ssh` and `docker` leave
// the worktree entirely.
var gateRunners = map[string]bool{
	"npm": true, "pnpm": true, "yarn": true, "node": true,
	"go": true, "make": true, "cargo": true, "mvn": true, "gradle": true,
	"python": true, "python3": true, "pytest": true, "ruff": true,
	"tsc": true, "eslint": true, "vitest": true, "jest": true, "dotnet": true,
}

// gateForbidden are the characters that would turn one declared command into
// several, or into something the declaration does not say. A gate is a command,
// not a shell script.
const gateForbidden = ";|&$`<>(){}[]\\\"'\n\r\t*?!#~"

// ValidateGate reports why a declared gate cannot be granted, or nil.
//
// It is exported because the grant is only as trustworthy as this check: the
// driver installs exactly what this accepts, and a test that proves a refusal
// here is proving the boundary itself.
func ValidateGate(gate string) error {
	trimmed := strings.TrimSpace(gate)
	if trimmed == "" {
		return errors.New("a gate cannot be empty")
	}
	if trimmed != gate {
		return fmt.Errorf("%q has surrounding whitespace; a gate is granted verbatim", gate)
	}
	if strings.ContainsAny(gate, gateForbidden) {
		return fmt.Errorf("%q contains shell syntax; a gate is one command, not a script", gate)
	}
	// Traversal, not merely a pair of dots: `go build ./...` is an ordinary
	// package pattern and refusing it would refuse Go projects outright.
	for _, token := range strings.Fields(gate) {
		if token == ".." || strings.HasPrefix(token, "../") ||
			strings.Contains(token, "/../") || strings.HasSuffix(token, "/..") {
			return fmt.Errorf("%q traverses out of the worktree", gate)
		}
	}
	fields := strings.Fields(gate)
	if strings.Contains(fields[0], "/") {
		return fmt.Errorf("%q names a path rather than a project runner", gate)
	}
	if !gateRunners[fields[0]] {
		return fmt.Errorf("%q is not a project build or test runner this engine will grant", gate)
	}
	return nil
}

// DecodePlan parses a delivery plan and validates it against the intent it was
// planned for.
func DecodePlan(data []byte, in Intent) (DeliveryPlan, error) {
	return DecodePlanWithRemediations(data, in, nil)
}

// DecodePlanWithRemediations parses a delivery plan, joins the corrective work
// authorized since, and validates every generation together.
//
// Together rather than one at a time: a remediation can be impeccable on its
// own and still collide with the plan it is added to — a duplicated package id,
// a dependency cycle across the join, two live packages authorized over one
// path. Those questions only exist for the union, so the union is what is
// checked.
func DecodePlanWithRemediations(data []byte, in Intent, remediations []Remediation) (DeliveryPlan, error) {
	var p DeliveryPlan
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("%w: %w", ErrPlanInvalid, err)
	}
	p.Remediations = remediations
	if err := p.Validate(in); err != nil {
		return p, err
	}
	return p, nil
}

// pathClaim records which package authorized a path, and which generation of
// the plan that package belongs to.
type pathClaim struct {
	label string
	id    string
	gen   int
}

// dependencyOrdered answers whether two packages of one generation can never
// hold a worktree at the same time, because one's work strictly follows the
// other's MERGE — directly or through a chain. Two such packages sharing a
// path are sequential writers, like two commits touching one file; the
// collision rule exists for CONCURRENT writers, and holding ordered packages
// to it made the final package of a delivery — the one that reconciles what
// every earlier package built — impossible to authorize at all.
func dependencyOrdered(byID map[string]WorkPackage, a, b string) bool {
	reaches := func(from, to string) bool {
		seen := map[string]bool{}
		stack := []string{from}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if cur == to {
				return true
			}
			if seen[cur] {
				continue
			}
			seen[cur] = true
			stack = append(stack, byID[cur].DependsOn...)
		}
		return false
	}
	return reaches(a, b) || reaches(b, a)
}

// Validate refuses a plan that cannot be executed against this intent.
//
// The checks fall into three groups, and it is worth being clear which is
// which. Well-formedness (ids, cycles, empty fields) protects the runner.
// Agreement with the intent (phases, acceptance coverage) protects the user
// from a plan that quietly drifted from what they asked for. Containment
// (traversal, protected paths, disjoint authorization) protects everything
// else from a plan that a language model wrote.
//
// It validates every GENERATION at once: the original packages, and the
// corrective work each remediation added afterwards. A remediation can be
// impeccable on its own and still be impossible beside the plan it is added to,
// and the union is the only place that shows.
func (p DeliveryPlan) Validate(in Intent) error {
	if p.SchemaVersion != PlanSchemaVersion {
		return fmt.Errorf("%w: schemaVersion %d, this engine speaks %d", ErrPlanInvalid, p.SchemaVersion, PlanSchemaVersion)
	}

	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if p.ProjectID != in.ProjectID {
		add("plan projectId %q does not match the intent's %q", p.ProjectID, in.ProjectID)
	}
	if len(p.Packages) == 0 {
		add("a plan with no work packages cannot deliver anything")
	}

	phases := map[string]bool{}
	for _, ph := range in.Lifecycle {
		phases[ph] = true
	}
	criteria := map[string]bool{}
	// human are the criteria only a person may accept. Delivery may prepare what
	// that person reads and may never claim their answer, so these are excluded
	// from both halves of the coverage rule below.
	human := map[string]bool{}
	for _, c := range in.Acceptance {
		criteria[c.ID] = true
		if c.IsHuman() {
			human[c.ID] = true
		}
	}

	ids := map[string]bool{}
	// owner maps an authorized path to the package that claimed it, so an
	// overlap names both sides rather than just saying "overlap".
	//
	// WHY THE GENERATION IS PART OF THE CLAIM, AND ONLY HERE. Writer isolation
	// exists because two workers writing one path is a collision — a statement
	// about work that can be in flight at the same time. A remediation is
	// authorized against a finding raised after the work it repairs had already
	// merged, so its packages cannot be running beside that work, and repairing
	// a file is the ordinary shape of a repair. Enforcing disjointness across
	// generations would leave corrective work able to touch only files the
	// delivery had never produced, which is not corrective work.
	//
	// So a path may be claimed by at most one package per generation, and a
	// later generation's claim supersedes an earlier one. Within any set of
	// packages that could run together, the rule is exactly what it always was.
	owner := map[string]pathClaim{}
	satisfied := map[string]bool{}

	for gen, packages := range p.generations() {
		genByID := map[string]WorkPackage{}
		for _, wp := range packages {
			genByID[wp.ID] = wp
		}
		for i, wp := range packages {
			label := planPackageLabel(gen, i)
			if IDPattern.MatchString(wp.ID) && !ids[wp.ID] {
				label = wp.ID
			}
			switch {
			case !IDPattern.MatchString(wp.ID):
				add("%s.id %q is not a valid identifier", label, wp.ID)
			case ids[wp.ID]:
				// Across every generation. A remedial package reusing a merged
				// package's id would give the run two tasks with one name, and
				// the journal that decides what a resumed run may skip is keyed
				// by exactly that name.
				add("%s.id %q is duplicated", label, wp.ID)
			default:
				ids[wp.ID] = true
			}

			if strings.TrimSpace(wp.Title) == "" {
				add("%s has no title", label)
			}
			if strings.TrimSpace(wp.Objective) == "" {
				add("%s has no objective — a worker cannot be given an empty instruction", label)
			}
			if !phases[wp.Phase] {
				add("%s.phase %q is not one of the lifecycle phases the intent declared", label, wp.Phase)
			}

			if len(wp.Gates) > MaxGatesPerPackage {
				add("%s declares %d gates; at most %d may be granted", label, len(wp.Gates), MaxGatesPerPackage)
			}
			seenGate := map[string]bool{}
			for _, gate := range wp.Gates {
				if err := ValidateGate(gate); err != nil {
					add("%s gate %v", label, err)
					continue
				}
				if seenGate[gate] {
					add("%s declares gate %q twice", label, gate)
				}
				seenGate[gate] = true
			}

			// A VERIFICATION PACKAGE IS HELD TO THE OPPOSITE RULES, AND HELD TO
			// THEM JUST AS HARD.
			//
			// A mutation package must be able to change something, or it cannot
			// produce its result. A verification package must be able to change
			// NOTHING, or it is not a verification: the moment it may write, the
			// evidence it reports on could be evidence it wrote itself, and the
			// criterion it reconciles would rest on a check marking its own work.
			//
			// So the containment boundary is not relaxed here, it is inverted. The
			// artifact must still be named, because something has to be there for
			// the check to be about — but it is named as evidence that must ALREADY
			// exist at the verified commit, not as a file to be produced.
			if wp.IsVerification() {
				if len(wp.AuthorizedPaths) != 0 {
					add("%s verifies existing evidence and authorizes %d path(s) — a package that may write cannot be the check on what was written", label, len(wp.AuthorizedPaths))
				}
				if !ShaPattern.MatchString(wp.Verifies.MergedSha) {
					add("%s.verifies.mergedSha %q is not a full commit sha — a verification names the exact tree it ran against, so a later reader can go and look at it", label, wp.Verifies.MergedSha)
				}
				if len(wp.Gates) == 0 {
					add("%s verifies nothing — a verification package with no gates asserts a criterion is met without checking anything", label)
				}
				if len(wp.DependsOn) != 0 {
					add("%s verifies existing evidence and declares dependencies — it merges nothing, so nothing it waited for could reach it", label)
				}
				if gen == 0 {
					add("%s verifies existing evidence but is in the original plan — there is nothing for a delivery's first plan to verify, because none of its work has been done yet", label)
				}
			} else if len(wp.AuthorizedPaths) == 0 {
				add("%s authorizes no paths — a package that may change nothing cannot produce anything", label)
			}
			clean := make([]string, 0, len(wp.AuthorizedPaths))
			for _, raw := range wp.AuthorizedPaths {
				c, err := repoRelative(raw)
				if err != nil {
					add("%s authorized path %q is not usable: %v", label, raw, err)
					continue
				}
				if prefix, protected := isProtected(c); protected {
					add("%s authorizes %q, which is under the protected path %q — a run may not change what judges it", label, c, prefix)
					continue
				}
				if claim, taken := owner[c]; taken && claim.gen == gen &&
					!dependencyOrdered(genByID, claim.id, wp.ID) {
					add("%s and %s both authorize %q with no dependency ordering them — two workers that could hold "+
						"the path at the same time is a collision, not a plan", label, claim.label, c)
					continue
				}
				owner[c] = pathClaim{label: label, id: wp.ID, gen: gen}
				clean = append(clean, c)
			}

			artifact, err := repoRelative(wp.Artifact)
			switch {
			case strings.TrimSpace(wp.Artifact) == "":
				add("%s names no required artifact — nothing would prove the package produced its result", label)
			case err != nil:
				add("%s artifact %q is not usable: %v", label, wp.Artifact, err)
			case wp.IsVerification():
				// Nothing to authorize: the artifact is evidence the verified
				// commit must already carry, and the check is that it does.
			default:
				if !containsPath(clean, artifact) {
					add("%s must authorize its own artifact %q", label, artifact)
				}
			}

			if len(wp.Satisfies) == 0 {
				add("%s satisfies no acceptance criterion — work nobody asked for is not delivery", label)
			}
			for _, c := range wp.Satisfies {
				switch {
				case !criteria[c]:
					add("%s satisfies %q, which the intent does not declare as an acceptance criterion", label, c)
				case human[c]:
					// THE DEFECT THIS REFUSAL EXISTS FOR. The package merges, the
					// merge is read as evidence, and a criterion reserved for a
					// person is scored met without one ever looking at it. A package
					// may produce the record a reviewer signs; it may not sign it.
					add("%s satisfies %q, which only a person may accept — a work package may prepare what a reviewer reads and may never claim their acceptance", label, c)
				default:
					satisfied[c] = true
				}
			}
		}
	}

	// SUPERSESSION REACHES BACKWARDS, AND ONLY INTO CORRECTIVE WORK.
	//
	// Checked here rather than where a remediation is admitted, because whether
	// an id is supersedable is a question about the whole composed plan: which
	// generation it belongs to, and whether that generation came first.
	basePackage := map[string]bool{}
	for _, wp := range p.Packages {
		basePackage[wp.ID] = true
	}
	earlier := map[string]bool{}
	for i, rm := range p.Remediations {
		seenHere := map[string]bool{}
		for _, id := range rm.Supersedes {
			switch {
			case seenHere[id]:
				add("remediation %d supersedes %q twice", i+1, id)
			case basePackage[id]:
				add("remediation %d supersedes %q, which the ORIGINAL plan authorized — that work is the history everything since was measured against, and a remediation may not withdraw it", i+1, id)
			case !earlier[id]:
				add("remediation %d supersedes %q, which no earlier remediation authorized — corrective work can only replace corrective work that already exists", i+1, id)
			}
			seenHere[id] = true
		}
		for _, wp := range rm.Packages {
			earlier[wp.ID] = true
		}
	}

	all := p.AllPackages()
	// verifier names the packages that merge nothing. A dependency runs from an
	// upstream's MERGE bead — waiting for repository state rather than for a
	// sibling worker's files — and a verification package never closes one, so a
	// package waiting on it would wait for something that cannot happen.
	verifier := map[string]bool{}
	for _, wp := range all {
		if wp.IsVerification() {
			verifier[wp.ID] = true
		}
	}
	for _, wp := range all {
		for _, dep := range wp.DependsOn {
			if dep == wp.ID {
				add("%s depends on itself", wp.ID)
				continue
			}
			if !ids[dep] {
				add("%s depends on %q, which is not in this plan", wp.ID, dep)
				continue
			}
			if verifier[dep] {
				add("%s depends on %q, which verifies existing evidence and merges nothing — the merge it would wait for can never happen", wp.ID, dep)
			}
		}
	}
	if cycle := findCycle(all); cycle != "" {
		add("work package dependencies contain a cycle: %s", cycle)
	}

	// Acceptance coverage. A criterion nothing addresses can never be met, so a
	// plan that leaves one uncovered has already decided the project cannot
	// complete — and saying so now beats discovering it at the completion gate.
	var uncovered []string
	for _, c := range in.Acceptance {
		if c.IsHuman() {
			// Not uncovered: answered by a person, outside this plan. Demanding
			// coverage here would make the only valid plan the one that lies.
			continue
		}
		if !satisfied[c.ID] {
			uncovered = append(uncovered, c.ID)
		}
	}
	sort.Strings(uncovered)
	if len(uncovered) > 0 {
		add("no work package addresses acceptance criteria %s — this plan cannot reach completion", strings.Join(uncovered, ", "))
	}

	// FIDELITY. Naming a criterion is not delivering it.
	//
	// The rule above is satisfied by a package that writes an id in a list, and
	// that is all it ever checked. A pilot's planner claimed a criterion
	// requiring six type names, wrote itself a contract with five different
	// ones, and every gate after it — the worker's, the controller's re-run of
	// the worker's, required CI — asked whether the PLAN had been carried out.
	// It had. The project completed with a required behavior missing.
	//
	// So where the author declared what the criterion actually requires, the
	// work claiming it has to carry those behaviors, and has to declare how
	// they are verified. A planner may split the criterion across packages,
	// reorder them and choose its own words for everything else: the check is
	// over what the covering packages say TOGETHER.
	for _, c := range in.Acceptance {
		// A provisional plan is exempt from this clause and from no other. It
		// is a shape the compiler will accept, not a promise to deliver
		// anything, and measuring a placeholder against what the work must
		// achieve refused every project that stated its requirements before its
		// planner had been asked for a plan. See DeliveryPlan.Provisional.
		if p.Provisional || c.IsHuman() || len(c.MustCover) == 0 {
			continue
		}
		covering := coveringPackages(all, c.ID)
		if len(covering) == 0 {
			continue // already reported as uncovered above
		}

		// Verifiable, not merely mentioned. A criterion carrying required
		// behavior is one completion will be decided on, and work with no
		// declared gate has nothing that can demonstrate it.
		var gated bool
		for _, wp := range covering {
			if len(wp.Gates) > 0 {
				gated = true
				break
			}
		}
		if !gated {
			add("%s requires behavior the plan must prove, and no work package claiming it declares a gate — "+
				"a criterion nothing verifies cannot be evidence of anything", c.ID)
		}

		missing := missingBehaviours(coveringText(covering), c.MustCover)
		if len(missing) > 0 {
			add("%s requires %s, which no work package claiming it plans to deliver — a planner may split "+
				"or reword the work and may not drop what it must achieve",
				c.ID, strings.Join(quoteAll(missing), ", "))
		}
	}

	// And each remediation on its own terms: the scope it was authorized for,
	// and fidelity over its OWN packages rather than over the union above.
	//
	// Checked here as well as at authorization because the remediation files
	// are the durable half of this mechanism and nothing else re-reads them. A
	// plan loaded from disk is checked by the same rule that let it be written.
	for _, rm := range p.Remediations {
		if err := rm.Validate(in); err != nil {
			add("remediation %d: %v", rm.Seq, err)
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrPlanInvalid, strings.Join(problems, "; "))
	}
	return nil
}

// planPackageLabel names a package's position for a refusal a planner can act
// on, before its id has been proved usable.
func planPackageLabel(gen, i int) string {
	if gen == 0 {
		return fmt.Sprintf("packages[%d]", i)
	}
	return fmt.Sprintf("remediation %d packages[%d]", gen, i)
}

// coveringPackages are the work packages that claim a criterion.
func coveringPackages(pkgs []WorkPackage, criterion string) []WorkPackage {
	var out []WorkPackage
	for _, wp := range pkgs {
		if containsPath(wp.Satisfies, criterion) {
			out = append(out, wp)
		}
	}
	return out
}

// coveringText is everything the packages claiming a criterion say they will
// do, lower-cased, as one document.
//
// The objective is where a package states its work, and it is what the worker
// is given; the title, artifact and gates are read too because a behavior can
// legitimately live in the name of the thing that proves it. Nothing else is
// consulted — an authorized path is a containment fact, not a promise.
func coveringText(pkgs []WorkPackage) string {
	var b strings.Builder
	for _, wp := range pkgs {
		b.WriteString(wp.Title)
		b.WriteByte('\n')
		b.WriteString(wp.Objective)
		b.WriteByte('\n')
		b.WriteString(wp.Artifact)
		b.WriteByte('\n')
		b.WriteString(strings.Join(wp.Gates, "\n"))
		b.WriteByte('\n')
	}
	return strings.ToLower(b.String())
}

// missingBehaviours reports which required behaviors the covering work does
// not say it will deliver, in the order the author wrote them.
//
// A behavior may be written as alternatives separated by `|`, and any one of
// them satisfies it: "decimal|number" says those two words mean one requirement
// here, because whether they do is the author's call and nobody else's. Nothing
// here decides whether two different sentences mean the same thing — that
// judgement is exactly what this design keeps out of Go, and an author who
// wants latitude says so by writing the alternatives down.
//
// Two rules make the match mean something.
//
// WHOLE WORDS. "date" is not delivered by "candidate". A match has to start and
// end on a word boundary, or a requirement is satisfied by any word that
// happens to contain it.
//
// ONE OCCURRENCE ANSWERS ONE REQUIREMENT. This is what the pilot needed and did
// not have. A plan listing "text, integer, decimal, boolean or date" and then
// "report the columns containing mixed inferred types" contains the word
// `mixed` — inside the OTHER requirement. Letting both requirements feed on the
// same occurrence would accept the exact plan that shipped a product with no
// mixed type in it. So a matched span is spent, and the longest requirements
// are matched first, which keeps a broad phrase from being consumed a word at a
// time by the narrow ones and makes the result independent of how the author
// happened to order them.
func missingBehaviours(said string, required []string) []string {
	order := make([]int, len(required))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return longestAlternative(required[order[a]]) > longestAlternative(required[order[b]])
	})

	spent := make([]bool, len(said))
	unmet := make([]bool, len(required))
	for _, idx := range order {
		if !claimOccurrence(said, spent, required[idx]) {
			unmet[idx] = true
		}
	}

	var missing []string
	for i, gone := range unmet {
		if gone {
			missing = append(missing, required[i])
		}
	}
	return missing
}

// longestAlternative is the length of the longest spelling a behavior accepts.
func longestAlternative(required string) int {
	longest := 0
	for _, alt := range strings.Split(required, "|") {
		if n := len(strings.TrimSpace(alt)); n > longest {
			longest = n
		}
	}
	return longest
}

// claimOccurrence spends the first unspent whole-word occurrence of any of a
// behaviour's spellings, and reports whether it found one.
func claimOccurrence(said string, spent []bool, required string) bool {
	for _, alt := range strings.Split(required, "|") {
		alt = strings.ToLower(strings.TrimSpace(alt))
		if alt == "" {
			continue
		}
		for at := 0; ; {
			found := strings.Index(said[at:], alt)
			if found < 0 {
				break
			}
			start := at + found
			end := start + len(alt)
			at = start + 1
			if !wholeWord(said, start, end) || spanIsSpent(spent, start, end) {
				continue
			}
			for i := start; i < end; i++ {
				spent[i] = true
			}
			return true
		}
	}
	return false
}

// wholeWord reports whether a span stands on its own rather than inside a
// longer word.
func wholeWord(said string, start, end int) bool {
	return !isWordByte(said, start-1) && !isWordByte(said, end)
}

func isWordByte(said string, at int) bool {
	if at < 0 || at >= len(said) {
		return false
	}
	c := said[at]
	return c == '_' || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9')
}

func spanIsSpent(spent []bool, start, end int) bool {
	for i := start; i < end; i++ {
		if spent[i] {
			return true
		}
	}
	return false
}

// quoteAll renders required behaviors for a refusal a planner can act on.
func quoteAll(items []string) []string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		out = append(out, fmt.Sprintf("%q", s))
	}
	return out
}

// Package returns the named package.
func (p DeliveryPlan) Package(id string) (WorkPackage, bool) {
	for _, wp := range p.Packages {
		if wp.ID == id {
			return wp, true
		}
	}
	return WorkPackage{}, false
}

// repoRelative normalizes a repository-relative path and refuses anything that
// could address a location outside the repository.
func repoRelative(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("empty")
	}
	if strings.ContainsRune(s, '\x00') {
		return "", errors.New("contains a NUL byte")
	}
	// Windows-authored plans legitimately use backslashes; the repository
	// speaks forward slashes.
	s = strings.ReplaceAll(s, `\`, "/")
	if strings.HasPrefix(s, "/") {
		return "", errors.New("is absolute")
	}
	if len(s) >= 2 && s[1] == ':' {
		return "", errors.New("is a drive-qualified path")
	}
	cleaned := path.Clean(s)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("escapes the repository")
	}
	return cleaned, nil
}

func isProtected(cleaned string) (string, bool) {
	for _, prefix := range protectedPrefixes {
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
			return prefix, true
		}
	}
	return "", false
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// findCycle returns a readable cycle in the dependency graph, or "".
func findCycle(pkgs []WorkPackage) string {
	deps := map[string][]string{}
	ids := make([]string, 0, len(pkgs))
	for _, wp := range pkgs {
		deps[wp.ID] = wp.DependsOn
		ids = append(ids, wp.ID)
	}
	sort.Strings(ids)

	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var walk func(string) string
	walk = func(id string) string {
		color[id] = grey
		stack = append(stack, id)
		for _, next := range deps[id] {
			if _, known := deps[next]; !known {
				continue
			}
			switch color[next] {
			case grey:
				return strings.Join(append(stack, next), " -> ")
			case white:
				if c := walk(next); c != "" {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return ""
	}
	for _, id := range ids {
		if color[id] == white {
			if c := walk(id); c != "" {
				return c
			}
		}
	}
	return ""
}
