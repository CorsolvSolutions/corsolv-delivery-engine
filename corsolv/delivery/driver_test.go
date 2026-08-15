//go:build integration

// These tests run the driver, which means spawning bash and letting it read
// files. That is a process-owning test by this repository's taxonomy, so it
// carries the integration tag rather than growing the untagged subprocess debt
// baseline — which only ever ratchets down.
//
// The contract they check is still exercised on every pull request: a Go change
// matches the `integration` path filter, and the integration shards run them.

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/handoff"
)

// driverPath is the executable the compiler points every task at.
func driverPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "driver.sh")
}

func bashOrSkip(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the driver runs on the delivery host, which is POSIX")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	return bash
}

func fixtureIntent() handoff.Intent {
	return handoff.Intent{
		SchemaVersion: handoff.SchemaVersion,
		ProjectID:     "driver-contract-test",
		Repository: handoff.Repository{
			Slug:          "CorsolvSolutions/driver-contract-test",
			Origin:        "https://github.com/CorsolvSolutions/driver-contract-test.git",
			DefaultBranch: "main",
		},
		Checkout:   "/tmp/driver-contract-test",
		Objective:  "Prove the compiler and the driver agree on a command line.",
		Lifecycle:  []string{"Build"},
		Acceptance: []handoff.Criterion{{ID: "ac-1", Statement: "The contract holds."}},
		Policy: handoff.Policy{
			NeedPush: true, NeedPR: true, NeedChecks: true, NeedMerge: true,
		},
		RequestedAt: time.Now().UTC(),
	}
}

func fixturePlan() handoff.DeliveryPlan {
	return handoff.DeliveryPlan{
		SchemaVersion: handoff.PlanSchemaVersion,
		ProjectID:     "driver-contract-test",
		PlannedBy:     "test",
		Packages: []handoff.WorkPackage{
			{
				ID: "wp-one", Title: "the first package", Phase: "Build",
				Objective:       "Create src/one.ts.",
				Artifact:        "src/one.ts",
				AuthorizedPaths: []string{"src/one.ts"},
				Gates:           []string{"npm install", "npm run verify"},
				Satisfies:       []string{"ac-1"},
			},
			{
				ID: "wp-two", Title: "the second package", Phase: "Build",
				Objective:       "Create src/two.ts, importing one.",
				Artifact:        "src/two.ts",
				AuthorizedPaths: []string{"src/two.ts"},
				DependsOn:       []string{"wp-one"},
				Satisfies:       []string{"ac-1"},
			},
		},
	}
}

// The contract this test exists for: every command line the compiler emits must
// parse in the driver. The two are written in different languages, in different
// files, and nothing but this test connects them — so a flag renamed on one
// side would otherwise be discovered by an unattended run at 3am.
func TestEveryCompiledCommandLineParsesInTheDriver(t *testing.T) {
	bash := bashOrSkip(t)
	driver := driverPath(t)
	if _, err := os.Stat(driver); err != nil {
		t.Fatalf("the driver must exist beside the command: %v", err)
	}

	host := handoff.HostProfile{
		DeliveryRoot:    t.TempDir(),
		Driver:          driver,
		GitHubCommand:   "/mnt/c/Program Files/GitHub CLI/gh.exe",
		GasCityCommand:  "/home/operator/.local/bin/gc",
		BeadsCommand:    "/home/operator/.local/bin/bd",
		Provider:        "claude",
		ProviderCommand: "/home/operator/.local/bin/claude",
	}
	_, work, err := handoff.Compile(fixtureIntent(), fixturePlan(), host, "run-1")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(work.Tasks) == 0 {
		t.Fatal("nothing compiled")
	}

	// An empty state directory: argument parsing succeeds and the stage then
	// stops on the missing intent. Exit 2 is the driver's "I do not understand
	// this invocation", which is exactly what must never happen.
	state := t.TempDir()

	for _, task := range work.Tasks {
		t.Run(task.ID, func(t *testing.T) {
			args := append([]string(nil), task.Argv[1:]...)
			for i, a := range args {
				if a == host.ProjectDir("driver-contract-test") {
					args[i] = state
				}
			}
			cmd := exec.Command(bash, append([]string{driver}, args...)...)
			cmd.Env = append(os.Environ(), "CORSOLV_ENGINE_REPO="+engineRepo(t))
			out, _ := cmd.CombinedOutput()

			code := cmd.ProcessState.ExitCode()
			if code == 2 {
				t.Fatalf("the driver did not understand the compiled invocation %v:\n%s", args, out)
			}
			if strings.Contains(string(out), "unknown argument") ||
				strings.Contains(string(out), "unknown stage") {
				t.Fatalf("the driver rejected the compiled invocation %v:\n%s", args, out)
			}
		})
	}
}

// The other half of the cross-language contract. The driver reads two files by
// name; the Go layer writes them. Nothing but this connects the two, and the
// symptom of getting it wrong is a run that fails at its very first stage with
// "no delivery intent" — which is exactly what happened before this test
// existed.
func TestTheGoLayerWritesEveryDocumentTheDriverReads(t *testing.T) {
	root := t.TempDir()
	in := fixtureIntent()
	plan := fixturePlan()

	if err := handoff.SaveIntent(root, in); err != nil {
		t.Fatalf("SaveIntent: %v", err)
	}
	if err := handoff.SavePlan(root, plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}

	// The exact paths driver.sh composes from -state.
	stateDir := filepath.Join(root, in.ProjectID)
	for _, name := range []string{"intent.json", "plan.json"} {
		if _, err := os.Stat(filepath.Join(stateDir, name)); err != nil {
			t.Errorf("the driver reads %s from the state directory, and it is not there: %v", name, err)
		}
	}

	bash := bashOrSkip(t)
	cmd := exec.Command(bash, driverPath(t), "dispatch", "-project", in.ProjectID, "-state", stateDir)
	cmd.Env = append(os.Environ(), "CORSOLV_ENGINE_REPO="+engineRepo(t))
	out, _ := cmd.CombinedOutput()

	// It will not get far without a city, but it must get PAST reading its
	// documents — that is the contract under test.
	if strings.Contains(string(out), "no delivery intent") || strings.Contains(string(out), "no delivery plan") {
		t.Fatalf("the driver could not read the documents the Go layer wrote:\n%s", out)
	}
}

// The driver's controller primitives must come from the checkout the driver
// itself is in. They are a matched pair: a fix that touches both arrives half
// applied if a copy of the driver sources its library from somewhere else.
func TestTheDriverSourcesItsLibraryFromItsOwnCheckout(t *testing.T) {
	bash := bashOrSkip(t)
	state := t.TempDir()

	// No CORSOLV_ENGINE_REPO at all, and a working directory that is not the
	// engine: whatever it finds, it finds from its own path.
	cmd := exec.Command(bash, driverPath(t), "dispatch", "-project", "p", "-state", state)
	cmd.Dir = state
	env := os.Environ()
	kept := env[:0]
	for _, e := range env {
		if !strings.HasPrefix(e, "CORSOLV_ENGINE_REPO=") {
			kept = append(kept, e)
		}
	}
	cmd.Env = kept
	out, _ := cmd.CombinedOutput()

	if strings.Contains(string(out), "sa-lib.sh") && strings.Contains(string(out), "No such file") {
		t.Fatalf("the driver could not find its own controller primitives:\n%s", out)
	}
	// It stops on the missing intent, which is proof it got through sourcing.
	if !strings.Contains(string(out), "no delivery intent") {
		t.Fatalf("the driver did not reach its own document check:\n%s", out)
	}
}

func TestDriverRefusesAnUnknownStage(t *testing.T) {
	bash := bashOrSkip(t)
	state := t.TempDir()

	cmd := exec.Command(bash, driverPath(t), "not-a-stage", "-project", "p", "-state", state)
	cmd.Env = append(os.Environ(), "CORSOLV_ENGINE_REPO="+engineRepo(t))
	out, _ := cmd.CombinedOutput()

	if cmd.ProcessState.ExitCode() != 2 {
		t.Fatalf("an unknown stage must exit 2, got %d:\n%s", cmd.ProcessState.ExitCode(), out)
	}
}

func TestDriverRefusesAnIncompleteInvocation(t *testing.T) {
	bash := bashOrSkip(t)

	cases := [][]string{
		{"city-up"},
		{"city-up", "-project", "p"},
		{"city-up", "-state", "/tmp"},
	}
	for _, args := range cases {
		cmd := exec.Command(bash, append([]string{driverPath(t)}, args...)...)
		cmd.Env = append(os.Environ(), "CORSOLV_ENGINE_REPO="+engineRepo(t))
		out, _ := cmd.CombinedOutput()
		if cmd.ProcessState.ExitCode() != 2 {
			t.Errorf("%v must exit 2, got %d:\n%s", args, cmd.ProcessState.ExitCode(), out)
		}
	}
}

// A stage cannot proceed without the documents the Go layer validated. This is
// the fail-closed boundary between the two halves: the driver never invents an
// intent or a plan.
func TestDriverRefusesWithoutValidatedDocuments(t *testing.T) {
	bash := bashOrSkip(t)
	state := t.TempDir()

	cmd := exec.Command(bash, driverPath(t), "dispatch", "-project", "p", "-state", state)
	cmd.Env = append(os.Environ(), "CORSOLV_ENGINE_REPO="+engineRepo(t))
	out, _ := cmd.CombinedOutput()

	if cmd.ProcessState.ExitCode() == 0 {
		t.Fatalf("a stage with no intent must not succeed:\n%s", out)
	}
	if !strings.Contains(string(out), "no delivery intent") {
		t.Fatalf("the refusal must name what is missing, got:\n%s", out)
	}
}

// The two failures the Website Status Checker pilot stopped on, in order.
//
// A run detached into its own process group does not inherit an interactive
// shell's PATH, and both binaries the first stage needs are installed under the
// operator's home rather than in a system directory. So `city-up` cloned the
// working rig and died on `gc: command not found`; with gc named absolutely it
// got one step further and died on `gc rig add: ... bd: not found`, because Gas
// City resolves beads by PATH lookup from a script it shells out to.
//
// Both are the same property, and it is what this proves: the run uses the
// DECLARED binaries. A gc on PATH that would ruin the run is present throughout
// and must lose — for the driver's own `gc init`, for every call the shared
// controller primitives make through sa_gc, and for the lookup a child makes on
// its own behalf.
func TestCityUpUsesTheDeclaredBinariesRatherThanPATH(t *testing.T) {
	r := runCityUpWithStubs(t, "printf 'reconciled 1 city\\n'; exit 0")
	out, sabotage, calls := r.out, r.sabotage, r.calls

	if strings.Contains(string(out), "command not found") {
		t.Fatalf("the driver looked for the Gas City CLI on PATH:\n%s", out)
	}
	if _, err := os.Stat(sabotage); err == nil {
		used, _ := os.ReadFile(sabotage) //nolint:gosec // test artifact
		t.Fatalf("the driver ran the gc on PATH instead of the declared one:\n%s", used)
	}
	recorded, err := os.ReadFile(calls) //nolint:gosec // test artifact
	if err != nil {
		t.Fatalf("the declared Gas City CLI was never invoked: %v\n%s", err, out)
	}
	if !strings.Contains(string(recorded), "init") {
		t.Fatalf("the declared Gas City CLI was not asked to build the city, only: %s", recorded)
	}
	// The city it created is the verdict the driver reads, so the stage must
	// have gone past `gc init` rather than stopping on it.
	if strings.Contains(string(out), "the city was not created") {
		t.Fatalf("the city the declared CLI built was not seen:\n%s", out)
	}

	// And the lookup gc makes on its own behalf has to land on the declared
	// beads binary, resolved through the run's own directory.
	found, err := os.ReadFile(r.seenBeads) //nolint:gosec // test artifact
	if err != nil {
		t.Fatalf("the declared Gas City CLI never reported its beads lookup: %v", err)
	}
	resolved := strings.TrimSpace(string(found))
	if resolved == "" {
		t.Fatalf("Gas City would not have found beads at all — the failure the pilot hit:\n%s", out)
	}
	assertResolvesTo(t, resolved, r.beads, "beads")

	// Same for the agent runtime, which Gas City resolves by the provider's
	// name — so the declared binary has to be exposed under that name.
	found, err = os.ReadFile(r.seenProvider) //nolint:gosec // test artifact
	if err != nil {
		t.Fatalf("the declared Gas City CLI never reported its provider lookup: %v", err)
	}
	resolved = strings.TrimSpace(string(found))
	if resolved == "" {
		t.Fatalf("Gas City would not have found its provider at all — the failure the pilot hit:\n%s", out)
	}
	assertResolvesTo(t, resolved, r.provider, "the agent runtime")
}

// The third failure, and the one that cost the most to see.
//
// A supervisor five days old held the API port with no control socket and no
// children. `gc init` correctly declined to start a second one, the city was
// registered with the first, and the driver — which asked only whether such a
// process existed — declared the city up. Dispatch then routed four packages to
// agents that were never spawned, and the run sat in `await` for its full
// ninety-minute deadline waiting for workers that did not exist.
//
// The verdict has to be the supervisor's own answer, and the failure has to
// name the decision rather than look like a fault to retry: restarting a
// machine-wide process is its owner's call.
func TestCityUpStopsWhenTheSupervisorCannotBeAskedToReconcile(t *testing.T) {
	r := runCityUpWithStubs(t, "printf 'gc supervisor reload: supervisor is not running\\n'; exit 1")

	if r.code == 0 {
		t.Fatalf("city-up reported success over a supervisor that will never start its agents:\n%s", r.out)
	}
	if !strings.Contains(string(r.out), "cannot be asked to reconcile") {
		t.Fatalf("the refusal must name what is wrong, got:\n%s", r.out)
	}
	if !strings.Contains(string(r.out), "decision for its owner") {
		t.Fatalf("the refusal must name whose decision it is, got:\n%s", r.out)
	}
}

// cityUpRun is one execution of the city-up stage against stubbed binaries.
type cityUpRun struct {
	out                     []byte
	code                    int
	sabotage, calls         string
	seenBeads, seenProvider string
	beads, provider         string
}

// runCityUpWithStubs runs the real city-up stage with every binary it reaches
// replaced by a stub, and with a gc on PATH that would ruin the run if used.
//
// supervisorReply is the body the stub runs for `gc supervisor reload`, which is
// the one answer the two callers differ on.
func runCityUpWithStubs(t *testing.T, supervisorReply string) cityUpRun {
	t.Helper()
	bash := bashOrSkip(t)
	for _, tool := range []string{"git", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}

	root := t.TempDir()
	origin := seedOriginRepo(t, root)

	in := fixtureIntent()
	in.Repository.Origin = "file://" + filepath.ToSlash(origin)

	// Written directly rather than through SaveIntent: a real intent's origin is
	// a forge URL, and this one has to be clonable without a network.
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(state, "intent.json"), in)
	writeJSONFile(t, filepath.Join(state, "plan.json"), fixturePlan())

	// The gc the run must NOT use. It is first on PATH and leaves a mark, so
	// "the declared one was used" is proved by an artifact rather than inferred
	// from the absence of an error.
	r := cityUpRun{
		sabotage:     filepath.Join(root, "path-gc-was-used"),
		calls:        filepath.Join(root, "declared-gc-calls"),
		seenBeads:    filepath.Join(root, "beads-gc-would-find"),
		seenProvider: filepath.Join(root, "provider-gc-would-find"),
		beads:        filepath.Join(root, "declared-bd-under-another-name"),
		provider:     filepath.Join(root, "declared-provider-under-another-name"),
	}
	pathBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(pathBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(pathBin, "gc"),
		"#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> "+shquote(r.sabotage)+"\nexit 1\n")

	// The gc the host declares. It answers `init` by creating the city, which is
	// the verdict the driver reads, records every call it received, and reports
	// which beads binary and which agent runtime its own PATH lookup would find
	// — the thing gc really does and the driver cannot pass on a command line.
	declared := filepath.Join(root, "declared-gc")
	writeScript(t, declared,
		"#!/usr/bin/env bash\n"+
			"printf '%s\\n' \"$*\" >> "+shquote(r.calls)+"\n"+
			"command -v bd > "+shquote(r.seenBeads)+" 2>&1 || true\n"+
			"command -v claude > "+shquote(r.seenProvider)+" 2>&1 || true\n"+
			"if [ \"${1:-}\" = init ]; then mkdir -p \"$2\" && printf 'name = \"test\"\\n' > \"$2/city.toml\"; exit 0; fi\n"+
			"if [ \"${1:-}\" = supervisor ]; then "+supervisorReply+"; fi\n"+
			"if [ \"${3:-}\" = import ]; then exit 0; fi\n"+
			"exit 1\n")

	// Neither the beads binary nor the agent runtime is invoked by the driver;
	// they only have to be findable, under paths nothing would stumble on by
	// accident.
	writeScript(t, r.beads, "#!/usr/bin/env bash\nexit 0\n")
	writeScript(t, r.provider, "#!/usr/bin/env bash\nexit 0\n")

	forge := filepath.Join(root, "gh")
	writeScript(t, forge, "#!/usr/bin/env bash\nprintf 'x\\n'\nexit 0\n")

	cmd := exec.Command(bash, driverPath(t), "city-up",
		"-project", in.ProjectID, "-state", state,
		"-gh", forge, "-gc", declared, "-bd", r.beads,
		"-provider", "claude", "-provider-bin", r.provider)
	cmd.Env = append(os.Environ(),
		"CORSOLV_ENGINE_REPO="+engineRepo(t),
		"PATH="+pathBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	r.out, _ = cmd.CombinedOutput()
	r.code = cmd.ProcessState.ExitCode()
	return r
}

// assertResolvesTo compares two paths after following symlinks, which is how
// the run exposes a declared binary under the name its consumer looks up.
func assertResolvesTo(t *testing.T, got, want, what string) {
	t.Helper()
	gotReal, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("resolving %q: %v", got, err)
	}
	wantReal, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	if gotReal != wantReal {
		t.Fatalf("Gas City would find %s at %q, not the declared %q", what, gotReal, wantReal)
	}
}

// seedOriginRepo makes a bare repository with one commit, so the driver's clone
// is a real clone with nothing to authenticate against.
func seedOriginRepo(t *testing.T, root string) string {
	t.Helper()
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "seed")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run(root, "init", "--bare", "--initial-branch=main", origin)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	run(root, "init", "--initial-branch=main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("seed\n"), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	run(work, "add", "README.md")
	run(work, "commit", "-m", "seed")
	run(work, "remote", "add", "origin", origin)
	run(work, "push", "-q", "origin", "main")
	return origin
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil { //nolint:gosec // an executable stub is the point
		t.Fatal(err)
	}
}

// shquote renders a path as a single-quoted shell word for the stubs above.
func shquote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// engineRepo is this checkout, which the driver sources its proven controller
// primitives from.
func engineRepo(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// corsolv/delivery -> the repository root
	return filepath.Dir(filepath.Dir(wd))
}
