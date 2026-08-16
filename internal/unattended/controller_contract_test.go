package unattended

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The cross-language half of the controller-result contract.
//
// The consumer is this package; the producer on the pilot's Windows host is
// corsolv/powershell/CorsolvControllerResult.psm1. Two implementations of one
// wire format drift, and the drift is invisible until a run adjudicates a
// document its producer thought it had written correctly.
//
// corsolv/powershell/controller-result.contract.json is what stops that: it is
// the authority for the vocabulary and for the outcome matrix, these tests
// check the Go side against it, and CorsolvControllerResult.Tests.ps1 checks
// the PowerShell side against the same file. A fixture added to it fails on
// both sides until both sides handle it.

type controllerContract struct {
	Version          int      `json:"version"`
	ResultPathEnvVar string   `json:"resultPathEnvVar"`
	States           []string `json:"states"`
	TerminalReasons  struct {
		Resumable     []string `json:"resumable"`
		Retryable     []string `json:"retryable"`
		HumanBoundary []string `json:"humanBoundary"`
	} `json:"terminalReasons"`
	Fixtures []struct {
		Name              string          `json:"name"`
		Why               string          `json:"why"`
		Document          json.RawMessage `json:"document"`
		ExitedZero        bool            `json:"exitedZero"`
		ExpectDisposition string          `json:"expectDisposition"`
	} `json:"fixtures"`
	Invalid []struct {
		Name string `json:"name"`
		Raw  string `json:"raw"`
	} `json:"invalid"`
}

func loadControllerContract(t *testing.T) controllerContract {
	t.Helper()
	path := filepath.Join("..", "..", "corsolv", "powershell", "controller-result.contract.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the shared controller-result contract: %v", err)
	}
	var c controllerContract
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	if len(c.Fixtures) == 0 || len(c.Invalid) == 0 || len(c.States) == 0 {
		t.Fatalf("%s declares no vocabulary to check against", path)
	}
	return c
}

func TestTheGoVocabularyIsTheContractsVocabulary(t *testing.T) {
	c := loadControllerContract(t)

	declared := make([]string, 0, len(c.States))
	declared = append(declared, c.States...)
	sort.Strings(declared)

	got := make([]string, 0)
	for _, s := range ControllerStates() {
		got = append(got, string(s))
	}
	sort.Strings(got)

	if len(got) != len(declared) {
		t.Fatalf("Go declares %v; the contract declares %v", got, declared)
	}
	for i := range got {
		if got[i] != declared[i] {
			t.Fatalf("Go declares %v; the contract declares %v", got, declared)
		}
	}
}

func TestTheGoTerminalReasonsAreTheContractsTerminalReasons(t *testing.T) {
	c := loadControllerContract(t)

	for _, reason := range c.TerminalReasons.Resumable {
		if !IsResumableReason(reason) {
			t.Fatalf("the contract declares %q resumable and Go does not", reason)
		}
	}
	for _, reason := range c.TerminalReasons.Retryable {
		if IsResumableReason(reason) {
			t.Fatalf("the contract declares %q retryable and Go resumes on it", reason)
		}
		if class := reasonClasses[reason]; class != FailureExternalService {
			t.Fatalf("terminal reason %q carries class %q, want external-service", reason, class)
		}
	}
	for _, reason := range c.TerminalReasons.HumanBoundary {
		if class := reasonClasses[reason]; class != FailureAuth {
			t.Fatalf("terminal reason %q carries class %q, want auth", reason, class)
		}
	}
}

func TestEveryContractFixtureAdjudicatesAsTheContractSays(t *testing.T) {
	c := loadControllerContract(t)
	for _, f := range c.Fixtures {
		t.Run(f.Name, func(t *testing.T) {
			res, err := ParseControllerResult(f.Document)
			if err != nil {
				t.Fatalf("the contract declares this fixture usable and Go refused it: %v", err)
			}
			v := InterpretExecution(Execution{
				ExitedZero: f.ExitedZero, DeclaredResult: true, Result: &res,
			}, nil)
			if string(v.Disposition) != f.ExpectDisposition {
				t.Fatalf("disposition = %s, want %s (%s) — %s",
					v.Disposition, f.ExpectDisposition, v.Reason, f.Why)
			}
		})
	}
}

func TestEveryContractInvalidFixtureIsRefused(t *testing.T) {
	c := loadControllerContract(t)
	for _, f := range c.Invalid {
		t.Run(f.Name, func(t *testing.T) {
			if _, err := ParseControllerResult([]byte(f.Raw)); err == nil {
				t.Fatalf("the contract declares this document unusable and Go accepted it")
			}
		})
	}
}

func TestTheContractNamesTheEnvironmentVariableTheRunExports(t *testing.T) {
	// The producer learns where to write from this variable and nothing else.
	// A rename on one side and not the other is a task that writes a result
	// nobody reads, which reaches the run as silence.
	c := loadControllerContract(t)
	if c.ResultPathEnvVar != "GC_UNATTENDED_RESULT_PATH" {
		t.Fatalf("the contract names %q; the run exports GC_UNATTENDED_RESULT_PATH", c.ResultPathEnvVar)
	}
	stateDir := t.TempDir()
	r := &Runner{Spec: Spec{StateDir: stateDir}}
	if got := r.resultPath(Task{ResultPath: "result.json"}); got != filepath.Join(stateDir, "result.json") {
		t.Fatalf("resultPath = %q, want it anchored in the state directory", got)
	}
}
