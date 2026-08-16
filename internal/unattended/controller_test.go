package unattended

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These are the contract-level regressions for the controller result: what a
// document means, and what one attempt's evidence resolves to. The run-level
// regressions — that the queue, the journal and the completion event actually
// behave this way — are in controller_runner_test.go.

func TestParseControllerResultAcceptsTheDeclaredVocabulary(t *testing.T) {
	for _, state := range ControllerStates() {
		res, err := ParseControllerResult([]byte(`{"state":"` + string(state) + `"}`))
		if err != nil {
			t.Fatalf("state %s: %v", state, err)
		}
		if res.State != state {
			t.Fatalf("state = %s, want %s", res.State, state)
		}
	}
}

func TestParseControllerResultRefusesWhatItCannotAdjudicate(t *testing.T) {
	// Every one of these is a document that does not say what happened. The
	// alternative to refusing is to fall back on the exit status, which is the
	// signal this contract exists to stop trusting.
	for name, body := range map[string]string{
		"empty":              ``,
		"whitespace":         "   \n\t ",
		"truncated json":     `{"state":"COMP`,
		"not an object":      `["COMPLETE"]`,
		"no state":           `{"terminal_reason":"max_turns"}`,
		"invented state":     `{"state":"MOSTLY_DONE"}`,
		"empty state":        `{"state":""}`,
		"state is not a str": `{"state":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseControllerResult([]byte(body)); err == nil {
				t.Fatalf("%s was accepted as a controller result", name)
			} else if !errors.Is(err, ErrControllerResultUnusable) {
				t.Fatalf("error = %v, want ErrControllerResultUnusable", err)
			}
		})
	}
}

func TestParseControllerResultNormalizesCase(t *testing.T) {
	// The producer is an agent harness, not this package. A state that differs
	// only in case is the same statement, and refusing it would make the
	// contract fail closed on a cosmetic difference rather than on a real one.
	res, err := ParseControllerResult([]byte(`{"state":"  human_blocked  "}`))
	if err != nil {
		t.Fatalf("ParseControllerResult: %v", err)
	}
	if res.State != StateHumanBlocked {
		t.Fatalf("state = %q, want %s", res.State, StateHumanBlocked)
	}
}

func TestReadControllerResultTreatsAMissingFileAsUnusable(t *testing.T) {
	// A task that promised to say what happened and wrote nothing has not
	// stayed silent in a permitted way; it has failed to report.
	_, err := ReadControllerResult(filepath.Join(t.TempDir(), "nothing.json"))
	if !errors.Is(err, ErrControllerResultUnusable) {
		t.Fatalf("error = %v, want ErrControllerResultUnusable", err)
	}
}

func TestReadControllerResultReadsARealFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(path, []byte(`{"state":"COMPLETE","num_turns":4}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	res, err := ReadControllerResult(path)
	if err != nil {
		t.Fatalf("ReadControllerResult: %v", err)
	}
	if res.State != StateComplete || res.NumTurns != 4 {
		t.Fatalf("result = %+v, want COMPLETE with 4 turns", res)
	}
}

func TestATurnCapIsResumableUnderEitherSpelling(t *testing.T) {
	// The same event arrives in two fields under two names. A run that
	// recognized only one of them recorded the other as a generic failure.
	for _, reason := range []string{ReasonMaxTurns, ReasonErrorMaxTurns, " MAX_TURNS ", "Error_Max_Turns"} {
		if !IsResumableReason(reason) {
			t.Fatalf("terminal reason %q is not resumable, and a turn cap must be", reason)
		}
	}
	for _, reason := range []string{"", "success", ReasonNetworkTimeout, ReasonAuthenticationFailed} {
		if IsResumableReason(reason) {
			t.Fatalf("terminal reason %q was treated as a turn cap", reason)
		}
	}
}

// structured builds an Execution carrying a usable structured result.
func structured(exitedZero bool, res ControllerResult, output string) Execution {
	return Execution{ExitedZero: exitedZero, Output: output, DeclaredResult: true, Result: &res}
}

func TestInterpretExecutionOutcomeMatrix(t *testing.T) {
	// The whole declared outcome matrix, in one table, so a change to any row
	// is visible as a change to the contract rather than as a change to a
	// branch somewhere.
	cases := []struct {
		name        string
		exec        Execution
		disposition Disposition
		class       FailureClass
		gate        GateResult
	}{
		{
			name:        "exit zero and CONTINUE continues",
			exec:        structured(true, ControllerResult{State: StateContinue}, "still working"),
			disposition: DispositionContinue,
		},
		{
			name:        "terminal_reason max_turns resumes",
			exec:        structured(true, ControllerResult{State: StateFailed, TerminalReason: ReasonMaxTurns}, ""),
			disposition: DispositionResume,
		},
		{
			name:        "subtype error_max_turns resumes",
			exec:        structured(false, ControllerResult{State: StateFailed, Subtype: ReasonErrorMaxTurns, IsError: true}, ""),
			disposition: DispositionResume,
		},
		{
			name:        "a bounded network timeout retries",
			exec:        structured(false, ControllerResult{State: StateFailed, TerminalReason: ReasonNetworkTimeout}, ""),
			disposition: DispositionRetry,
			class:       FailureExternalService,
			gate:        GateFail,
		},
		{
			name:        "a rate limit retries",
			exec:        structured(false, ControllerResult{State: StateFailed, TerminalReason: ReasonRateLimited}, ""),
			disposition: DispositionRetry,
			class:       FailureExternalService,
			gate:        GateFail,
		},
		{
			name:        "an authentication failure is a human boundary",
			exec:        structured(false, ControllerResult{State: StateFailed, TerminalReason: ReasonAuthenticationFailed}, ""),
			disposition: DispositionHumanBlocked,
			class:       FailureAuth,
			gate:        GateError,
		},
		{
			name:        "an explicit HUMAN_BLOCKED stops",
			exec:        structured(true, ControllerResult{State: StateHumanBlocked, Detail: "a person must approve the release"}, ""),
			disposition: DispositionHumanBlocked,
			class:       FailureHumanDecision,
			gate:        GateError,
		},
		{
			name:        "HUMAN_BLOCKED on a credential is still an authentication boundary",
			exec:        structured(true, ControllerResult{State: StateHumanBlocked, TerminalReason: ReasonAuthenticationFailed}, ""),
			disposition: DispositionHumanBlocked,
			class:       FailureAuth,
			gate:        GateError,
		},
		{
			name:        "COMPLETE succeeds",
			exec:        structured(true, ControllerResult{State: StateComplete}, ""),
			disposition: DispositionSucceeded,
			gate:        GatePass,
		},
		{
			name:        "an unrecognized terminal reason is classified from the output",
			exec:        structured(false, ControllerResult{State: StateFailed, TerminalReason: "something_new"}, "--- FAIL: TestThing"),
			disposition: DispositionRetry,
			class:       FailureCodeDefect,
			gate:        GateFail,
		},
		{
			name: "a declared result that never arrived fails safe",
			exec: Execution{
				ExitedZero: true, DeclaredResult: true,
				ResultErr: ErrControllerResultUnusable,
			},
			disposition: DispositionFailSafe,
			class:       FailureEnvironment,
			gate:        GateError,
		},
		{
			name:        "an unsupervised task still succeeds on exit zero",
			exec:        Execution{ExitedZero: true},
			disposition: DispositionSucceeded,
			gate:        GatePass,
		},
		{
			name:        "an unsupervised task still fails on a non-zero exit",
			exec:        Execution{Output: "--- FAIL: TestThing"},
			disposition: DispositionRetry,
			class:       FailureCodeDefect,
			gate:        GateFail,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := InterpretExecution(tc.exec, nil)
			if v.Disposition != tc.disposition {
				t.Fatalf("disposition = %s (%s), want %s", v.Disposition, v.Reason, tc.disposition)
			}
			if v.Class != tc.class {
				t.Fatalf("class = %q, want %q", v.Class, tc.class)
			}
			if v.GateResult != tc.gate {
				t.Fatalf("gate result = %q, want %q", v.GateResult, tc.gate)
			}
			if strings.TrimSpace(v.Reason) == "" {
				t.Fatal("every verdict must say why")
			}
		})
	}
}

func TestStructuredResultOutranksTheResidualExitStatus(t *testing.T) {
	// THE DEFECT. A process exit status is a residue, and the pilot produced it
	// wrong in both directions: a wrapper exiting zero after its agent was cut
	// off, and a stage exiting non-zero for a condition that was correct.
	blockedButClean := InterpretExecution(
		structured(true, ControllerResult{State: StateHumanBlocked}, ""), nil)
	if blockedButClean.Disposition != DispositionHumanBlocked {
		t.Fatalf("a clean exit overrode a HUMAN_BLOCKED result: %s", blockedButClean.Disposition)
	}
	if !blockedButClean.Structured {
		t.Fatal("the verdict must record that a structured result decided it")
	}

	completeButDirty := InterpretExecution(
		structured(false, ControllerResult{State: StateComplete}, "warning: the supervisor was already running"), nil)
	if completeButDirty.Disposition != DispositionSucceeded {
		t.Fatalf("a non-zero exit overrode a COMPLETE result: %s (%s)",
			completeButDirty.Disposition, completeButDirty.Reason)
	}

	continueButDirty := InterpretExecution(
		structured(false, ControllerResult{State: StateContinue}, "exit 1 from a wrapper"), nil)
	if continueButDirty.Disposition != DispositionContinue {
		t.Fatalf("a non-zero exit overrode a CONTINUE result: %s", continueButDirty.Disposition)
	}
}

func TestATurnCapOutranksTheStateTheRuntimeWroteBesideIt(t *testing.T) {
	// A harness that stops an agent does not always get to tell it so, and
	// writes FAILED on its behalf. Reading the cap first is what keeps
	// "interrupted" from being recorded as "defeated".
	v := InterpretExecution(structured(false, ControllerResult{
		State: StateFailed, TerminalReason: ReasonMaxTurns, IsError: true,
		Detail: "the agent reached its turn limit",
	}, ""), nil)
	if v.Disposition != DispositionResume {
		t.Fatalf("disposition = %s (%s), want resume", v.Disposition, v.Reason)
	}
	if v.Class != "" {
		t.Fatalf("a turn cap carried failure class %q; it is not a failure", v.Class)
	}
	if v.GateResult != "" {
		t.Fatalf("a turn cap recorded gate verdict %q; the gate has not finished running", v.GateResult)
	}
}

func TestAFailSafeVerdictNeverCertifies(t *testing.T) {
	// The point of failing safe is that the gate ledger records an absence of
	// knowledge, which blocks progression exactly as a missing gate does.
	v := InterpretExecution(Execution{
		ExitedZero: true, DeclaredResult: true, ResultErr: ErrControllerResultUnusable,
	}, nil)
	if v.GateResult.Passed() {
		t.Fatal("a result nobody could read certified a gate")
	}
	if v.GateResult != GateError {
		t.Fatalf("gate result = %q, want error — nothing was observed about the code", v.GateResult)
	}
}

func TestAFailSafeKeepsTheExecutionsOwnSignatureWhenThereIsOne(t *testing.T) {
	// A supervised task that timed out told us nothing about its work, and the
	// timeout is still the best account of what happened to it. Losing it would
	// give a recognized transport failure the shorter retry budget of a generic
	// tooling fault.
	v := InterpretExecution(Execution{
		DeclaredResult: true,
		ResultErr:      ErrControllerResultUnusable,
		Err:            errors.New("task supervised timed out after 30m0s"),
	}, nil)
	if v.Disposition != DispositionFailSafe {
		t.Fatalf("disposition = %s, want fail-safe", v.Disposition)
	}
	if v.Class != FailureRetryable {
		t.Fatalf("class = %s, want retryable — the timeout signature must survive", v.Class)
	}
	if !strings.Contains(v.Reason, "unusable") {
		t.Fatalf("reason = %q, must still say the task produced no usable result", v.Reason)
	}
	if !strings.Contains(v.Reason, "ran out of time") {
		t.Fatalf("reason = %q, must carry the execution's own signature too", v.Reason)
	}

	// A clean exit with no result is a tooling fault and nothing more: there is
	// no failure signature to read, because the process claimed success.
	clean := InterpretExecution(Execution{
		ExitedZero: true, DeclaredResult: true, ResultErr: ErrControllerResultUnusable,
	}, nil)
	if clean.Class != FailureEnvironment {
		t.Fatalf("class = %s, want environment", clean.Class)
	}
}

func TestProjectDeclaredSignaturesStillApplyToAStructuredFailure(t *testing.T) {
	// A FAILED result whose terminal reason this package does not recognize is
	// classified from the output, so a project's own declared signatures are
	// not bypassed by a task adopting the structured contract.
	rules := []ClassificationRule{{
		Pattern: `the widget service is down`,
		Class:   FailureExternalService,
		Reason:  "the project declared this signature",
	}}
	v := InterpretExecution(structured(false,
		ControllerResult{State: StateFailed, TerminalReason: "widget_unavailable"},
		"the widget service is down"), rules)
	if v.Class != FailureExternalService {
		t.Fatalf("class = %s, want external-service from the project's own rule", v.Class)
	}
	if v.Reason != "the project declared this signature" {
		t.Fatalf("reason = %q, want the project's own words", v.Reason)
	}
}

func TestObserveClearsAStaleResultBeforeEachAttempt(t *testing.T) {
	// A previous attempt's answer read as this one's is the same false pass the
	// contract exists to prevent, one attempt later.
	if runtime.GOOS == "windows" {
		t.Skip("this fixture writes its result from a POSIX shell")
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := osMkdirAll(stateDir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := filepath.Join(stateDir, "result.json")
	if err := os.WriteFile(stale, []byte(`{"state":"COMPLETE"}`), 0o644); err != nil {
		t.Fatalf("seed the stale result: %v", err)
	}

	r := &Runner{Spec: Spec{StateDir: stateDir, Ownership: Ownership{Worktree: t.TempDir()}}}
	task := Task{ID: "supervised", Band: BandPrimary, ResultPath: "result.json", Argv: sh("exit 0")}

	e := r.observe(context.Background(), task)
	if e.Result != nil {
		t.Fatalf("the previous attempt's result was read as this attempt's: %+v", e.Result)
	}
	if e.ResultErr == nil {
		t.Fatal("a task that wrote no result must report that it wrote none")
	}
	if InterpretExecution(e, nil).Disposition != DispositionFailSafe {
		t.Fatal("a supervised task that said nothing must fail safe")
	}
}

func TestObserveReadsTheResultTheTaskActuallyWrote(t *testing.T) {
	// The whole plumbing: the run exports the path, the task writes there, and
	// the run reads it back.
	if runtime.GOOS == "windows" {
		t.Skip("this fixture writes its result from a POSIX shell")
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	r := &Runner{Spec: Spec{StateDir: stateDir, Ownership: Ownership{Worktree: t.TempDir()}}}
	task := Task{
		ID: "supervised", Band: BandPrimary, ResultPath: "result.json",
		Argv: sh(`printf '{"state":"CONTINUE","num_turns":3}' > "$GC_UNATTENDED_RESULT_PATH"`),
	}

	e := r.observe(context.Background(), task)
	if e.ResultErr != nil {
		t.Fatalf("reading the task's own result: %v", e.ResultErr)
	}
	if e.Result == nil || e.Result.State != StateContinue || e.Result.NumTurns != 3 {
		t.Fatalf("result = %+v, want CONTINUE after 3 turns", e.Result)
	}
	if InterpretExecution(e, nil).Disposition != DispositionContinue {
		t.Fatal("a task that reported CONTINUE must continue")
	}
}
