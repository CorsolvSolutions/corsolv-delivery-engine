package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/handoff"
	"github.com/gastownhall/gascity/internal/unattended"
)

// THE DEFECT THESE TESTS EXIST FOR.
//
// A delivery-owned criterion can be reported met, and the project can reach
// Complete on the strength of it. Later evidence can then prove that criterion
// was never actually satisfied — and until this packet there was no supported
// way for the engine to say so.
//
// The two operations that look like they should express it both refuse, and
// both refusals are correct on their own terms:
//
//   - `delivery accept` refuses, because a delivery-owned criterion accepted by
//     hand is forged evidence.
//   - `delivery plan -from` refuses, because the project already has a plan and
//     merged work must not have its history rewritten.
//
// Together they left the truth unstateable. The only remaining moves were
// hand-editing durable state, rewriting history, or leaving a false completion
// standing. TestNoSupportedTransitionMakesAMetCriterionUnmet pins that gap as
// it was; every other test in this file pins the mechanism that closes it.

// reconRunID is the run identity the fixture's finished delivery already has.
// Nothing in this packet may mint another.
const reconRunID = "reconciliation-probe-20260821T090000Z"

// reconIntent is the synthetic project: three criteria, all of them delivery's
// own to satisfy and prove.
//
// ac-3 carries MustCover because the fidelity control merged just before this
// packet has to keep applying to REMEDIAL work. A criterion whose required
// behaviors were disproved must not be repaired by a plan that quietly drops
// them, and a fixture with no required behaviors could not prove that.
func reconIntent() handoff.Intent {
	return handoff.Intent{
		SchemaVersion: handoff.SchemaVersion,
		ProjectID:     "reconciliation-probe",
		Repository: handoff.Repository{
			Slug:          "CorsolvSolutions/reconciliation-probe",
			Origin:        "https://github.com/CorsolvSolutions/reconciliation-probe.git",
			DefaultBranch: "main",
		},
		Checkout:  `D:\Development\reconciliation-probe`,
		Objective: "Prove a criterion reported met can be disproved by later evidence and repaired in place.",
		Lifecycle: []string{"Build", "Release"},
		Acceptance: []handoff.Criterion{
			{ID: "ac-1", Statement: "The tool reads a CSV file and reports its row and column counts."},
			{ID: "ac-2", Statement: "The tool writes its report to a file the caller names."},
			{
				ID:        "ac-3",
				Statement: "The report states an inferred type for every column.",
				MustCover: []string{"text", "integer", "decimal|number", "boolean", "date", "mixed"},
			},
		},
		Policy: handoff.Policy{
			NeedPush: true, NeedPR: true, NeedChecks: true, NeedMerge: true,
		},
		RequestedBy: "portal",
		RequestedAt: time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC),
	}
}

// reconPlan is the plan the delivery actually ran: one package per criterion.
//
// wp-3 names every behavior ac-3 requires, so it passes the fidelity check —
// which is the point. The plan was honest about what it intended to deliver;
// what it delivered is what later evidence disproves.
func reconPlan(in handoff.Intent) handoff.DeliveryPlan {
	return handoff.DeliveryPlan{
		SchemaVersion: handoff.PlanSchemaVersion,
		ProjectID:     in.ProjectID,
		PlannedBy:     "agent:planner-1",
		PlannedAt:     time.Date(2026, 8, 21, 9, 5, 0, 0, time.UTC),
		Packages: []handoff.WorkPackage{
			{
				ID:              "wp-1",
				Title:           "Read the CSV and count its rows and columns",
				Phase:           "Build",
				Objective:       "Create src/read.ts, which parses a CSV file and reports its row and column counts.",
				Artifact:        "src/read.ts",
				AuthorizedPaths: []string{"src/read.ts", "src/read.test.ts"},
				Gates:           []string{"npm test"},
				Satisfies:       []string{"ac-1"},
			},
			{
				ID:              "wp-2",
				Title:           "Write the report to the named file",
				Phase:           "Build",
				Objective:       "Create src/report.ts, which writes the profile to the output path the caller names.",
				Artifact:        "src/report.ts",
				AuthorizedPaths: []string{"src/report.ts", "src/report.test.ts"},
				Gates:           []string{"npm test"},
				Satisfies:       []string{"ac-2"},
			},
			{
				ID:    "wp-3",
				Title: "Infer a type for every column",
				Phase: "Build",
				Objective: "Create src/types.ts, which infers each column's type as text, integer, " +
					"decimal, boolean or date, and reports mixed where a column holds more than one.",
				Artifact:        "src/types.ts",
				AuthorizedPaths: []string{"src/types.ts", "src/types.test.ts"},
				Gates:           []string{"npm test"},
				Satisfies:       []string{"ac-3"},
			},
		},
	}
}

// reconFixture is a delivery that has FINISHED: every package merged with a met
// completion gate, every criterion met, and the project Complete.
type reconFixture struct {
	host     handoff.HostProfile
	hostPath string
	intent   handoff.Intent
	plan     handoff.DeliveryPlan
	log      string
}

// newReconFixture builds that finished delivery on disk.
//
// The driver is a stub whose body the caller supplies, for the same reason the
// acceptance-refresh fixture uses one: what these tests assert is which stages
// a transition runs, and a stub is the only thing that can prove a stage did
// NOT run.
func newReconFixture(t *testing.T, driverBody string) reconFixture {
	t.Helper()
	if os.PathSeparator == '\\' {
		t.Skip("the driver runs on the delivery host, which is POSIX")
	}

	root := t.TempDir()
	logPath := filepath.Join(root, "driver-calls.log")

	driver := filepath.Join(root, "driver.sh")
	body := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n" + driverBody
	if err := os.WriteFile(driver, []byte(body), 0o755); err != nil { //nolint:gosec // a stub must be executable
		t.Fatal(err)
	}

	host := handoff.HostProfile{DeliveryRoot: root, Driver: driver, Provider: "claude"}
	hostPath := filepath.Join(root, "host.toml")
	profile := "deliveryRoot = " + tomlQuote(root) + "\n" +
		"driver = " + tomlQuote(driver) + "\n" +
		"provider = \"claude\"\n"
	if err := os.WriteFile(hostPath, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}

	in := reconIntent()
	plan := reconPlan(in)
	if err := handoff.SaveIntent(root, in); err != nil {
		t.Fatal(err)
	}
	if err := handoff.SavePlan(root, plan); err != nil {
		t.Fatal(err)
	}

	adm, err := handoff.Admit(root, in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	record := adm.Record.AppendRun(reconRunID, handoff.ReasonInitial, time.Now())
	if err := handoff.SaveRecord(root, record); err != nil {
		t.Fatal(err)
	}

	writeReconProjection(t, host.DeliveryProjectionPath(in.ProjectID), reconProjected{
		merged: []string{"wp-1", "wp-2", "wp-3"},
		met:    []string{"ac-1", "ac-2", "ac-3"},
	})

	if err := os.MkdirAll(host.StateDir(in.ProjectID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unattended.WriteCompletion(host.StateDir(in.ProjectID), unattended.CompletionEvent{
		RunID:     reconRunID,
		ProjectID: in.ProjectID,
		Outcome:   unattended.RunCompleted,
		Reason:    "every declared task succeeded",
	}); err != nil {
		t.Fatal(err)
	}

	return reconFixture{host: host, hostPath: hostPath, intent: in, plan: plan, log: logPath}
}

// reconProjected is the state of the published document a test wants written.
type reconProjected struct {
	// merged names the packages the document reports merged with a met gate.
	merged []string
	// planned names packages the document reports as not yet started.
	planned []string
	// met names the deliverables the document reports satisfied.
	met []string
}

// writeReconProjection writes PROJECT-STATE.yml as a finished run would leave it.
func writeReconProjection(t *testing.T, path string, p reconProjected) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("schemaVersion: 1\nproject:\n")
	b.WriteString("  projectId: \"reconciliation-probe\"\n")
	b.WriteString("  latestAcceptedMainSha: \"a1b2c3d4\"\n")
	b.WriteString("activeTasks:\n")
	for _, id := range p.merged {
		b.WriteString("  - taskId: \"" + id + "\"\n    status: \"merged\"\n    completionGateStatus: \"met\"\n")
	}
	for _, id := range p.planned {
		b.WriteString("  - taskId: \"" + id + "\"\n    status: \"planned\"\n    completionGateStatus: \"not-met\"\n")
	}
	b.WriteString("deliverables:\n")
	met := map[string]bool{}
	for _, id := range p.met {
		met[id] = true
	}
	for _, id := range []string{"ac-1", "ac-2", "ac-3"} {
		b.WriteString("  - deliverableId: \"" + id + "\"\n")
		if met[id] {
			b.WriteString("    met: true\n")
		} else {
			b.WriteString("    met: false\n")
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f reconFixture) status(t *testing.T) handoff.Status {
	t.Helper()
	st, err := observe(f.host, f.intent.ProjectID)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	return st
}

func (f reconFixture) record(t *testing.T) handoff.Record {
	t.Helper()
	r, found, err := handoff.LoadRecord(f.host.DeliveryRoot, f.intent.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("the fixture's delivery record is gone")
	}
	return r
}

// captureStderr runs fn with os.Stderr redirected, and returns what it wrote.
//
// The refusals are the product here: this packet's first obligation is to
// record the exact words the engine used to say no, so a later reader can tell
// this gap from a superficially similar one.
func captureStderr(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	saved := os.Stderr
	tmp, err := os.CreateTemp(t.TempDir(), "stderr-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = tmp
	code := fn()
	os.Stderr = saved
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(out)
}

// narrowerPlan is the replacement a person would reach for: a plan that repairs
// ac-3 and drops two of the behaviors it requires.
func narrowerPlan(in handoff.Intent) handoff.DeliveryPlan {
	return handoff.DeliveryPlan{
		SchemaVersion: handoff.PlanSchemaVersion,
		ProjectID:     in.ProjectID,
		PlannedBy:     "operator",
		PlannedAt:     time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC),
		Packages: []handoff.WorkPackage{{
			ID:              "wp-3-fix",
			Title:           "Repair the inferred column type",
			Phase:           "Build",
			Objective:       "Rewrite src/types.ts to infer text, integer and date columns.",
			Artifact:        "src/types.ts",
			AuthorizedPaths: []string{"src/types.ts"},
			Gates:           []string{"npm test"},
			Satisfies:       []string{"ac-3"},
		}},
	}
}

// FIRST REPRODUCE. A finished, Complete delivery whose ac-3 later evidence
// disproves has no supported transition that can say so.
//
// This test is the durable record of the gap AND a regression on the two
// refusals, which must both survive this packet: the fix is a third supported
// operation, never a hole in either of these.
func TestNoSupportedTransitionMakesAMetCriterionUnmet(t *testing.T) {
	f := newReconFixture(t, `echo "no stage may run: $1" >&2; exit 9`)

	before := f.status(t)
	if before.State != handoff.StateCompleted {
		t.Fatalf("the fixture must start Complete, got %q: %v", before.State, before.Evidence.Reasons)
	}
	if got := strings.Join(before.Evidence.AcceptanceMet, ","); got != "ac-1,ac-2,ac-3" {
		t.Fatalf("AcceptanceMet = %q, want all three", got)
	}
	if before.PackagesComplete != 3 || before.PackagesTotal != 3 {
		t.Fatalf("progress = %d/%d, want 3/3", before.PackagesComplete, before.PackagesTotal)
	}

	// PATH 1. A person signing off a delivery-owned criterion.
	code, out := captureStderr(t, func() int {
		return cmdAccept(t.Context(), []string{
			"-project", f.intent.ProjectID,
			"-criterion", "ac-3",
			"-by", "Jon Pratten",
			"-note", "new evidence shows this was never true",
			"-host", f.hostPath,
		})
	})
	if code != exitRefused {
		t.Fatalf("accept exited %d, want %d", code, exitRefused)
	}
	if !strings.Contains(out, "delivery's to satisfy and prove") {
		t.Fatalf("accept refused for an unexpected reason:\n%s", out)
	}
	t.Logf("REFUSAL 1 — delivery accept -criterion ac-3:\n%s", strings.TrimSpace(out))

	// PATH 2. Replacing the plan with one that repairs ac-3.
	replacement := filepath.Join(t.TempDir(), "replacement.json")
	data, err := handoff.MarshalPlan(narrowerPlan(f.intent))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, data, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out = captureStderr(t, func() int {
		return cmdPlan([]string{
			"-project", f.intent.ProjectID,
			"-from", replacement,
			"-host", f.hostPath,
		})
	})
	if code != exitRefused {
		t.Fatalf("plan -from exited %d, want %d", code, exitRefused)
	}
	if !strings.Contains(out, "already has a plan") {
		t.Fatalf("plan refused for an unexpected reason:\n%s", out)
	}
	t.Logf("REFUSAL 2 — delivery plan -from <replacement>:\n%s", strings.TrimSpace(out))

	// AND THE GAP ITSELF. Both refusals left the project exactly as it was:
	// Complete, 3 of 3, with a criterion the evidence says is false.
	after := f.status(t)
	if after.State != handoff.StateCompleted {
		t.Fatalf("State = %q — neither refusal may change the project", after.State)
	}
	if after.PackagesComplete != 3 {
		t.Fatalf("progress = %d/3 — neither refusal may change the project", after.PackagesComplete)
	}
	if calls := f.driverCalls(t); len(calls) != 0 {
		t.Fatalf("a refused operation invoked the driver: %v", calls)
	}
}

// driverCalls reports which stages the stub driver was asked to run.
func (f reconFixture) driverCalls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			calls = append(calls, line)
		}
	}
	return calls
}

// renderPath is where a test stages the document the stub's project stage
// installs, standing in for the real stage re-rendering it from the record.
func (f reconFixture) renderPath() string {
	return filepath.Join(f.host.DeliveryRoot, "rendered.yml")
}

// renderingFixture is the finished delivery with a driver whose project stage
// installs whatever the test has staged — which is what the real one does, from
// the record and the plan. What the tests below assert is the transition and
// which stages ran; internal/projector pins the rendering rule itself.
func renderingFixture(t *testing.T) reconFixture {
	t.Helper()
	f := newReconFixture(t, "")
	body := `case "$1" in
  project) cp -f ` + shellQuote(f.renderPath()) + ` ` +
		shellQuote(f.host.DeliveryProjectionPath(f.intent.ProjectID)) + ` ;;
  publish-projection) : ;;
  *) echo "no stage may run here but project and publish-projection: $1" >&2; exit 9 ;;
esac
`
	driver := filepath.Join(f.host.DeliveryRoot, "driver.sh")
	full := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> " + shellQuote(f.log) + "\n" + body
	if err := os.WriteFile(driver, []byte(full), 0o755); err != nil { //nolint:gosec // a stub must be executable
		t.Fatal(err)
	}
	// The document as the finished run left it, staged for the first refresh.
	writeReconProjection(t, f.renderPath(), reconProjected{
		merged: []string{"wp-1", "wp-2", "wp-3"},
		met:    []string{"ac-1", "ac-2", "ac-3"},
	})
	return f
}

// stage sets what the next refresh will install as the published projection.
func (f reconFixture) stage(t *testing.T, p reconProjected) {
	t.Helper()
	writeReconProjection(t, f.renderPath(), p)
}

func (f reconFixture) invalidate(t *testing.T, criterion, by, reason, evidence string) (int, string) {
	t.Helper()
	return captureStderr(t, func() int {
		return cmdInvalidate(t.Context(), []string{
			"-project", f.intent.ProjectID,
			"-criterion", criterion,
			"-by", by,
			"-reason", reason,
			"-evidence", evidence,
			"-host", f.hostPath,
		})
	})
}

// writeRemediation stages a remediation document for cmdRemediate to authorize.
//
// It declares only what a person decides — which finding it repairs, and the
// work that repairs it. Position, timestamp and author are the engine's, and a
// document that named them would be refused.
func writeRemediation(t *testing.T, dir string, rm handoff.Remediation) string {
	t.Helper()
	data, err := json.MarshalIndent(rm, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "remediation.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// reconRemediation is the corrective work for ac-3: it repairs the file the
// disproved package produced, and it carries every behavior ac-3 requires.
//
// It answers the fixture's FIRST finding, which is the only one these tests
// raise. A test needing a remediation against a different finding writes its
// own rather than parameterizing this one into something that reads like a
// general builder.
const reconInvalidation = 1

func reconRemediation(in handoff.Intent) handoff.Remediation {
	return handoff.Remediation{
		SchemaVersion: handoff.RemediationSchemaVersion,
		ProjectID:     in.ProjectID,
		Repairs:       []handoff.Repair{{CriterionID: "ac-3", Invalidation: reconInvalidation}},
		Packages: []handoff.WorkPackage{{
			ID:    "wp-3-fix",
			Title: "Repair the inferred column type",
			Phase: "Build",
			Objective: "Rewrite src/types.ts so every column is inferred as text, integer, decimal, " +
				"boolean or date, and a column holding more than one value type is reported as mixed.",
			Artifact:        "src/types.ts",
			AuthorizedPaths: []string{"src/types.ts", "src/types.test.ts"},
			Gates:           []string{"npm test"},
			Satisfies:       []string{"ac-3"},
		}},
	}
}

// B at the command surface. Each of the three things that make this a governed
// act is required, and a refused invalidation publishes nothing.
func TestInvalidateRequiresAnActorAReasonAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		by, reason, evidence string
	}{
		{"no actor", "", "the report has no mixed type", "issue-12"},
		{"no reason", "Jon Pratten", "", "issue-12"},
		{"no evidence", "Jon Pratten", "the report has no mixed type", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReconFixture(t, `echo "no stage may run for a refused invalidation: $1" >&2; exit 9`)
			code, out := f.invalidate(t, "ac-3", tc.by, tc.reason, tc.evidence)
			if code != exitUsage {
				t.Fatalf("exited %d, want %d:\n%s", code, exitUsage, out)
			}
			if calls := f.driverCalls(t); len(calls) != 0 {
				t.Fatalf("a refused invalidation invoked the driver: %v", calls)
			}
			if st := f.status(t); st.State != handoff.StateCompleted {
				t.Fatalf("State = %q — a refused invalidation changes nothing", st.State)
			}
			if rec := f.record(t); len(rec.Invalidations) != 0 {
				t.Fatalf("a refused invalidation reached the record: %+v", rec.Invalidations)
			}
		})
	}
}

// I, H. A successful invalidation refreshes the projection through the run's own
// two evidence stages, and runs nothing that executes work.
func TestInvalidateRefreshesTheProjectionThroughTheRunsOwnStages(t *testing.T) {
	f := renderingFixture(t)
	f.stage(t, reconProjected{
		merged: []string{"wp-1", "wp-2", "wp-3"},
		met:    []string{"ac-1", "ac-2"},
	})

	code, out := f.invalidate(t, "ac-3", "Jon Pratten",
		"the report never reports a mixed column; a column of integers and text is reported integer",
		"https://github.com/CorsolvSolutions/reconciliation-probe/issues/12")
	if code != exitHumanBoundary {
		t.Fatalf("invalidate exited %d, want %d:\n%s", code, exitHumanBoundary, out)
	}

	calls := f.driverCalls(t)
	if len(calls) != 2 {
		t.Fatalf("the driver was invoked %d time(s), want exactly 2:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	if !strings.HasPrefix(calls[0], handoff.StageProject+" ") {
		t.Fatalf("first stage = %q, want %s", calls[0], handoff.StageProject)
	}
	if !strings.HasPrefix(calls[1], handoff.StagePublishProjection+" ") {
		t.Fatalf("second stage = %q, want %s", calls[1], handoff.StagePublishProjection)
	}
	for _, forbidden := range []string{
		handoff.StageCityUp, handoff.StageDispatch, handoff.StageAwait, handoff.StagePublish,
	} {
		for _, call := range calls {
			if strings.HasPrefix(call, forbidden+" ") {
				t.Fatalf("the refresh ran %q — an invalidation may not execute work", forbidden)
			}
		}
	}

	// The run identity is the one the delivery already had. Nothing here mints a
	// run, and nothing reopens the finished one.
	rec := f.record(t)
	if len(rec.Runs) != 1 || rec.Runs[0].RunID != reconRunID {
		t.Fatalf("runs = %+v, want exactly the original %q", rec.Runs, reconRunID)
	}

	// H. The merged packages are untouched.
	after := f.status(t)
	if got := strings.Join(after.Evidence.CompletePackages, ","); got != "wp-1,wp-2,wp-3" {
		t.Fatalf("CompletePackages = %v — an invalidation reopens no work", after.Evidence.CompletePackages)
	}
	if len(after.Evidence.OutstandingPackages) != 0 {
		t.Fatalf("OutstandingPackages = %v, want none", after.Evidence.OutstandingPackages)
	}
	// And the published document reflects the change, so the portal can too.
	doc, err := os.ReadFile(f.host.DeliveryProjectionPath(f.intent.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "deliverableId: \"ac-3\"\n    met: false") {
		t.Fatalf("the published projection still reports ac-3 met:\n%s", doc)
	}
}

// J. The status a portal or an operator reads says WHY the criterion is unmet,
// in the finding's own words, rather than listing it among ordinary unfinished
// work.
func TestStatusReportsWhyAnInvalidatedCriterionIsUnmet(t *testing.T) {
	f := renderingFixture(t)
	f.stage(t, reconProjected{merged: []string{"wp-1", "wp-2", "wp-3"}, met: []string{"ac-1", "ac-2"}})
	if code, out := f.invalidate(t, "ac-3", "Jon Pratten",
		"no column is ever reported as mixed", "issue-12"); code != exitHumanBoundary {
		t.Fatalf("invalidate exited %d:\n%s", code, out)
	}

	st := f.status(t)
	if len(st.Evidence.Invalidated) != 1 {
		t.Fatalf("Evidence.Invalidated = %+v, want one entry", st.Evidence.Invalidated)
	}
	inv := st.Evidence.Invalidated[0]
	switch {
	case inv.CriterionID != "ac-3":
		t.Fatalf("CriterionID = %q", inv.CriterionID)
	case inv.By != "Jon Pratten":
		t.Fatalf("By = %q", inv.By)
	case inv.Reason != "no column is ever reported as mixed":
		t.Fatalf("Reason = %q", inv.Reason)
	case inv.Evidence != "issue-12":
		t.Fatalf("Evidence = %q", inv.Evidence)
	case inv.PreviousState != handoff.CriterionMet:
		t.Fatalf("PreviousState = %q", inv.PreviousState)
	}

	// The JSON a portal parses carries it, so the reason survives the wire.
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"invalidated"`, `"no column is ever reported as mixed"`, `"issue-12"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("the status JSON omits %s:\n%s", want, data)
		}
	}
	var explained bool
	for _, r := range st.Evidence.Reasons {
		if strings.Contains(r, "ac-3 was met and is no longer") {
			explained = true
		}
	}
	if !explained {
		t.Fatalf("no reason explains the change: %v", st.Evidence.Reasons)
	}
}

// THE LIFECYCLE. One project, start to finish:
//
//	3/3 Complete
//	-> new evidence disproves ac-3
//	-> 2/3, not Complete
//	-> additive remedial work authorized
//	-> the remedial package merges with its gate met
//	-> 3/3 Complete
//
// F, H, K, N, O, P.
func TestCompleteToIncompleteToCompleteOnTheSameProject(t *testing.T) {
	f := renderingFixture(t)

	planBefore, err := os.ReadFile(handoff.PlanPath(f.host.DeliveryRoot, f.intent.ProjectID))
	if err != nil {
		t.Fatal(err)
	}

	// --- 3/3 Complete ------------------------------------------------------
	before := f.status(t)
	if before.State != handoff.StateCompleted {
		t.Fatalf("State = %q, want %q: %v", before.State, handoff.StateCompleted, before.Evidence.Reasons)
	}
	if before.CriteriaMet != 3 || before.CriteriaTotal != 3 {
		t.Fatalf("criteria = %d/%d, want 3/3", before.CriteriaMet, before.CriteriaTotal)
	}
	if before.PackagesComplete != 3 || before.PackagesTotal != 3 {
		t.Fatalf("packages = %d/%d, want 3/3", before.PackagesComplete, before.PackagesTotal)
	}
	t.Logf("BEFORE      state=%s criteria=%d/%d packages=%d/%d",
		before.State, before.CriteriaMet, before.CriteriaTotal,
		before.PackagesComplete, before.PackagesTotal)

	// --- new evidence disproves ac-3 ---------------------------------------
	f.stage(t, reconProjected{merged: []string{"wp-1", "wp-2", "wp-3"}, met: []string{"ac-1", "ac-2"}})
	code, out := f.invalidate(t, "ac-3", "Jon Pratten",
		"the profiler never reports a mixed column: a column of integers and text is reported integer",
		"https://github.com/CorsolvSolutions/reconciliation-probe/issues/12")
	if code != exitHumanBoundary {
		t.Fatalf("invalidate exited %d, want %d:\n%s", code, exitHumanBoundary, out)
	}

	// F. Below 100%, and no longer Complete.
	disputed := f.status(t)
	if disputed.State == handoff.StateCompleted {
		t.Fatal("the project is still Complete over a disproved criterion")
	}
	if disputed.CriteriaMet != 2 || disputed.CriteriaTotal != 3 {
		t.Fatalf("criteria = %d/%d, want 2/3", disputed.CriteriaMet, disputed.CriteriaTotal)
	}
	// G, H. Everything else is exactly as it was.
	if got := strings.Join(disputed.Evidence.AcceptanceMet, ","); got != "ac-1,ac-2" {
		t.Fatalf("AcceptanceMet = %q, want ac-1,ac-2", got)
	}
	if got := strings.Join(disputed.Evidence.CompletePackages, ","); got != "wp-1,wp-2,wp-3" {
		t.Fatalf("CompletePackages = %q — no merged work may be reopened", got)
	}
	t.Logf("DISPUTED    state=%s criteria=%d/%d packages=%d/%d why=%q",
		disputed.State, disputed.CriteriaMet, disputed.CriteriaTotal,
		disputed.PackagesComplete, disputed.PackagesTotal,
		disputed.Evidence.Invalidated[0].Reason)

	// --- additive remedial work authorized ---------------------------------
	doc := writeRemediation(t, t.TempDir(), reconRemediation(f.intent))
	f.stage(t, reconProjected{
		merged:  []string{"wp-1", "wp-2", "wp-3"},
		planned: []string{"wp-3-fix"},
		met:     []string{"ac-1", "ac-2"},
	})
	code, out = captureStderr(t, func() int {
		return cmdRemediate(t.Context(), []string{
			"-project", f.intent.ProjectID,
			"-from", doc,
			"-by", "Jon Pratten",
			"-host", f.hostPath,
		})
	})
	if code != exitHumanBoundary {
		t.Fatalf("remediate exited %d, want %d:\n%s", code, exitHumanBoundary, out)
	}

	planning := f.status(t)
	if planning.State == handoff.StateCompleted {
		t.Fatal("the project is Complete with its repair unfinished")
	}
	if planning.PackagesTotal != 4 || planning.PackagesComplete != 3 {
		t.Fatalf("packages = %d/%d, want 3/4", planning.PackagesComplete, planning.PackagesTotal)
	}
	if got := strings.Join(planning.Evidence.OutstandingPackages, ","); got != "wp-3-fix" {
		t.Fatalf("OutstandingPackages = %q, want wp-3-fix alone", got)
	}
	if len(planning.Evidence.Invalidated) != 1 ||
		strings.Join(planning.Evidence.Invalidated[0].RemedialPackages, ",") != "wp-3-fix" {
		t.Fatalf("Invalidated = %+v, want the repair named", planning.Evidence.Invalidated)
	}
	t.Logf("REMEDIATING state=%s criteria=%d/%d packages=%d/%d repair=%v",
		planning.State, planning.CriteriaMet, planning.CriteriaTotal,
		planning.PackagesComplete, planning.PackagesTotal,
		planning.Evidence.Invalidated[0].RemedialPackages)

	// K. The original plan is byte-identical to what was written before any of
	// this happened.
	planAfter, err := os.ReadFile(handoff.PlanPath(f.host.DeliveryRoot, f.intent.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	if string(planBefore) != string(planAfter) {
		t.Fatalf("the original plan was rewritten:\n--- before\n%s\n--- after\n%s", planBefore, planAfter)
	}

	// --- the remedial package executes, is gated and merges -----------------
	writeReconProjection(t, f.host.DeliveryProjectionPath(f.intent.ProjectID), reconProjected{
		merged: []string{"wp-1", "wp-2", "wp-3", "wp-3-fix"},
		met:    []string{"ac-1", "ac-2", "ac-3"},
	})

	// N, O. The criterion is met again, and the project is Complete again.
	after := f.status(t)
	if after.State != handoff.StateCompleted {
		t.Fatalf("State = %q, want %q: %v", after.State, handoff.StateCompleted, after.Evidence.Reasons)
	}
	if after.CriteriaMet != 3 || after.CriteriaTotal != 3 {
		t.Fatalf("criteria = %d/%d, want 3/3", after.CriteriaMet, after.CriteriaTotal)
	}
	if after.PackagesComplete != 4 || after.PackagesTotal != 4 {
		t.Fatalf("packages = %d/%d, want 4/4", after.PackagesComplete, after.PackagesTotal)
	}
	if len(after.Evidence.Invalidated) != 0 {
		t.Fatalf("Invalidated = %+v — an answered finding is not a standing one", after.Evidence.Invalidated)
	}
	t.Logf("AFTER       state=%s criteria=%d/%d packages=%d/%d",
		after.State, after.CriteriaMet, after.CriteriaTotal,
		after.PackagesComplete, after.PackagesTotal)

	// --- P. The audit trail ------------------------------------------------
	rec := f.record(t)
	if len(rec.Invalidations) != 1 {
		t.Fatalf("invalidations = %+v, want the one finding", rec.Invalidations)
	}
	inv := rec.Invalidations[0]
	if inv.By != "Jon Pratten" || inv.PreviousState != handoff.CriterionMet || inv.Seq != 1 {
		t.Fatalf("the finding is not intact: %+v", inv)
	}
	if len(rec.Runs) != 1 || rec.Runs[0].RunID != reconRunID {
		t.Fatalf("the original run history is gone: %+v", rec.Runs)
	}
	rems, err := handoff.LoadRemediations(f.host.DeliveryRoot, f.intent.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rems) != 1 || rems[0].AuthorizedBy != "Jon Pratten" || rems[0].Seq != 1 {
		t.Fatalf("the authorization is not intact: %+v", rems)
	}
	if !rems[0].RepairsInvalidation("ac-3", 1) {
		t.Fatalf("the remediation does not name the finding it answers: %+v", rems[0].Repairs)
	}
	t.Logf("AUDIT       plan(3 packages, byte-identical) -> invalidation %d by %s (%s) -> remediation %d by %s (%v) -> repaired",
		inv.Seq, inv.By, inv.Evidence, rems[0].Seq, rems[0].AuthorizedBy, rems[0].Criteria())
}

// M at the command surface. A repair that drops behaviors the criterion
// requires is refused, and nothing is written.
func TestRemediateRefusesANarrowerRepair(t *testing.T) {
	f := renderingFixture(t)
	f.stage(t, reconProjected{merged: []string{"wp-1", "wp-2", "wp-3"}, met: []string{"ac-1", "ac-2"}})
	if code, out := f.invalidate(t, "ac-3", "Jon Pratten",
		"no mixed column is ever reported", "issue-12"); code != exitHumanBoundary {
		t.Fatalf("invalidate exited %d:\n%s", code, out)
	}

	narrow := reconRemediation(f.intent)
	narrow.Packages[0].Objective = "Rewrite src/types.ts to infer text, integer and date columns."
	doc := writeRemediation(t, t.TempDir(), narrow)

	code, out := captureStderr(t, func() int {
		return cmdRemediate(t.Context(), []string{
			"-project", f.intent.ProjectID, "-from", doc, "-by", "Jon Pratten", "-host", f.hostPath,
		})
	})
	if code != exitRefused {
		t.Fatalf("remediate exited %d, want %d:\n%s", code, exitRefused, out)
	}
	for _, want := range []string{`"decimal|number"`, `"boolean"`, `"mixed"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("the refusal does not name the dropped behavior %s:\n%s", want, out)
		}
	}
	rems, err := handoff.LoadRemediations(f.host.DeliveryRoot, f.intent.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rems) != 0 {
		t.Fatalf("a refused remediation was written: %+v", rems)
	}
	t.Logf("REFUSAL 3 — delivery remediate with a narrower repair:\n%s", strings.TrimSpace(out))
}

// T. A project that has never had a finding behaves exactly as it did before
// this mechanism existed, and its record gains nothing on disk.
func TestAProjectWithNoFindingsIsUnchanged(t *testing.T) {
	f := newReconFixture(t, `echo "no stage may run: $1" >&2; exit 9`)

	st := f.status(t)
	if st.State != handoff.StateCompleted || st.CriteriaMet != 3 {
		t.Fatalf("State = %q, criteria = %d/%d", st.State, st.CriteriaMet, st.CriteriaTotal)
	}
	if len(st.Evidence.Invalidated) != 0 {
		t.Fatalf("Invalidated = %+v, want none", st.Evidence.Invalidated)
	}

	code, out := captureStderr(t, func() int {
		return cmdRemediate(t.Context(), []string{"-project", f.intent.ProjectID, "-host", f.hostPath})
	})
	if code != exitOK {
		t.Fatalf("remediate exited %d, want %d:\n%s", code, exitOK, out)
	}
	if !strings.Contains(out, "no remediation authorized") {
		t.Fatalf("remediate said: %q", out)
	}

	raw, err := os.ReadFile(handoff.RecordPath(f.host.DeliveryRoot, f.intent.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "invalidations") {
		t.Fatalf("a record with no findings gained an invalidations key:\n%s", raw)
	}
	if calls := f.driverCalls(t); len(calls) != 0 {
		t.Fatalf("reading a project's state invoked the driver: %v", calls)
	}
}

// Q, S at the command surface. Invalidating something the project does not
// declare is refused, and so is a criterion only a person may accept. Neither
// reaches the record, and neither publishes anything.
func TestInvalidateRefusesACriterionItMayNotWithdraw(t *testing.T) {
	t.Run("a criterion the project does not declare", func(t *testing.T) {
		f := newReconFixture(t, `echo "no stage may run: $1" >&2; exit 9`)
		code, out := f.invalidate(t, "ac-99", "Jon Pratten", "disproved", "issue-12")
		if code != exitRefused {
			t.Fatalf("exited %d, want %d:\n%s", code, exitRefused, out)
		}
		if !strings.Contains(out, "declares no acceptance criterion") {
			t.Fatalf("refusal = %q", out)
		}
		if rec := f.record(t); len(rec.Invalidations) != 0 {
			t.Fatalf("a finding was recorded against a criterion that does not exist: %+v", rec.Invalidations)
		}
		if calls := f.driverCalls(t); len(calls) != 0 {
			t.Fatalf("a refused invalidation invoked the driver: %v", calls)
		}
	})

	// S. A person's answer is theirs to give and theirs to revise. Delivery
	// withdrawing one would hand the machine a veto over a human boundary — the
	// mirror image of the acceptance it is already forbidden to claim.
	t.Run("a criterion only a person may accept", func(t *testing.T) {
		f := newHumanBoundaryReconFixture(t)
		code, out := f.invalidate(t, "ac-sign", "an automated worker",
			"my implementation could not satisfy it", "run-log")
		if code != exitRefused {
			t.Fatalf("exited %d, want %d:\n%s", code, exitRefused, out)
		}
		if !strings.Contains(out, "a person's to accept") {
			t.Fatalf("refusal = %q", out)
		}
		rec := f.record(t)
		if len(rec.Invalidations) != 0 {
			t.Fatalf("delivery recorded a finding against a person's answer: %+v", rec.Invalidations)
		}
		if len(rec.Acceptances) != 1 || rec.Acceptances[0].By != "Jon Pratten" {
			t.Fatalf("the person's acceptance was disturbed: %+v", rec.Acceptances)
		}
	})
}

// newHumanBoundaryReconFixture is the finished delivery with a fourth criterion
// only a person could answer, and that person's recorded acceptance.
func newHumanBoundaryReconFixture(t *testing.T) reconFixture {
	t.Helper()
	if os.PathSeparator == '\\' {
		t.Skip("the driver runs on the delivery host, which is POSIX")
	}

	root := t.TempDir()
	logPath := filepath.Join(root, "driver-calls.log")
	driver := filepath.Join(root, "driver.sh")
	body := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> " + shellQuote(logPath) +
		"\necho \"no stage may run: $1\" >&2; exit 9\n"
	if err := os.WriteFile(driver, []byte(body), 0o755); err != nil { //nolint:gosec // a stub must be executable
		t.Fatal(err)
	}

	host := handoff.HostProfile{DeliveryRoot: root, Driver: driver, Provider: "claude"}
	hostPath := filepath.Join(root, "host.toml")
	profile := "deliveryRoot = " + tomlQuote(root) + "\n" +
		"driver = " + tomlQuote(driver) + "\n" +
		"provider = \"claude\"\n"
	if err := os.WriteFile(hostPath, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}

	in := reconIntent()
	in.Acceptance = append(in.Acceptance, handoff.Criterion{
		ID:         "ac-sign",
		Statement:  "A person accepts the release.",
		AcceptedBy: handoff.AcceptedByHuman,
	})
	plan := reconPlan(in)
	if err := handoff.SaveIntent(root, in); err != nil {
		t.Fatal(err)
	}
	if err := handoff.SavePlan(root, plan); err != nil {
		t.Fatal(err)
	}

	adm, err := handoff.Admit(root, in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	record := adm.Record.AppendRun(reconRunID, handoff.ReasonInitial, time.Now())
	record, err = record.Accept("ac-sign", "Jon Pratten", "approved", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff.SaveRecord(root, record); err != nil {
		t.Fatal(err)
	}

	writeReconProjection(t, host.DeliveryProjectionPath(in.ProjectID), reconProjected{
		merged: []string{"wp-1", "wp-2", "wp-3"},
		met:    []string{"ac-1", "ac-2", "ac-3"},
	})

	return reconFixture{host: host, hostPath: hostPath, intent: in, plan: plan, log: logPath}
}
