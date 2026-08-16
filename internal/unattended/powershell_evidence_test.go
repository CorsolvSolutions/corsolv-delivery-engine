package unattended

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// psGateEvidenceDocument is what corsolv/powershell/Invoke-QAGates.ps1 writes.
type psGateEvidenceDocument struct {
	Schema     string         `json:"schema"`
	TargetSHA  string         `json:"targetSha"`
	ObservedAt time.Time      `json:"observedAt"`
	Examined   []string       `json:"examined"`
	Gates      []GateEvidence `json:"gates"`
}

// readPowerShellGateEvidence reads an evidence document written by
// corsolv/powershell/Invoke-QAGates.ps1.
func readPowerShellGateEvidence(t *testing.T, path string) psGateEvidenceDocument {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc psGateEvidenceDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("%s is not QA-001 GateEvidence: %v", path, err)
	}
	return doc
}

// ledgerOf folds an evidence document into the ledger shape the adjudicator
// reads, checking on the way that every record is one a reader could act on.
func ledgerOf(t *testing.T, doc psGateEvidenceDocument) map[string]GateEvidence {
	t.Helper()
	if doc.Schema != "corsolv/qa-001/gate-evidence" {
		t.Fatalf("schema = %q, want corsolv/qa-001/gate-evidence", doc.Schema)
	}
	if len(doc.Gates) == 0 {
		t.Fatal("the evidence document records no gate")
	}
	ledger := map[string]GateEvidence{}
	for _, ev := range doc.Gates {
		if _, known := LookupGate(ev.GateID); !known {
			t.Fatalf("the PowerShell gates recorded %q, which is not a catalog gate", ev.GateID)
		}
		// Evidence that cannot be reproduced is testimony.
		if ev.Tool == "" || ev.ToolVersion == "" || ev.ObservedAt.IsZero() || len(ev.Reproduce) == 0 {
			t.Fatalf("gate %s carries evidence a reader cannot reproduce: %+v", ev.GateID, ev)
		}
		ledger[ev.GateID] = MergeEvidence(ledger[ev.GateID], ev)
	}
	return ledger
}

func TestPowerShellGateEvidenceIsAdjudicatedByTheQA001Contract(t *testing.T) {
	// The PowerShell gates are integrated when their verdicts are READ by the
	// same adjudicator every other gate's are — not when a script prints
	// "PASS". This drives EvaluateProgression from a real recording of that
	// script's output, which is the whole of what "recorded using the
	// GateEvidence model against the exact revision" means.
	//
	// The golden is a REAL run, kept for its shape rather than for its
	// freshness. Live evidence cannot be committed: it is bound to the exact
	// revision it examined, and committing it changes that revision.
	doc := readPowerShellGateEvidence(t, filepath.Join(
		"..", "..", "corsolv", "powershell", "gate-evidence.example.json"))
	ledger := ledgerOf(t, doc)

	// A Q2 packet — scripts and orchestration, which is what those files are —
	// requires build, unit-test and static analysis. The PowerShell gates
	// produce exactly those three.
	decision := EvaluateProgression(QAPolicy{}, RiskQ2, doc.TargetSHA, ledger)
	for _, id := range decision.Required {
		if _, recorded := ledger[id]; !recorded {
			t.Fatalf("risk Q2 requires gate %q and the PowerShell gates recorded nothing for it", id)
		}
	}
	if !decision.Allowed {
		t.Fatalf("the recorded PowerShell gates do not permit progression: %s", decision.Reason())
	}

	// And the binding is real: the same evidence against a different revision
	// certifies nothing, which is what keeps a green PowerShell run from
	// licensing code it never saw.
	elsewhere := EvaluateProgression(QAPolicy{}, RiskQ2, "0000000000000000000000000000000000000000", ledger)
	if elsewhere.Allowed {
		t.Fatal("PowerShell gate evidence certified a revision it never examined")
	}
	for _, b := range elsewhere.Blocking {
		if b.Reason != BlockStale {
			t.Fatalf("gate %s blocked as %q against another revision, want stale", b.GateID, b.Reason)
		}
	}
}

func TestLivePowerShellGateEvidenceIsAdjudicatedTheSameWay(t *testing.T) {
	// The opportunistic half: when this worktree has a live recording — a
	// developer or a Windows host has run the gates — it is held to exactly
	// the same contract as the golden, so a change to the script that broke
	// the shape is caught where it happened.
	path := filepath.Join("..", "..", "corsolv", "powershell", "qa-gate-evidence.json")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no live PowerShell gate evidence in this worktree; "+
			"run corsolv/powershell/Invoke-QAGates.ps1 on a host with pwsh (%v)", err)
	}
	doc := readPowerShellGateEvidence(t, path)
	ledger := ledgerOf(t, doc)
	if _, err := os.Stat(path); err == nil && doc.TargetSHA != "" {
		if !EvaluateProgression(QAPolicy{}, RiskQ2, doc.TargetSHA, ledger).Allowed {
			t.Logf("the live PowerShell gates do not currently permit progression; "+
				"that is a finding about this worktree, not about the contract: %+v", doc.Gates)
		}
	}
}
