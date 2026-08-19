package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Planner turns a business brief into bounded work packages.
//
// It is an interface for one reason, and it is not testability. Deciding what
// work a brief implies is a judgement, and this fork's standing rule is that
// judgement lives in a prompt rather than in Go. The interface is the seam
// where Go stops deciding: everything above it validates and executes, and the
// single implementation below asks a model.
type Planner interface {
	// Plan returns a delivery plan for the intent, or an error.
	Plan(ctx context.Context, in Intent) (DeliveryPlan, error)
}

// ErrPlannerFailed is returned when planning could not produce a usable plan.
var ErrPlannerFailed = errors.New("handoff: planning failed")

// PlanPrompt renders the instruction the planning agent is given.
//
// It is a function rather than a template file because its content is a
// contract with the validator in plan.go: every rule stated here has a matching
// refusal there. Splitting them across a file nobody reads next to the code
// that enforces them is how the two drift apart, and a plan that violates an
// unstated rule fails at validation with no explanation the agent can act on.
func PlanPrompt(in Intent) string {
	var b strings.Builder

	b.WriteString("You are planning the delivery of a software project.\n\n")
	b.WriteString("Return ONE JSON object and nothing else. No prose, no markdown fence.\n\n")

	b.WriteString("PROJECT\n")
	fmt.Fprintf(&b, "  id:         %s\n", in.ProjectID)
	fmt.Fprintf(&b, "  repository: %s (default branch %s)\n", in.Repository.Slug, in.Repository.DefaultBranch)
	fmt.Fprintf(&b, "  lifecycle:  %s\n", strings.Join(in.Lifecycle, ", "))
	b.WriteString("\nOBJECTIVE\n  ")
	b.WriteString(strings.ReplaceAll(in.Objective, "\n", "\n  "))
	b.WriteString("\n\nACCEPTANCE CRITERIA\n")
	for _, c := range in.Acceptance {
		if c.IsHuman() {
			fmt.Fprintf(&b, "  %s: %s [ACCEPTED BY A PERSON - do not claim it]\n", c.ID, c.Statement)
			continue
		}
		fmt.Fprintf(&b, "  %s: %s\n", c.ID, c.Statement)
	}

	b.WriteString(`
OUTPUT SHAPE

{
  "schemaVersion": 1,
  "projectId": "<exactly the project id above>",
  "plannedBy": "planner",
  "plannedAt": "<RFC3339 UTC timestamp>",
  "packages": [
    {
      "id": "wp-<short-kebab-name>",
      "title": "<one line>",
      "phase": "<one of the lifecycle phases above, verbatim>",
      "objective": "<what a coding agent must do, in full sentences>",
      "artifact": "<the one repository-relative file this package must produce>",
      "authorizedPaths": ["<every repository-relative path this package may create or change>"],
      "gates": ["<the exact commands the worker must run to verify this package>"],
      "dependsOn": ["<ids of packages that must be MERGED first>"],
      "satisfies": ["<acceptance criterion ids this package delivers>"]
    }
  ]
}

RULES — a plan breaking any of these is rejected and you will be asked again.

 1. Every acceptance criterion above must be satisfied by at least one package,
    EXCEPT one marked "ACCEPTED BY A PERSON". A criterion nothing addresses
    means the project can never complete.
 1a. A criterion marked "ACCEPTED BY A PERSON" must appear in NO package's
    satisfies list. A package may produce the record that person reads - a
    release note, a sample output, a sign-off sheet left blank - and may
    never claim their answer. Listing one is rejected.
 2. authorizedPaths must list EVERY file the package will create or change,
    including its tests. A worker may not touch anything outside its list, so a
    missing path means the work is thrown away at publication.
 3. artifact must be one of that package's own authorizedPaths.
 4. No two packages may authorize the same path. Two workers writing one file is
    a collision, not a plan. Split the work so each file has one owner.
 5. Paths are repository-relative. No leading slash, no "..", no drive letters.
 6. You may not authorize anything under .git/, .github/workflows/ or
    delivery/gascity/. A package that could edit the workflow judging it could
    make its own gate pass.
 7. dependsOn is for work that genuinely cannot start until another package has
    MERGED — for example, code that imports a module another package creates.
    Do not serialize work that could run in parallel. There must be no cycles.
 8. Prefer few, genuinely independent packages. Each one costs a worker, a
    branch, a pull request and a CI run.
 9. objective is read by a coding agent with no other context. State what to
    create, what it must export or contain, and how it will be verified.
10. Every id matches ^[a-z0-9][a-z0-9-]{1,63}$.
11. gates are the ONLY commands the worker will be permitted to run, so any
    verification you state in the objective must appear here or the worker
    cannot perform it. List the real commands, one per entry, exactly as they
    would be typed — for example "npm install" then "npm run verify". Each must
    begin with a project build or test runner (npm, pnpm, yarn, node, go, make,
    cargo, mvn, gradle, python, python3, pytest, ruff, tsc, eslint, vitest,
    jest, dotnet). No shell syntax: no &&, |, ;, quotes, redirects or
    substitutions, and no paths — one command per entry. git, gh, npx, curl,
    bash and sudo are refused: publication is the controller's authority, never
    the worker's. At most 8 per package.
`)
	return b.String()
}

// ExtractPlanJSON pulls the JSON object out of a model's reply.
//
// Models wrap JSON in prose or a fence even when told not to, and failing the
// whole run over a stray "Here you go:" would be brittle in the one place that
// most needs to survive contact with a language model. Anything that is not a
// single balanced object is still refused: this recovers a well-formed answer
// from noisy packaging, it does not repair a malformed one.
func ExtractPlanJSON(reply string) ([]byte, error) {
	s := strings.TrimSpace(reply)
	if s == "" {
		return nil, fmt.Errorf("%w: the planner returned nothing", ErrPlannerFailed)
	}

	// Strip a fenced block if present, keeping only its contents.
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			s = strings.TrimSpace(rest[:end])
		}
	}

	start := strings.IndexByte(s, '{')
	if start < 0 {
		return nil, fmt.Errorf("%w: the planner's reply contains no JSON object", ErrPlannerFailed)
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
			// Braces inside a string are not structure.
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return []byte(s[start : i+1]), nil
			}
		}
	}
	return nil, fmt.Errorf("%w: the planner's JSON object is not balanced", ErrPlannerFailed)
}

// AgentPlanner asks a coding agent to produce the plan.
type AgentPlanner struct {
	// Command is the agent CLI, e.g. "claude".
	Command string
	// Args are the arguments before the prompt. The prompt is appended as a
	// single final argument, never interpolated into a shell string.
	Args []string
	// Timeout bounds the call. Zero uses a default.
	Timeout time.Duration
	// Attempts is how many times a rejected plan is sent back. Zero uses a
	// default of two: a model that has been shown its own validation error is
	// usually right the second time, and a third try rarely differs.
	Attempts int
}

// Plan asks the agent for a plan and refuses anything that does not validate.
//
// A rejected plan is fed back with the validator's own words rather than a
// generic retry, because the validator's message names the exact rule broken
// and the agent can act on it. That is the whole retry strategy — there is no
// repair logic here, because Go repairing a plan would be Go deciding what the
// work is.
func (p AgentPlanner) Plan(ctx context.Context, in Intent) (DeliveryPlan, error) {
	if strings.TrimSpace(p.Command) == "" {
		return DeliveryPlan{}, fmt.Errorf("%w: no planning agent is configured", ErrPlannerFailed)
	}
	attempts := p.Attempts
	if attempts <= 0 {
		attempts = 2
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	prompt := PlanPrompt(in)
	var lastErr error

	for attempt := 1; attempt <= attempts; attempt++ {
		reply, err := p.ask(ctx, prompt, timeout)
		if err != nil {
			lastErr = err
			// A transport failure is not something the agent can be told about
			// usefully, so the prompt is unchanged for the retry.
			continue
		}

		raw, err := ExtractPlanJSON(reply)
		if err != nil {
			lastErr = err
			prompt = PlanPrompt(in) + "\nYour previous reply was rejected: " + err.Error() +
				"\nReturn ONE JSON object and nothing else.\n"
			continue
		}

		plan, err := DecodePlan(raw, in)
		if err != nil {
			lastErr = err
			prompt = PlanPrompt(in) + "\nYour previous plan was rejected by the validator:\n  " +
				err.Error() + "\nCorrect exactly that and return the whole JSON object again.\n"
			continue
		}
		if strings.TrimSpace(plan.PlannedBy) == "" {
			plan.PlannedBy = "agent:" + p.Command
		}
		if plan.PlannedAt.IsZero() {
			plan.PlannedAt = time.Now().UTC()
		}
		return plan, nil
	}

	return DeliveryPlan{}, fmt.Errorf("%w after %d attempt(s): %w", ErrPlannerFailed, attempts, lastErr)
}

// ask runs the agent once and returns its reply.
func (p AgentPlanner) ask(ctx context.Context, prompt string, timeout time.Duration) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The prompt is one argv element. It is never spliced into a shell string,
	// so nothing a portal wrote into the objective can become a command.
	args := append(append([]string(nil), p.Args...), prompt)
	cmd := exec.CommandContext(callCtx, p.Command, args...) //nolint:gosec // the command comes from the host profile

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Both streams, because agent CLIs report the problems a PERSON has
			// to act on — an expired login, an exhausted spend limit — on
			// stdout, and reporting only stderr turns those into a bare
			// "exited 1" that names nothing anyone can fix.
			return "", fmt.Errorf("%w: the planning agent exited %d: %s",
				ErrPlannerFailed, exitErr.ExitCode(),
				firstMeaningfulLine(string(out), string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("%w: running the planning agent: %w", ErrPlannerFailed, err)
	}
	return string(out), nil
}

// firstMeaningfulLine picks the most useful line the agent produced, preferring
// stderr and falling back to stdout.
func firstMeaningfulLine(stdout, stderr string) string {
	for _, stream := range []string{stderr, stdout} {
		for _, line := range strings.Split(stream, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				return trimmed
			}
		}
	}
	return "it produced no output at all"
}

// StaticPlanner replays a plan that was decided elsewhere.
//
// It exists for the case where a person has already written the plan and wants
// it executed verbatim. The plan still goes through the same validator, so
// nothing here is a way around the containment rules.
type StaticPlanner struct{ Raw []byte }

// Plan validates and returns the static plan.
func (s StaticPlanner) Plan(_ context.Context, in Intent) (DeliveryPlan, error) {
	plan, err := DecodePlan(s.Raw, in)
	if err != nil {
		return DeliveryPlan{}, fmt.Errorf("%w: %w", ErrPlannerFailed, err)
	}
	return plan, nil
}

// EnsurePlan returns the delivery's plan, producing one if it does not exist.
//
// Planning is done once and kept. A second run must execute the plan the first
// one was interrupted partway through, not a freshly imagined one — the work
// already merged was merged against the old plan, and re-planning would leave
// the run reconciling two.
func EnsurePlan(ctx context.Context, deliveryRoot string, in Intent, planner Planner) (DeliveryPlan, bool, error) {
	if existing, found, err := LoadPlan(deliveryRoot, in); err != nil {
		return DeliveryPlan{}, false, err
	} else if found {
		return existing, false, nil
	}

	plan, err := planner.Plan(ctx, in)
	if err != nil {
		return DeliveryPlan{}, false, err
	}
	if err := SavePlan(deliveryRoot, plan); err != nil {
		return DeliveryPlan{}, false, err
	}
	return plan, true, nil
}

// MarshalPlan renders a plan as the JSON a portal reads.
func MarshalPlan(p DeliveryPlan) ([]byte, error) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding delivery plan: %w", err)
	}
	return data, nil
}
