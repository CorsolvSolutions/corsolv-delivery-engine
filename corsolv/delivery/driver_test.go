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
		DeliveryRoot: t.TempDir(),
		Driver:       driver,
		Provider:     "claude",
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
