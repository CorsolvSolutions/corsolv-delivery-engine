package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakePlanner replays canned replies in order, recording the prompts it saw.
type fakePlanner struct {
	replies []string
	calls   int
}

func (f *fakePlanner) Plan(_ context.Context, in Intent) (DeliveryPlan, error) {
	f.calls++
	if f.calls > len(f.replies) {
		return DeliveryPlan{}, errors.New("no more replies")
	}
	raw, err := ExtractPlanJSON(f.replies[f.calls-1])
	if err != nil {
		return DeliveryPlan{}, err
	}
	return DecodePlan(raw, in)
}

func planJSON(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(validPlan())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPlanPromptStatesEveryRuleTheValidatorEnforces(t *testing.T) {
	prompt := PlanPrompt(planIntent())

	// Each phrase below corresponds to a refusal in plan.go. A rule the
	// validator enforces and the prompt never states is a rule the agent is
	// failed for breaking without being told.
	for _, want := range []string{
		"authorizedPaths",
		"artifact must be one of",
		"No two packages may authorize the same path",
		".github/workflows",
		"must be satisfied by at least one package",
		"no cycles",
		"repository-relative",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the planning prompt must state %q", want)
		}
	}

	// The intent's own content must reach the agent, or it is planning blind.
	if !strings.Contains(prompt, planIntent().Objective) {
		t.Error("the prompt must carry the objective")
	}
	for _, c := range planIntent().Acceptance {
		if !strings.Contains(prompt, c.Statement) {
			t.Errorf("the prompt must carry acceptance criterion %q", c.ID)
		}
	}
	for _, phase := range planIntent().Lifecycle {
		if !strings.Contains(prompt, phase) {
			t.Errorf("the prompt must carry lifecycle phase %q", phase)
		}
	}
}

func TestExtractPlanJSON(t *testing.T) {
	body := `{"a":1,"b":{"c":2}}`

	cases := []struct {
		name  string
		reply string
		want  string
		ok    bool
	}{
		{"bare object", body, body, true},
		{"fenced", "```json\n" + body + "\n```", body, true},
		{"fenced with no language", "```\n" + body + "\n```", body, true},
		{"with a preamble", "Here you go:\n" + body, body, true},
		{"with a trailing note", body + "\n\nLet me know if you want changes.", body, true},
		{"braces inside strings", `{"note":"a } and a { inside"}`, `{"note":"a } and a { inside"}`, true},
		{"escaped quote before a brace", `{"note":"say \" then }"}`, `{"note":"say \" then }"}`, true},
		{"no object at all", "I cannot do that.", "", false},
		{"unbalanced", `{"a":1`, "", false},
		{"empty", "   ", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractPlanJSON(tc.reply)
			if tc.ok {
				if err != nil {
					t.Fatalf("expected extraction to succeed, got: %v", err)
				}
				if string(got) != tc.want {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected a refusal, got %q", got)
			}
			if !errors.Is(err, ErrPlannerFailed) {
				t.Fatalf("expected ErrPlannerFailed, got: %v", err)
			}
		})
	}
}

// A plan a model produced still goes through the full validator. This is the
// case that matters: the model wrote something structurally plausible and
// unsafe.
func TestAPlannedPlanIsStillValidated(t *testing.T) {
	unsafe := validPlan()
	unsafe.Packages[0].AuthorizedPaths = []string{"src/add.ts", ".github/workflows/ci.yml"}
	raw, err := json.Marshal(unsafe)
	if err != nil {
		t.Fatal(err)
	}

	planner := &fakePlanner{replies: []string{string(raw)}}
	if _, err := planner.Plan(context.Background(), planIntent()); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("a model-authored plan must face the validator, got: %v", err)
	}
}

func TestStaticPlannerValidates(t *testing.T) {
	good := StaticPlanner{Raw: []byte(planJSON(t))}
	if _, err := good.Plan(context.Background(), planIntent()); err != nil {
		t.Fatalf("a valid static plan must be accepted, got: %v", err)
	}

	bad := StaticPlanner{Raw: []byte(`{"schemaVersion":1,"projectId":"wrong","packages":[]}`)}
	if _, err := bad.Plan(context.Background(), planIntent()); !errors.Is(err, ErrPlannerFailed) {
		t.Fatalf("an invalid static plan must be refused, got: %v", err)
	}
}

func TestEnsurePlanPlansOnceAndKeepsIt(t *testing.T) {
	root := t.TempDir()
	in := planIntent()
	planner := &fakePlanner{replies: []string{planJSON(t), planJSON(t)}}

	first, created, err := EnsurePlan(context.Background(), root, in, planner)
	if err != nil {
		t.Fatalf("first EnsurePlan: %v", err)
	}
	if !created {
		t.Fatal("the first call must produce a plan")
	}
	if planner.calls != 1 {
		t.Fatalf("the planner was called %d times", planner.calls)
	}

	second, created, err := EnsurePlan(context.Background(), root, in, planner)
	if err != nil {
		t.Fatalf("second EnsurePlan: %v", err)
	}
	if created {
		t.Fatal("a delivery must not be re-planned once it has a plan")
	}
	if planner.calls != 1 {
		t.Fatalf("the planner was called again: %d calls", planner.calls)
	}
	if len(first.Packages) != len(second.Packages) {
		t.Fatal("the reloaded plan must be the one that was saved")
	}
}

// Re-planning after an interruption would leave the run reconciling work
// already merged against a plan that no longer describes it.
func TestEnsurePlanDoesNotReplanAfterAnInterruption(t *testing.T) {
	root := t.TempDir()
	in := planIntent()

	original := validPlan()
	if err := SavePlan(root, original); err != nil {
		t.Fatal(err)
	}

	// A planner that would return something different if it were ever asked.
	different := validPlan()
	different.Packages = different.Packages[:1]
	different.Packages[0].Satisfies = []string{"ac-1", "ac-2"}
	raw, err := json.Marshal(different)
	if err != nil {
		t.Fatal(err)
	}
	planner := &fakePlanner{replies: []string{string(raw)}}

	got, created, err := EnsurePlan(context.Background(), root, in, planner)
	if err != nil {
		t.Fatal(err)
	}
	if created || planner.calls != 0 {
		t.Fatal("a delivery resuming after an interruption must reuse its existing plan")
	}
	if len(got.Packages) != 2 {
		t.Fatalf("expected the original two-package plan, got %d", len(got.Packages))
	}
}

// An agent CLI reports the problems a person has to act on — an expired login,
// an exhausted spend limit — on stdout. Reporting only stderr turns those into
// a bare "exited 1" that names nothing anyone can fix.
func TestFailureReportingPrefersWhateverTheAgentActuallySaid(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		stderr string
		want   string
	}{
		{"stderr wins", "some stdout", "the real error", "the real error"},
		{"stdout when stderr is empty", "You've hit your monthly spend limit", "", "You've hit your monthly spend limit"},
		{"stdout when stderr is only whitespace", "login expired", "  \n \n", "login expired"},
		{"leading blank lines are skipped", "\n\n  useful line\n", "", "useful line"},
		{"nothing at all is said plainly", "", "", "it produced no output at all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstMeaningfulLine(tc.stdout, tc.stderr); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgentPlannerNeedsACommand(t *testing.T) {
	if _, err := (AgentPlanner{}).Plan(context.Background(), planIntent()); !errors.Is(err, ErrPlannerFailed) {
		t.Fatalf("a planner with no agent must fail closed, got: %v", err)
	}
}

// The prompt goes to the agent as a single argument. Nothing a portal wrote can
// become a command, even though the objective is free text.
func TestPlanPromptIsNeverInterpolatedIntoAShell(t *testing.T) {
	in := planIntent()
	in.Objective = "`rm -rf /` ; $(reboot) && echo pwned"

	p := AgentPlanner{Command: "claude", Args: []string{"-p"}}
	args := append(append([]string(nil), p.Args...), PlanPrompt(in))

	if len(args) != 2 {
		t.Fatalf("the prompt must be exactly one argument, got %d", len(args))
	}
	if args[0] != "-p" {
		t.Fatalf("args[0] = %q", args[0])
	}
	if !strings.Contains(args[1], "rm -rf") {
		t.Fatal("the objective must reach the prompt verbatim")
	}
}

func TestMarshalPlanRoundTrips(t *testing.T) {
	data, err := MarshalPlan(validPlan())
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodePlan(data, planIntent())
	if err != nil {
		t.Fatalf("a marshaled plan must decode: %v", err)
	}
	if len(back.Packages) != 2 {
		t.Fatalf("got %d packages", len(back.Packages))
	}
}

// THE DEFECT THIS EXISTS FOR.
//
// `Criterion.MustCover` made a criterion's required behaviors load-bearing:
// `DeliveryPlan.Validate` refuses a plan whose covering work does not carry
// them, and refuses one that declares no gate to prove them. Both refusals are
// right. Neither was ever stated to the planner.
//
// So the agent was handed the criterion's prose, wrote a plan against it, and
// was rejected against a rubric it had not been shown — with two attempts in
// total, one of them already spent. That is the exact failure the prompt test
// beside this one was written to prevent: a rule the validator enforces and the
// prompt never states is a rule the agent is failed for breaking without being
// told.
//
// The first pilot to declare required behaviors would have discovered this by
// burning its planning attempts on it.
func TestPlanPromptCarriesTheBehaviorsTheValidatorDemands(t *testing.T) {
	in := planIntent()
	in.Acceptance[0].MustCover = []string{"integer", "decimal|number", "mixed"}
	prompt := PlanPrompt(in)

	// The behaviors themselves, in the author's own words — including the
	// alternatives, because "decimal|number" says those two spellings are one
	// requirement and a planner shown only "decimal" would not know it may
	// write "number".
	for _, want := range []string{"integer", "decimal|number", "mixed"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt must carry required behavior %q; the validator refuses a plan that drops it", want)
		}
	}

	// Attached to the criterion that requires them. A list of behaviors the
	// planner cannot tie to a criterion cannot be planned against.
	idx := strings.Index(prompt, in.Acceptance[0].ID)
	behavior := strings.Index(prompt, "decimal|number")
	if idx < 0 || behavior < 0 || behavior < idx {
		t.Fatalf("required behaviors must appear with their own criterion, got prompt:\n%s", prompt)
	}
	if other := strings.Index(prompt, in.Acceptance[1].ID); other >= 0 && behavior > other {
		t.Errorf("required behaviors for %s appeared after %s, so they read as that criterion's",
			in.Acceptance[0].ID, in.Acceptance[1].ID)
	}

	// And the rule itself, stated as a rule, because carrying the words without
	// saying what is done with them invites a planner to treat them as prose.
	for _, want := range []string{"MUST COVER", "gate"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt must state the coverage rule (%q)", want)
		}
	}
}

// A criterion that declares no required behavior must read exactly as it always
// did. Every project written before this field existed is that project, and a
// prompt that grew an empty "MUST COVER" section for each of them would spend
// the agent's attention on nothing.
func TestPlanPromptIsUnchangedForCriteriaWithNoRequiredBehavior(t *testing.T) {
	prompt := PlanPrompt(planIntent())
	if strings.Contains(prompt, "MUST COVER") {
		t.Errorf("a criterion with no required behavior must not produce a MUST COVER line:\n%s", prompt)
	}
}

// A criterion reserved to a person is not delivery's to cover, and the validator
// skips its required behaviors entirely. Showing them to the planner would ask
// for work on a criterion the very next line forbids it to claim.
func TestPlanPromptDoesNotDemandBehaviorsForAPersonsCriterion(t *testing.T) {
	in := planIntent()
	in.Acceptance[1].AcceptedBy = AcceptedByHuman
	in.Acceptance[1].MustCover = []string{"a signed release note"}
	prompt := PlanPrompt(in)

	if strings.Contains(prompt, "a signed release note") {
		t.Errorf("a person's criterion must not carry required behaviors into the plan prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "ACCEPTED BY A PERSON") {
		t.Error("the prompt must still mark the criterion as a person's")
	}
}
