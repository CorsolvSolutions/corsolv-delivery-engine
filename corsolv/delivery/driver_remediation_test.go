//go:build integration

// These tests run the driver, which means spawning bash and letting it read
// files. That is a process-owning test by this repository's taxonomy, so it
// carries the integration tag rather than growing the untagged subprocess debt
// baseline — which only ever ratchets down.

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/handoff"
)

// THE TWO HALVES HAVE TO AGREE ABOUT WHAT WORK EXISTS.
//
// Corrective work authorized after a criterion is disproved is an append-only
// document beside the plan, never an edit to it. The Go layer joins the two in
// LoadPlan; the driver is a separate program in a separate language and has to
// arrive at the same list, or a remedial package would be planned, assessed and
// reported by one half and never dispatched by the other.
//
// These pin that agreement at the boundary the two share: the documents on disk.

// reconciliationState writes a delivery's validated documents into a state
// directory the way the Go layer does, and returns the directory.
func reconciliationState(t *testing.T, remediations ...handoff.Remediation) (root, state string, in handoff.Intent) {
	t.Helper()
	root = t.TempDir()
	in = reconIntent()
	plan := reconPlan(in)

	if err := handoff.SaveIntent(root, in); err != nil {
		t.Fatal(err)
	}
	if err := handoff.SavePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	for _, rm := range remediations {
		if err := handoff.SaveRemediation(root, rm); err != nil {
			t.Fatal(err)
		}
	}
	host := handoff.HostProfile{DeliveryRoot: root}
	return root, host.ProjectDir(in.ProjectID), in
}

// runDriverStage invokes one driver stage and returns its combined output.
//
// The stage is expected to FAIL: `dispatch` stops on "no city" the moment it
// starts, which is after the driver has loaded and composed its documents. That
// is everything these tests care about, and nothing that needs a city.
func runDriverStage(t *testing.T, stage, projectID, state string) string {
	t.Helper()
	bash := bashOrSkip(t)
	cmd := exec.Command(bash, driverPath(t), stage, "-project", projectID, "-state", state)
	cmd.Env = append(os.Environ(), "CORSOLV_ENGINE_REPO="+engineRepo(t))
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// effectivePackages are the package ids in the plan the driver composed.
func effectivePackages(t *testing.T, state string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(state, "evidence", "effective-plan.json"))
	if err != nil {
		t.Fatalf("the driver composed no effective plan: %v", err)
	}
	var p struct {
		SchemaVersion int    `json:"schemaVersion"`
		ProjectID     string `json:"projectId"`
		Packages      []struct {
			ID string `json:"id"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("the composed plan is not readable JSON: %v\n%s", err, raw)
	}
	if p.SchemaVersion != handoff.PlanSchemaVersion {
		t.Fatalf("the composed plan lost the base plan's schema version: %d", p.SchemaVersion)
	}
	if p.ProjectID != "reconciliation-probe" {
		t.Fatalf("the composed plan lost the base plan's project id: %q", p.ProjectID)
	}
	var ids []string
	for _, wp := range p.Packages {
		ids = append(ids, wp.ID)
	}
	return ids
}

// A delivery with no remediations composes to exactly its plan. This is every
// delivery that ran before the mechanism existed, and it must be untouched.
func TestTheDriverComposesAPlanWithNoRemediationsUnchanged(t *testing.T) {
	_, state, in := reconciliationState(t)
	runDriverStage(t, "dispatch", in.ProjectID, state)

	if got := strings.Join(effectivePackages(t, state), ","); got != "wp-1,wp-2,wp-3" {
		t.Fatalf("effective packages = %q, want the plan's own three", got)
	}
}

// A delivery WITH corrective work composes to the plan plus that work, in the
// order it was authorized — the same list, in the same order, that the Go
// layer's LoadPlan produces from the same files.
func TestTheDriverAndTheGoLayerComposeTheSameEffectivePlan(t *testing.T) {
	rm := reconRemediation(reconIntent())
	rm.Seq = 1
	rm.AuthorizedBy = "Jon Pratten"
	rm.AuthorizedAt = reconIntent().RequestedAt

	root, state, in := reconciliationState(t, rm)
	runDriverStage(t, "dispatch", in.ProjectID, state)

	fromDriver := effectivePackages(t, state)
	if got := strings.Join(fromDriver, ","); got != "wp-1,wp-2,wp-3,wp-3-fix" {
		t.Fatalf("the driver composed %q, want the plan plus its corrective work", got)
	}

	plan, found, err := handoff.LoadPlan(root, in)
	if err != nil || !found {
		t.Fatalf("LoadPlan: %v (found=%v)", err, found)
	}
	var fromGo []string
	for _, wp := range plan.AllPackages() {
		fromGo = append(fromGo, wp.ID)
	}
	if strings.Join(fromDriver, ",") != strings.Join(fromGo, ",") {
		t.Fatalf("the two halves disagree about what work exists:\n driver: %v\n go:     %v", fromDriver, fromGo)
	}

	// AND THE ORIGINAL PLAN IS UNTOUCHED. The composed document is derived, in
	// the run's evidence directory; plan.json is the historical record the
	// merged work was measured against, and the driver never writes it.
	base, err := os.ReadFile(handoff.PlanPath(root, in.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(base), "wp-3-fix") {
		t.Fatalf("the driver wrote corrective work into the original plan:\n%s", base)
	}
}

// A delivery whose plan is missing is still refused, and by the base plan's own
// name — the composed document must never stand in for one that was never
// validated.
func TestTheDriverStillRefusesWithoutAValidatedPlan(t *testing.T) {
	root := t.TempDir()
	in := reconIntent()
	if err := handoff.SaveIntent(root, in); err != nil {
		t.Fatal(err)
	}
	host := handoff.HostProfile{DeliveryRoot: root}
	out := runDriverStage(t, "dispatch", in.ProjectID, host.ProjectDir(in.ProjectID))

	if !strings.Contains(out, "no delivery plan at") {
		t.Fatalf("the refusal must name the missing plan, got:\n%s", out)
	}
	if !strings.Contains(out, "plan.json") {
		t.Fatalf("the refusal must name the BASE plan rather than the composed one, got:\n%s", out)
	}
}

// remedialPackage is the corrective work authorized after a delivery completed.
func remedialPackage(projectID string) handoff.Remediation {
	return handoff.Remediation{
		SchemaVersion: handoff.RemediationSchemaVersion,
		ProjectID:     projectID,
		Seq:           1,
		Repairs:       []handoff.Repair{{CriterionID: "ac-1", Invalidation: 1}},
		AuthorizedBy:  "Jon Pratten",
		Packages: []handoff.WorkPackage{{
			ID: "wp-three", Title: "the repair", Phase: "Build",
			Objective:       "Rewrite src/one.ts so the contract actually holds.",
			Artifact:        "src/one.ts",
			AuthorizedPaths: []string{"src/one.ts"},
			Gates:           []string{"npm run verify"},
			Satisfies:       []string{"ac-1"},
		}},
	}
}

// completedDelivery seeds a city exactly as a finished delivery left it: both
// original packages dispatched, closed, published and merged, and one declared
// worker agent apiece.
func completedDelivery(t *testing.T) *recoveryEnv {
	t.Helper()
	e := newRecoveryEnv(t)
	base := e.initRig()
	e.seedRuntime(map[string]string{
		"dispatched":       "2026-08-21T09:00:00Z",
		"bead.wp-one":      beadOne,
		"wt.wp-one":        e.makeWorktree("wp-one"),
		"merge.wp-one":     "merge-one",
		"bead.wp-two":      beadTwo,
		"wt.wp-two":        e.makeWorktree("wp-two"),
		"merge.wp-two":     "merge-two",
		"merged.wp-one":    "1111111111111111111111111111111111111111",
		"merged.wp-two":    "2222222222222222222222222222222222222222",
		"published.wp-one": "merged",
		"published.wp-two": "merged",
		"baseSha":          base,
	})
	e.setBead(beadOne, "closed")
	e.setBead(beadTwo, "closed")
	e.declareAgent("wp-one")
	e.declareAgent("wp-two")
	return e
}

// verificationPackage is corrective work for a criterion whose evidence is
// already on the authoritative branch: nothing to write, everything to check.
func verificationPackage(projectID, mergedSha string) handoff.Remediation {
	return handoff.Remediation{
		SchemaVersion: handoff.RemediationSchemaVersion,
		ProjectID:     projectID,
		Seq:           1,
		Repairs:       []handoff.Repair{{CriterionID: "ac-1", Invalidation: 1}},
		AuthorizedBy:  "Jon Pratten",
		Packages: []handoff.WorkPackage{{
			ID: "wp-verify-one", Title: "the evidence is checked", Phase: "Build",
			Objective: "Verify evidence/report.json against the contract at the merged commit that carries it.",
			Artifact:  "evidence/report.json",
			Gates:     []string{"npm test"},
			Satisfies: []string{"ac-1"},
			Verifies:  &handoff.Verification{MergedSha: mergedSha},
		}},
	}
}

// A CRITERION WHOSE EVIDENCE IS ALREADY MERGED IS REPAIRED BY CHECKING IT, NOT
// BY MANUFACTURING A CHANGE.
//
// THE DEFECT THIS EXISTS FOR. Remediation had exactly one shape: a worker
// changes files, the controller commits, opens a pull request and merges. That
// is right whenever the repair is work. It is impossible whenever it is not —
// and on scorm-course-studio it was not. Two authorized remedial packages named
// evidence that was already on main (PR #16 and #17 had merged it), so there was
// no diff for a worker to produce, and publication refused exactly as it should:
// nothing was produced. The criterion could not be repaired by the only
// lifecycle available, and the only way to force it through would have been to
// invent a change nobody needed and merge it so the shape fit.
//
// So verification is its own lifecycle. It creates no bead, starts no worker,
// cuts no branch, opens no pull request and merges nothing — because it changes
// nothing — and it earns the criterion by proving three separate things about a
// named commit: that the commit is really on the authoritative branch, that the
// evidence is really in it, and that the declared gates really pass against it.
func TestVerificationOnlyRemediationChecksMergedEvidenceAndPublishesNothing(t *testing.T) {
	e := completedDelivery(t)

	// The corrective evidence, already merged — the state the whole mechanism
	// exists for.
	const report = "{\n  \"columns\": 4\n}\n"
	e.git(e.rigPath, "worktree", "list") // touch the rig before writing into it
	merged := e.advanceMergedMainAt("evidence/report.json", report)

	if err := handoff.SaveRemediation(e.root, verificationPackage(e.project, merged)); err != nil {
		t.Fatal(err)
	}

	if code, out := e.runStage("city-up", nil); code != 0 {
		t.Fatalf("reconciling the city must succeed, got %d:\n%s", code, out)
	}

	// DISPATCH ROUTES IT NOWHERE, because there is nobody to route it to.
	code, out := e.runStage("dispatch", []string{"GC_STUB_ENFORCE_AGENTS=1"})
	if code != 0 {
		t.Fatalf("dispatch must succeed with a verification package present, got %d:\n%s", code, out)
	}
	calls := e.gcCalls()
	if found := callsContaining(calls, "bd create the evidence is checked"); len(found) != 0 {
		t.Errorf("a verification package got a work bead nobody could close by doing anything: %v", found)
	}
	if got := slingsFor(calls, "verify-one"); got != 0 {
		t.Errorf("a verification package was routed to a worker: %d sling(s)", got)
	}

	// THE VERIFY STAGE.
	e.truncateGCLog()
	code, out = e.runStage("verify", []string{"DELIVERY_PACKAGE=ignored"}, "-package", "wp-verify-one")
	if code != 0 {
		t.Fatalf("verifying merged evidence must succeed, got %d:\n%s", code, out)
	}

	// It ran against the exact commit, in a detached tree, and the evidence was
	// really there.
	wt := filepath.Join(e.city, ".gc", "worktrees", recoveryRigName, "verify-wp-verify-one")
	if head := e.git(wt, "rev-parse", "HEAD"); head != merged {
		t.Errorf("the verification ran at %s, want the commit it named %s", head[:9], merged[:9])
	}
	if body, err := os.ReadFile(filepath.Join(wt, "evidence", "report.json")); err != nil || string(body) != report {
		t.Errorf("the verified tree does not carry the evidence: %v", err)
	}
	// Detached, deliberately: there is nothing to put on a branch, and a branch
	// would be a thing a later step could try to push. `symbolic-ref` failing IS
	// the assertion here, so it is asked without the fatal wrapper.
	sym := exec.Command("git", "-C", wt, "symbolic-ref", "--quiet", "--short", "HEAD")
	sym.Env = e.scrubbedEnv(nil)
	if branch, err := sym.Output(); err == nil {
		t.Errorf("the verification cut a branch %q — it has nothing to put on one", strings.TrimSpace(string(branch)))
	}

	// And the declared gate really ran.
	if log, err := os.ReadFile(filepath.Join(e.root, "npm-calls.log")); err != nil || !strings.Contains(string(log), "test") {
		t.Errorf("the declared gate was not run against the verified tree: %v %s", err, log)
	}

	// All three controls are recorded, each only past the step that earned it.
	rt := e.runtimeFacts()
	for key, want := range map[string]string{
		"verifiedCommit.wp-verify-one":   merged,
		"verifiedEvidence.wp-verify-one": "evidence/report.json",
		"verifiedGates.wp-verify-one":    merged,
		"verified.wp-verify-one":         merged,
	} {
		if rt[key] != want {
			t.Errorf("%s = %q, want %q", key, rt[key], want)
		}
	}

	// Nothing was published, because nothing changed.
	for _, key := range []string{"pr.wp-verify-one", "merged.wp-verify-one", "published.wp-verify-one", "bead.wp-verify-one"} {
		if rt[key] != "" {
			t.Errorf("%s = %q — a verification package must publish nothing", key, rt[key])
		}
	}

	// IDEMPOTENT.
	if code, out := e.runStage("verify", nil, "-package", "wp-verify-one"); code != 0 {
		t.Fatalf("re-verifying must succeed, got %d:\n%s", code, out)
	}
}

// THE NEGATIVE CONTROL. A verification that fails leaves the criterion unmet,
// and records only what it actually established.
func TestVerificationOnlyRemediationRefusesWhenTheEvidenceDoesNotHoldUp(t *testing.T) {
	e := completedDelivery(t)
	merged := e.advanceMergedMainAt("evidence/report.json", "{}\n")
	if err := handoff.SaveRemediation(e.root, verificationPackage(e.project, merged)); err != nil {
		t.Fatal(err)
	}
	if code, out := e.runStage("city-up", nil); code != 0 {
		t.Fatalf("city-up: %d\n%s", code, out)
	}

	// The gate fails, which is the only thing different about this run.
	code, out := e.runStage("verify", []string{"NPM_STUB_EXIT=1"}, "-package", "wp-verify-one")
	if code == 0 {
		t.Fatalf("a verification whose gates fail must not report success:\n%s", out)
	}
	rt := e.runtimeFacts()
	if rt["verified.wp-verify-one"] != "" {
		t.Errorf("a failed verification recorded itself as verified: %q", rt["verified.wp-verify-one"])
	}
	if rt["verifiedGates.wp-verify-one"] != "" {
		t.Errorf("a failed gate recorded a passing gate control: %q", rt["verifiedGates.wp-verify-one"])
	}
	// What it DID establish is still recorded — the commit was on the branch and
	// the evidence was there. Losing that would make a failed check
	// indistinguishable from one that never ran.
	if rt["verifiedCommit.wp-verify-one"] != merged {
		t.Errorf("the checks that passed before the failure were discarded: %q", rt["verifiedCommit.wp-verify-one"])
	}
}

// And a commit that is not on the authoritative branch is refused before
// anything else is asked, because nothing it proved would be about the
// delivered product.
func TestVerificationOnlyRemediationRefusesACommitThatIsNotOnTheBranch(t *testing.T) {
	e := completedDelivery(t)
	e.advanceMergedMainAt("evidence/report.json", "{}\n")

	// A real commit, in the repository, never merged.
	side := e.git(e.rigPath, "commit-tree", "-m", "unmerged",
		e.git(e.rigPath, "rev-parse", "HEAD^{tree}"))
	if err := handoff.SaveRemediation(e.root, verificationPackage(e.project, side)); err != nil {
		t.Fatal(err)
	}
	if code, out := e.runStage("city-up", nil); code != 0 {
		t.Fatalf("city-up: %d\n%s", code, out)
	}

	code, out := e.runStage("verify", nil, "-package", "wp-verify-one")
	if code == 0 {
		t.Fatalf("verifying a commit that is not on the branch must not succeed:\n%s", out)
	}
	if !strings.Contains(out, "not on the authoritative branch") {
		t.Errorf("the refusal must say what is wrong with the commit, got:\n%s", out)
	}
	if e.runtimeFacts()["verifiedCommit.wp-verify-one"] != "" {
		t.Errorf("an unmerged commit was recorded as being on the branch")
	}
}

// CORRECTIVE WORK IS BASED ON THE BRANCH AS IT STANDS NOW, NOT ON THE BRANCH
// THE DELIVERY STARTED FROM.
//
// THE DEFECT THIS EXISTS FOR. Dispatch cut a package's tree from `baseSha` —
// what the default branch was when city-up cloned the rig. While a plan could
// only be dispatched once that was the merged head, so the two were the same
// tree and the difference was invisible.
//
// Corrective work authorized after a delivery completed has no upstreams, so it
// takes the branch that cuts immediately — and got the repository as it stood
// BEFORE any of the delivery's own work merged. On scorm-course-studio run
// 20260823T174122Z that was 9 files against main's 177: no package.json, no
// src, and none of the merged evidence the criterion had been disproved
// against. Both authorized remedial packages then failed for reasons that had
// nothing to do with the repair they were asked to make —
// `npm error enoent Could not read package.json`, and a required artifact that
// could not be produced because the product that produces it was not there.
//
// A criterion is invalidated against the evidence of the CURRENT merged branch.
// The work that repairs it has to be based there too, or remediation is
// impossible on every delivery that has ever finished.
func TestRemedialWorkIsCutFromCurrentMergedMainNotTheRunsOriginalBase(t *testing.T) {
	e := newRecoveryEnv(t)

	// Commit A: the delivery's own starting point, recorded as baseSha.
	commitA := e.initRig()

	// The delivery runs, and its packages merge. Commit B carries the product
	// that the original base did not have — this is the package.json whose
	// absence produced the live ENOENT.
	const manifest = "{\n  \"name\": \"the-product\",\n  \"scripts\": { \"verify\": \"true\" }\n}\n"
	commitB := e.advanceMergedMain("package.json", manifest)
	if commitA == commitB {
		t.Fatal("the fixture must advance the default branch past the run's base")
	}

	e.seedRuntime(map[string]string{
		"dispatched":       "2026-08-21T09:00:00Z",
		"bead.wp-one":      beadOne,
		"wt.wp-one":        e.makeWorktree("wp-one"),
		"merge.wp-one":     "merge-one",
		"bead.wp-two":      beadTwo,
		"wt.wp-two":        e.makeWorktree("wp-two"),
		"merge.wp-two":     "merge-two",
		"merged.wp-one":    "1111111111111111111111111111111111111111",
		"merged.wp-two":    "2222222222222222222222222222222222222222",
		"published.wp-one": "merged",
		"published.wp-two": "merged",
		"baseSha":          commitA, // history, and it must stay history
	})
	e.setBead(beadOne, "closed")
	e.setBead(beadTwo, "closed")
	e.declareAgent("wp-one")
	e.declareAgent("wp-two")

	// The criterion is disproved against commit B, and corrective work is
	// authorized to repair it.
	if err := handoff.SaveRemediation(e.root, remedialPackage(e.project)); err != nil {
		t.Fatal(err)
	}

	if code, out := e.runStage("city-up", nil); code != 0 {
		t.Fatalf("reconciling the city must succeed, got %d:\n%s", code, out)
	}
	code, out := e.runStage("dispatch", []string{"GC_STUB_ENFORCE_AGENTS=1"})
	if code != 0 {
		t.Fatalf("dispatching corrective work must succeed, got %d:\n%s", code, out)
	}

	wt := filepath.Join(e.city, ".gc", "worktrees", recoveryRigName, "worker-wp-three")

	// THE VERDICT, in the form the live failure took: the file the package's own
	// gate reads is there to be read.
	got, err := os.ReadFile(filepath.Join(wt, "package.json"))
	if err != nil {
		t.Fatalf("the remedial tree has no package.json — the live ENOENT, reproduced: %v\ntree: %v\n%s",
			err, dirEntries(t, wt), out)
	}
	if string(got) != manifest {
		t.Errorf("the remedial tree carries a different package.json than merged main:\n%s", got)
	}

	// And it is based on merged main, not on the run's historical base.
	head := e.git(wt, "rev-parse", "HEAD")
	if head != commitB {
		t.Fatalf("the remedial tree is based on %s, want merged main %s (the run's base was %s)",
			head[:9], commitB[:9], commitA[:9])
	}

	// IDEMPOTENT. Preparing again neither re-cuts nor re-beads.
	e.truncateGCLog()
	if code, out := e.runStage("dispatch", []string{"GC_STUB_ENFORCE_AGENTS=1"}); code != 0 {
		t.Fatalf("a second dispatch must succeed, got %d:\n%s", code, out)
	}
	if again := e.git(wt, "rev-parse", "HEAD"); again != commitB {
		t.Errorf("a second dispatch moved the remedial base to %s", again[:9])
	}
	if created := callsContaining(e.gcCalls(), "bd create"); len(created) != 0 {
		t.Errorf("a second dispatch created work again: %v", created)
	}

	// And the finished packages were left entirely alone.
	for _, pkg := range []string{"wp-one", "wp-two"} {
		if got := slingsFor(e.gcCalls(), pkg); got != 0 {
			t.Errorf("%s was routed again: %d sling(s) — merged work must not be reopened", pkg, got)
		}
	}
}

// AND THE BASE UNDER SOMEONE'S FEET IS NEVER MOVED.
//
// Re-cutting is only ever safe because of what it refuses to do. A tree holding
// commits the default branch does not have is holding WORK, and a tree a live
// worker is sitting in is being used right now. Either one re-cut is the
// recovery causing the next failure — the same mistake as handing merged work
// back to a worker.
func TestAStaleTreeIsNotRecutWhenItHoldsWorkOrAWorker(t *testing.T) {
	e := newRecoveryEnv(t)
	commitA := e.initRig()
	e.advanceMergedMain("package.json", "{}\n")

	// wp-one is open, on a tree cut from the old base, and its worker has
	// committed something that is not on the default branch.
	wt := filepath.Join(e.city, ".gc", "worktrees", recoveryRigName, "worker-wp-one")
	e.git(e.rigPath, "worktree", "add", "-q", "-b", "delivery/test/wp-one", wt, commitA)
	if err := os.WriteFile(filepath.Join(wt, "in-progress.txt"), []byte("a worker's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.git(wt, "add", "-A")
	e.git(wt, "commit", "-q", "-m", "work in progress")
	carried := e.git(wt, "rev-parse", "HEAD")

	e.seedRuntime(map[string]string{
		"dispatched":    "2026-08-21T09:00:00Z",
		"bead.wp-one":   beadOne,
		"wt.wp-one":     wt,
		"branch.wp-one": "delivery/test/wp-one",
		"bead.wp-two":   beadTwo,
		"baseSha":       commitA,
	})
	e.setBead(beadOne, "open")
	e.setBead(beadTwo, "closed")
	e.declareAgent("wp-one")

	if code, out := e.runStage("await", []string{"DELIVERY_WORK_DEADLINE=0"}); code == 0 {
		t.Fatalf("await must not report success with wp-one open:\n%s", out)
	}
	if after := e.git(wt, "rev-parse", "HEAD"); after != carried {
		t.Fatalf("a tree carrying unmerged work was re-cut: %s -> %s", carried[:9], after[:9])
	}
	if _, err := os.Stat(filepath.Join(wt, "in-progress.txt")); err != nil {
		t.Fatalf("the worker's own work was destroyed: %v", err)
	}

	// And with a LIVE worker, even a tree carrying nothing is left alone.
	e2 := newRecoveryEnv(t)
	baseA := e2.initRig()
	e2.advanceMergedMain("package.json", "{}\n")
	wt2 := filepath.Join(e2.city, ".gc", "worktrees", recoveryRigName, "worker-wp-one")
	e2.git(e2.rigPath, "worktree", "add", "-q", "-b", "delivery/test/wp-one", wt2, baseA)
	e2.seedRuntime(map[string]string{
		"dispatched":    "2026-08-21T09:00:00Z",
		"bead.wp-one":   beadOne,
		"wt.wp-one":     wt2,
		"branch.wp-one": "delivery/test/wp-one",
		"bead.wp-two":   beadTwo,
		"baseSha":       baseA,
	})
	e2.setBead(beadOne, "open")
	e2.setBead(beadTwo, "closed")
	e2.declareAgent("wp-one")
	e2.setSessions(liveSession("worker-wp-one"))

	e2.runStage("await", []string{"DELIVERY_WORK_DEADLINE=0"})
	if after := e2.git(wt2, "rev-parse", "HEAD"); after != baseA {
		t.Fatalf("a live worker's base was moved underneath it: %s -> %s", baseA[:9], after[:9])
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{"<no such directory>"}
	}
	var out []string
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	sort.Strings(out)
	return out
}

// A CITY IS RECONCILED AGAINST THE EFFECTIVE PLAN, NOT AGAINST THE PLAN IT WAS
// BUILT FROM.
//
// THE DEFECT THIS EXISTS FOR. city-up declared one worker agent per package,
// once, in the branch that builds a new city. A resumed run takes the early
// return above that branch, so a delivery whose effective plan had GROWN — the
// only way it can grow is append-only corrective work — kept exactly the agents
// it started with. Dispatch then routed the remedial package to an agent that
// was never declared and `gc sling` refused:
//
//	gc sling: agent "rig-.../worker-wp-remediate-architecture-artifact"
//	          not found in city.toml
//
// The dispatch stage exhausted its attempts, the run failed 52 seconds in, and
// two authorized remedial packages could not be executed at all. It is the same
// lesson TestResumedDispatchCreatesOnlyTheNewlyAddedPackage pinned for beads,
// one layer down: the agent the bead is routed to.
//
// The other half matters just as much. Reconciliation must leave the workers
// that already exist completely alone — rewriting a declaration underneath a
// delivery part-way through it is the same failure as handing merged work back
// to a worker.
func TestCityUpDeclaresWorkersForRemedialPackagesOnAResumedRun(t *testing.T) {
	e := completedDelivery(t)
	original := e.agentDecls()
	if len(original) != 2 {
		t.Fatalf("the fixture must start as a completed delivery with two workers, got %v", original)
	}

	// A resumed run with NOTHING authorized since is a delivery that ran before
	// this mechanism existed, and it must be untouched.
	if code, out := e.runStage("city-up", nil); code != 0 {
		t.Fatalf("reconciling an unchanged city must succeed, got %d:\n%s", code, out)
	}
	if got := e.agentDecls(); !reflect.DeepEqual(got, original) {
		t.Fatalf("reconciling an unchanged city altered its agents:\n before: %v\n after:  %v", original, got)
	}

	// Now the corrective work, authorized after the city was built.
	if err := handoff.SaveRemediation(e.root, remedialPackage(e.project)); err != nil {
		t.Fatal(err)
	}

	code, out := e.runStage("city-up", nil)
	if code != 0 {
		t.Fatalf("reconciling a city against corrective work must succeed, got %d:\n%s", code, out)
	}

	after := e.agentDecls()
	decl, ok := after["worker-wp-three"]
	if !ok {
		t.Fatalf("the remedial package got no worker agent; the city carries %v\n%s", keysOf(after), out)
	}
	// Declared as the real thing, not as a placeholder: the worktree is its own,
	// the scope is the rig, and the bounded-project opt-in survived.
	for _, want := range []string{
		`scope = "rig"`,
		`dir = "` + recoveryRigName + `"`,
		`max_active_sessions = 1`,
		filepath.Join(e.city, ".gc", "worktrees", recoveryRigName, "worker-wp-three"),
		`permission_mode = "bounded-project"`,
		poolWorkerPrompt,
	} {
		if !strings.Contains(decl, want) {
			t.Errorf("the remedial worker's declaration is missing %q:\n%s", want, decl)
		}
	}

	// The workers that already existed are byte-identical.
	for name, body := range original {
		if after[name] != body {
			t.Errorf("reconciliation rewrote an existing worker %s:\n before: %s\n after:  %s", name, body, after[name])
		}
	}
	if len(after) != 3 {
		t.Fatalf("reconciliation must add exactly the one missing worker, city carries %v", keysOf(after))
	}
	if !strings.Contains(out, "1 worker agent(s) added") {
		t.Errorf("the stage must report what it added, got:\n%s", out)
	}

	// AND IT IS IDEMPOTENT. A second reconciliation has nothing to do.
	if code, out := e.runStage("city-up", nil); code != 0 {
		t.Fatalf("a second reconciliation must succeed, got %d:\n%s", code, out)
	}
	if got := e.agentDecls(); !reflect.DeepEqual(got, after) {
		t.Fatalf("a second reconciliation changed the city:\n before: %v\n after:  %v", after, got)
	}

	// THE VERDICT: dispatch now reaches the remedial worker. This is the call
	// that refused on the live delivery, made against a stub that refuses on
	// exactly the same condition real gc does.
	e.truncateGCLog()
	code, out = e.runStage("dispatch", []string{"GC_STUB_ENFORCE_AGENTS=1"})
	if code != 0 {
		t.Fatalf("dispatching corrective work must reach its worker, got %d:\n%s", code, out)
	}
	if got := slingsFor(e.gcCalls(), "wp-three"); got != 1 {
		t.Fatalf("the remedial package must be routed exactly once, got %d\ncalls: %v\n%s",
			got, e.gcCalls(), out)
	}
	for _, pkg := range []string{"wp-one", "wp-two"} {
		if got := slingsFor(e.gcCalls(), pkg); got != 0 {
			t.Errorf("%s was routed again: %d sling(s) — merged work must not be reopened", pkg, got)
		}
	}
}

// And the negative control, which is what keeps the test above honest: with the
// remedial worker absent, dispatch fails with the live delivery's own message.
// Without this, a stub that quietly accepted every route would let the test
// pass against the unfixed driver.
func TestDispatchRefusesWhenTheRemedialWorkerWasNeverDeclared(t *testing.T) {
	e := completedDelivery(t)
	if err := handoff.SaveRemediation(e.root, remedialPackage(e.project)); err != nil {
		t.Fatal(err)
	}
	// The city as the defect left it: reconciliation never ran, so the remedial
	// package has no agent.
	code, out := e.runStage("dispatch", []string{"GC_STUB_ENFORCE_AGENTS=1"})
	if code == 0 {
		t.Fatalf("dispatch must not report success routing to an undeclared agent:\n%s", out)
	}
	if !strings.Contains(out, "not found in city.toml") {
		t.Errorf("the refusal must be the one a real city produces, got:\n%s", out)
	}
	if !strings.Contains(out, "worker-wp-three") {
		t.Errorf("the refusal must name the worker that is missing, got:\n%s", out)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DISPATCH IS PER PACKAGE, NOT ALL-OR-NOTHING.
//
// THE DEFECT THIS EXISTS FOR. Dispatch used to short-circuit on a single
// `dispatched` flag, which was right while a plan could never grow. Corrective
// work authorized after a criterion is disproved adds a package to a delivery
// that has already dispatched, and the flag sent this stage straight past it:
// the remedial package got no work bead, and its publication died an hour later
// on "no work bead for wp-three" — in a stage that had done nothing wrong.
//
// The other half of the rule matters just as much. A dispatch that creates the
// new package must leave the finished ones alone: re-routing a closed bead
// hands merged work back to a worker, which is the failure the recovery rules
// already forbid on every other path.
func TestResumedDispatchCreatesOnlyTheNewlyAddedPackage(t *testing.T) {
	e := newRecoveryEnv(t)
	base := e.initRig()

	// The delivery as it stood when it completed: both packages dispatched,
	// closed, published and merged.
	e.seedRuntime(map[string]string{
		"dispatched":       "2026-08-21T09:00:00Z",
		"bead.wp-one":      beadOne,
		"wt.wp-one":        e.makeWorktree("wp-one"),
		"merge.wp-one":     "merge-one",
		"bead.wp-two":      beadTwo,
		"wt.wp-two":        e.makeWorktree("wp-two"),
		"merge.wp-two":     "merge-two",
		"merged.wp-one":    "1111111111111111111111111111111111111111",
		"merged.wp-two":    "2222222222222222222222222222222222222222",
		"published.wp-one": "merged",
		"published.wp-two": "merged",
		"baseSha":          base,
	})
	e.setBead(beadOne, "closed")
	e.setBead(beadTwo, "closed")

	// And the corrective work authorized afterwards.
	if err := handoff.SaveRemediation(e.root, handoff.Remediation{
		SchemaVersion: handoff.RemediationSchemaVersion,
		ProjectID:     e.project,
		Seq:           1,
		Repairs:       []handoff.Repair{{CriterionID: "ac-1", Invalidation: 1}},
		AuthorizedBy:  "Jon Pratten",
		Packages: []handoff.WorkPackage{{
			ID: "wp-three", Title: "the repair", Phase: "Build",
			Objective:       "Rewrite src/one.ts so the contract actually holds.",
			Artifact:        "src/one.ts",
			AuthorizedPaths: []string{"src/one.ts"},
			Gates:           []string{"npm run verify"},
			Satisfies:       []string{"ac-1"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	code, out := e.runStage("dispatch", nil)
	if code != 0 {
		t.Fatalf("dispatching corrective work must succeed, got %d:\n%s", code, out)
	}

	// The new package got everything a package needs.
	calls := e.gcCalls()
	if found := callsContaining(calls, "bd create the repair"); len(found) == 0 {
		t.Fatalf("the remedial package got no work bead:\n%v\n%s", calls, out)
	}
	if found := callsContaining(calls, "gc.delivery_package=wp-three"); len(found) == 0 {
		t.Fatalf("the remedial package's scope was never stamped:\n%v", calls)
	}

	// And the finished packages got nothing at all.
	for _, pkg := range []string{"wp-one", "wp-two"} {
		if got := slingsFor(calls, pkg); got != 0 {
			t.Errorf("%s was routed again: %d sling(s) — merged work must not be reopened", pkg, got)
		}
		if found := callsContaining(calls, "gc.delivery_package="+pkg); len(found) != 0 {
			t.Errorf("%s was re-stamped by a dispatch that only added corrective work: %v", pkg, found)
		}
	}
	if found := callsContaining(calls, "bd create the first package"); len(found) != 0 {
		t.Errorf("a second work bead was created for already-merged work: %v", found)
	}
	if found := callsContaining(calls, "bd create the second package"); len(found) != 0 {
		t.Errorf("a second work bead was created for already-merged work: %v", found)
	}
}
