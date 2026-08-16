// The completion gate is derived from a control ledger, so the ledger's row
// shape is a contract between whoever writes it and this program. It was an
// undocumented one, and a delivery run broke on it: the run rendered its
// projection through this binary, wrote no ledger at all, and the whole `project`
// stage died on a missing file after every package had already merged.
//
// These pin both halves. The rows are spelled here exactly as the delivery
// driver's build_controls writes them, so a change to either side that stops
// them matching fails here rather than at the end of a live delivery.
package main

import "testing"

// driverLedger is what corsolv/delivery/driver.sh build_controls emits for one
// fully adjudicated package. Keep it byte-identical to that function.
const driverLedger = "control\tstatus\treason\n" +
	"wp-scaffold required CI passed on the exact pull-request head\tPASS\trun 31907504842\n" +
	"wp-scaffold independent assurance passed\tPASS\tthe controller re-ran the package's declared gates\n" +
	"wp-scaffold merged through repository governance\tPASS\tc971d8489\n"

func TestTheDriversLedgerRowsMeetTheCompletionGate(t *testing.T) {
	gate, status := deriveCompletionGate(driverLedger, "wp-scaffold")
	if status != "met" {
		t.Fatalf("the driver's own ledger rows must meet the gate, got %q\nledger:\n%s", status, driverLedger)
	}
	if gate == "" {
		t.Error("a met gate must still name what it consists of")
	}
}

// A label keys a task to ITS rows. Another package's passes must never satisfy
// this one's gate — that would score acceptance for work nothing verified.
func TestOnePackagesControlsDoNotSatisfyAnothers(t *testing.T) {
	_, status := deriveCompletionGate(driverLedger, "wp-status-core")
	if status != "not-met" {
		t.Fatalf("wp-status-core has no rows and must be not-met, got %q", status)
	}
}

// The conservative default is the whole point: a merge with no recorded
// verification is publication without acceptance.
func TestAMissingControlLeavesTheGateShortOfMet(t *testing.T) {
	partial := "control\tstatus\treason\n" +
		"wp-scaffold required CI passed on the exact pull-request head\tPASS\trun 1\n" +
		"wp-scaffold merged through repository governance\tPASS\tabc1234\n"
	_, status := deriveCompletionGate(partial, "wp-scaffold")
	if status != "partially-met" {
		t.Fatalf("two of three controls is partially-met, got %q", status)
	}
}

func TestAnEmptyLedgerMeetsNothing(t *testing.T) {
	_, status := deriveCompletionGate("control\tstatus\treason\n", "wp-scaffold")
	if status != "not-met" {
		t.Fatalf("no controls is not-met, got %q", status)
	}
}

// A recorded FAIL is not a pass. Reading only the control's name would let a
// failed run project a met gate.
func TestAFailedControlIsNotAPass(t *testing.T) {
	failed := "control\tstatus\treason\n" +
		"wp-scaffold required CI passed on the exact pull-request head\tFAIL\tconclusion 'failure'\n" +
		"wp-scaffold independent assurance passed\tPASS\tgates re-run\n" +
		"wp-scaffold merged through repository governance\tPASS\tabc1234\n"
	_, status := deriveCompletionGate(failed, "wp-scaffold")
	if status != "partially-met" {
		t.Fatalf("a failed required-CI control cannot count toward the gate, got %q", status)
	}
}
