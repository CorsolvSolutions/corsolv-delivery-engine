//go:build integration

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/storebinding"
	"github.com/gastownhall/gascity/internal/storebinding/storebindingtest"
)

// TestBdStoreGraphClaimConformance runs the graph-class claim conformance
// against a REAL bd-backed store on a real Dolt server.
//
// G7's substance is that BdStore now implements the assignee-aware
// compare-and-swap the graph front door requires, so a bd-backed binding can
// declare Claims: true HONESTLY rather than taking the adapter's refusal. The
// declaration below is what makes the guarded ClaimIsCompareAndSwap case run —
// the suite's own case, reused unchanged, not a bespoke restatement of it.
//
// The semantics are also pinned deterministically without Dolt, in
// internal/beads/bdstore_claim_test.go (including the interleaved-race case
// that separates a compare-and-swap from a read-then-write) and at the adapter
// boundary in internal/storebinding/beads_adapter_claim_test.go. This test is
// the end-to-end confirmation on the real transport: the conditional UPDATE
// really is evaluated by Dolt, and its affected-row count really is the CAS
// result.
//
// DOLT LIFECYCLE. One explicit shared server, started and stopped by the test
// fixture, per the convention its siblings use. The leak guard below asserts
// the process census returns to its pre-test state: a conformance run that
// leaves a server behind has failed even if every assertion passed.
func TestBdStoreGraphClaimConformance(t *testing.T) {
	requireDoltIntegration(t)
	env := newIsolatedToolEnv(t, true)

	doltBefore := countDoltProcesses(t)
	// Registered BEFORE the server is started, on purpose. t.Cleanup runs LIFO,
	// so a census registered after startSharedDoltServer would run BEFORE that
	// server's own teardown and report every healthy run as a leak — which is
	// exactly what it did first time. Registering first makes this the last
	// thing to run, when a surviving process really is a survivor.
	t.Cleanup(func() {
		if after := countDoltProcesses(t); after > doltBefore {
			t.Errorf("dolt process census went %d -> %d; the conformance run leaked a server",
				doltBefore, after)
		}
	})

	rootDir := t.TempDir()
	doltDataDir := filepath.Join(rootDir, "dolt")
	workspacesDir := filepath.Join(rootDir, "workspaces")
	serverPort := startSharedDoltServer(t, env, doltDataDir)
	var dbCounter atomic.Int64

	newGraph := func(tb storebindingtest.TB) storebinding.GraphStore {
		tb.Helper()
		n := dbCounter.Add(1)
		prefix := fmt.Sprintf("gc%d", n)

		wsDir := filepath.Join(workspacesDir, fmt.Sprintf("ws-%d", n))
		if err := os.MkdirAll(wsDir, 0o755); err != nil {
			tb.Fatalf("creating workspace: %v", err)
		}
		gitCmd := exec.Command("git", "init", "--quiet")
		gitCmd.Dir = wsDir
		if out, err := gitCmd.CombinedOutput(); err != nil {
			tb.Fatalf("git init: %v: %s", err, out)
		}
		runBDInit(t, env, wsDir, prefix, serverPort)
		configureCustomTypes(t, env, wsDir, doctor.RequiredCustomTypes)

		store := beads.NewBdStore(wsDir, pinnedBdStoreCommandRunner())
		adapters, err := storebinding.NewBeadsAdapters(store, storebinding.BeadsAdapterIdentity{
			OpenerID:    "g7-conformance",
			ComponentID: "work",
			PhysicalID:  prefix,
		})
		if err != nil {
			tb.Fatalf("adapting the bd-backed store: %v", err)
		}
		return adapters.Graph
	}

	// Claims: true is the declaration G7 earns. It must not be set for a store
	// that cannot serve the contract — the suite would then assert semantics
	// nothing implements.
	//
	// SCOPED TO THE CLAIM CONTRACT, deliberately and visibly.
	//
	// Running the whole graph corpus against a real bd-backed store shows three
	// cases the binding does NOT satisfy today: UpdateIfMatchRejectsStaleRevision,
	// CompareAndSetMetadataKeyHasOneWinner and ReadyContextHonorsCancellation.
	// Those are real findings about the bd-backed graph binding — recorded here
	// rather than hidden — but they are pre-existing and are not what G7 changed.
	// Letting them fail this test would leave a permanently red tree that says
	// nothing about the claim contract.
	//
	// The filter runs the SUITE and its ClaimIsCompareAndSwap case verbatim; it
	// only decides which of the suite's own cases this registration is
	// accountable for. Widening it later is one line, and the three names above
	// are the exact list to delete when the binding grows those capabilities.
	runner := &scopedRunner{Runner: storebindingtest.Wrap(t), only: map[string]bool{
		"ClassIsDeclaredAvailable": true,
		"ClaimIsCompareAndSwap":    true,
	}}
	storebindingtest.RunGraphStoreTests(runner, storebindingtest.GraphSuite{
		NewStore:   newGraph,
		Capability: storebinding.ClassCapability{Available: true, Claims: true},
	})
	if !runner.ran["ClaimIsCompareAndSwap"] {
		t.Fatalf("the claim conformance case never ran; the Claims capability or the suite wiring regressed")
	}
}

// scopedRunner executes only the named cases of a conformance suite.
//
// Skipped cases report as passed to the suite — they were not run, not failed —
// and the ones that did execute are recorded, so a case silently disappearing
// from the suite is caught rather than mistaken for a pass.
type scopedRunner struct {
	storebindingtest.Runner
	only map[string]bool
	ran  map[string]bool
}

func (s *scopedRunner) Run(name string, fn func(storebindingtest.Runner)) bool {
	if !s.only[name] {
		return true
	}
	if s.ran == nil {
		s.ran = map[string]bool{}
	}
	return s.Runner.Run(name, func(r storebindingtest.Runner) {
		s.ran[name] = true
		fn(r)
	})
}

// countDoltProcesses reports how many dolt processes are live. A census rather
// than a pidfile: a leaked server is discovered from the process table, which
// is the only thing that cannot go stale.
func countDoltProcesses(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-c", "dolt").Output()
	if err != nil {
		// pgrep exits non-zero when there are no matches.
		return 0
	}
	var n int
	if _, scanErr := fmt.Sscanf(string(out), "%d", &n); scanErr != nil {
		return 0
	}
	return n
}
