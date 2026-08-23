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

// verificationLedger is what build_controls emits for a fully adjudicated
// VERIFICATION package. Keep it byte-identical to that function too: a
// verification has no pull request and no merge of its own, so its three rows
// are different rows, and a change to either side that stops them matching would
// score every honest verification as unverified.
const verificationLedger = "control\tstatus\treason\n" +
	"wp-verify-one verified commit is on the authoritative branch\tPASS\t73e4eebbd715b355d932c0791d5ccf6c14296b49\n" +
	"wp-verify-one required evidence present at the verified commit\tPASS\tevidence/report.json\n" +
	"wp-verify-one declared verification gates passed at the verified commit\tPASS\tthe controller ran the package's declared gates against 73e4eeb\n"

// A VERIFICATION IS HELD TO ITS OWN EVIDENCE, AND HELD TO ALL OF IT.
func TestAVerificationsLedgerRowsMeetTheVerificationGate(t *testing.T) {
	gate, status := deriveCompletionGate(verificationLedger, "wp-verify-one", gateKindVerification)
	if status != "met" {
		t.Fatalf("a verification's own ledger rows must meet its gate, got %q\nledger:\n%s", status, verificationLedger)
	}
	if gate == "" {
		t.Error("a met gate must still name what it consists of")
	}
}

// AND IT IS NOT HELD TO THE OTHER KIND'S. Scoring a verification against the
// promoted route's three controls would report every honest verification as
// not-met, because it structurally cannot have them; scoring a mutation against
// a verification's would accept a merge that nothing checked.
func TestTheTwoGateKindsDoNotSatisfyEachOther(t *testing.T) {
	if _, status := deriveCompletionGate(verificationLedger, "wp-verify-one", ""); status != "not-met" {
		t.Errorf("a verification judged as a mutation must be not-met, got %q", status)
	}
	if _, status := deriveCompletionGate(driverLedger, "wp-scaffold", gateKindVerification); status != "not-met" {
		t.Errorf("a merge judged as a verification must be not-met, got %q", status)
	}
}

// A verification that proved only part of what it claims is not an acceptance,
// for the same reason a partial merge gate is not.
func TestAPartialVerificationDoesNotMeetTheGate(t *testing.T) {
	partial := "control\tstatus\treason\n" +
		"wp-verify-one verified commit is on the authoritative branch\tPASS\t73e4eeb\n" +
		"wp-verify-one required evidence present at the verified commit\tPASS\tevidence/report.json\n"
	if _, status := deriveCompletionGate(partial, "wp-verify-one", gateKindVerification); status != "partially-met" {
		t.Errorf("a verification whose gates never ran must not be met, got %q", status)
	}
}

func TestTheDriversLedgerRowsMeetTheCompletionGate(t *testing.T) {
	gate, status := deriveCompletionGate(driverLedger, "wp-scaffold", "")
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
	_, status := deriveCompletionGate(driverLedger, "wp-status-core", "")
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
	_, status := deriveCompletionGate(partial, "wp-scaffold", "")
	if status != "partially-met" {
		t.Fatalf("two of three controls is partially-met, got %q", status)
	}
}

func TestAnEmptyLedgerMeetsNothing(t *testing.T) {
	_, status := deriveCompletionGate("control\tstatus\treason\n", "wp-scaffold", "")
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
	_, status := deriveCompletionGate(failed, "wp-scaffold", "")
	if status != "partially-met" {
		t.Fatalf("a failed required-CI control cannot count toward the gate, got %q", status)
	}
}
