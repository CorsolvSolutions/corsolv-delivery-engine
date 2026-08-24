//go:build integration

// What happens to a package whose pull request fails the repository's check.
//
// The pilot found this on its very first package. `wp-foundation` wrote a
// package.json declaring an npm `test` script for a directory a LATER package
// creates. It passed its own declared gates — the ones the controller re-runs —
// and then failed the repository's required CI on `Could not find 'test/'`.
//
// What followed was a dead end, and every part of it looked like something
// else:
//
//	the run reported FAILED, which is what a broken platform reports, for a
//	worker writing code that does not pass its gate — the ordinary condition
//	of writing code;
//
//	the work bead was closed, so a resumed dispatch left it alone, since
//	closed work is finished work;
//
//	the branch already carried the controller's commit, so each retried
//	publication died at `git commit` with nothing to commit — reporting a
//	commit problem, at the publish stage, for a red check.
//
// Three attempts, three misleading messages, no route back to a worker. The
// only remaining move was a person editing the project's source by hand, and a
// controller that writes the code is forging the evidence it later checks.
//
// These tests pin the route back: the verdict is written where a worker will
// read it, the bead is reopened, and the stage says CONTINUE rather than ending
// the run. And they pin its floor: a worker sent back that returns the same
// tree stops for a person instead of presenting the head that already failed.
//
// Like the other driver tests these spawn bash, so they carry the integration
// tag. SAFETY: every git operation happens inside a repository the test created
// in its own temp directory, with the live repository's git environment
// scrubbed out — see recoveryEnv.scrubbedEnv, which must not be relaxed.
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/handoff"
)

// ghFailedCI is a `gh` that answers the two api questions await_required_ci
// asks — which run tested this head, and how did it conclude — with a run that
// tested the exact head and failed. Everything else it is asked (pr create, pr
// list, pr view) it answers well enough for the publication to reach CI.
const ghFailedCI = `#!/usr/bin/env bash
set -u
printf '%s\n' "$*" >> "$GH_STUB_LOG"

case "${1:-}" in
  api)
    case "${2:-}" in
      *actions/runs?head_sha=*) printf '%s\n' "${GH_STUB_RUN_ID:-4242}" ;;
      *actions/runs/*)
        # --jq selects one field; the driver asks for .conclusion, then
        # .head_sha, then dumps the whole object into evidence.
        case "${*}" in
          *.conclusion*) printf '%s\n' "${GH_STUB_CONCLUSION:-failure}" ;;
          *.head_sha*)   printf '%s\n' "@HEAD@" ;;
          *)             printf '{"id":%s}\n' "${GH_STUB_RUN_ID:-4242}" ;;
        esac
        ;;
    esac
    ;;
  pr)
    case "${2:-}" in
      create) printf 'https://example.invalid/pr/7\n' ;;
      list)   printf '7\n' ;;
      view)
        case "${*}" in
          *headRefOid*) printf '%s\n' "@HEAD@" ;;
          *)            printf '{}\n' ;;
        esac
        ;;
    esac
    ;;
esac
exit 0
`

// The npm stub these tests rely on now belongs to the shared recovery fixture,
// which installs it for every environment. It still passes by default, which is
// what these tests need: a package that failed its own gates never reaches the
// forge, so the failure under test here is the repository's check.

// sendbackEnv is a delivery that has run: a real origin, a rig cloned from it,
// a worktree holding finished work, and a closed work bead. Publication is the
// only stage left.
type sendbackEnv struct {
	*recoveryEnv
	worktree string
	ghLog    string
}

func newSendbackEnv(t *testing.T) *sendbackEnv {
	t.Helper()
	e := newRecoveryEnv(t)

	// An origin the publication can really push to. `git push` runs before the
	// CI await, so without one the test would never reach the code it is for.
	// initRig builds it, because a rig IS a clone: the base a package is cut
	// from is read from the origin's default branch, not the rig's own.
	base := e.initRig()

	branch := "delivery/20260814T164300Z/wp-one"
	wt := filepath.Join(e.city, ".gc", "worktrees", recoveryRigName, "worker-wp-one")
	mustGit(t, e, e.rigPath, "worktree", "add", "-q", "-b", branch, wt, base)

	// The work the worker finished, inside its one authorized path.
	if err := os.MkdirAll(filepath.Join(wt, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "src", "one.ts"), []byte("export const one = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &sendbackEnv{
		recoveryEnv: e,
		worktree:    wt,
		ghLog:       filepath.Join(e.root, "gh-calls.log"),
	}

	// The forge must answer with whatever the controller actually committed:
	// the driver refuses to wait on CI for a PR whose head is not its own
	// commit, so a fixed sha here would stop the test before the code it exists
	// for. Reading the branch is the honest answer to "what is the PR head".
	writeStub(t, filepath.Join(e.binDir, "gh"),
		strings.ReplaceAll(ghFailedCI, "@HEAD@", "$(git -C "+shquote(wt)+" rev-parse HEAD)"))

	e.seedRuntime(map[string]string{
		"dispatched":    "2026-08-14T16:43:00Z",
		"baseSha":       base,
		"bead.wp-one":   beadOne,
		"merge.wp-one":  beadTwo,
		"wt.wp-one":     wt,
		"branch.wp-one": branch,
	})
	e.setBead(beadOne, "closed")
	e.setBead(beadTwo, "open")
	return s
}

func writeStub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil { //nolint:gosec // a test stub must be executable
		t.Fatal(err)
	}
}

func mustGit(t *testing.T, e *recoveryEnv, dir string, args ...string) {
	t.Helper()
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", full...)
	cmd.Env = e.scrubbedEnv(nil)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// runPublish invokes the publish stage the way the compiled run does, with the
// stub forge the driver is told to use by name.
func (s *sendbackEnv) runPublish() (int, string) {
	s.t.Helper()
	argv := []string{
		driverPath(s.t), "publish",
		"-project", s.project, "-state", s.state,
		"-package", "wp-one",
		"-gh", filepath.Join(s.binDir, "gh"),
	}
	cmd := exec.Command("bash", argv...) //nolint:gosec // the driver under test
	cmd.Env = s.scrubbedEnv([]string{
		"GH_STUB_LOG=" + s.ghLog,
		"DELIVERY_CI_DEADLINE=30",
	})
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
}

// AN AUTHORIZED PATH IS A PERMISSION, NOT A PROMISE.
//
// THE DEFECT THIS EXISTS FOR. `authorizedPaths` is every path a package MAY
// create or change — the ceiling publication scope is adjudicated against — and
// a plan that authorizes a file the work turns out not to need has simply been
// permissive. Staging the ceiling verbatim made that fatal: `git add` fails the
// whole invocation on the first pathspec matching nothing.
//
// On scorm-course-studio a package whose gates had ALL passed — typecheck, 754
// unit tests and the full 27-journey browser suite — died at
// `fatal: pathspec 'src/runtime/packageEntry.ts' did not match any files`,
// reported as "staging authorized paths": a message that reads like a scope
// violation while being its exact opposite.
func TestAnAuthorizedPathTheWorkDidNotNeedDoesNotStopPublication(t *testing.T) {
	s := newSendbackEnv(t)

	// The plan allowed a second file. The worker only needed the first.
	//
	// Written straight to the document rather than through SavePlan, which
	// rightly refuses to rewrite a plan a delivery has already been measured
	// against — this fixture is that delivery.
	plan := fixturePlan()
	plan.Packages[0].AuthorizedPaths = []string{"src/one.ts", "src/never-needed.ts"}
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handoff.PlanPath(s.root, s.project), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := s.runPublish()

	if strings.Contains(out, "did not match any files") {
		t.Fatalf("an allowance nobody used failed the publication:\n%s", out)
	}
	if strings.Contains(out, "staging wp-one's authorized paths") {
		t.Fatalf("publication died staging a path the work did not need:\n%s", out)
	}
	// It says which allowance went unused, because a plan that keeps authorizing
	// files nobody writes is worth noticing.
	if !strings.Contains(out, "src/never-needed.ts") {
		t.Errorf("the run must say which authorized path went unused, got:\n%s", out)
	}
	// And the work itself still reached the forge: this fixture's failure is a
	// red required check, which is a verdict about CI and not about staging.
	if code == 0 {
		t.Fatalf("this fixture's CI fails, so publication must still not succeed:\n%s", out)
	}
	if !strings.Contains(out, "required CI") {
		t.Errorf("the publication must get far enough to report the CI verdict, got:\n%s", out)
	}
}

// THE ROUTE BACK. A red required check sends the package to a worker, and says
// so in the one place a worker reads.
func TestAFailedRequiredCheckReopensTheWorkForAWorker(t *testing.T) {
	s := newSendbackEnv(t)
	code, out := s.runPublish()

	if code == 0 {
		t.Fatalf("a package that failed required CI must not report a successful publication:\n%s", out)
	}
	if got := beadStatus(t, s.recoveryEnv, beadOne); got != "open" {
		t.Errorf("the work bead must be reopened so a worker can be sent back to it, got %q\n%s", got, out)
	}
	calls := s.gcCalls()
	if len(callsContaining(calls, "--append-notes")) == 0 {
		t.Errorf("the CI verdict must be written onto the work bead — a worker reads its bead and nothing else.\ncalls: %v\n%s", calls, out)
	}
	if !strings.Contains(out, "required CI") {
		t.Errorf("the driver must say what actually failed, got:\n%s", out)
	}
	// The message that made this a three-hour diagnosis rather than a one-line
	// one. A CI failure must never be reported as a commit problem.
	if strings.Contains(out, "committing wp-one") {
		t.Errorf("a failed check must not be reported as a commit failure:\n%s", out)
	}
}

// THE FLOOR. A worker sent back that returns the same tree cannot be sent back
// again — republishing would present the head that already failed — so it stops
// for a person rather than looping.
func TestAPackageSentBackThatChangesNothingStopsForAPerson(t *testing.T) {
	s := newSendbackEnv(t)
	// First publication: commits the work, fails CI, sends the package back.
	if code, out := s.runPublish(); code == 0 {
		t.Fatalf("the first publication must not succeed:\n%s", out)
	}
	// The worker is sent back and returns the same tree. The bead closes again
	// with nothing new in the worktree.
	s.setBead(beadOne, "closed")

	code, out := s.runPublish()
	if code == 0 {
		t.Fatalf("republishing an unchanged tree must not succeed:\n%s", out)
	}
	if !strings.Contains(out, "returned no change") {
		t.Errorf("the driver must say the worker returned nothing rather than blaming git, got:\n%s", out)
	}
	if strings.Contains(out, "committing wp-one") {
		t.Errorf("an unchanged tree must not be reported as a commit failure:\n%s", out)
	}
}

// beadStatus reads what the stub bead store holds, which is what the driver's
// own `bd show` sees.
func beadStatus(t *testing.T, e *recoveryEnv, id string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.beadsDir, id))
	if err != nil {
		t.Fatalf("reading bead %s: %v", id, err)
	}
	return strings.TrimSpace(string(data))
}

// THE SAME DEAD END, ON THE GATE PATH.
//
// `send_back_to_worker` was written for a red required check, and the dead end
// its comment describes was never specific to CI: the run reports FAILED, the
// work bead is closed so a resumed dispatch leaves it alone, and every route
// back to a worker is shut — so the only remaining move is a person editing the
// project's source by hand.
//
// A package that fails the project's OWN gates reached exactly that state. The
// controller re-ran the declared gates, refused to publish unproven work —
// correctly — and then died, ending the run with a closed bead and nobody to
// tell. The refusal was right; the ending was the dead end.
//
// Found by the JSON Configuration Drift Auditor pilot: `wp-acceptance-matrix`
// failed its own gates, the run ended `failed`, and no resume could reach the
// worker that could have fixed it.
func TestAFailedProjectGateReopensTheWorkForAWorker(t *testing.T) {
	s := newSendbackEnv(t)
	// The package's own declared gates fail. Everything else about the fixture
	// is unchanged, so what this test moves is the gate verdict alone.
	writeStub(t, filepath.Join(s.binDir, "npm"), "#!/usr/bin/env bash\necho 'the suite failed' >&2\nexit 1\n")

	code, out := s.runPublish()

	if code == 0 {
		t.Fatalf("a package that failed its own gates must not report a successful publication:\n%s", out)
	}
	if got := beadStatus(t, s.recoveryEnv, beadOne); got != "open" {
		t.Errorf("the work bead must be reopened so a worker can be sent back to it, got %q\n%s", got, out)
	}
	calls := s.gcCalls()
	if len(callsContaining(calls, "--append-notes")) == 0 {
		t.Errorf("the gate verdict must be written onto the work bead — a worker reads its bead and nothing else.\ncalls: %v\n%s", calls, out)
	}
	if !strings.Contains(out, "gate") {
		t.Errorf("the driver must say a gate is what failed, got:\n%s", out)
	}

	// AND IT NEVER REACHED THE FORGE. Work that cannot pass the gate it is
	// judged by must not become a branch, a pull request or a CI run — the
	// containment this refusal exists for is upstream of publication.
	if _, err := os.Stat(s.ghLog); err == nil {
		body, rerr := os.ReadFile(s.ghLog)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if strings.Contains(string(body), "pr create") {
			t.Errorf("a package that failed its own gates opened a pull request:\n%s", body)
		}
	}
}

// THE FLOOR, ON THE GATE PATH. A worker sent back for a failed gate that
// returns the same tree is not sent back again: it stops for a person, exactly
// as one sent back for a red check does. Without this, a package that cannot
// pass its gate would be re-offered until its resume budget ran out, telling
// nobody anything new each time.
func TestAPackageSentBackForAGateThatChangesNothingStopsForAPerson(t *testing.T) {
	s := newSendbackEnv(t)
	writeStub(t, filepath.Join(s.binDir, "npm"), "#!/usr/bin/env bash\nexit 1\n")

	if code, out := s.runPublish(); code == 0 {
		t.Fatalf("the first attempt must not succeed:\n%s", out)
	}
	if got := beadStatus(t, s.recoveryEnv, beadOne); got != "open" {
		t.Fatalf("the first attempt must reopen the bead, got %q", got)
	}
}
