package beads_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// bdClaimFake models ONE bead row behind a bd CommandRunner, applying the
// conditional UPDATE's precondition itself.
//
// The point is not to reimplement SQL. It is that the CAS precondition is
// evaluated by the STORE LAYER rather than by Claim's Go code: the fake answers
// `bd sql` by testing the same WHERE clause the query carries, and reports
// rows_affected. If Claim ever regressed into a read-then-write — deciding the
// outcome in Go and issuing an unconditional update — these assertions would
// still pass on the happy path but the contended one would hand the bead to two
// owners, which is exactly what ClaimTwoConcurrentClaimantsProduceOneWinner
// below refuses.
type bdClaimFake struct {
	mu       sync.Mutex
	id       string
	status   string
	assignee string
	updates  int // conditional UPDATEs actually issued
	// beforeSwap runs while the row lock is held, immediately before the
	// precondition is evaluated. It lets a test interleave a competing claim
	// exactly inside the CAS window.
	beforeSwap func()
}

func (f *bdClaimFake) runner() beads.CommandRunner {
	return func(_, _ string, args ...string) ([]byte, error) {
		switch {
		case len(args) >= 2 && args[0] == "show":
			return f.showJSON()
		case len(args) >= 3 && args[0] == "sql":
			return f.sqlUpdate(args[len(args)-1])
		}
		return nil, fmt.Errorf("bdClaimFake: unexpected bd invocation %v", args)
	}
}

func (f *bdClaimFake) showJSON() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status == "" {
		return nil, fmt.Errorf("issue not found")
	}
	row := map[string]any{
		"id": f.id, "title": "claimable", "issue_type": "task",
		"status": f.status, "assignee": f.assignee,
	}
	return json.Marshal([]map[string]any{row})
}

// sqlUpdate evaluates the query's own precondition rather than trusting the
// caller: unassigned, or already held by the same assignee, and not terminal.
func (f *bdClaimFake) sqlUpdate(query string) ([]byte, error) {
	if f.beforeSwap != nil {
		hook := f.beforeSwap
		f.beforeSwap = nil
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++

	want := ""
	if i := strings.Index(query, "SET status = 'in_progress', assignee = '"); i >= 0 {
		rest := query[i+len("SET status = 'in_progress', assignee = '"):]
		if j := strings.Index(rest, "'"); j >= 0 {
			want = rest[:j]
		}
	}
	rows := 0
	statusOK := f.status == "open" || f.status == "in_progress"
	heldOK := f.assignee == "" || f.assignee == want
	if statusOK && heldOK && want != "" {
		f.status = "in_progress"
		f.assignee = want
		rows = 1
	}
	return json.Marshal(map[string]int{"rows_affected": rows})
}

func newClaimFakeStore(t *testing.T, f *bdClaimFake) *beads.BdStore {
	t.Helper()
	return beads.NewBdStore("/city", f.runner())
}

// TestBdStoreClaimIsCompareAndSwap walks the exact ownership sequence the graph
// front-door contract promises, on the bd-backed store.
func TestBdStoreClaimIsCompareAndSwap(t *testing.T) {
	f := &bdClaimFake{id: "bd-1", status: "open"}
	store := newClaimFakeStore(t, f)

	claimed, ok, err := store.Claim("bd-1", "alice")
	if err != nil {
		t.Fatalf("Claim by the first holder: %v", err)
	}
	if !ok {
		t.Fatalf("Claim on an unassigned open bead lost; nothing can ever claim it")
	}
	if claimed.Assignee != "alice" {
		t.Errorf("Assignee = %q after a won claim, want alice", claimed.Assignee)
	}
	if claimed.Status != "in_progress" {
		t.Errorf("Status = %q after a won claim, want in_progress", claimed.Status)
	}

	updatesAfterFirst := f.updates
	repeat, ok, err := store.Claim("bd-1", "alice")
	if err != nil {
		t.Fatalf("Claim by the same holder: %v", err)
	}
	if !ok {
		t.Fatalf("re-claim by the current holder reported a conflict; the claim is not idempotent")
	}
	if repeat.Assignee != "alice" {
		t.Errorf("re-claim returned assignee %q, want alice", repeat.Assignee)
	}
	if f.updates != updatesAfterFirst {
		t.Errorf("a same-holder re-claim issued %d conditional update(s); it must be a true no-op",
			f.updates-updatesAfterFirst)
	}

	if _, ok, err := store.Claim("bd-1", "bob"); err != nil {
		t.Fatalf("Claim by a second holder: %v", err)
	} else if ok {
		t.Fatalf("a second holder also won the claim; the bead has two owners")
	}
	if f.assignee != "alice" {
		t.Errorf("assignee = %q after a losing claim, want alice to still hold it", f.assignee)
	}
}

// TestBdStoreClaimRefusesTerminalAndMissingBeads pins the two classifications
// the SQL predicate cannot make on its own.
func TestBdStoreClaimRefusesTerminalAndMissingBeads(t *testing.T) {
	closed := &bdClaimFake{id: "bd-2", status: "closed"}
	if _, ok, err := newClaimFakeStore(t, closed).Claim("bd-2", "alice"); err != nil {
		t.Fatalf("Claim on a closed bead: %v", err)
	} else if ok {
		t.Errorf("a closed bead was claimed; a claim must never resurrect terminal work")
	}
	if closed.updates != 0 {
		t.Errorf("a closed bead was sent %d conditional update(s); it must not be written at all", closed.updates)
	}

	missing := &bdClaimFake{id: "bd-3"} // status "" => not found
	if _, _, err := newClaimFakeStore(t, missing).Claim("bd-3", "alice"); err == nil {
		t.Errorf("Claim on a missing bead returned no error; want a not-found error")
	}

	empty := &bdClaimFake{id: "bd-4", status: "open"}
	if _, _, err := newClaimFakeStore(t, empty).Claim("bd-4", "   "); err == nil {
		t.Errorf("Claim with an empty assignee succeeded; an unowned claim is not a claim")
	}
}

// TestBdStoreClaimAdmitsWorkAlreadyRoutedToTheSameAssignee covers the bead that
// is assigned but not yet started: the worker it was routed to must be able to
// claim it, and nobody else may.
func TestBdStoreClaimAdmitsWorkAlreadyRoutedToTheSameAssignee(t *testing.T) {
	f := &bdClaimFake{id: "bd-5", status: "open", assignee: "alice"}
	store := newClaimFakeStore(t, f)

	if _, ok, err := store.Claim("bd-5", "alice"); err != nil {
		t.Fatalf("Claim of work already routed to this assignee: %v", err)
	} else if !ok {
		t.Fatalf("the assignee could not claim work already routed to it")
	}

	other := &bdClaimFake{id: "bd-6", status: "open", assignee: "alice"}
	if _, ok, err := newClaimFakeStore(t, other).Claim("bd-6", "bob"); err != nil {
		t.Fatalf("Claim of another assignee routed work: %v", err)
	} else if ok {
		t.Errorf("bob claimed work routed to alice")
	}
}

// TestBdStoreClaimTwoConcurrentClaimantsProduceOneWinner is the assertion that
// separates a real compare-and-swap from a read-then-write.
//
// The competing claim is interleaved INSIDE the CAS window: alice has already
// read the bead as unassigned, and bob wins it before alice's conditional
// update evaluates. A read-then-write would have decided the outcome from
// alice's stale read and overwritten bob. The conditional predicate must
// instead match zero rows and report the conflict.
func TestBdStoreClaimTwoConcurrentClaimantsProduceOneWinner(t *testing.T) {
	f := &bdClaimFake{id: "bd-7", status: "open"}
	store := newClaimFakeStore(t, f)

	f.beforeSwap = func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.status = "in_progress"
		f.assignee = "bob" // bob won between alice's read and alice's swap
	}

	_, ok, err := store.Claim("bd-7", "alice")
	if err != nil {
		t.Fatalf("Claim losing a race: %v", err)
	}
	if ok {
		t.Fatalf("alice won a bead bob already held; the claim is a read-then-write, not a CAS")
	}
	if f.assignee != "bob" {
		t.Errorf("assignee = %q after the race, want bob to still hold it", f.assignee)
	}
}
