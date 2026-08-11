package storebinding

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// bdClaimRunnerFake is the same one-row model the store-side claim tests use,
// reduced to what the adapter path needs. It evaluates the conditional UPDATE's
// precondition itself so the adapter is exercised against a store whose claim
// really is a compare-and-swap rather than a stub that always says yes.
type bdClaimRunnerFake struct {
	mu       sync.Mutex
	id       string
	status   string
	assignee string
}

func (f *bdClaimRunnerFake) runner() beads.CommandRunner {
	return func(_, _ string, args ...string) ([]byte, error) {
		switch {
		case len(args) >= 2 && args[0] == "show":
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.status == "" {
				return nil, fmt.Errorf("issue not found")
			}
			return json.Marshal([]map[string]any{{
				"id": f.id, "title": "claimable", "issue_type": "task",
				"status": f.status, "assignee": f.assignee,
			}})
		case len(args) >= 3 && args[0] == "sql":
			f.mu.Lock()
			defer f.mu.Unlock()
			query := args[len(args)-1]
			want := ""
			if i := strings.Index(query, "SET status = 'in_progress', assignee = '"); i >= 0 {
				rest := query[i+len("SET status = 'in_progress', assignee = '"):]
				if j := strings.Index(rest, "'"); j >= 0 {
					want = rest[:j]
				}
			}
			rows := 0
			if (f.status == "open" || f.status == "in_progress") &&
				(f.assignee == "" || f.assignee == want) && want != "" {
				f.status, f.assignee, rows = "in_progress", want, 1
			}
			return json.Marshal(map[string]int{"rows_affected": rows})
		}
		return nil, fmt.Errorf("bdClaimRunnerFake: unexpected bd invocation %v", args)
	}
}

// TestBeadsGraphAdapterServesTheAssignmentClaimForBdStore is the G7 deletion
// license: the graph front door's assignment-claim capability is genuinely
// AVAILABLE over a bd-backed store, rather than reported unsupported.
//
// The adapter refuses, by design, to emulate the compare-and-swap with a
// read-then-write — a store lacking the two-argument claim gets
// "unsupported: assignment claim" instead of a fabricated single-winner
// guarantee. Before this change BdStore only had the ambient-actor
// ClaimAsActor(id), so every bd-backed binding took that refusal and no
// bd-backed provider could declare Claims: true honestly.
//
// This asserts the refusal is now satisfied by an implementation rather than
// bypassed: the same call that used to return the capability error now performs
// the swap.
func TestBeadsGraphAdapterServesTheAssignmentClaimForBdStore(t *testing.T) {
	f := &bdClaimRunnerFake{id: "bd-1", status: "open"}
	adapter := &beadsGraphAdapter{store: beads.NewBdStore("/city", f.runner())}

	claimed, ok, err := adapter.Claim("bd-1", "alice")
	if err != nil {
		if errors.Is(err, ErrBeadsAdapterCapability) {
			t.Fatalf("the adapter still reports assignment claim unsupported over BdStore: %v", err)
		}
		t.Fatalf("Claim through the graph adapter: %v", err)
	}
	if !ok {
		t.Fatalf("Claim on an unassigned open bead lost through the adapter")
	}
	if claimed.Assignee != "alice" {
		t.Errorf("Assignee = %q after a won claim, want alice", claimed.Assignee)
	}

	// And the conflict half still travels as ok=false rather than as an error,
	// which is what lets a caller distinguish "someone else owns it" from
	// "the store cannot do this".
	if _, ok, err := adapter.Claim("bd-1", "bob"); err != nil {
		t.Fatalf("Claim by a second holder through the adapter: %v", err)
	} else if ok {
		t.Fatalf("a second holder also won the claim through the adapter")
	}
}

// TestBeadsGraphAdapterStillRefusesStoresWithoutAnAssignmentClaim pins the
// other half: the refusal must survive for stores that genuinely cannot do it.
// Satisfying one store must not turn the capability into an unconditional yes.
func TestBeadsGraphAdapterStillRefusesStoresWithoutAnAssignmentClaim(t *testing.T) {
	adapter := &beadsGraphAdapter{store: claimlessStore{Store: beads.NewMemStore()}}

	if _, _, err := adapter.Claim("anything", "alice"); err == nil {
		t.Fatalf("a store with no assignment claim reported a successful claim; " +
			"the adapter must refuse rather than emulate the compare-and-swap")
	} else if !errors.Is(err, ErrBeadsAdapterCapability) {
		t.Fatalf("refusal error = %v, want an undeclared-capability error", err)
	}
}

// claimlessStore models a backend that cannot offer the assignee-aware
// compare-and-swap.
//
// It deliberately declares NO Claim method. Defining one — even a panicking
// stub — would make it satisfy the very interface it exists to lack, and the
// adapter would call it instead of refusing. The wrapper struct is what hides
// any claim the embedded store may have: promotion stops at the embedded
// interface's method set, and beads.Store does not include Claim (the adapter
// discovers it by type assertion, which is the whole reason this refusal path
// exists).
type claimlessStore struct{ beads.Store }
