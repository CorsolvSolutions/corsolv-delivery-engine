//go:build integration

// How the controller runs a PROJECT's own commands.
//
// Three defects, all found by one live pilot delivery and all inside the single
// mechanism publication rests on — the controller's independent re-run of the
// gates a work package declared.
//
//	A  dispatch routed a package whose upstreams had not merged. A routed bead
//	   is Gas City's to act on, so when the upstream merged Gas City cut a
//	   worktree OF ITS OWN and woke a worker in it before the controller cut
//	   one. The controller's preparation — where a package's gate grant is
//	   installed and its dependencies are installed — only ever ran on a tree
//	   the controller itself created. That worker was left deny-by-default,
//	   told to verify itself, could run neither `npm` nor `node`, and closed
//	   `blocked` having written two of its four files and proved nothing.
//
//	B  the gate loop read its gates one at a time from a here-string, so the
//	   first gate that touched stdin drained the list feeding it. Every package
//	   in that pilot was published after the controller re-ran exactly ONE of
//	   its declared gates, while the run's own account said it ran them all.
//
//	C  a project's commands were resolved on the run's inherited PATH. In the
//	   environment a detached run really inherits on the pilot host, `npm` is
//	   the WINDOWS npm — reaching a Linux worktree through a `\\wsl.localhost\`
//	   UNC path — and `node` does not resolve at all. The controller's
//	   verification was performed by a foreign toolchain, and reported removing
//	   130 packages from a tree it had just been given.
//
// Like the other driver tests these spawn bash and git, so they carry the
// integration tag. Every path they touch is inside the test's own temp
// directory.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/handoff"
)

// DEFECT A. A package whose upstreams have not merged has no worktree, and a
// worker routed to a directory that does not exist is a worker the controller
// never prepared. Dispatch routes what it gave a tree to, and nothing else.
func TestDispatchRoutesOnlyThePackagesItGaveAWorktree(t *testing.T) {
	e := newRecoveryEnv(t)
	base := e.initRig()
	e.seedRuntime(map[string]string{"baseSha": base})

	code, out := e.runStage("dispatch", nil)
	if code != 0 {
		t.Fatalf("dispatch must succeed, got %d:\n%s", code, out)
	}

	calls := e.gcCalls()
	if got := slingsFor(calls, "wp-one"); got != 1 {
		t.Errorf("the package that was given a worktree must be routed exactly once, got %d\ncalls: %v\n%s",
			got, calls, out)
	}
	if got := slingsFor(calls, "wp-two"); got != 0 {
		t.Fatalf("a package whose upstream has not merged has no worktree and must not be routed, got %d "+
			"sling(s) — Gas City cuts a tree of its own for a routed bead, and the controller's gate grant "+
			"never reaches that worker\ncalls: %v\n%s", got, calls, out)
	}
}

// DEFECT A, the other half: preparation must reach a tree the controller found
// rather than one it cut. A run started before routing waited for the base —
// or any other reason Gas City gets there first — leaves exactly this state,
// and the grant must still arrive.
func TestAWorktreeTheControllerDidNotCutIsStillPrepared(t *testing.T) {
	e := newRecoveryEnv(t)
	base := e.initRig()

	// The tree Gas City cut for a bead it was asked to act on: present, on a
	// branch of its own naming, with no grant in it.
	wt := filepath.Join(e.city, ".gc", "worktrees", recoveryRigName, "worker-wp-two")
	mustGit(t, e, e.rigPath, "worktree", "add", "-q", "-b", "gc-worker-wp-two", wt, base)

	e.seedRuntime(map[string]string{
		"dispatched":    "2026-08-20T22:22:36Z",
		"baseSha":       base,
		"bead.wp-two":   beadTwo,
		"wt.wp-two":     wt,
		"branch.wp-two": "delivery/20260820T222236Z/wp-two",
	})
	// Closed, so the stage returns as soon as it has seen to the worktree.
	e.setBead(beadTwo, "closed")

	argv := []string{
		driverPath(t), "await",
		"-project", e.project, "-state", e.state,
		"-package", "wp-two",
		"-deadline", "30",
	}
	cmd := exec.Command("bash", argv...) //nolint:gosec // the driver under test
	cmd.Env = e.scrubbedEnv(nil)
	raw, _ := cmd.CombinedOutput()
	code, out := cmd.ProcessState.ExitCode(), string(raw)
	if code != 0 {
		t.Fatalf("await over closed work must succeed, got %d:\n%s", code, out)
	}

	grant := filepath.Join(wt, ".claude", "settings.local.json")
	if _, err := os.Stat(grant); err != nil {
		t.Fatalf("a worktree the controller did not cut received no gate grant (%v) — its worker is "+
			"deny-by-default and cannot run the gates its own bead requires\n%s", err, out)
	}
}

// gateRecordingEnv is a publication whose package declares gates that record
// themselves, so what the controller re-ran can be read afterwards.
type gateRecordingEnv struct {
	*sendbackEnv
	ran      string
	declared string
}

// newGateRecordingEnv rewrites the fixture plan's gates and installs a stub for
// the runner they name.
//
// The stub's first subcommand DRAINS ITS STDIN, which is what a real `npm
// install` did to the list of gates that was feeding the loop.
func newGateRecordingEnv(t *testing.T, gates []string, runner string) *gateRecordingEnv {
	t.Helper()
	s := newSendbackEnv(t)

	plan := fixturePlan()
	plan.Packages[0].Gates = gates
	writeJSONFile(t, filepath.Join(s.state, "plan.json"), plan)

	g := &gateRecordingEnv{
		sendbackEnv: s,
		ran:         filepath.Join(s.root, "gates-that-ran.txt"),
		declared:    filepath.Join(s.root, "declared-toolchain"),
	}
	if err := os.MkdirAll(g.declared, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub(t, filepath.Join(g.declared, runner),
		"#!/usr/bin/env bash\n"+
			"printf 'declared %s\\n' \"$*\" >> "+shquote(g.ran)+"\n"+
			"if [ \"${1:-}\" = 'install' ]; then cat > /dev/null; fi\n"+
			"exit 0\n")
	return g
}

// runPublishWithToolchain publishes with the declared project toolchain, the
// way a compiled run does once the host profile names one.
func (g *gateRecordingEnv) runPublishWithToolchain() (int, string) {
	g.t.Helper()
	argv := []string{
		driverPath(g.t), "publish",
		"-project", g.project, "-state", g.state,
		"-package", "wp-one",
		"-gh", filepath.Join(g.binDir, "gh"),
		"-project-path", g.declared,
	}
	cmd := exec.Command("bash", argv...) //nolint:gosec // the driver under test
	cmd.Env = g.scrubbedEnv([]string{
		"GH_STUB_LOG=" + g.ghLog,
		"DELIVERY_CI_DEADLINE=30",
	})
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
}

func (g *gateRecordingEnv) gatesThatRan() string {
	g.t.Helper()
	data, err := os.ReadFile(g.ran) //nolint:gosec // test artifact
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		g.t.Fatal(err)
	}
	return string(data)
}

// DEFECT B. Every declared gate is re-run by the controller, including the ones
// that come after a gate which reads stdin.
func TestTheControllerRerunsEveryDeclaredGateEvenAfterOneThatReadsStdin(t *testing.T) {
	g := newGateRecordingEnv(t,
		[]string{"projrun install", "projrun lint", "projrun test"}, "projrun")

	_, out := g.runPublishWithToolchain()

	ran := g.gatesThatRan()
	if ran == "" {
		t.Fatalf("no declared gate ran at all:\n%s", out)
	}
	for _, want := range []string{"install", "lint", "test"} {
		if !strings.Contains(ran, want) {
			t.Errorf("the controller did not re-run the declared gate %q — a gate that reads stdin must "+
				"not end the list of gates it was itself read from\nran:\n%s\n%s", want, ran, out)
		}
	}
}

// DEFECT C. A project's own command resolves through the toolchain the host
// declared for it, never through whatever the run happened to inherit.
//
// The same command name exists in both places here, exactly as two `npm`s do on
// the pilot host: one that belongs to the project, and one the run merely
// inherited from a foreign operating system.
func TestProjectGatesRunUnderTheDeclaredProjectToolchain(t *testing.T) {
	g := newGateRecordingEnv(t, []string{"projrun verify"}, "projrun")

	// The one on the run's own PATH — the Windows npm's stand-in.
	writeStub(t, filepath.Join(g.binDir, "projrun"),
		"#!/usr/bin/env bash\nprintf 'inherited %s\\n' \"$*\" >> "+shquote(g.ran)+"\nexit 0\n")

	_, out := g.runPublishWithToolchain()

	ran := strings.TrimSpace(g.gatesThatRan())
	if ran == "" {
		t.Fatalf("the declared gate never ran:\n%s", out)
	}
	if strings.Contains(ran, "inherited") {
		t.Fatalf("the controller verified the work with the INHERITED toolchain, not the declared one — "+
			"this is the Windows npm reaching a Linux worktree\nran:\n%s\n%s", ran, out)
	}
}

// A host that declares no project toolchain is unaffected: a project's commands
// resolve on the run's own PATH, exactly as they did before this existed.
func TestAnUndeclaredProjectToolchainLeavesThePathAlone(t *testing.T) {
	host := handoff.HostProfile{
		DeliveryRoot: t.TempDir(),
		Driver:       driverPath(t),
		Provider:     "claude",
	}
	_, work, err := handoff.Compile(fixtureIntent(), fixturePlan(), host, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range work.Tasks {
		for _, arg := range task.Argv {
			if arg == "-project-path" {
				t.Fatalf("task %q was given a project toolchain the host never declared: %v", task.ID, task.Argv)
			}
		}
	}
}
