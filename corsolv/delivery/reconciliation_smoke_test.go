//go:build integration

// A process-owning test by this repository's taxonomy — it builds and runs the
// delivery binary — so it carries the integration tag.

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/handoff"
	"github.com/gastownhall/gascity/internal/unattended"
)

// THE LIFECYCLE, THROUGH THE COMMAND SURFACE A PERSON ACTUALLY USES.
//
// TestCompleteToIncompleteToCompleteOnTheSameProject proves the transition by
// calling the command functions directly. This proves the same transition
// through the built binary: the subcommand dispatch, the flag parsing, the exit
// codes a supervising script reads, and the JSON a portal parses. Those are
// four things a Go-level call cannot exercise, and each has been wrong before.

// smokeEnv is a synthetic delivery on disk plus the binary that operates it.
type smokeEnv struct {
	t          *testing.T
	bin        string
	root       string
	hostPath   string
	host       handoff.HostProfile
	intent     handoff.Intent
	renderPath string
}

// newSmokeEnv builds the delivery binary and seeds a FINISHED delivery: three
// packages merged with met completion gates, three criteria met, Complete.
func newSmokeEnv(t *testing.T) *smokeEnv {
	t.Helper()
	bashOrSkip(t)

	root := t.TempDir()
	bin := filepath.Join(root, "delivery")
	build := exec.Command("go", "build", "-o", bin, "./corsolv/delivery")
	build.Dir = engineRepo(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the delivery binary: %v\n%s", err, out)
	}

	// The driver stands in for the run's two evidence stages. The real ones
	// re-render the projection from the record and install it in the project's
	// repository; this installs whatever the test has staged, so what the smoke
	// proves is the command surface rather than the projector's rule.
	render := filepath.Join(root, "rendered.yml")
	driver := filepath.Join(root, "driver.sh")
	projection := handoff.HostProfile{DeliveryRoot: root}.DeliveryProjectionPath(reconIntent().ProjectID)
	body := "#!/usr/bin/env bash\ncase \"$1\" in\n" +
		"  project) cp -f " + shellQuote(render) + " " + shellQuote(projection) + " ;;\n" +
		"  publish-projection) : ;;\n" +
		"  *) echo \"no stage may run here: $1\" >&2; exit 9 ;;\nesac\n"
	if err := os.WriteFile(driver, []byte(body), 0o755); err != nil { //nolint:gosec // a stub must be executable
		t.Fatal(err)
	}

	hostPath := filepath.Join(root, "host.toml")
	profile := "deliveryRoot = " + tomlQuote(root) + "\n" +
		"driver = " + tomlQuote(driver) + "\n" + "provider = \"claude\"\n"
	if err := os.WriteFile(hostPath, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}

	in := reconIntent()
	host := handoff.HostProfile{DeliveryRoot: root, Driver: driver, Provider: "claude"}
	if err := handoff.SaveIntent(root, in); err != nil {
		t.Fatal(err)
	}
	if err := handoff.SavePlan(root, reconPlan(in)); err != nil {
		t.Fatal(err)
	}
	adm, err := handoff.Admit(root, in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff.SaveRecord(root, adm.Record.AppendRun(reconRunID, handoff.ReasonInitial, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(host.StateDir(in.ProjectID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unattended.WriteCompletion(host.StateDir(in.ProjectID), unattended.CompletionEvent{
		RunID: reconRunID, ProjectID: in.ProjectID,
		Outcome: unattended.RunCompleted, Reason: "every declared task succeeded",
	}); err != nil {
		t.Fatal(err)
	}

	e := &smokeEnv{t: t, bin: bin, root: root, hostPath: hostPath, host: host, intent: in, renderPath: render}
	e.publish(reconProjected{merged: []string{"wp-1", "wp-2", "wp-3"}, met: []string{"ac-1", "ac-2", "ac-3"}})
	e.stage(reconProjected{merged: []string{"wp-1", "wp-2", "wp-3"}, met: []string{"ac-1", "ac-2", "ac-3"}})
	return e
}

// publish writes the projection as a finished run left it.
func (e *smokeEnv) publish(p reconProjected) {
	e.t.Helper()
	writeReconProjection(e.t, e.host.DeliveryProjectionPath(e.intent.ProjectID), p)
}

// stage sets what the next refresh will install.
func (e *smokeEnv) stage(p reconProjected) {
	e.t.Helper()
	writeReconProjection(e.t, e.renderPath, p)
}

// run invokes the real binary and returns its exit code, stdout and stderr.
func (e *smokeEnv) run(args ...string) (int, string, string) {
	e.t.Helper()
	cmd := exec.Command(e.bin, append(args, "-host", e.hostPath)...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run()
	return cmd.ProcessState.ExitCode(), stdout.String(), stderr.String()
}

// status runs `delivery status` and parses the JSON a portal would parse.
func (e *smokeEnv) status() (int, handoff.Status) {
	e.t.Helper()
	code, out, errOut := e.run("status", "-project", e.intent.ProjectID)
	var st handoff.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		e.t.Fatalf("status did not print parseable JSON: %v\nstdout:\n%s\nstderr:\n%s", err, out, errOut)
	}
	return code, st
}

// COMPLETE -> new evidence disproves a required criterion -> NOT COMPLETE ->
// governed remediation -> COMPLETE again. One project throughout.
func TestReconciliationLifecycleThroughTheBinary(t *testing.T) {
	e := newSmokeEnv(t)
	planPath := handoff.PlanPath(e.root, e.intent.ProjectID)
	planBefore, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}

	// --- COMPLETE ----------------------------------------------------------
	code, before := e.status()
	if code != exitOK || before.State != handoff.StateCompleted {
		t.Fatalf("status exited %d with state %q, want %d/%q", code, before.State, exitOK, handoff.StateCompleted)
	}
	if before.CriteriaMet != 3 || before.CriteriaTotal != 3 || before.PackagesComplete != 3 {
		t.Fatalf("before: criteria %d/%d packages %d/%d, want 3/3 and 3/3",
			before.CriteriaMet, before.CriteriaTotal, before.PackagesComplete, before.PackagesTotal)
	}
	t.Logf("BEFORE       exit=%d state=%s criteria=%d/%d packages=%d/%d",
		code, before.State, before.CriteriaMet, before.CriteriaTotal,
		before.PackagesComplete, before.PackagesTotal)

	// --- the evidence that disproves ac-3 ----------------------------------
	e.stage(reconProjected{merged: []string{"wp-1", "wp-2", "wp-3"}, met: []string{"ac-1", "ac-2"}})
	code, out, errOut := e.run("invalidate",
		"-project", e.intent.ProjectID,
		"-criterion", "ac-3",
		"-by", "Jon Pratten",
		"-reason", "the profiler never reports a mixed column: a column of integers and text is reported integer",
		"-evidence", "https://github.com/CorsolvSolutions/reconciliation-probe/issues/12")
	if code != exitHumanBoundary {
		t.Fatalf("invalidate exited %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitHumanBoundary, out, errOut)
	}
	if !strings.Contains(out, "INVALIDATED ac-3 (invalidation 1) by Jon Pratten") {
		t.Fatalf("invalidate did not report what it recorded:\n%s", out)
	}

	// --- NOT COMPLETE ------------------------------------------------------
	code, disputed := e.status()
	if code != exitHumanBoundary {
		t.Fatalf("status exited %d after an invalidation, want %d", code, exitHumanBoundary)
	}
	if disputed.State == handoff.StateCompleted {
		t.Fatal("the project is still Complete over a disproved criterion")
	}
	if disputed.CriteriaMet != 2 {
		t.Fatalf("criteria = %d/%d, want 2/3", disputed.CriteriaMet, disputed.CriteriaTotal)
	}
	if len(disputed.Evidence.Invalidated) != 1 || disputed.Evidence.Invalidated[0].By != "Jon Pratten" {
		t.Fatalf("the status does not carry the finding: %+v", disputed.Evidence.Invalidated)
	}
	// Unrelated criteria and unrelated merged work are untouched.
	if got := strings.Join(disputed.Evidence.AcceptanceMet, ","); got != "ac-1,ac-2" {
		t.Fatalf("AcceptanceMet = %q, want ac-1,ac-2", got)
	}
	if got := strings.Join(disputed.Evidence.CompletePackages, ","); got != "wp-1,wp-2,wp-3" {
		t.Fatalf("CompletePackages = %q — merged work must not be reopened", got)
	}
	t.Logf("DISPUTED     exit=%d state=%s criteria=%d/%d packages=%d/%d",
		code, disputed.State, disputed.CriteriaMet, disputed.CriteriaTotal,
		disputed.PackagesComplete, disputed.PackagesTotal)

	// --- a narrower repair is refused --------------------------------------
	narrow := reconRemediation(e.intent)
	narrow.Packages[0].Objective = "Rewrite src/types.ts to infer text, integer and date columns."
	code, _, errOut = e.run("remediate", "-project", e.intent.ProjectID,
		"-from", writeRemediation(t, t.TempDir(), narrow), "-by", "Jon Pratten")
	if code != exitRefused {
		t.Fatalf("a narrower repair exited %d, want %d:\n%s", code, exitRefused, errOut)
	}
	if !strings.Contains(errOut, `"mixed"`) {
		t.Fatalf("the refusal does not name the dropped behavior:\n%s", errOut)
	}
	t.Logf("FIDELITY     exit=%d refused: %s", code,
		strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(errOut), "REFUSED")))

	// --- governed, additive remediation ------------------------------------
	e.stage(reconProjected{
		merged:  []string{"wp-1", "wp-2", "wp-3"},
		planned: []string{"wp-3-fix"},
		met:     []string{"ac-1", "ac-2"},
	})
	code, out, errOut = e.run("remediate", "-project", e.intent.ProjectID,
		"-from", writeRemediation(t, t.TempDir(), reconRemediation(e.intent)), "-by", "Jon Pratten")
	if code != exitHumanBoundary {
		t.Fatalf("remediate exited %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitHumanBoundary, out, errOut)
	}
	if !strings.Contains(out, "REMEDIATION 1 authorized by Jon Pratten") {
		t.Fatalf("remediate did not report what it authorized:\n%s", out)
	}

	code, planning := e.status()
	if planning.PackagesTotal != 4 || planning.PackagesComplete != 3 {
		t.Fatalf("packages = %d/%d, want 3/4", planning.PackagesComplete, planning.PackagesTotal)
	}
	t.Logf("REMEDIATING  exit=%d state=%s criteria=%d/%d packages=%d/%d repair=%v",
		code, planning.State, planning.CriteriaMet, planning.CriteriaTotal,
		planning.PackagesComplete, planning.PackagesTotal,
		planning.Evidence.Invalidated[0].RemedialPackages)

	// `delivery plan` still prints exactly what was planned, and says the
	// corrective work exists rather than folding it in.
	code, planOut, planErr := e.run("plan", "-project", e.intent.ProjectID)
	if code != exitOK {
		t.Fatalf("plan exited %d:\n%s", code, planErr)
	}
	if strings.Contains(planOut, "wp-3-fix") {
		t.Fatalf("`delivery plan` folded corrective work into the original plan:\n%s", planOut)
	}
	if !strings.Contains(planErr, "1 authorized remediation(s) adding 1 work package(s)") {
		t.Fatalf("`delivery plan` did not name the corrective work beside it:\n%s", planErr)
	}

	// --- the remedial package executes, is gated and merges -----------------
	e.publish(reconProjected{
		merged: []string{"wp-1", "wp-2", "wp-3", "wp-3-fix"},
		met:    []string{"ac-1", "ac-2", "ac-3"},
	})

	// --- COMPLETE AGAIN ----------------------------------------------------
	code, after := e.status()
	if code != exitOK || after.State != handoff.StateCompleted {
		t.Fatalf("status exited %d with state %q, want %d/%q: %v",
			code, after.State, exitOK, handoff.StateCompleted, after.Evidence.Reasons)
	}
	if after.CriteriaMet != 3 || after.PackagesComplete != 4 || after.PackagesTotal != 4 {
		t.Fatalf("after: criteria %d/%d packages %d/%d, want 3/3 and 4/4",
			after.CriteriaMet, after.CriteriaTotal, after.PackagesComplete, after.PackagesTotal)
	}
	if len(after.Evidence.Invalidated) != 0 {
		t.Fatalf("an answered finding is still reported standing: %+v", after.Evidence.Invalidated)
	}
	t.Logf("AFTER        exit=%d state=%s criteria=%d/%d packages=%d/%d",
		code, after.State, after.CriteriaMet, after.CriteriaTotal,
		after.PackagesComplete, after.PackagesTotal)

	// --- the audit trail, on disk ------------------------------------------
	planAfter, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(planBefore) != string(planAfter) {
		t.Fatalf("the original plan was rewritten:\n--- before\n%s\n--- after\n%s", planBefore, planAfter)
	}
	code, remOut, _ := e.run("remediate", "-project", e.intent.ProjectID)
	if code != exitOK {
		t.Fatalf("listing remediations exited %d", code)
	}
	for _, want := range []string{`"criterionId": "ac-3"`, `"invalidation": 1`, `"authorizedBy": "Jon Pratten"`} {
		if !strings.Contains(remOut, want) {
			t.Fatalf("the authorization record is missing %s:\n%s", want, remOut)
		}
	}
	rec, _, err := handoff.LoadRecord(e.root, e.intent.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Invalidations) != 1 || len(rec.Runs) != 1 {
		t.Fatalf("history was not preserved: %d finding(s), %d run(s)", len(rec.Invalidations), len(rec.Runs))
	}
	t.Logf("AUDIT        plan unchanged (%d bytes) -> invalidation %d by %s -> remediation 1 -> repaired",
		len(planAfter), rec.Invalidations[0].Seq, rec.Invalidations[0].By)
}
