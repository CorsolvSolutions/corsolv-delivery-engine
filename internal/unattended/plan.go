package unattended

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Band is the priority tier a task belongs to.
//
// The bands exist to answer one question: when the thing the run most wants to
// do is blocked, what should it do instead? Without an answer a run ends at its
// first dependency — the nine-minute run — and with a bad answer it invents
// busywork. A declared band gives the queue somewhere real to go.
type Band string

// The bands, in the order the queue prefers them.
const (
	// BandPrimary is the work the run exists to do.
	BandPrimary Band = "primary"
	// BandDependency unblocks the primary work.
	BandDependency Band = "dependency"
	// BandValidation proves the work is correct: tests, typecheck, build.
	BandValidation Band = "validation"
	// BandAssurance proves it more broadly: regression, static analysis, review.
	BandAssurance Band = "assurance"
	// BandEvidence reconciles and records what happened.
	BandEvidence Band = "evidence"
	// BandDocumentation writes down what a reader will need.
	BandDocumentation Band = "documentation"
	// BandNextStage prepares work that follows this run.
	BandNextStage Band = "next-stage"
)

var bandRank = map[Band]int{
	BandPrimary: 0, BandDependency: 1, BandValidation: 2, BandAssurance: 3,
	BandEvidence: 4, BandDocumentation: 5, BandNextStage: 6,
}

// Valid reports whether the band is one of the declared seven.
func (b Band) Valid() bool { _, ok := bandRank[b]; return ok }

// Rank orders bands for selection. Lower is preferred.
func (b Band) Rank() int {
	if r, ok := bandRank[b]; ok {
		return r
	}
	return len(bandRank)
}

// Task is one unit of declared work.
type Task struct {
	ID    string `toml:"id" json:"id"`
	Title string `toml:"title" json:"title"`
	Band  Band   `toml:"band" json:"band"`

	Argv []string `toml:"argv" json:"argv"`
	Dir  string   `toml:"dir,omitempty" json:"dir,omitempty"`

	// Needs names tasks that must have succeeded first.
	Needs []string `toml:"needs,omitempty" json:"needs,omitempty"`

	// RequiresChecks names preflight check IDs this task depends on. A task
	// whose required check reported a human boundary is held rather than
	// attempted — which is how a boundary discovered at preflight becomes a
	// planned detour instead of a mid-run wall.
	RequiresChecks []string `toml:"requiresChecks,omitempty" json:"requiresChecks,omitempty"`

	// Mutates marks a task that changes the worktree. The fence is verified
	// before it runs and an advance recorded after, so an external change can
	// never be mistaken for this run's own work.
	Mutates bool `toml:"mutates,omitempty" json:"mutates,omitempty"`

	TimeoutSeconds int `toml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
	// MaxAttempts overrides the failure class's policy for this task. Zero uses
	// the policy, which is almost always right.
	MaxAttempts int `toml:"maxAttempts,omitempty" json:"maxAttempts,omitempty"`

	// DeliveryStatus, when set, is the delivery-projection status this task's
	// success establishes — one of the projector's canonical vocabulary.
	//
	// It is declared, never inferred. Most tasks in a run are internal
	// machinery, and a run task succeeding is not by itself a delivery
	// milestone; projecting every green command as delivery progress would
	// overstate what happened. A task with no DeliveryStatus simply does not
	// appear in the projection.
	DeliveryStatus string `toml:"deliveryStatus,omitempty" json:"deliveryStatus,omitempty"`
	// CompletionGate names the evidence gate this task's delivery status is
	// judged against, for the projection's completion-gate column.
	CompletionGate string `toml:"completionGate,omitempty" json:"completionGate,omitempty"`
	// Phase is the delivery phase the task belongs to, for the projection.
	Phase string `toml:"phase,omitempty" json:"phase,omitempty"`

	// QAGate names the catalog gate this task's mechanical execution
	// produces evidence for. It is what makes a gate pluggable: the gate is a
	// class of evidence, and the packet says which command satisfies it here.
	// The exit status is the verdict, and the fence's position when the task
	// ran is the revision the verdict is bound to.
	QAGate string `toml:"qaGate,omitempty" json:"qaGate,omitempty"`
}

// Plan is the declared work queue for a run — the work packet.
type Plan struct {
	RunID string `toml:"runId" json:"runId"`

	// Risk is the packet's declared risk class. It is required: it is the
	// input to mandatory-gate selection, so a packet without one cannot be
	// asked what would have to pass before it may progress.
	Risk RiskClass `toml:"risk" json:"risk"`

	Tasks []Task `toml:"task" json:"tasks"`
}

// LoadPlan reads and validates a work plan.
func LoadPlan(path string) (Plan, error) {
	var p Plan
	data, err := os.ReadFile(path)
	if err != nil {
		return p, fmt.Errorf("reading work plan %q: %w", path, err)
	}
	if err := toml.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("parsing work plan %q: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return p, fmt.Errorf("work plan %q: %w", path, err)
	}
	return p, nil
}

// ErrPlanInvalid is returned for a plan that cannot be executed as written.
var ErrPlanInvalid = fmt.Errorf("unattended: work plan is invalid")

// Validate refuses a plan that cannot be executed.
//
// It is strict because a plan is read once, at the start of a run nobody is
// watching. Every ambiguity left in it is resolved hours later by a machine
// with no way to ask.
func (p Plan) Validate() error {
	var problems []string
	// Risk is checked here as well as in ValidateQAPacket so that loading a
	// plan on its own — which is what `preflight -plan` does — already refuses
	// a packet that never classified itself.
	if !p.Risk.Valid() {
		declared := string(p.Risk)
		if strings.TrimSpace(declared) == "" {
			declared = "unset"
		}
		problems = append(problems, fmt.Sprintf("risk %s is not a declared class (%s)",
			quoteOrBare(declared), joinRiskClasses()))
	}
	seen := map[string]bool{}
	for i, t := range p.Tasks {
		switch {
		case strings.TrimSpace(t.ID) == "":
			problems = append(problems, fmt.Sprintf("task[%d] has no id", i))
		case seen[t.ID]:
			problems = append(problems, fmt.Sprintf("task[%d] id %q is duplicated", i, t.ID))
		default:
			seen[t.ID] = true
		}
		if len(t.Argv) == 0 {
			problems = append(problems, fmt.Sprintf("task %q has no argv", t.ID))
		}
		if !t.Band.Valid() {
			problems = append(problems, fmt.Sprintf("task %q band %q is not a declared band", t.ID, string(t.Band)))
		}
	}
	for _, t := range p.Tasks {
		for _, need := range t.Needs {
			if need == t.ID {
				problems = append(problems, fmt.Sprintf("task %q needs itself", t.ID))
				continue
			}
			if !seen[need] {
				problems = append(problems, fmt.Sprintf("task %q needs %q, which is not in this plan", t.ID, need))
			}
		}
	}
	if cycle := findTaskCycle(p.Tasks); cycle != "" {
		problems = append(problems, "task dependencies contain a cycle: "+cycle)
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrPlanInvalid, strings.Join(problems, "; "))
	}
	return nil
}

// findTaskCycle returns a readable cycle if the needs graph has one.
//
// A cycle is caught here rather than at run time because its symptom at run
// time is a queue reporting "nothing ready" and stopping — indistinguishable
// from an exhausted work queue, which is the one legitimate reason to end a run.
func findTaskCycle(tasks []Task) string {
	needs := map[string][]string{}
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		needs[t.ID] = t.Needs
		ids = append(ids, t.ID)
	}
	sort.Strings(ids)

	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var walk func(id string) string
	walk = func(id string) string {
		color[id] = grey
		stack = append(stack, id)
		for _, next := range needs[id] {
			if _, known := needs[next]; !known {
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

// PlanChecks verify the run knows what it is trying to do, and has somewhere to
// go when the primary path is blocked.
func PlanChecks(p Plan) Checks {
	var cs Checks

	if len(p.Tasks) == 0 {
		cs = append(cs, fail("plan.work", CategoryProject, "the run has declared work",
			"at least one task", "none",
			"declare the work in the plan — a run with an empty queue has nothing to converge on"))
		cs = append(cs, notReached("plan.fallback", CategoryProject,
			"useful work exists below the primary band", "the plan declares no work at all"))
		return cs
	}

	counts := map[Band]int{}
	for _, t := range p.Tasks {
		counts[t.Band]++
	}
	var parts []string
	for _, b := range []Band{BandPrimary, BandDependency, BandValidation, BandAssurance, BandEvidence, BandDocumentation, BandNextStage} {
		if counts[b] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", b, counts[b]))
		}
	}
	cs = append(cs, pass("plan.work", CategoryProject, "the run has declared work",
		fmt.Sprintf("%d task(s): %s", len(p.Tasks), strings.Join(parts, " "))))

	fallbacks := len(p.Tasks) - counts[BandPrimary]
	if fallbacks > 0 {
		cs = append(cs, pass("plan.fallback", CategoryProject, "useful work exists below the primary band",
			fmt.Sprintf("%d task(s) below primary", fallbacks)))
	} else {
		cs = append(cs, fail("plan.fallback", CategoryProject, "useful work exists below the primary band",
			"at least one non-primary task", "none",
			"declare validation, assurance, evidence or documentation work, so a blocked primary path does not end the run"))
	}
	return cs
}
