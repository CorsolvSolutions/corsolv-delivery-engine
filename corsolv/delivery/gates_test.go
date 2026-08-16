//go:build integration

// These run the real driver against a real git repository, which makes them
// process-owning by this repository's taxonomy — the same reason driver_test.go
// carries the integration tag.

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The defect these exist for, in the worker's own words:
//
//	"Bash in this worker session is deny-by-default and every npm invocation is
//	 denied (even 'npm --version'), as is 'node -e'."
//
// The scaffold package told its worker to prove itself with `npm install && npm
// run verify`. The worker wrote all eight authorised files and then closed
// `blocked`, because it was structurally forbidden from running either command.
// That is the honest outcome of an impossible instruction, and it is why a
// package now declares the gates it will be permitted to run.
//
// The behaviour of the grant itself was established directly against the agent
// runtime before this was written: with the launch allowlist alone, `npm run
// verify` is refused with "Permission to use Bash has been denied because
// Claude Code is running in don't ask mode"; with the same launch flags and the
// worktree-local allow list this test asserts, the command executes. What is
// asserted here is that the driver installs exactly that, for exactly the
// declared gates, in exactly the worker's own tree.

// grantedGates reads the permission grant the driver installed in a worktree.
func grantedGates(t *testing.T, worktree string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(worktree, ".claude", "settings.local.json")) //nolint:gosec // test artifact
	if err != nil {
		t.Fatalf("no permission grant in the worker's worktree: %v", err)
	}
	var doc struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the grant is not valid JSON (%v): %s", err, raw)
	}
	return doc.Permissions.Allow
}

// runDispatchWithGates runs the real dispatch stage over a plan whose first
// package declares gates, and returns the worktree that package's worker gets.
func runDispatchWithGates(t *testing.T, gates []string) (worktree string, out []byte) {
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

	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := fixturePlan()
	plan.Packages[0].Gates = gates
	writeJSONFile(t, filepath.Join(state, "intent.json"), in)
	writeJSONFile(t, filepath.Join(state, "plan.json"), plan)

	// A city and rig the dispatch stage can use without a live Gas City: the
	// stage's bead work is stubbed, and what is under test is the grant it
	// installs beside the worktree it cuts.
	city := filepath.Join(state, "city")
	rig := filepath.Join(state, "rig")
	if err := os.MkdirAll(filepath.Join(city, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "clone", "-q", origin, rig).CombinedOutput(); err != nil {
		t.Fatalf("cloning the rig: %v\n%s", err, out)
	}
	base, err := exec.Command("git", "-C", rig, "rev-parse", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	runtime := map[string]string{
		"city":    city,
		"rigPath": rig,
		"rigName": "rig-test",
		"runTag":  "20260815T000000Z",
		"baseSha": strings.TrimSpace(string(base)),
	}
	writeJSONFile(t, filepath.Join(state, "runtime.json"), runtime)

	// Every gc call the stage makes is answered by a stub; none of them is what
	// this test is about, and a real city would make it a fleet test.
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stubGC := filepath.Join(bin, "gc")
	writeScript(t, stubGC, "#!/usr/bin/env bash\n"+
		"for a in \"$@\"; do case \"$a\" in create) printf 'b-stub\\n'; exit 0;; esac; done\n"+
		"exit 0\n")

	cmd := exec.Command(bash, driverPath(t), "dispatch",
		"-project", in.ProjectID, "-state", state, "-gc", stubGC)
	cmd.Env = append(os.Environ(),
		"CORSOLV_ENGINE_REPO="+engineRepo(t),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, _ = cmd.CombinedOutput()

	return filepath.Join(city, ".gc", "worktrees", "rig-test", "worker-wp-one"), out
}

// A declared npm gate — install and the project's own verification — is granted.
func TestDeclaredGatesAreGrantedToTheWorker(t *testing.T) {
	wt, out := runDispatchWithGates(t, []string{"npm install", "npm run verify"})
	allow := grantedGates(t, wt)

	for _, want := range []string{"Bash(npm install:*)", "Bash(npm run verify:*)"} {
		var found bool
		for _, got := range allow {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the worker was not granted its declared gate %s; got %v\n%s", want, allow, out)
		}
	}
}

// Everything the package did not declare stays denied: the grant contains the
// declared gates and nothing else at all.
func TestUndeclaredCommandsAreNotGranted(t *testing.T) {
	wt, _ := runDispatchWithGates(t, []string{"npm run verify"})
	allow := grantedGates(t, wt)

	if len(allow) != 1 || allow[0] != "Bash(npm run verify:*)" {
		t.Fatalf("the grant must be exactly the declared gate, got %v", allow)
	}
	for _, never := range []string{
		"Bash(npm install:*)", "Bash(npm:*)", "Bash(git:*)", "Bash(gh:*)",
		"Bash(bash:*)", "Bash(sh:*)", "Bash(npx:*)", "Bash(:*)", "Bash",
	} {
		for _, got := range allow {
			if got == never {
				t.Errorf("the grant carries %q, which no package declared", never)
			}
		}
	}
}

// The grant is scoped to the worker's own tree. One package's gates are not
// another's, and nothing outside the bounded project is widened.
func TestTheGrantIsScopedToTheWorkersOwnWorktree(t *testing.T) {
	wt, _ := runDispatchWithGates(t, []string{"npm run verify"})

	if _, err := os.Stat(filepath.Join(wt, ".claude", "settings.local.json")); err != nil {
		t.Fatalf("the grant must live in the worker's worktree: %v", err)
	}
	// wp-two declared no gates and has no worktree yet; when it gets one, it
	// must not inherit wp-one's grant.
	sibling := filepath.Join(filepath.Dir(wt), "worker-wp-two")
	if _, err := os.Stat(filepath.Join(sibling, ".claude", "settings.local.json")); err == nil {
		allow := grantedGates(t, sibling)
		for _, got := range allow {
			if strings.Contains(got, "verify") {
				t.Errorf("wp-two inherited wp-one's gate %q", got)
			}
		}
	}
	// And nothing was written above the worktree.
	for _, above := range []string{filepath.Dir(filepath.Dir(wt)), filepath.Dir(wt)} {
		if _, err := os.Stat(filepath.Join(above, ".claude", "settings.local.json")); err == nil {
			t.Errorf("a permission grant was installed outside a worktree, at %s", above)
		}
	}
}

// A package that declares no gates is granted none — the absence of a
// declaration is not an invitation.
func TestAPackageWithNoGatesIsGrantedNothing(t *testing.T) {
	wt, _ := runDispatchWithGates(t, nil)
	if allow := grantedGates(t, wt); len(allow) != 0 {
		t.Fatalf("a package declaring no gates was granted %v", allow)
	}
}
