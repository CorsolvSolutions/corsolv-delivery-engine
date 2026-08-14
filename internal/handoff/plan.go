package handoff

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
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
	// DependsOn names packages that must be MERGED before this one starts.
	DependsOn []string `json:"dependsOn,omitempty"`
	// Satisfies names the acceptance criteria this package contributes to.
	Satisfies []string `json:"satisfies"`
}

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

// DecodePlan parses a delivery plan and validates it against the intent it was
// planned for.
func DecodePlan(data []byte, in Intent) (DeliveryPlan, error) {
	var p DeliveryPlan
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("%w: %w", ErrPlanInvalid, err)
	}
	if err := p.Validate(in); err != nil {
		return p, err
	}
	return p, nil
}

// Validate refuses a plan that cannot be executed against this intent.
//
// The checks fall into three groups, and it is worth being clear which is
// which. Well-formedness (ids, cycles, empty fields) protects the runner.
// Agreement with the intent (phases, acceptance coverage) protects the user
// from a plan that quietly drifted from what they asked for. Containment
// (traversal, protected paths, disjoint authorization) protects everything
// else from a plan that a language model wrote.
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
	for _, c := range in.Acceptance {
		criteria[c.ID] = true
	}

	ids := map[string]bool{}
	// owner maps an authorized path to the package that claimed it, so an
	// overlap names both sides rather than just saying "overlap".
	owner := map[string]string{}
	satisfied := map[string]bool{}

	for i, wp := range p.Packages {
		label := fmt.Sprintf("packages[%d]", i)
		switch {
		case !IDPattern.MatchString(wp.ID):
			add("%s.id %q is not a valid identifier", label, wp.ID)
		case ids[wp.ID]:
			add("%s.id %q is duplicated", label, wp.ID)
		default:
			ids[wp.ID] = true
			label = wp.ID
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

		if len(wp.AuthorizedPaths) == 0 {
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
			if other, taken := owner[c]; taken {
				add("%s and %s both authorize %q — two workers writing one path is a collision, not a plan", label, other, c)
				continue
			}
			owner[c] = label
			clean = append(clean, c)
		}

		artifact, err := repoRelative(wp.Artifact)
		switch {
		case strings.TrimSpace(wp.Artifact) == "":
			add("%s names no required artifact — nothing would prove the package produced its result", label)
		case err != nil:
			add("%s artifact %q is not usable: %v", label, wp.Artifact, err)
		default:
			if !containsPath(clean, artifact) {
				add("%s must authorize its own artifact %q", label, artifact)
			}
		}

		if len(wp.Satisfies) == 0 {
			add("%s satisfies no acceptance criterion — work nobody asked for is not delivery", label)
		}
		for _, c := range wp.Satisfies {
			if !criteria[c] {
				add("%s satisfies %q, which the intent does not declare as an acceptance criterion", label, c)
			}
			satisfied[c] = true
		}
	}

	for _, wp := range p.Packages {
		for _, dep := range wp.DependsOn {
			if dep == wp.ID {
				add("%s depends on itself", wp.ID)
				continue
			}
			if !ids[dep] {
				add("%s depends on %q, which is not in this plan", wp.ID, dep)
			}
		}
	}
	if cycle := findCycle(p.Packages); cycle != "" {
		add("work package dependencies contain a cycle: %s", cycle)
	}

	// Acceptance coverage. A criterion nothing addresses can never be met, so a
	// plan that leaves one uncovered has already decided the project cannot
	// complete — and saying so now beats discovering it at the completion gate.
	var uncovered []string
	for _, c := range in.Acceptance {
		if !satisfied[c.ID] {
			uncovered = append(uncovered, c.ID)
		}
	}
	sort.Strings(uncovered)
	if len(uncovered) > 0 {
		add("no work package addresses acceptance criteria %s — this plan cannot reach completion", strings.Join(uncovered, ", "))
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrPlanInvalid, strings.Join(problems, "; "))
	}
	return nil
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
