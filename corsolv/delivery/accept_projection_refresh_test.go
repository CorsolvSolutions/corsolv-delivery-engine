package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/handoff"
	"github.com/gastownhall/gascity/internal/unattended"
)

// The projection is rendered by two stages of a RUN, and a criterion reserved
// to a person is answered after that run has finished. So the engine's own
// state turned complete while the document the portal reads went on reporting
// the deliverable outstanding: one project, two answers, and nothing to say
// which was right.
//
// These tests pin the refresh that closes it, and — as much as they pin what it
// does — they pin what it must NOT do: no run, no worker, no reopened package,
// no new run id.

// acceptedRunID is the run identity the fixture's delivery already has. The
// refresh must reuse it rather than mint anything.
const acceptedRunID = "human-acceptance-probe-20260820T081021Z"

// refreshFixture is a delivery that has finished its machine work and is
// waiting on a person: a merged package with a met gate, and a criterion only
// that person may answer.
type refreshFixture struct {
	host     handoff.HostProfile
	hostPath string
	intent   handoff.Intent
	log      string
}

func newRefreshFixture(t *testing.T, driverBody string) refreshFixture {
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

	in := mixedIntent()
	plan := realPlan(in)
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
	record := adm.Record.AppendRun(acceptedRunID, handoff.ReasonInitial, time.Now())
	if err := handoff.SaveRecord(root, record); err != nil {
		t.Fatal(err)
	}

	// The document as the finished run left it: the package merged with its gate
	// met, the person's criterion still outstanding.
	writeProjectionFile(t, host.DeliveryProjectionPath(in.ProjectID), false)

	// And the run's own account of itself: every task succeeded, which is what
	// makes the delivery blocked on a person rather than merely unstarted.
	if err := os.MkdirAll(host.StateDir(in.ProjectID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unattended.WriteCompletion(host.StateDir(in.ProjectID), unattended.CompletionEvent{
		RunID:     acceptedRunID,
		ProjectID: in.ProjectID,
		Outcome:   unattended.RunCompleted,
		Reason:    "every declared task succeeded",
	}); err != nil {
		t.Fatal(err)
	}

	return refreshFixture{host: host, hostPath: hostPath, intent: in, log: logPath}
}

// writeProjectionFile writes the projection in the two states this transition
// moves between.
func writeProjectionFile(t *testing.T, path string, accepted bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	human := "  - deliverableId: \"ac-2\"\n    statement: \"D2\"\n    satisfiedBy: []\n    met: false\n"
	if accepted {
		human = "  - deliverableId: \"ac-2\"\n    statement: \"D2\"\n    satisfiedBy: []\n    met: true\n" +
			"    acceptedBy: \"Jon Pratten\"\n    acceptedAt: \"2026-08-20T10:35:02Z\"\n"
	}
	doc := "schemaVersion: 1\nproject:\n" +
		"  projectId: \"human-acceptance-probe\"\n" +
		"  latestAcceptedMainSha: \"f0c1a08e\"\n" +
		"activeTasks:\n" +
		"  - taskId: \"wp-d1\"\n    status: \"merged\"\n    completionGateStatus: \"met\"\n" +
		"deliverables:\n" +
		"  - deliverableId: \"ac-1\"\n    statement: \"D1\"\n    satisfiedBy: [\"wp-d1\"]\n    met: true\n" +
		human
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
func tomlQuote(s string) string  { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

func (f refreshFixture) driverCalls(t *testing.T) []string {
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

func (f refreshFixture) accept(t *testing.T, criterion string) int {
	t.Helper()
	return cmdAccept(context.Background(), []string{
		"-project", f.intent.ProjectID,
		"-criterion", criterion,
		"-by", "Jon Pratten",
		"-note", "approved",
		"-host", f.hostPath,
	})
}

func (f refreshFixture) status(t *testing.T) handoff.Status {
	t.Helper()
	st, err := observe(f.host, f.intent.ProjectID)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	return st
}

// A, D, E, F, G, H, I. A successful acceptance refreshes the projection through
// the run's own two stages, and touches nothing else.
func TestAcceptRefreshesTheProjectionThroughTheRunsOwnStages(t *testing.T) {
	// The stub stands in for the real project stage: it re-renders the document
	// from the record the acceptance has just written.
	f := newRefreshFixture(t, `case "$1" in
  project) : ;;
  publish-projection) : ;;
  *) echo "no stage may run here but project and publish-projection: $1" >&2; exit 9 ;;
esac
`)

	before := f.status(t)
	if before.State != handoff.StateBlocked {
		t.Fatalf("the fixture must start blocked, got %q", before.State)
	}
	if len(before.Evidence.AwaitingHuman) != 1 {
		t.Fatalf("AwaitingHuman = %v, want [ac-2]", before.Evidence.AwaitingHuman)
	}

	// The real project stage re-renders the document from the record; the stub
	// installs the same result, so what is asserted below is the transition
	// itself rather than the projector's rule — which internal/projector pins.
	rc := f.acceptWithRender(t, "ac-2")
	if rc != exitOK {
		t.Fatalf("accept exited %d, want %d", rc, exitOK)
	}

	calls := f.driverCalls(t)
	if len(calls) != 2 {
		t.Fatalf("the driver was invoked %d time(s), want exactly 2:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	// I. Only the two evidence stages, in order — never one that executes work.
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
				t.Fatalf("the refresh ran %q — an acceptance may not execute work", forbidden)
			}
		}
	}

	after := f.status(t)
	// D. HUMAN_BLOCKED -> COMPLETE.
	if after.State != handoff.StateCompleted {
		t.Fatalf("State = %q, want %q: %v", after.State, handoff.StateCompleted, after.Evidence.Reasons)
	}
	// E. Nothing is awaited any more.
	if len(after.Evidence.AwaitingHuman) != 0 {
		t.Fatalf("AwaitingHuman = %v, want none", after.Evidence.AwaitingHuman)
	}
	// F. Every criterion is accounted for.
	if got := strings.Join(after.Evidence.AcceptanceMet, ","); got != "ac-1,ac-2" {
		t.Fatalf("AcceptanceMet = %v, want [ac-1 ac-2]", after.Evidence.AcceptanceMet)
	}
	// G. The run identity is the one the delivery already had.
	record, _, err := handoff.LoadRecord(f.host.DeliveryRoot, f.intent.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Runs) != 1 || record.Runs[0].RunID != acceptedRunID {
		t.Fatalf("runs = %+v, want exactly the original %q", record.Runs, acceptedRunID)
	}
	for _, call := range calls {
		if strings.Contains(call, "-project "+f.intent.ProjectID) {
			continue
		}
		t.Fatalf("a refresh stage was not addressed to this project: %q", call)
	}
	// H. The merged package is untouched.
	if got := strings.Join(after.Evidence.CompletePackages, ","); got != "wp-d1" {
		t.Fatalf("CompletePackages = %v, want [wp-d1] — no work may be reopened", after.Evidence.CompletePackages)
	}
	if len(after.Evidence.OutstandingPackages) != 0 {
		t.Fatalf("OutstandingPackages = %v, want none", after.Evidence.OutstandingPackages)
	}
}

// acceptWithRender runs the acceptance with a driver whose project stage
// re-renders the document, which is what the real one does from the record.
func (f refreshFixture) acceptWithRender(t *testing.T, criterion string) int {
	t.Helper()
	render := filepath.Join(f.host.DeliveryRoot, "rendered.yml")
	writeProjectionFile(t, render, true)
	body := `case "$1" in
  project) cp -f ` + shellQuote(render) + ` ` + shellQuote(f.host.DeliveryProjectionPath(f.intent.ProjectID)) + ` ;;
  publish-projection) : ;;
  *) echo "no stage may run here but project and publish-projection: $1" >&2; exit 9 ;;
esac
`
	driver := filepath.Join(f.host.DeliveryRoot, "driver.sh")
	full := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> " + shellQuote(f.log) + "\n" + body
	if err := os.WriteFile(driver, []byte(full), 0o755); err != nil { //nolint:gosec // a stub must be executable
		t.Fatal(err)
	}
	return f.accept(t, criterion)
}

// J. Reconciliation is idempotent: asking again after the acceptance gives the
// same completion, and the first answer stands.
func TestReconcilingAfterAnAcceptanceKeepsTheSameCompletion(t *testing.T) {
	f := newRefreshFixture(t, "")
	if rc := f.acceptWithRender(t, "ac-2"); rc != exitOK {
		t.Fatalf("accept exited %d", rc)
	}

	first := f.status(t)
	for i := 0; i < 3; i++ {
		again := f.status(t)
		if again.State != first.State || again.Evidence.Met != first.Evidence.Met {
			t.Fatalf("reconciliation %d disagreed: %q/%v vs %q/%v",
				i, again.State, again.Evidence.Met, first.State, first.Evidence.Met)
		}
	}

	// The same person accepting again does not change the answer or duplicate it.
	if rc := f.acceptWithRender(t, "ac-2"); rc != exitOK {
		t.Fatalf("re-accepting exited %d, want %d", rc, exitOK)
	}
	record, _, err := handoff.LoadRecord(f.host.DeliveryRoot, f.intent.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Acceptances) != 1 {
		t.Fatalf("acceptances = %d, want 1", len(record.Acceptances))
	}
	if f.status(t).State != handoff.StateCompleted {
		t.Fatal("the delivery must stay complete")
	}
}

// K, L. A refused acceptance publishes nothing. A criterion delivery owns is
// delivery's to prove, and an attempt to sign it off by hand must not reach the
// document at all.
func TestARefusedAcceptanceRefreshesNothing(t *testing.T) {
	f := newRefreshFixture(t, `echo "the driver must not run for a refused acceptance: $1" >&2; exit 9`)

	if rc := f.accept(t, "ac-1"); rc != exitRefused {
		t.Fatalf("accepting a delivery-owned criterion exited %d, want %d", rc, exitRefused)
	}
	if calls := f.driverCalls(t); len(calls) != 0 {
		t.Fatalf("a refused acceptance invoked the driver: %v", calls)
	}
	if st := f.status(t); st.State != handoff.StateBlocked {
		t.Fatalf("State = %q, want %q — a refused acceptance changes nothing", st.State, handoff.StateBlocked)
	}
	if got := strings.Join(f.status(t).Evidence.AwaitingHuman, ","); got != "ac-2" {
		t.Fatalf("AwaitingHuman = %q, want ac-2", got)
	}
}

// K. And a refresh that FAILS is reported rather than swallowed. The acceptance
// is durable either way, but a command that returned success over a document
// still saying otherwise would recreate the split this fix exists to close.
func TestAcceptReportsAProjectionItCouldNotRefresh(t *testing.T) {
	f := newRefreshFixture(t, `exit 3`)

	if rc := f.accept(t, "ac-2"); rc != exitRefused {
		t.Fatalf("accept exited %d, want %d when the projection cannot be refreshed", rc, exitRefused)
	}

	// The acceptance itself stands: it was recorded before the document was
	// asked to catch up, and losing it would be worse than a stale document.
	record, _, err := handoff.LoadRecord(f.host.DeliveryRoot, f.intent.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Acceptances) != 1 || record.Acceptances[0].CriterionID != "ac-2" {
		t.Fatalf("the acceptance must be durable even when publication fails, got %+v", record.Acceptances)
	}
}

// A delivery that has never run has no projection to refresh, and saying so is
// not an error — a person may accept before anything has been rendered.
func TestAcceptingBeforeAnythingHasRunRefreshesNothing(t *testing.T) {
	f := newRefreshFixture(t, `echo "nothing to refresh" >&2; exit 9`)

	record, _, err := handoff.LoadRecord(f.host.DeliveryRoot, f.intent.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	record.Runs = nil
	if err := handoff.SaveRecord(f.host.DeliveryRoot, record); err != nil {
		t.Fatal(err)
	}

	if rc := f.accept(t, "ac-2"); rc == exitUsage {
		t.Fatalf("accept exited %d", rc)
	}
	if calls := f.driverCalls(t); len(calls) != 0 {
		t.Fatalf("a delivery with no run invoked the driver: %v", calls)
	}
}
