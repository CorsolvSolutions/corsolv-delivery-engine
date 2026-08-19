//go:build integration

// THE BASH DRIVER'S HALF OF THE CONTROLLER-RESULT CONTRACT.
//
// QA-002 made a supervised task's own statement of what happened the verdict,
// and took the residual exit status out of the decision. It did that for the Go
// consumer and for the PowerShell producer. The Bash driver — the executable a
// compiled delivery run invokes for every stage, and the only thing that ever
// reaches a command line — was still saying everything it had to say through an
// exit status.
//
// These are the parity regressions. Every one of them drives the REAL producer:
// either driver.sh itself, or the driver's own controller-contract.sh, never a
// document this file wrote to look like one. Every one of them then asks the
// REAL consumer — internal/unattended's InterpretExecution, the same function
// the run itself calls — what the run would do about it. Nothing here
// re-implements the decision, which is the point: the driver must not be able to
// make a different authoritative decision from the controller for the same
// supervised result, and it cannot, because it makes no decision at all.
//
// Like the rest of this directory's driver tests they spawn bash, so they carry
// the integration tag rather than growing the untagged subprocess baseline.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/handoff"
	"github.com/gastownhall/gascity/internal/unattended"
)

// contractDocument is one fixture's document, in the wire spelling both
// producers emit and the consumer reads.
type contractDocument struct {
	State          string `json:"state"`
	TerminalReason string `json:"terminal_reason,omitempty"`
	Subtype        string `json:"subtype,omitempty"`
	IsError        bool   `json:"is_error,omitempty"`
	Detail         string `json:"detail,omitempty"`
	NumTurns       int    `json:"num_turns,omitempty"`
}

// sharedContract is the document all three implementations are checked against.
type sharedContract struct {
	ResultPathEnvVar string   `json:"resultPathEnvVar"`
	States           []string `json:"states"`
	Fixtures         []struct {
		Name              string           `json:"name"`
		Why               string           `json:"why"`
		Document          contractDocument `json:"document"`
		ExitedZero        bool             `json:"exitedZero"`
		ExpectDisposition string           `json:"expectDisposition"`
	} `json:"fixtures"`
	Invalid []struct {
		Name string `json:"name"`
		Raw  string `json:"raw"`
	} `json:"invalid"`
}

func loadSharedContract(t *testing.T) sharedContract {
	t.Helper()
	path := filepath.Join(engineRepo(t), "corsolv", "powershell", "controller-result.contract.json")
	data, err := os.ReadFile(path) //nolint:gosec // the repository's own contract
	if err != nil {
		t.Fatalf("reading the shared controller-result contract: %v", err)
	}
	var c sharedContract
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	if len(c.Fixtures) == 0 || len(c.Invalid) == 0 {
		t.Fatalf("%s declares no matrix to check against", path)
	}
	return c
}

// contractLib is the driver's own producer, which is what these tests drive.
func contractLib(t *testing.T) string {
	t.Helper()
	return filepath.Join(filepath.Dir(driverPath(t)), "controller-contract.sh")
}

// stateFromDriverProducer has the driver's own producer write one document, and
// returns whatever the run's consumer makes of the file that appears.
//
// The document is never assembled here. A test that wrote the JSON itself would
// prove the consumer understands a document this file believes the driver
// produces, which is the one thing already known.
func stateFromDriverProducer(t *testing.T, path string, args ...string) (string, []byte, error) {
	t.Helper()
	bash := bashOrSkip(t)

	script := `set -eu
. "$1"
shift
cr_write "$@"`
	cmd := exec.Command(bash, append([]string{"-c", script, "bash", contractLib(t)}, args...)...) //nolint:gosec // the producer under test
	cmd.Env = append(os.Environ(), "GC_UNATTENDED_RESULT_PATH="+path)
	out, err := cmd.CombinedOutput()
	written, readErr := os.ReadFile(path) //nolint:gosec // a path this test chose
	if readErr != nil {
		written = nil
	}
	return string(out), written, err
}

// THE MATRIX. Every outcome the contract declares, produced by the driver's own
// writer and adjudicated by the run's own interpreter.
//
// The two directions of the residual exit status are both in it, and both were
// observed on the pilot: a correct outcome behind a non-zero exit, and a
// non-outcome behind a clean one. `exitedZero` is carried through exactly as the
// contract states it, so a fixture whose whole point is that the exit status
// disagrees with the statement is checked with the exit status disagreeing.
func TestEveryContractOutcomeTheDriverProducesAdjudicatesAsTheContractSays(t *testing.T) {
	c := loadSharedContract(t)
	for _, f := range c.Fixtures {
		t.Run(f.Name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "result.json")
			out, _, err := stateFromDriverProducer(t, path, producerArgs(f.Document)...)
			if err != nil {
				t.Fatalf("the driver's producer refused a document the contract declares usable: %v\n%s", err, out)
			}

			res, err := unattended.ReadControllerResult(path)
			if err != nil {
				t.Fatalf("the run could not adjudicate what the driver wrote: %v", err)
			}
			v := unattended.InterpretExecution(unattended.Execution{
				ExitedZero: f.ExitedZero, DeclaredResult: true, Result: &res,
			}, nil)
			if string(v.Disposition) != f.ExpectDisposition {
				t.Fatalf("disposition = %s, want %s (%s) — %s",
					v.Disposition, f.ExpectDisposition, v.Reason, f.Why)
			}
		})
	}
}

// producerArgs renders a contract document as the options a stage passes to the
// driver's writer.
func producerArgs(doc contractDocument) []string {
	args := []string{"--state", doc.State}
	if doc.TerminalReason != "" {
		args = append(args, "--reason", doc.TerminalReason)
	}
	if doc.Subtype != "" {
		args = append(args, "--subtype", doc.Subtype)
	}
	if doc.Detail != "" {
		args = append(args, "--detail", doc.Detail)
	}
	if doc.NumTurns > 0 {
		args = append(args, "--turns", itoa(doc.NumTurns))
	}
	if doc.IsError {
		args = append(args, "--error")
	}
	return args
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// The retry policy a named terminal reason resolves to is the run's, and it has
// to be reachable for the reason to mean anything. A bounded retry that is
// bounded at one attempt is not a retry.
func TestAPermittedTransientReasonIsRetriedUnderTheExternalServicePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	if _, _, err := stateFromDriverProducer(t, path, "--state", "FAILED", "--reason", "network_timeout"); err != nil {
		t.Fatalf("the driver's producer refused a declared terminal reason: %v", err)
	}
	res, err := unattended.ReadControllerResult(path)
	if err != nil {
		t.Fatal(err)
	}
	v := unattended.InterpretExecution(unattended.Execution{DeclaredResult: true, Result: &res}, nil)
	if v.Disposition != unattended.DispositionRetry {
		t.Fatalf("disposition = %s, want retry", v.Disposition)
	}
	if v.Class != unattended.FailureExternalService {
		t.Fatalf("class = %s, want external-service", v.Class)
	}
	if got := unattended.PolicyFor(v.Class).MaxAttempts; got < 2 {
		t.Fatalf("the external-service policy allows %d attempt(s); a bounded retry needs more than one", got)
	}

	// And the compiled stages must not cap it back to one. They used to: every
	// stage declared MaxAttempts 1, so a rate limit from the forge ended a
	// delivery the policy would have carried.
	_, work, err := handoff.Compile(fixtureIntent(), fixturePlan(), parityHost(t), "run-1")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	for _, task := range work.Tasks {
		if task.MaxAttempts == 1 {
			t.Fatalf("task %q caps itself at one attempt, so no class policy can retry it", task.ID)
		}
	}
}

// A document the run could not adjudicate is never a pass, and the driver
// refuses to produce one in the first place.
//
// The two halves are separate obligations. The producer refusing is what keeps a
// stage from reporting an outcome nobody can read; the consumer refusing is what
// keeps a malformed document — from any producer, or from a half-finished write
// — out of the success path.
func TestAnUnusableResultIsNeverASuccess(t *testing.T) {
	c := loadSharedContract(t)
	for _, f := range c.Invalid {
		t.Run("consumer/"+f.Name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "result.json")
			if err := os.WriteFile(path, []byte(f.Raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := unattended.ReadControllerResult(path)
			if err == nil {
				t.Fatal("the contract declares this document unusable and the run accepted it")
			}
			// A clean exit deliberately: the exit status is the very signal the
			// promise to state an outcome was made to replace.
			v := unattended.InterpretExecution(unattended.Execution{
				ExitedZero: true, DeclaredResult: true, ResultErr: err,
			}, nil)
			if v.Disposition != unattended.DispositionFailSafe {
				t.Fatalf("disposition = %s, want fail-safe", v.Disposition)
			}
			if v.GateResult == unattended.GatePass {
				t.Fatal("an unusable result recorded a passing gate verdict")
			}
		})
	}

	for _, state := range []string{"MOSTLY_DONE", "done", "", "COMPLETE!"} {
		t.Run("producer/"+state, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "result.json")
			out, written, err := stateFromDriverProducer(t, path, "--state", state)
			if err == nil {
				t.Fatalf("the driver produced a result stating %q, which the run cannot adjudicate:\n%s", state, written)
			}
			if written != nil {
				t.Fatalf("a refused write left a document behind: %s", written)
			}
			if !strings.Contains(out, "refusing to write") {
				t.Fatalf("the refusal must say what it refused, got:\n%s", out)
			}
		})
	}
}

// A STAGE THAT IS KILLED STATES NOTHING, AND A PREVIOUS ATTEMPT'S STATEMENT IS
// NOT ITS STATEMENT.
//
// This is the staleness half of the contract, at the producer. The run clears
// the result path before each attempt it starts; the driver clears it on the way
// in as well, because the two are not the same guarantee — a stage re-entered
// inside one attempt, or run by anything other than the run, would otherwise
// inherit an answer it did not give. What the run must see from a killed stage
// is an absence, and an absence is never a pass.
func TestAKilledStageLeavesNoStatementBehindAtAll(t *testing.T) {
	e := newRecoveryEnv(t)
	e.seedRuntime(map[string]string{
		"dispatched":    "2026-08-14T16:43:00Z",
		"bead.wp-one":   beadOne,
		"bead.wp-two":   beadTwo,
		"wt.wp-one":     e.makeWorktree("wp-one"),
		"branch.wp-one": "delivery/20260814T164300Z/wp-one",
	})
	e.setBead(beadOne, "open")
	e.setBead(beadTwo, "open")

	result := filepath.Join(t.TempDir(), "result.json")
	// The previous attempt's answer, sitting exactly where this attempt's would
	// go. If it survives, the run reads a COMPLETE this stage never stated.
	if err := os.WriteFile(result, []byte(`{"state":"COMPLETE"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", //nolint:gosec // the driver under test
		driverPath(t), "await", "-package", "wp-one",
		"-project", e.project, "-state", e.state, "-deadline", "600")
	cmd.Env = e.scrubbedEnv([]string{"GC_UNATTENDED_RESULT_PATH=" + result})
	// The stage's own `sleep` is orphaned by the kill and keeps the output pipes
	// open; without this the test waits out a sleep nobody is waiting for.
	cmd.WaitDelay = time.Second
	out, _ := cmd.CombinedOutput()
	if ctx.Err() == nil {
		t.Fatalf("the stage was meant to be killed while it was still waiting:\n%s", out)
	}

	if _, err := os.Stat(result); !os.IsNotExist(err) {
		data, _ := os.ReadFile(result) //nolint:gosec // a path this test chose
		t.Fatalf("a killed stage left a statement behind: %s", data)
	}

	// What the run does about the silence.
	res, err := unattended.ReadControllerResult(result)
	if err == nil {
		t.Fatalf("the run read a result from a stage that never wrote one: %+v", res)
	}
	v := unattended.InterpretExecution(unattended.Execution{
		ExitedZero: true, DeclaredResult: true, ResultErr: err,
	}, nil)
	if v.Disposition != unattended.DispositionFailSafe {
		t.Fatalf("disposition = %s, want fail-safe", v.Disposition)
	}
}

// THE SMOKE, AND THE DEFECT THIS PACKET EXISTS FOR: a misleading exit status
// cannot override what the stage said.
//
// Both directions, each through a wrapper that leaves a residue disagreeing with
// the statement — which is exactly the shape the pilot produced when a wrapper
// exited zero over an agent that had been cut off.
func TestAMisleadingExitStatusCannotOverrideTheStatement(t *testing.T) {
	t.Run("a clean exit over unfinished work resumes", func(t *testing.T) {
		e := newRecoveryEnv(t)
		e.seedRuntime(map[string]string{
			"dispatched":    "2026-08-14T16:43:00Z",
			"bead.wp-one":   beadOne,
			"bead.wp-two":   beadTwo,
			"wt.wp-one":     e.makeWorktree("wp-one"),
			"branch.wp-one": "delivery/20260814T164300Z/wp-one",
		})
		e.setBead(beadOne, "open")
		e.setBead(beadTwo, "open")

		result := filepath.Join(t.TempDir(), "result.json")
		out, code := e.runWrapped(t, 0, result,
			"await", "-package", "wp-one", "-deadline", "0")
		if code != 0 {
			t.Fatalf("the wrapper must have exited zero, got %d:\n%s", code, out)
		}

		v := adjudicate(t, result, true)
		if v.Disposition != unattended.DispositionContinue {
			t.Fatalf("disposition = %s, want continue — %s\n%s", v.Disposition, v.Reason, out)
		}
		if !v.Disposition.Resumable() {
			t.Fatal("unfinished work must be re-offered rather than finished with")
		}
	})

	t.Run("a non-zero exit over completed work still completes", func(t *testing.T) {
		e := newRecoveryEnv(t)
		e.seedRuntime(map[string]string{
			"dispatched":  "2026-08-14T16:43:00Z",
			"bead.wp-one": beadOne,
			"wt.wp-one":   e.makeWorktree("wp-one"),
			"bead.wp-two": beadTwo,
			"wt.wp-two":   e.makeWorktree("wp-two"),
		})
		e.setBead(beadOne, "closed")
		e.setBead(beadTwo, "closed")

		result := filepath.Join(t.TempDir(), "result.json")
		out, code := e.runWrapped(t, 17, result, "dispatch")
		if code != 17 {
			t.Fatalf("the wrapper must have exited 17, got %d:\n%s", code, out)
		}

		v := adjudicate(t, result, false)
		if v.Disposition != unattended.DispositionSucceeded {
			t.Fatalf("disposition = %s, want succeeded — %s\n%s", v.Disposition, v.Reason, out)
		}
	})
}

// A BOUNDARY A PERSON MUST LIFT IS STATED AS ONE, AND THE TWO KINDS ARE STATED
// APART.
//
// Both stop the run and neither is retried, so telling them apart is not retry
// arithmetic: it is the difference between a person spending ten seconds on a
// login and a person having to make a decision about a machine that is not this
// run's to restart.
func TestTheTwoHumanBoundariesStopSafelyAndDistinctly(t *testing.T) {
	t.Run("a supervisor this run may not restart", func(t *testing.T) {
		e := newRecoveryEnv(t)
		e.seedRuntime(nil) // a city already exists, so city-up only reconciles

		result := filepath.Join(t.TempDir(), "result.json")
		code, out := e.runStageWithResult(t, result, []string{
			"GC_STUB_SUPERVISOR_REPLY=gc supervisor reload: supervisor is not running",
			"GC_STUB_SUPERVISOR_CODE=1",
		}, "city-up")
		if code == 0 {
			t.Fatalf("city-up reported success over a supervisor that will never start its agents:\n%s", out)
		}

		v := adjudicate(t, result, false)
		if v.Disposition != unattended.DispositionHumanBlocked {
			t.Fatalf("disposition = %s, want human-blocked — %s\n%s", v.Disposition, v.Reason, out)
		}
		if v.Class != unattended.FailureHumanDecision {
			t.Fatalf("class = %s, want human-decision: restarting a machine-wide process is a judgement, not a credential", v.Class)
		}
		if v.State != unattended.StateHumanBlocked {
			t.Fatalf("state = %s, want HUMAN_BLOCKED", v.State)
		}
	})

	t.Run("a credential the forge refused", func(t *testing.T) {
		e := newRecoveryEnv(t)
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Skip("git is not available")
		}
		// A git that refuses the clone the way a forge refuses an expired
		// credential, and is otherwise the real thing. city-up clones before it
		// does anything else, so this is the first git call it makes.
		e.writeStub("git", `#!/usr/bin/env bash
if [ "${1:-}" = clone ]; then
  printf 'remote: Invalid username or password.\nfatal: Authentication failed for '"'"'https://github.com/example/repo.git'"'"'\n' >&2
  exit 128
fi
exec "$GIT_REAL" "$@"
`)

		result := filepath.Join(t.TempDir(), "result.json")
		code, out := e.runStageWithResult(t, result, []string{"GIT_REAL=" + realGit}, "city-up")
		if code == 0 {
			t.Fatalf("city-up reported success over a refused clone:\n%s", out)
		}

		v := adjudicate(t, result, false)
		if v.Disposition != unattended.DispositionHumanBlocked {
			t.Fatalf("disposition = %s, want human-blocked — %s\n%s", v.Disposition, v.Reason, out)
		}
		if v.Class != unattended.FailureAuth {
			t.Fatalf("class = %s, want auth\n%s", v.Class, out)
		}
		if v.TerminalReason != unattended.ReasonAuthenticationFailed {
			t.Fatalf("terminal reason = %q, want %q — the refusal is captured into an evidence file, and a reason nobody named never reaches the run",
				v.TerminalReason, unattended.ReasonAuthenticationFailed)
		}
	})
}

// THE PROGRESSION DECISION IS READ, NEVER DERIVED.
//
// QA-001 answers whether a packet may progress, from gate evidence bound to a
// revision. The driver renders the projection an acceptance assessment reads,
// from inside the run, and it has to cap what it claims by that answer. These
// are the three ways a gate refuses — it never ran, it ran and failed, it passed
// against different code — plus the one way it permits.
func TestTheDriverReadsTheRunsProgressionDecision(t *testing.T) {
	bash := bashOrSkip(t)
	const head = "1111111111111111111111111111111111111111"
	policy := unattended.QAPolicy{}

	cases := []struct {
		name     string
		risk     unattended.RiskClass
		evidence map[string]unattended.GateEvidence
		refused  bool
		names    string
	}{
		{
			name: "every mandatory gate passed against the code in hand",
			risk: unattended.RiskQ1,
			evidence: map[string]unattended.GateEvidence{
				unattended.GateBuild:    {GateID: unattended.GateBuild, Result: unattended.GatePass, TargetSHA: head},
				unattended.GateUnitTest: {GateID: unattended.GateUnitTest, Result: unattended.GatePass, TargetSHA: head},
			},
		},
		{
			name:     "a mandatory gate never ran",
			risk:     unattended.RiskQ1,
			evidence: map[string]unattended.GateEvidence{unattended.GateBuild: {GateID: unattended.GateBuild, Result: unattended.GatePass, TargetSHA: head}},
			refused:  true,
			names:    unattended.GateUnitTest,
		},
		{
			name: "a mandatory gate ran and failed",
			risk: unattended.RiskQ1,
			evidence: map[string]unattended.GateEvidence{
				unattended.GateBuild:    {GateID: unattended.GateBuild, Result: unattended.GatePass, TargetSHA: head},
				unattended.GateUnitTest: {GateID: unattended.GateUnitTest, Result: unattended.GateFail, TargetSHA: head},
			},
			refused: true,
			names:   unattended.GateUnitTest,
		},
		{
			name: "a mandatory gate passed against different code",
			risk: unattended.RiskQ1,
			evidence: map[string]unattended.GateEvidence{
				unattended.GateBuild:    {GateID: unattended.GateBuild, Result: unattended.GatePass, TargetSHA: head},
				unattended.GateUnitTest: {GateID: unattended.GateUnitTest, Result: unattended.GatePass, TargetSHA: "2222222222222222222222222222222222222222"},
			},
			refused: true,
			names:   unattended.GateUnitTest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			decision := unattended.EvaluateProgression(policy, tc.risk, head, tc.evidence)
			if decision.Allowed == tc.refused {
				t.Fatalf("the fixture does not produce the decision it is for: allowed=%v", decision.Allowed)
			}
			// Written by the run's own publisher, so the shape the driver reads
			// is the shape the run actually writes.
			if err := unattended.WriteProgress(stateDir, unattended.Progress{RunID: "run-1", QA: decision}); err != nil {
				t.Fatal(err)
			}

			refusal := readRefusal(t, bash, contractLib(t), filepath.Join(stateDir, unattended.HeartbeatName))
			if tc.refused {
				if refusal == "" {
					t.Fatal("the run refused progression and the driver read no refusal")
				}
				if !strings.Contains(refusal, tc.names) {
					t.Fatalf("the refusal must name the gate that blocks, got %q", refusal)
				}
				return
			}
			if refusal != "" {
				t.Fatalf("the run permitted progression and the driver read a refusal: %q", refusal)
			}
		})
	}

	t.Run("a heartbeat carrying no decision is not a permission", func(t *testing.T) {
		stateDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(stateDir, unattended.HeartbeatName),
			[]byte(`{"runId":"run-1"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if readRefusal(t, bash, contractLib(t), filepath.Join(stateDir, unattended.HeartbeatName)) == "" {
			t.Fatal("a decision nobody recorded must not read as a decision that permitted something")
		}
	})

	t.Run("a stage outside a run is not governed by one", func(t *testing.T) {
		if readRefusal(t, bash, contractLib(t), filepath.Join(t.TempDir(), "heartbeat.json")) != "" {
			t.Fatal("a stage with no run to be governed by must not invent a refusal")
		}
	})
}

// AND THE CONSEQUENCE, THROUGH THE DOCUMENT A PERSON ACTUALLY READS.
//
// The projection the driver renders is the one handoff.Assess scores a delivery
// from. A package that merged is publication; whether that publication may be
// counted as delivered is the packet's mandatory gates' answer. This runs the
// real project stage twice over identical facts — the same merge, the same CI
// run, the same controller re-run of the gates — and changes nothing but the
// run's own progression decision.
func TestADeliveryProjectionCannotClaimCompletionTheRunsGatesRefuse(t *testing.T) {
	const head = "1111111111111111111111111111111111111111"

	permitted := unattended.EvaluateProgression(unattended.QAPolicy{}, unattended.RiskQ1, head,
		map[string]unattended.GateEvidence{
			unattended.GateBuild:    {GateID: unattended.GateBuild, Result: unattended.GatePass, TargetSHA: head},
			unattended.GateUnitTest: {GateID: unattended.GateUnitTest, Result: unattended.GatePass, TargetSHA: head},
		})
	refused := unattended.EvaluateProgression(unattended.QAPolicy{}, unattended.RiskQ1, head,
		map[string]unattended.GateEvidence{
			unattended.GateBuild: {GateID: unattended.GateBuild, Result: unattended.GatePass, TargetSHA: head},
		})
	if !permitted.Allowed || refused.Allowed {
		t.Fatalf("the fixtures do not produce the two decisions this test is for")
	}

	for _, tc := range []struct {
		name     string
		decision unattended.ProgressionDecision
		complete bool
	}{
		{name: "permitted", decision: permitted, complete: true},
		{name: "refused", decision: refused, complete: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRecoveryEnv(t)
			e.initRig()
			e.seedRuntime(map[string]string{
				"dispatched":       "2026-08-14T16:43:00Z",
				"bead.wp-one":      beadOne,
				"wt.wp-one":        e.makeWorktree("wp-one"),
				"branch.wp-one":    "delivery/20260814T164300Z/wp-one",
				"pr.wp-one":        "1",
				"ci.wp-one":        "9001",
				"published.wp-one": "merged",
				"merged.wp-one":    head,
				"bead.wp-two":      beadTwo,
			})
			e.setBead(beadOne, "closed")
			e.setBead(beadTwo, "open")
			e.seedCityEvents()
			e.buildProjector(t)

			runState := filepath.Join(e.root, "run")
			if err := os.MkdirAll(runState, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := unattended.WriteProgress(runState, unattended.Progress{RunID: "run-1", QA: tc.decision}); err != nil {
				t.Fatal(err)
			}

			result := filepath.Join(t.TempDir(), "result.json")
			code, out := e.runStageWithResult(t, result, []string{
				"GC_UNATTENDED_STATE_DIR=" + runState,
			}, "project")
			if code != 0 {
				t.Fatalf("the project stage must render whatever it may claim, got %d:\n%s", code, out)
			}

			ev, err := handoff.Assess(fixturePlan(), fixtureIntent(),
				filepath.Join(e.state, "PROJECT-STATE.yml"), nil)
			if err != nil {
				t.Fatalf("assessing the projection the driver rendered: %v", err)
			}
			if got := contains(ev.CompletePackages, "wp-one"); got != tc.complete {
				t.Fatalf("wp-one complete = %v, want %v — outstanding=%v blocking=%v gateNotMet=%v\n%s",
					got, tc.complete, ev.OutstandingPackages, ev.BlockingTasks, ev.GateNotMet, out)
			}

			ledger, err := os.ReadFile(filepath.Join(e.state, "evidence", "controls.tsv")) //nolint:gosec // a path this test chose
			if err != nil {
				t.Fatal(err)
			}
			// The facts survive either way. What the refusal withholds is the
			// claim, not the record of what happened.
			if !strings.Contains(string(ledger), "run 9001") {
				t.Fatalf("the control ledger lost the fact it recorded:\n%s", ledger)
			}
			if claimed := strings.Contains(string(ledger), "\tPASS\t"); claimed != tc.complete {
				t.Fatalf("the ledger claims PASS = %v, want %v:\n%s", claimed, tc.complete, ledger)
			}
		})
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// adjudicate reads what a stage stated and returns what the run would do about
// it. exitedZero is the residue the stage happened to leave, which is exactly
// what must not decide the answer.
func adjudicate(t *testing.T, path string, exitedZero bool) unattended.ControllerVerdict {
	t.Helper()
	res, err := unattended.ReadControllerResult(path)
	if err != nil {
		t.Fatalf("the stage stated no usable outcome: %v", err)
	}
	return unattended.InterpretExecution(unattended.Execution{
		ExitedZero: exitedZero, DeclaredResult: true, Result: &res,
	}, nil)
}

// readRefusal asks the driver's own reader what the run decided.
func readRefusal(t *testing.T, bash, lib, heartbeat string) string {
	t.Helper()
	script := `set -eu
. "$1"
cr_progression_refusal "$2"`
	cmd := exec.Command(bash, "-c", script, "bash", lib, heartbeat) //nolint:gosec // the reader under test
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reading the progression decision: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func parityHost(t *testing.T) handoff.HostProfile {
	t.Helper()
	return handoff.HostProfile{
		DeliveryRoot:    t.TempDir(),
		Driver:          driverPath(t),
		GasCityCommand:  "/home/operator/.local/bin/gc",
		BeadsCommand:    "/home/operator/.local/bin/bd",
		Provider:        "claude",
		ProviderCommand: "/home/operator/.local/bin/claude",
	}
}

// --- fixture plumbing -------------------------------------------------------

// writeStub installs an executable ahead of the real one on the driver's PATH.
func (e *recoveryEnv) writeStub(name, body string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.binDir, name), []byte(body), 0o755); err != nil { //nolint:gosec // a stub must be executable
		e.t.Fatal(err)
	}
}

// seedCityEvents gives the projector an event log to read. An empty one is a
// truthful fixture: these tests are about what the projection may CLAIM, and
// the claim is derived from the control ledger rather than from events.
func (e *recoveryEnv) seedCityEvents() {
	e.t.Helper()
	dir := filepath.Join(e.city, ".gc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), nil, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// buildProjector puts the projection generator where the driver expects to find
// one already built, so the stage under test is the stage and not a Go build.
func (e *recoveryEnv) buildProjector(t *testing.T) {
	t.Helper()
	out := filepath.Join(e.state, "projector-gen")
	if err := os.MkdirAll(e.state, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", out, "./corsolv/projector-gen")
	cmd.Dir = engineRepo(t)
	if built, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the projector: %v\n%s", err, built)
	}
}

// runStageWithResult runs a stage as a SUPERVISED task: the run exports where to
// state the outcome, and the stage states it there.
func (e *recoveryEnv) runStageWithResult(t *testing.T, result string, extraEnv []string, stage string, args ...string) (int, string) {
	t.Helper()
	argv := append([]string{driverPath(e.t), stage}, args...)
	argv = append(argv, "-project", e.project, "-state", e.state)
	cmd := exec.Command("bash", argv...) //nolint:gosec // the driver under test
	cmd.Env = e.scrubbedEnv(append([]string{"GC_UNATTENDED_RESULT_PATH=" + result}, extraEnv...))
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
}

// runWrapped runs a stage inside a wrapper that leaves a residual exit status of
// its own, which is the shape a harness produces when it swallows what happened
// underneath it.
func (e *recoveryEnv) runWrapped(t *testing.T, wrapperExit int, result, stage string, args ...string) (string, int) {
	t.Helper()
	argv := append([]string{driverPath(e.t), stage}, args...)
	argv = append(argv, "-project", e.project, "-state", e.state)

	script := `"$@" >&2 || true
exit ` + itoa(wrapperExit)
	cmd := exec.Command("bash", append([]string{"-c", script, "bash", "bash"}, argv...)...) //nolint:gosec // the driver under test
	cmd.Env = e.scrubbedEnv([]string{"GC_UNATTENDED_RESULT_PATH=" + result})
	out, _ := cmd.CombinedOutput()
	return string(out), cmd.ProcessState.ExitCode()
}
