//go:build integration

// A stale tree's work is carried forward, never thrown away.
//
// THE INCIDENT THESE EXIST FOR, on aws-infrastructure-intelligence
// (2026-10-12). wp-resource-collectors' worker closed its bead with 34
// uncommitted files in its tree — workers cannot run git, so their output is
// always uncommitted until the controller publishes it. The controller re-ran
// the declared gates, two tests failed, and the package was sent back, which
// reopened the bead. When the publish stage resumed, other packages had merged
// and `main` had moved, so the tree was a strict ancestor of the merged head
// with an open bead and no live worker: stale. recut_worktree then removed the
// tree with --force and deleted its branch ("Deleted branch
// gc-worker-wp-resource-collectors (was 61e2884)"), and the worker rebuilt the
// whole package from nothing.
//
// The re-cut was written for a tree cut from a base with no project in it. A
// tree sent back is finished work that is merely behind `main`, and these pin
// that it reaches the new base intact — or, when it cannot be carried cleanly,
// stays exactly where it was.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// staleTreeFixture is the incident's state: wp-one's tree cut from commit A,
// whatever the worker left in it (written by `worker`), and the default branch
// moved on by `advance`. The bead is open and nobody holds it.
type staleTree struct {
	e       *recoveryEnv
	wt      string
	branch  string
	oldBase string
	newHead string
}

func staleTreeFixture(t *testing.T, worker func(wt string), advance func(e *recoveryEnv) string) staleTree {
	t.Helper()
	e := newRecoveryEnv(t)
	commitA := e.initRig()

	const branch = "delivery/test/wp-one"
	wt := filepath.Join(e.city, ".gc", "worktrees", recoveryRigName, "worker-wp-one")
	e.git(e.rigPath, "worktree", "add", "-q", "-b", branch, wt, commitA)
	worker(wt)
	newHead := advance(e)
	if newHead == commitA {
		t.Fatal("the fixture must move the default branch past the tree's base")
	}

	e.seedRuntime(map[string]string{
		"dispatched":    "2026-10-12T08:00:00Z",
		"bead.wp-one":   beadOne,
		"wt.wp-one":     wt,
		"branch.wp-one": branch,
		"bead.wp-two":   beadTwo,
		"baseSha":       commitA,
	})
	e.setBead(beadOne, "open")
	e.setBead(beadTwo, "closed")
	e.declareAgent("wp-one")
	return staleTree{e: e, wt: wt, branch: branch, oldBase: commitA, newHead: newHead}
}

func writeFileAt(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFileAt(t *testing.T, root, rel string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// preservedRefs lists the preservation refs the rig holds for a package.
func preservedRefs(e *recoveryEnv, pkg string) []string {
	e.t.Helper()
	out := e.git(e.rigPath, "for-each-ref", "--format=%(refname)", "refs/delivery/preserved/"+pkg+"/")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func recutEvidence(t *testing.T, e *recoveryEnv) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.state, "evidence", "recut-wp-one.txt"))
	if err != nil {
		t.Fatalf("no re-cut evidence was kept: %v", err)
	}
	return string(data)
}

// What the sent-back worker had written: a modified tracked file, an
// authorized new file, and a new file in a new directory — all uncommitted.
var workerOutput = map[string]string{
	"README.md":         "fixture\nand the worker's own notes on it\n",
	"src/one.ts":        "export const one = 1; // fixed after the send-back\n",
	"src/lib/helper.ts": "export function helper(): number { return 34; }\n",
	"test/one.test.ts":  "import { one } from '../src/one';\n",
}

// (a) The incident, repaired: the tree lands on the merged head with every
// file the worker wrote, byte for byte, still uncommitted — and the bead is
// routed to a worker, and a preservation ref is kept.
func TestAStaleTreeHoldingWorkerOutputIsCarriedOntoTheMergedHead(t *testing.T) {
	f := staleTreeFixture(t,
		func(wt string) {
			for rel, body := range workerOutput {
				writeFileAt(t, wt, rel, body)
			}
		},
		func(e *recoveryEnv) string {
			return e.advanceMergedMain("package.json", "{\n  \"name\": \"merged-meanwhile\"\n}\n")
		})
	e := f.e

	code, out := e.runStage("await", []string{"DELIVERY_WORK_DEADLINE=0"}, "-package", "wp-one")
	if code == 0 {
		t.Fatalf("await must not succeed with its work still open:\n%s", out)
	}

	if head := e.git(f.wt, "rev-parse", "HEAD"); head != f.newHead {
		t.Fatalf("the tree is on %s, want the merged head %s (old base %s)\n%s\nevidence:\n%s",
			head[:9], f.newHead[:9], f.oldBase[:9], out, recutEvidence(t, e))
	}
	if branch := e.git(f.wt, "symbolic-ref", "--short", "HEAD"); branch != f.branch {
		t.Errorf("the re-cut tree is on branch %q, want %q", branch, f.branch)
	}
	for rel, want := range workerOutput {
		got, ok := readFileAt(t, f.wt, rel)
		if !ok {
			t.Errorf("the worker's %s was lost in the re-cut\n%s", rel, out)
			continue
		}
		if got != want {
			t.Errorf("the worker's %s changed in the re-cut:\n got %q\nwant %q", rel, got, want)
		}
	}
	if got, ok := readFileAt(t, f.wt, "package.json"); !ok || !strings.Contains(got, "merged-meanwhile") {
		t.Errorf("the re-cut tree does not carry what merged meanwhile: %q", got)
	}

	// Uncommitted, exactly as the worker left it: publish stages and commits it.
	if staged := e.git(f.wt, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("the carried work must not be staged, got staged:\n%s", staged)
	}
	status := e.git(f.wt, "status", "--porcelain")
	for _, want := range []string{"M README.md", "?? src/", "?? test/"} {
		if !strings.Contains(status, want) {
			t.Errorf("the carried work must be uncommitted (%q missing), status:\n%s", want, status)
		}
	}

	if got := slingsFor(e.gcCalls(), "wp-one"); got != 1 {
		t.Errorf("the re-cut package's bead must be routed once, got %d sling(s)\ncalls: %v", got, e.gcCalls())
	}

	// (d) The preservation ref is kept, and holds the work.
	refs := preservedRefs(e, "wp-one")
	if len(refs) != 1 {
		t.Fatalf("want one preservation ref for wp-one, got %v\n%s", refs, out)
	}
	if got := e.git(e.rigPath, "show", refs[0]+":src/lib/helper.ts"); got+"\n" != workerOutput["src/lib/helper.ts"] {
		t.Errorf("the preservation ref does not hold the worker's file: %q", got)
	}
	if parent := e.git(e.rigPath, "rev-parse", refs[0]+"^"); parent != f.oldBase {
		t.Errorf("the preservation commit sits on %s, want the old base %s", parent[:9], f.oldBase[:9])
	}

	if !strings.Contains(out, "carried its 4 path(s) of worker output forward") {
		t.Errorf("the log must say the work was carried forward, got:\n%s", out)
	}
	if ev := recutEvidence(t, e); !strings.Contains(ev, refs[0]) || !strings.Contains(ev, "re-applied, uncommitted") {
		t.Errorf("the evidence must name the ref and the outcome, got:\n%s", ev)
	}
}

// (b) When the default branch changed the same lines the worker did, nothing
// is re-cut: the tree, its branch, its base and every file stay exactly as
// they were, the log says why, and the work is preserved besides.
func TestAStaleTreeWhoseWorkConflictsWithMainIsLeftIntact(t *testing.T) {
	f := staleTreeFixture(t,
		func(wt string) {
			for rel, body := range workerOutput {
				writeFileAt(t, wt, rel, body)
			}
		},
		func(e *recoveryEnv) string {
			return e.advanceMergedMain("README.md", "fixture, rewritten by a package that merged meanwhile\n")
		})
	e := f.e

	code, out := e.runStage("await", []string{"DELIVERY_WORK_DEADLINE=0"}, "-package", "wp-one")
	if code == 0 {
		t.Fatalf("await must not succeed with its work still open:\n%s", out)
	}

	if head := e.git(f.wt, "rev-parse", "HEAD"); head != f.oldBase {
		t.Fatalf("a tree whose work conflicts with main was moved: %s -> %s\n%s", f.oldBase[:9], head[:9], out)
	}
	if branch := e.git(f.wt, "symbolic-ref", "--short", "HEAD"); branch != f.branch {
		t.Errorf("the kept tree is on branch %q, want %q", branch, f.branch)
	}
	for rel, want := range workerOutput {
		got, ok := readFileAt(t, f.wt, rel)
		if !ok {
			t.Errorf("the worker's %s was lost\n%s", rel, out)
			continue
		}
		if got != want {
			t.Errorf("the worker's %s changed:\n got %q\nwant %q", rel, got, want)
		}
	}

	if got := slingsFor(e.gcCalls(), "wp-one"); got != 1 {
		t.Errorf("the kept package's bead must still be routed once, got %d sling(s)\ncalls: %v", got, e.gcCalls())
	}

	// (d)
	refs := preservedRefs(e, "wp-one")
	if len(refs) != 1 {
		t.Fatalf("want one preservation ref for wp-one, got %v\n%s", refs, out)
	}

	if !strings.Contains(out, "conflicts with what the default branch merged") ||
		!strings.Contains(out, "left on its own base with every file in place") {
		t.Errorf("the log must say the tree was kept because its work conflicts, got:\n%s", out)
	}
	if ev := recutEvidence(t, e); !strings.Contains(ev, "NOT re-cut") || !strings.Contains(ev, "README.md") {
		t.Errorf("the evidence must record the refusal and the conflicting path, got:\n%s", ev)
	}

	// And a later stage over the same tree does not mint a second ref.
	e.runStage("await", []string{"DELIVERY_WORK_DEADLINE=0"}, "-package", "wp-one")
	if again := preservedRefs(e, "wp-one"); len(again) != 1 {
		t.Errorf("an unchanged tree was preserved again: %v", again)
	}
}

// (c) The case the re-cut was written for is unchanged: a stale tree with no
// worker output — the controller's own gate grant does not count — is re-cut
// onto the merged head and routed, and nothing is preserved because nothing
// needed to be.
func TestAStaleTreeWithNoWorkerOutputIsRecutAsBefore(t *testing.T) {
	f := staleTreeFixture(t,
		func(wt string) {
			writeFileAt(t, wt, ".claude/settings.local.json", "{\"permissions\":{\"allow\":[]}}\n")
		},
		func(e *recoveryEnv) string {
			return e.advanceMergedMain("package.json", "{}\n")
		})
	e := f.e

	code, out := e.runStage("await", []string{"DELIVERY_WORK_DEADLINE=0"}, "-package", "wp-one")
	if code == 0 {
		t.Fatalf("await must not succeed with its work still open:\n%s", out)
	}
	if head := e.git(f.wt, "rev-parse", "HEAD"); head != f.newHead {
		t.Fatalf("a stale tree with no work in it must be re-cut onto %s, is on %s\n%s", f.newHead[:9], head[:9], out)
	}
	if !strings.Contains(out, "re-cut wp-one from the merged base "+f.newHead[:9]+" — its worker runs now") {
		t.Errorf("the plain re-cut must be announced as before, got:\n%s", out)
	}
	if got := slingsFor(e.gcCalls(), "wp-one"); got != 1 {
		t.Errorf("the re-cut package's bead must be routed once, got %d sling(s)", got)
	}
	if refs := preservedRefs(e, "wp-one"); len(refs) != 0 {
		t.Errorf("a tree with no worker output must not be preserved, got %v", refs)
	}
}
