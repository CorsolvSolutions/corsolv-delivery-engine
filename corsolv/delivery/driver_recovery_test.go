//go:build integration

// Recovery after an interruption.
//
// The failure these exist for happened on a live pilot delivery. A worker was
// killed after a successful dispatch. Its worktree survived, its work bead
// stayed open, and nothing was running. The resumed run entered dispatch, saw
// the durable `dispatched` timestamp, and returned immediately — so no worker
// was ever started again. Await then polled that orphaned bead for thirty
// minutes, reported SUCCESS when its deadline expired, and publication refused
// on a missing artifact: the wrong error, hours late, on a run whose own record
// said the wait had succeeded.
//
// Two defects, proved separately here:
//
//	A  `dispatched` was read as "a worker exists and owns every open bead".
//	   It is durable history and says nothing about now.
//	B  an expired deadline was reported as stage success.
//
// Like driver_test.go these spawn bash, so they carry the integration tag
// rather than growing the untagged subprocess baseline.
//
// SAFETY. Every git operation happens inside a repository this test created in
// its own temp directory, and the driver is spawned with GIT_DIR, GIT_WORK_TREE
// and their companions REMOVED from its environment. Leaking a live repository's
// git environment into a fixture is how shared repository metadata was corrupted
// once already; scrubENV below is that boundary, and it must not be relaxed.
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

const (
	recoveryRigName = "rig-test"
	beadOne         = "r2-ghj" // the pilot's own orphaned bead
	beadTwo         = "r2-xyz"
)

// gcStub is a `gc` that answers the three questions the driver's recovery path
// asks — is this bead closed, is a worker running, and (for a first dispatch)
// what id did this bead get — and records every invocation so a test can prove
// what the driver did and, more importantly, what it did not do.
const gcStub = `#!/usr/bin/env bash
set -u
printf '%s\n' "$*" >> "$GC_STUB_LOG"

# sa_gc prepends the run's scope; the verb is what follows it.
while [ $# -gt 0 ]; do
  case "${1:-}" in
    --city|--rig) shift 2 || exit 0 ;;
    *) break ;;
  esac
done

case "${1:-} ${2:-}" in
  'bd show')
    id="${3:-}"
    status="$(cat "$GC_STUB_BEADS/$id" 2>/dev/null || printf 'open')"
    if [ "${4:-}" = '--json' ]; then
      printf '[{"id":"%s","status":"%s"}]\n' "$id" "$status"
    else
      printf 'id: %s\nstatus: %s\n' "$id" "$status"
    fi
    ;;
  'session list')
    cat "$GC_STUB_SESSIONS" 2>/dev/null || printf '{"sessions":[]}\n'
    ;;
  'bd create')
    n=$(( $(cat "$GC_STUB_SEQ" 2>/dev/null || printf '0') + 1 ))
    printf '%s' "$n" > "$GC_STUB_SEQ"
    id="stub-bead-$n"
    printf 'open' > "$GC_STUB_BEADS/$id"
    printf '%s\n' "$id"
    ;;
  'rig list')
    printf '%s:\n    Beads: initialized\n' "${GC_STUB_RIG:-rig}"
    ;;
  'supervisor reload')
    # The one answer city-up treats as authoritative. A stale machine-wide
    # supervisor is the pilot's third failure, and a test that wants one says so
    # here rather than through the absence of a reply.
    printf '%s\n' "${GC_STUB_SUPERVISOR_REPLY:-reconciled 1 city}"
    exit "${GC_STUB_SUPERVISOR_CODE:-0}"
    ;;
esac
exit 0
`

type recoveryEnv struct {
	t        *testing.T
	root     string
	project  string
	state    string
	city     string
	rigPath  string
	binDir   string
	gcLog    string
	beadsDir string
	sessions string
}

// newRecoveryEnv lays out a delivery that has already been dispatched: the two
// validated documents, a city, and a rig with a worktree per package.
func newRecoveryEnv(t *testing.T) *recoveryEnv {
	t.Helper()
	bashOrSkip(t)

	root := t.TempDir()
	intent := fixtureIntent()
	if err := handoff.SaveIntent(root, intent); err != nil {
		t.Fatalf("SaveIntent: %v", err)
	}
	if err := handoff.SavePlan(root, fixturePlan()); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}

	e := &recoveryEnv{
		t:        t,
		root:     root,
		project:  intent.ProjectID,
		state:    filepath.Join(root, intent.ProjectID),
		city:     filepath.Join(root, "city"),
		rigPath:  filepath.Join(root, "rig"),
		binDir:   filepath.Join(root, "bin"),
		gcLog:    filepath.Join(root, "gc-calls.log"),
		beadsDir: filepath.Join(root, "beads"),
		sessions: filepath.Join(root, "sessions.json"),
	}

	for _, dir := range []string{e.city, e.binDir, e.beadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.city, "city.toml"), []byte("# test city\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.binDir, "gc"), []byte(gcStub), 0o755); err != nil { //nolint:gosec // a test stub must be executable
		t.Fatal(err)
	}
	e.setSessions(`{"schema_version":"1","sessions":[]}`)
	return e
}

// initRig creates the working rig as a real repository with one commit, so the
// driver's own worktree provisioning runs for real. It is created here, in this
// test's temp directory, and nothing outside it is ever touched.
func (e *recoveryEnv) initRig() string {
	e.t.Helper()
	if err := os.MkdirAll(e.rigPath, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.rigPath, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Fixture"},
		{"config", "user.email", "fixture@example.com"},
		{"add", "-A"},
		{"commit", "-q", "-m", "fixture"},
	} {
		cmd := exec.Command("git", append([]string{"-C", e.rigPath}, args...)...)
		cmd.Env = e.scrubbedEnv(nil)
		if out, err := cmd.CombinedOutput(); err != nil {
			e.t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	head := exec.Command("git", "-C", e.rigPath, "rev-parse", "HEAD")
	head.Env = e.scrubbedEnv(nil)
	out, err := head.Output()
	if err != nil {
		e.t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func (e *recoveryEnv) setBead(id, status string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.beadsDir, id), []byte(status), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *recoveryEnv) setSessions(body string) {
	e.t.Helper()
	if err := os.WriteFile(e.sessions, []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// liveSession is what Gas City reports while a worker is running.
func liveSession(agent string) string {
	return `{"schema_version":"1","sessions":[{"id":"s-1","template":"` +
		recoveryRigName + `/` + agent + `","state":"active","closed":false}]}`
}

// killedSession is what Gas City reports AFTER a worker is killed: the session
// record survives, and the live runtime probe downgrades its stale `active` to
// `asleep`. This is the shape that must read as "no worker" — a record alone is
// not a worker.
func killedSession(agent string) string {
	return `{"schema_version":"1","sessions":[{"id":"s-1","template":"` +
		recoveryRigName + `/` + agent + `","state":"asleep","closed":false}]}`
}

// seedRuntime writes the run's scratch memory as an interrupted run left it.
func (e *recoveryEnv) seedRuntime(extra map[string]string) {
	e.t.Helper()
	rt := map[string]string{
		"city":    e.city,
		"rigPath": e.rigPath,
		"rigName": recoveryRigName,
		"runTag":  "20260814T164300Z",
	}
	for k, v := range extra {
		rt[k] = v
	}
	data, err := json.MarshalIndent(rt, "", "  ")
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.MkdirAll(e.state, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.state, "runtime.json"), append(data, '\n'), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// makeWorktree creates the directory a killed worker leaves behind.
func (e *recoveryEnv) makeWorktree(pkg string) string {
	e.t.Helper()
	wt := filepath.Join(e.city, ".gc", "worktrees", recoveryRigName, "worker-"+pkg)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		e.t.Fatal(err)
	}
	return wt
}

// scrubbedEnv is the safety boundary. A live repository's git environment must
// never reach a fixture: GIT_DIR and GIT_WORK_TREE point at the repository this
// checkout belongs to, and a fixture inheriting them writes its worktree
// registrations and index into shared metadata.
func (e *recoveryEnv) scrubbedEnv(extra []string) []string {
	blocked := []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=", "GIT_OBJECT_DIRECTORY=", "PATH="}
	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		skip := false
		for _, prefix := range blocked {
			if strings.HasPrefix(kv, prefix) {
				skip = true
				break
			}
		}
		if !skip {
			env = append(env, kv)
		}
	}
	env = append(env,
		"PATH="+e.binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"CORSOLV_ENGINE_REPO="+engineRepo(e.t),
		"GC_STUB_LOG="+e.gcLog,
		"GC_STUB_BEADS="+e.beadsDir,
		"GC_STUB_SESSIONS="+e.sessions,
		"GC_STUB_SEQ="+filepath.Join(e.root, "bead-seq"),
		"GC_STUB_RIG="+recoveryRigName,
	)
	return append(env, extra...)
}

// runStage invokes the driver exactly as the compiled run does.
func (e *recoveryEnv) runStage(stage string, extraEnv []string) (int, string) {
	e.t.Helper()
	argv := []string{driverPath(e.t), stage, "-project", e.project, "-state", e.state}
	cmd := exec.Command("bash", argv...) //nolint:gosec // the driver under test
	cmd.Env = e.scrubbedEnv(extraEnv)
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
}

// gcCalls is every gc invocation the driver made, in order.
func (e *recoveryEnv) gcCalls() []string {
	e.t.Helper()
	data, err := os.ReadFile(e.gcLog)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		e.t.Fatal(err)
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			calls = append(calls, line)
		}
	}
	return calls
}

func (e *recoveryEnv) truncateGCLog() {
	e.t.Helper()
	if err := os.WriteFile(e.gcLog, nil, 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// slingsFor counts the routing directives naming a package's worker.
func slingsFor(calls []string, pkg string) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(c, "sling ") && strings.Contains(c, "worker-"+pkg+" ") {
			n++
		}
	}
	return n
}

func callsContaining(calls []string, needle string) []string {
	var out []string
	for _, c := range calls {
		if strings.Contains(c, needle) {
			out = append(out, c)
		}
	}
	return out
}

// DEFECT A, case 3: an open bead with no worker is an orphan, and resuming must
// put a worker back on it. This is the pilot's exact state.
//
// The dependent package in the same fixture proves the other half: it is open
// too, and it has no worktree because its upstream has not merged. It is
// waiting, not orphaned, and routing a worker to a directory that does not
// exist would be the recovery causing the next failure.
func TestResumeRoutesAnOpenBeadAgainWhenNoWorkerHoldsIt(t *testing.T) {
	e := newRecoveryEnv(t)
	wt := e.makeWorktree("wp-one")
	e.seedRuntime(map[string]string{
		"dispatched":  "2026-08-14T16:43:00Z",
		"bead.wp-one": beadOne,
		"wt.wp-one":   wt,
		"bead.wp-two": beadTwo,
		"wt.wp-two":   filepath.Join(e.city, ".gc", "worktrees", recoveryRigName, "worker-wp-two"),
	})
	e.setBead(beadOne, "open")
	e.setBead(beadTwo, "open")
	// The worker was killed: its session record survives, downgraded to asleep.
	e.setSessions(killedSession("worker-wp-one"))

	code, out := e.runStage("dispatch", nil)
	if code != 0 {
		t.Fatalf("resuming dispatch must succeed, got %d:\n%s", code, out)
	}

	calls := e.gcCalls()
	if got := slingsFor(calls, "wp-one"); got != 1 {
		t.Fatalf("an open bead with no live worker must be routed again exactly once, got %d\ncalls: %v\n%s",
			got, calls, out)
	}
	if got := slingsFor(calls, "wp-two"); got != 0 {
		t.Fatalf("a package still waiting on its upstream has no worktree and must not be routed, got %d\ncalls: %v",
			got, calls)
	}
	// Recovery re-routes; it never re-creates the work.
	if created := callsContaining(calls, "bd create"); len(created) != 0 {
		t.Fatalf("recovery must not create beads again, got: %v", created)
	}
	if !strings.Contains(out, beadOne) {
		t.Errorf("the driver must name the bead it recovered, got:\n%s", out)
	}
}

// DEFECT A, case 2: a bead someone is still working on must not get a second
// worker. Recovery that cannot tell "nobody is doing this" from "somebody is"
// is not recovery, it is duplication.
func TestResumeDoesNotRouteABeadALiveWorkerHolds(t *testing.T) {
	e := newRecoveryEnv(t)
	wt := e.makeWorktree("wp-one")
	e.seedRuntime(map[string]string{
		"dispatched":  "2026-08-14T16:43:00Z",
		"bead.wp-one": beadOne,
		"wt.wp-one":   wt,
		"bead.wp-two": beadTwo,
	})
	e.setBead(beadOne, "open")
	e.setBead(beadTwo, "open")
	e.setSessions(liveSession("worker-wp-one"))

	code, out := e.runStage("dispatch", nil)
	if code != 0 {
		t.Fatalf("resuming dispatch must succeed, got %d:\n%s", code, out)
	}
	if got := slingsFor(e.gcCalls(), "wp-one"); got != 0 {
		t.Fatalf("a bead a live worker holds must not be routed again, got %d sling(s)\ncalls: %v\n%s",
			got, e.gcCalls(), out)
	}
}

// DEFECT A, case 1: closed work is finished work.
func TestResumeDoesNotRouteAClosedBead(t *testing.T) {
	e := newRecoveryEnv(t)
	wt := e.makeWorktree("wp-one")
	e.seedRuntime(map[string]string{
		"dispatched":  "2026-08-14T16:43:00Z",
		"bead.wp-one": beadOne,
		"wt.wp-one":   wt,
	})
	e.setBead(beadOne, "closed")
	// No session at all — the worker finished and went away, which is exactly
	// when a liveness-only rule would wrongly re-route completed work.
	e.setSessions(`{"schema_version":"1","sessions":[]}`)

	code, out := e.runStage("dispatch", nil)
	if code != 0 {
		t.Fatalf("resuming dispatch must succeed, got %d:\n%s", code, out)
	}
	if got := slingsFor(e.gcCalls(), "wp-one"); got != 0 {
		t.Fatalf("a closed bead must not be routed again, got %d sling(s)\ncalls: %v\n%s",
			got, e.gcCalls(), out)
	}
}

// A delivery that finished must survive being resumed without anything being
// replayed. Recovery reads state; on a completed run it must issue no directive
// at all — no bead created, no metadata written, no dependency wired, no worker
// routed — because every one of those would act on a city and a rig whose work
// is already published.
func TestResumeReplaysNothingWhenEveryPackageIsClosed(t *testing.T) {
	e := newRecoveryEnv(t)
	e.seedRuntime(map[string]string{
		"dispatched":       "2026-08-14T16:43:00Z",
		"bead.wp-one":      beadOne,
		"wt.wp-one":        e.makeWorktree("wp-one"),
		"bead.wp-two":      beadTwo,
		"wt.wp-two":        e.makeWorktree("wp-two"),
		"merged.wp-one":    "1111111111111111111111111111111111111111",
		"merged.wp-two":    "2222222222222222222222222222222222222222",
		"published.wp-one": "merged",
		"published.wp-two": "merged",
	})
	e.setBead(beadOne, "closed")
	e.setBead(beadTwo, "closed")

	code, out := e.runStage("dispatch", nil)
	if code != 0 {
		t.Fatalf("resuming a completed dispatch must succeed, got %d:\n%s", code, out)
	}
	for _, directive := range []string{"sling ", "bd create", "bd update", "bd dep", "rig add"} {
		if found := callsContaining(e.gcCalls(), directive); len(found) != 0 {
			t.Errorf("a completed delivery must not be replayed, but %q was issued: %v", directive, found)
		}
	}
}

// The whole sequence, end to end: a real first dispatch, a killed worker, and
// the resume that has to notice. Nothing here is seeded — the runtime facts the
// second run reads are the ones the first run wrote.
func TestAKilledWorkerIsRecoveredByTheNextResume(t *testing.T) {
	e := newRecoveryEnv(t)
	base := e.initRig()
	e.seedRuntime(map[string]string{"baseSha": base})

	code, out := e.runStage("dispatch", nil)
	if code != 0 {
		t.Fatalf("the first dispatch must succeed, got %d:\n%s", code, out)
	}
	first := e.gcCalls()
	if got := slingsFor(first, "wp-one"); got != 1 {
		t.Fatalf("the first dispatch must route wp-one once, got %d\ncalls: %v\n%s", got, first, out)
	}
	if len(callsContaining(first, "bd create")) == 0 {
		t.Fatalf("the first dispatch must create the work beads:\n%s", out)
	}

	// The worker is killed. Its bead is still open, its worktree survives, and
	// the run's own record still says the work was dispatched.
	e.setSessions(killedSession("worker-wp-one"))
	e.truncateGCLog()

	code, out = e.runStage("dispatch", nil)
	if code != 0 {
		t.Fatalf("the resumed dispatch must succeed, got %d:\n%s", code, out)
	}
	second := e.gcCalls()
	if got := slingsFor(second, "wp-one"); got != 1 {
		t.Fatalf("the resumed dispatch must put a worker back on the still-open bead, got %d\ncalls: %v\n%s",
			got, second, out)
	}
	if created := callsContaining(second, "bd create"); len(created) != 0 {
		t.Fatalf("the resumed dispatch must not create the work a second time, got: %v", created)
	}
}

// DEFECT B: a deadline is not an outcome.
//
// Reporting success here is what let publication run against work that had
// never been done, and what put "the wait succeeded" in the run's durable
// record while its bead sat open.
func TestAwaitRefusesToSucceedWhenItsDeadlineExpiresWithWorkOpen(t *testing.T) {
	e := newRecoveryEnv(t)
	// wp-one has a worktree because dispatch gave it one; wp-two does not,
	// because its base does not exist until wp-one merges. Await ensures the
	// tree of what it waits for before it waits, so a fixture without one would
	// test the missing-tree refusal instead of the deadline this is here for.
	e.seedRuntime(map[string]string{
		"dispatched":    "2026-08-14T16:43:00Z",
		"bead.wp-one":   beadOne,
		"bead.wp-two":   beadTwo,
		"wt.wp-one":     e.makeWorktree("wp-one"),
		"branch.wp-one": "delivery/20260814T164300Z/wp-one",
	})
	e.setBead(beadOne, "open")
	e.setBead(beadTwo, "open")

	code, out := e.runStage("await", []string{"DELIVERY_WORK_DEADLINE=0"})
	if code == 0 {
		t.Fatalf("await must not report success with mandatory work still open:\n%s", out)
	}
	if !strings.Contains(out, "wp-one") {
		t.Errorf("the refusal must name the work that is still open, got:\n%s", out)
	}
}

// And the converse, which is what keeps the fix honest: a package waiting on an
// upstream merge is not this stage's work. Publish cuts its base and waits for
// it there, so counting it here would make the deadline — and now the failure —
// the normal outcome of every delivery with a dependent package.
func TestAwaitSucceedsWhenOnlyDependentWorkRemainsOpen(t *testing.T) {
	e := newRecoveryEnv(t)
	e.seedRuntime(map[string]string{
		"dispatched":    "2026-08-14T16:43:00Z",
		"bead.wp-one":   beadOne,
		"bead.wp-two":   beadTwo,
		"wt.wp-one":     e.makeWorktree("wp-one"),
		"branch.wp-one": "delivery/20260814T164300Z/wp-one",
	})
	e.setBead(beadOne, "closed")
	e.setBead(beadTwo, "open") // wp-two depends on wp-one and has no base yet

	code, out := e.runStage("await", []string{"DELIVERY_WORK_DEADLINE=0"})
	if code != 0 {
		t.Fatalf("await must succeed once the work it is responsible for is closed, got %d:\n%s", code, out)
	}
}
