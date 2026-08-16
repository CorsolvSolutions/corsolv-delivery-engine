package unattended

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// This file is the controller-result contract: a supervised task states in a
// structured document what actually happened to it, and that statement — not
// the exit status its process happened to leave behind — decides what the run
// does next.
//
// THE DEFECT IT EXISTS FOR. A process exit status is a residue. The Website
// Status Checker pilot and the overnight run produced four separate instances
// of the same shape: `gc init` exiting non-zero for a condition that was not
// merely benign but correct; a wrapper exiting zero after the agent inside it
// had been cut off by its turn cap; an authentication refusal reaching the
// queue as an ordinary command failure and being retried; and a stage reporting
// success because its deadline expired rather than because its work finished.
// In every one of them the run took the residue as the verdict and was wrong.
//
// The contract's whole content is that a task which declares it will say what
// happened must be believed about what happened, and must be disbelieved —
// safely — when it says nothing. There is no field by which a structured result
// can claim more than the run's mandatory gates permit: COMPLETE ends a task,
// it does not certify one. Certification is qa.go's, and it reads evidence.

// ControllerState is what a supervised task says about its own execution.
//
// The vocabulary is deliberately four values wide. Each names a distinct thing
// the run must do next, and no two of them share a response: continue driving,
// stop and take the next work, stop and wait for a person, treat it as a
// failure of the declared class.
type ControllerState string

// The declared controller states.
const (
	// StateContinue — the task made progress and has more to do. The agent
	// says so itself, which is the only party that knows.
	StateContinue ControllerState = "CONTINUE"
	// StateComplete — the task finished the work it was given. It is a claim
	// about the task, never about the packet: whether the packet may progress
	// is decided by EvaluateProgression against gate evidence.
	StateComplete ControllerState = "COMPLETE"
	// StateHumanBlocked — the task reached a limit only a person can lift.
	// The run stops safely rather than retrying into it.
	StateHumanBlocked ControllerState = "HUMAN_BLOCKED"
	// StateFailed — the task did not succeed and is not blocked on a person.
	// What happens next is decided from the terminal reason.
	StateFailed ControllerState = "FAILED"
)

var controllerStates = map[ControllerState]bool{
	StateContinue: true, StateComplete: true, StateHumanBlocked: true, StateFailed: true,
}

// Valid reports whether the state is one of the declared four.
func (s ControllerState) Valid() bool { return controllerStates[s] }

// ControllerStates returns the declared states, sorted, for error messages.
func ControllerStates() []ControllerState {
	out := make([]ControllerState, 0, len(controllerStates))
	for s := range controllerStates {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// The terminal reasons this package understands.
//
// They are the harness's account of *why* execution ended, which is a different
// question from what the agent achieved — and the one that separates "the agent
// gave up" from "the agent was cut off mid-sentence". A reason this table does
// not name is not an error: it is carried through and adjudicated by the
// declared state alone, which is the conservative direction.
const (
	// ReasonMaxTurns — the harness stopped the agent at its turn cap. The work
	// is unfinished, nothing about it failed, and resuming continues it.
	ReasonMaxTurns = "max_turns"
	// ReasonErrorMaxTurns — the same event under the name the agent runtime
	// reports it by in its result subtype. It is listed separately because it
	// arrives in a different field and reads like an error; it is not one.
	ReasonErrorMaxTurns = "error_max_turns"
	// ReasonNetworkTimeout — a bounded transport failure. Retryable, with the
	// external-service policy's backoff and attempt budget.
	ReasonNetworkTimeout = "network_timeout"
	// ReasonRateLimited — a third party refused for load. Same policy.
	ReasonRateLimited = "rate_limited"
	// ReasonAuthenticationFailed — a credential was missing, expired or
	// refused. A credential does not become valid by being asked again.
	ReasonAuthenticationFailed = "authentication_failed"
	// ReasonPermissionDenied — the runtime refused the action itself. Like an
	// authentication failure, only a person changes the answer.
	ReasonPermissionDenied = "permission_denied"
)

// resumableReasons are the terminal reasons that mean the work was interrupted
// rather than defeated.
//
// It is a set rather than a condition because the two spellings of the same
// event arrive in two different fields, and a run that recognized only one of
// them reported a turn cap as a generic failure — which spends the task's
// retry budget re-running work that was going fine.
var resumableReasons = map[string]bool{
	ReasonMaxTurns:      true,
	ReasonErrorMaxTurns: true,
}

// IsResumableReason reports whether a terminal reason means the work was cut
// off with more to do.
func IsResumableReason(reason string) bool {
	return resumableReasons[normalizeReason(reason)]
}

// reasonClasses maps a recognized terminal reason onto the failure class whose
// policy governs it. A reason absent from the table carries no class of its
// own and is classified from the output text, exactly as an unsupervised task
// is.
var reasonClasses = map[string]FailureClass{
	ReasonNetworkTimeout:       FailureExternalService,
	ReasonRateLimited:          FailureExternalService,
	ReasonAuthenticationFailed: FailureAuth,
	ReasonPermissionDenied:     FailureAuth,
}

func normalizeReason(reason string) string {
	return strings.ToLower(strings.TrimSpace(reason))
}

// ControllerResult is what a supervised task writes down about its own
// execution, in the file its task declared.
//
// The wire spelling is snake_case because that is what the agent runtimes this
// engine drives already emit, and a contract that required a translation layer
// on the producing side would be one more place for the translation to be
// wrong. It is matched strictly: a field spelled some other way is a field this
// package did not read, which resolves to an absent value, which fails safe.
type ControllerResult struct {
	// State is the task's own account of what happened. Required.
	State ControllerState `json:"state"`
	// TerminalReason is the harness's account of why execution ended.
	TerminalReason string `json:"terminal_reason,omitempty"`
	// Subtype is the same fact under the name an agent runtime reports it by
	// in its result envelope. Both are read; see effectiveReason.
	Subtype string `json:"subtype,omitempty"`
	// IsError is the runtime's own error flag. It corroborates and never
	// overrides: a runtime that flags an error while declaring CONTINUE has
	// said two things, and the declared state is the one with meaning.
	IsError bool `json:"is_error,omitempty"`
	// Detail is a human-readable account, redacted before it is recorded.
	Detail string `json:"detail,omitempty"`
	// NumTurns is how many turns the agent used, for the record.
	NumTurns int `json:"num_turns,omitempty"`
}

// effectiveReason resolves the two fields that carry the same fact.
//
// terminal_reason is preferred because it is this contract's own field; subtype
// is read as a fallback so a result copied straight out of an agent runtime's
// envelope is understood without being rewritten first.
func (r ControllerResult) effectiveReason() string {
	if reason := normalizeReason(r.TerminalReason); reason != "" {
		return reason
	}
	return normalizeReason(r.Subtype)
}

// ErrControllerResultUnusable is returned when a declared structured result is
// absent, unreadable, or does not state a declared outcome.
//
// It is one error rather than three because the run's response to all three is
// identical and deliberately so: the task promised to say what happened and did
// not, so nothing is known, and nothing is assumed.
var ErrControllerResultUnusable = errors.New("unattended: the task's structured controller result is unusable")

// ParseControllerResult reads a structured controller result from bytes.
//
// It refuses anything it cannot adjudicate. A document that parses as JSON but
// declares no recognized state is refused for the same reason a document that
// does not parse at all is: neither one says what happened, and the alternative
// to refusing is to fall back on the exit status this contract exists to stop
// trusting.
func ParseControllerResult(data []byte) (ControllerResult, error) {
	var res ControllerResult
	if len(strings.TrimSpace(string(data))) == 0 {
		return res, fmt.Errorf("%w: it is empty", ErrControllerResultUnusable)
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return res, fmt.Errorf("%w: it is not readable JSON: %w", ErrControllerResultUnusable, err)
	}
	res.State = ControllerState(strings.ToUpper(strings.TrimSpace(string(res.State))))
	if !res.State.Valid() {
		declared := string(res.State)
		if declared == "" {
			declared = "unset"
		}
		return res, fmt.Errorf("%w: state %s is not a declared controller state (%s)",
			ErrControllerResultUnusable, quoteOrBare(declared), joinControllerStates())
	}
	return res, nil
}

// ReadControllerResult reads a structured controller result from a file.
//
// A missing file is an unusable result, not a permitted silence. A task that
// declared it would write one and did not has failed to say what happened,
// which is exactly the case whose old handling — believe the exit status —
// this contract replaces.
func ReadControllerResult(path string) (ControllerResult, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ControllerResult{}, fmt.Errorf("%w: %s was never written", ErrControllerResultUnusable, path)
	}
	if err != nil {
		return ControllerResult{}, fmt.Errorf("%w: reading %s: %w", ErrControllerResultUnusable, path, err)
	}
	return ParseControllerResult(data)
}

func joinControllerStates() string {
	out := make([]string, 0, len(controllerStates))
	for _, s := range ControllerStates() {
		out = append(out, string(s))
	}
	return strings.Join(out, ", ")
}

// Disposition is what the run does about one execution.
//
// It is separate from the state a task declares because the mapping between
// them is the run's policy rather than the task's: CONTINUE and a turn cap are
// two different statements that both mean "drive this again", and an
// authentication refusal and an explicit block are two different statements
// that both mean "stop and wait for a person" while needing to be reported
// apart.
type Disposition string

// The dispositions.
const (
	// DispositionSucceeded — the task is finished and the queue is done with
	// it. It is a statement about the task, never about the packet.
	DispositionSucceeded Disposition = "succeeded"
	// DispositionContinue — the task said it has more to do. It is re-offered
	// without spending a retry, because nothing failed.
	DispositionContinue Disposition = "continue"
	// DispositionResume — the harness cut the task off at its turn cap. It is
	// re-offered for the same reason and recorded distinctly, because a turn
	// cap and an agent's own "not finished yet" are different events.
	DispositionResume Disposition = "resume"
	// DispositionRetry — the task failed in a way the class policy retries.
	DispositionRetry Disposition = "retry"
	// DispositionHumanBlocked — the task reached a limit a person must lift.
	// The run stops safely; it does not retry and it does not carry on
	// mutating.
	DispositionHumanBlocked Disposition = "human-blocked"
	// DispositionFailSafe — nothing is known about what happened, because the
	// declared structured result is absent or unusable. Treated as a failure
	// and never as a pass.
	DispositionFailSafe Disposition = "fail-safe"
)

// Resumable reports whether the disposition re-offers the same task rather than
// finishing with it.
func (d Disposition) Resumable() bool {
	return d == DispositionContinue || d == DispositionResume
}

// ControllerVerdict is the whole decision about one execution.
type ControllerVerdict struct {
	// Disposition is what the run does next.
	Disposition Disposition `json:"disposition"`
	// Class is the failure class the queue's retry policy is taken from. It is
	// empty for a disposition that is not a failure.
	Class FailureClass `json:"class,omitempty"`
	// Reason states why, in the words a report reads.
	Reason string `json:"reason"`
	// Structured records whether a structured result decided this, so the
	// journal can say whether the exit status was consulted at all.
	Structured bool `json:"structured"`
	// State is the declared controller state, when there was one.
	State ControllerState `json:"state,omitempty"`
	// TerminalReason is the resolved terminal reason, when there was one.
	TerminalReason string `json:"terminalReason,omitempty"`
	// GateResult is the verdict this execution records for the task's QA gate,
	// when the queue is finished with the task. A gate can only ever be
	// certified by a pass; every other disposition records what it actually
	// observed.
	GateResult GateResult `json:"gateResult,omitempty"`
}

// Execution is one attempt's observable outcome, before it is interpreted.
type Execution struct {
	// ExitedZero reports whether the process exited zero. It is evidence, not
	// a verdict — see InterpretExecution.
	ExitedZero bool
	// Output is the attempt's combined output, unredacted.
	Output string
	// Err is a failure to run the command at all: a timeout, a missing
	// executable, a spawn refusal.
	Err error
	// DeclaredResult reports whether the task promised a structured result.
	DeclaredResult bool
	// Result is the parsed structured result. Nil when none was declared, or
	// when the declared one could not be parsed.
	Result *ControllerResult
	// ResultErr is why a declared structured result could not be used.
	ResultErr error
}

// text renders what a classifier should read.
func (e Execution) text() string {
	if e.Err != nil {
		return e.Err.Error() + "\n" + e.Output
	}
	return e.Output
}

// InterpretExecution decides what one attempt means.
//
// THE PRECEDENCE RULE, which is the point of this function: when a task
// declared that it would state its own outcome, that statement is the verdict
// and the exit status is not consulted. Both directions matter and both were
// observed in the pilot. A wrapper that exits zero after its agent was cut off
// must not read as success; a stage that exits non-zero for a condition that is
// correct must not read as failure. A task that declared nothing is unchanged:
// its exit status is all there is, and it is classified exactly as before.
//
// The one thing a structured result cannot do is certify. COMPLETE finishes a
// task; whether the packet may progress is EvaluateProgression's answer, taken
// from gate evidence bound to a revision, and no state in this vocabulary
// substitutes for it.
func InterpretExecution(e Execution, rules []ClassificationRule) ControllerVerdict {
	// A declared result that cannot be used outranks everything, including a
	// clean exit. This is the fail-safe half of the precedence rule: the task
	// promised to say what happened, it did not, and the exit status is the
	// very signal that promise was made to replace.
	if e.DeclaredResult && e.Result == nil {
		silence := "the task declared a structured controller result and did not produce a usable one"
		if e.ResultErr != nil {
			silence = e.ResultErr.Error()
		}
		v := ControllerVerdict{
			Disposition: DispositionFailSafe,
			Class:       FailureEnvironment,
			Reason:      silence + " — nothing is known about this attempt, so nothing is assumed",
			Structured:  true,
			GateResult:  GateError,
		}
		// When the execution ITSELF also failed, its own signature is better
		// information than "the harness produced nothing", and losing it would
		// downgrade a recognized timeout or rate limit to a generic tooling
		// fault with a shorter retry budget. The disposition is unchanged: a
		// task that said nothing is never a pass, whatever its output looks
		// like.
		if e.Err != nil || !e.ExitedZero {
			c := Classify(e.text(), rules)
			v.Class = c.Class
			v.Reason = c.Reason + "; and " + silence
		}
		return v
	}

	if e.Result != nil {
		return interpretStructured(*e.Result, rules, e.text())
	}

	// No structured result was promised. The exit status is the whole of the
	// evidence, which is what it has always been for an ordinary command.
	if e.ExitedZero && e.Err == nil {
		return ControllerVerdict{
			Disposition: DispositionSucceeded,
			Reason:      "the command exited zero",
			GateResult:  GatePass,
		}
	}
	class := Classify(e.text(), rules)
	return ControllerVerdict{
		Disposition: DispositionRetry,
		Class:       class.Class,
		Reason:      class.Reason,
		GateResult:  GateFail,
	}
}

// interpretStructured maps a usable structured result onto a disposition.
func interpretStructured(res ControllerResult, rules []ClassificationRule, text string) ControllerVerdict {
	reason := res.effectiveReason()
	v := ControllerVerdict{
		Structured:     true,
		State:          res.State,
		TerminalReason: reason,
	}

	// A turn cap is checked before the declared state, and deliberately. The
	// harness that imposes it does not always get to tell the agent it was cut
	// off, so a result can carry max_turns beside any state at all — including
	// a FAILED the runtime wrote on the agent's behalf. Reading the cap first
	// is what stops "interrupted" being recorded as "defeated".
	if IsResumableReason(reason) {
		v.Disposition = DispositionResume
		v.Reason = fmt.Sprintf("the harness stopped the agent at its turn cap (%s); the work is unfinished, not failed", reason)
		return v
	}

	switch res.State {
	case StateComplete:
		v.Disposition = DispositionSucceeded
		v.Reason = "the task reported COMPLETE"
		v.GateResult = GatePass
		return v

	case StateContinue:
		v.Disposition = DispositionContinue
		v.Reason = "the task reported CONTINUE; it has more to do"
		return v

	case StateHumanBlocked:
		v.Disposition = DispositionHumanBlocked
		v.Class = classForBlockedReason(reason)
		v.Reason = blockedReasonText(reason, res.Detail)
		v.GateResult = GateError
		return v

	case StateFailed:
		// A reason this package recognizes carries its own class; anything
		// else is classified from the output text, exactly as an unsupervised
		// failure is, so a project's own declared signatures still apply.
		class, known := reasonClasses[reason]
		why := "the task reported FAILED with terminal reason " + reason
		if !known {
			c := Classify(text, rules)
			class, why = c.Class, c.Reason
		}
		v.Class = class
		v.GateResult = GateFail
		v.Reason = why
		if class == FailureAuth {
			v.Disposition = DispositionHumanBlocked
			v.GateResult = GateError
			return v
		}
		v.Disposition = DispositionRetry
		return v
	}

	// Unreachable for a parsed result — ParseControllerResult refuses an
	// unrecognized state — and kept because an unreachable default that fails
	// safe costs nothing, while an unreachable default that falls through to
	// success costs everything.
	v.Disposition = DispositionFailSafe
	v.Class = FailureEnvironment
	v.Reason = "the structured result declared state " + string(res.State) + ", which this run cannot adjudicate"
	v.GateResult = GateError
	return v
}

// classForBlockedReason keeps an authentication block distinguishable from a
// judgement block.
//
// Both stop the run and neither is retried, so the class is not doing retry
// work here. It decides how the stop is REPORTED, and the two are different
// jobs for whoever reads it: an authentication boundary is usually seconds of a
// person's time, and a judgement boundary is a conversation.
func classForBlockedReason(reason string) FailureClass {
	if class, ok := reasonClasses[reason]; ok && class == FailureAuth {
		return FailureAuth
	}
	return FailureHumanDecision
}

func blockedReasonText(reason, detail string) string {
	base := "the task reported HUMAN_BLOCKED"
	if reason != "" {
		base += " (" + reason + ")"
	}
	if d := strings.TrimSpace(detail); d != "" {
		base += ": " + firstLine(Redact(d))
	}
	return base
}
