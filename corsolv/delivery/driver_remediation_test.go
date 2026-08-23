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
