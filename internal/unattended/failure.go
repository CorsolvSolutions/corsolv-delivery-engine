package unattended

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// FailureClass is what kind of thing went wrong.
//
// The classes exist because the right response to a failure is entirely
// determined by which of these it is, and nothing else. Retrying an
// authentication failure wastes an hour; stopping on a flaky test wastes a
// night. A run that cannot tell them apart must either stop on everything —
// which is the nine-minute run — or retry everything, which never converges.
type FailureClass string

// The declared failure classes.
const (
	// FailureRetryable — transient, and the same attempt may well succeed.
	FailureRetryable FailureClass = "retryable"
	// FailureCodeDefect — the code is wrong. Ordinary engineering work, and
	// explicitly not a reason to stop an unattended run.
	FailureCodeDefect FailureClass = "code-defect"
	// FailureEnvironment — the machine is wrong: a missing tool, a full disk, a
	// stale server. Retrying does not help; the run may still have other work.
	FailureEnvironment FailureClass = "environment"
	// FailureAuth — a credential is missing, expired, or refused.
	FailureAuth FailureClass = "auth"
	// FailureGovernance — a policy refused the action: branch protection, a
	// required review, an environment approval.
	FailureGovernance FailureClass = "governance"
	// FailureDependency — the task needs something another task has not
	// produced yet.
	FailureDependency FailureClass = "dependency"
	// FailureConcurrentWriter — another session owns the worktree, or moved it.
	FailureConcurrentWriter FailureClass = "concurrent-writer"
	// FailureExternalService — a third party is down or rate-limiting.
	FailureExternalService FailureClass = "external-service"
	// FailureHumanDecision — the answer is a product or business judgement.
	FailureHumanDecision FailureClass = "human-decision"
	// FailureIrreversibleBoundary — proceeding would do something that cannot be
	// undone without authorization.
	FailureIrreversibleBoundary FailureClass = "irreversible-boundary"
)

var failureClasses = map[FailureClass]bool{
	FailureRetryable: true, FailureCodeDefect: true, FailureEnvironment: true,
	FailureAuth: true, FailureGovernance: true, FailureDependency: true,
	FailureConcurrentWriter: true, FailureExternalService: true,
	FailureHumanDecision: true, FailureIrreversibleBoundary: true,
}

// Valid reports whether the class is one of the declared ten.
func (c FailureClass) Valid() bool { return failureClasses[c] }

// Policy is how a run responds to one class of failure.
type Policy struct {
	// MaxAttempts is the total number of attempts, including the first. One
	// means never retried.
	MaxAttempts int
	// BaseBackoff is the delay before the second attempt; each further attempt
	// doubles it, capped at MaxBackoff.
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// StopsRun marks a class that ends the run rather than moving to other work.
	// Only classes a person must resolve appear here: everything else leaves the
	// queue free to do something useful instead.
	StopsRun bool
	// Why states the reasoning, so a stop is auditable.
	Why string
}

// policies is the complete response table. Every declared class has an entry —
// enforced by TestEveryFailureClassHasAPolicy — because a class with no policy
// would fall through to some default nobody chose.
var policies = map[FailureClass]Policy{
	FailureRetryable: {
		MaxAttempts: 4, BaseBackoff: 5 * time.Second, MaxBackoff: 2 * time.Minute,
		Why: "transient by definition; retried with backoff, then treated as a blocked task rather than a stopped run",
	},
	FailureCodeDefect: {
		MaxAttempts: 3, BaseBackoff: time.Second, MaxBackoff: 10 * time.Second,
		Why: "ordinary engineering work — a failing test or build is what the run is for, not a reason to end it",
	},
	FailureEnvironment: {
		MaxAttempts: 2, BaseBackoff: 10 * time.Second, MaxBackoff: 30 * time.Second,
		Why: "the machine will not fix itself, but one retry costs little and covers a transient filesystem or process race",
	},
	FailureAuth: {
		MaxAttempts: 1,
		Why:         "a credential does not become valid by being asked again; the task is held and the queue moves on",
	},
	FailureGovernance: {
		MaxAttempts: 1,
		Why:         "a policy refusal is a decision, not a fault; the task is held for the human who can lift it",
	},
	FailureDependency: {
		MaxAttempts: 1,
		Why:         "the queue re-offers the task when its dependency succeeds, so retrying in place would only burn attempts",
	},
	FailureConcurrentWriter: {
		MaxAttempts: 1, StopsRun: true,
		Why: "another session owns this worktree; continuing would mean two writers, which is the failure this layer exists to prevent",
	},
	FailureExternalService: {
		MaxAttempts: 5, BaseBackoff: 15 * time.Second, MaxBackoff: 5 * time.Minute,
		Why: "third-party outages and rate limits recover on their own; backoff is longer and attempts are more generous",
	},
	FailureHumanDecision: {
		MaxAttempts: 1,
		Why:         "the answer is a judgement this run is not entitled to make",
	},
	FailureIrreversibleBoundary: {
		MaxAttempts: 1, StopsRun: true,
		Why: "the next step cannot be undone without authorization, so it is never taken unattended",
	},
}

// PolicyFor returns the response policy for a class.
//
// An unrecognized class gets the most cautious policy there is: one attempt and
// a stop. A class nobody declared is a class nobody reasoned about, and guessing
// generously about an unreasoned failure is how a run does damage.
func PolicyFor(c FailureClass) Policy {
	if p, ok := policies[c]; ok {
		return p
	}
	return Policy{
		MaxAttempts: 1, StopsRun: true,
		Why: "unrecognized failure class " + string(c) + " — nothing is known about it, so nothing is assumed",
	}
}

// Backoff returns the delay before the given attempt number, where attempt 1 is
// the first retry.
func (p Policy) Backoff(attempt int) time.Duration {
	if attempt < 1 || p.BaseBackoff <= 0 {
		return 0
	}
	shift := attempt - 1
	if shift > 20 { // guards the shift itself, long before the cap matters
		shift = 20
	}
	d := time.Duration(float64(p.BaseBackoff) * math.Pow(2, float64(shift)))
	if p.MaxBackoff > 0 && d > p.MaxBackoff {
		return p.MaxBackoff
	}
	return d
}

// builtinRules classify the failures every project shares. Project-specific
// signatures are declared in the spec and take precedence — a project knows what
// its own tooling's stderr means, and that is a judgement Go should not hold.
var builtinRules = []struct {
	re    *regexp.Regexp
	class FailureClass
	why   string
}{
	{regexp.MustCompile(`(?i)another session (owns|took ownership)|worktree already has a live owner|fence (branch-changed|head-moved|owner-lost)`), FailureConcurrentWriter, "the worktree ownership guard fired"},
	{regexp.MustCompile(`(?i)\b(401|403)\b|not logged in|authentication failed|bad credentials|permission denied \(publickey\)|token .*(expired|invalid)`), FailureAuth, "the forge or transport refused the credential"},
	{regexp.MustCompile(`(?i)protected branch|required status check|review required|at least \d+ approving review|environment .* requires approval`), FailureGovernance, "a repository or deployment policy refused the action"},
	{regexp.MustCompile(`(?i)\b(429|502|503|504)\b|rate limit|service unavailable|temporarily unavailable|connection reset by peer|i/o timeout|TLS handshake timeout`), FailureExternalService, "a third party refused or dropped the request"},
	{regexp.MustCompile(`(?i)command not found|executable file not found|no such file or directory|no space left on device|cannot allocate memory|address already in use`), FailureEnvironment, "the machine could not provide something the command needed"},
	{regexp.MustCompile(`(?i)^--- FAIL|\bFAIL\b.*\[build failed\]|\bassertion failed\b|\bcompile error\b|\bsyntax error\b|undefined:|cannot use .* as .* value`), FailureCodeDefect, "the code under test did not do what it should"},
	{regexp.MustCompile(`(?i)merge conflict|CONFLICT \(content\)|would be overwritten by merge`), FailureCodeDefect, "a merge conflict is ordinary work, resolved in the tree"},
	{regexp.MustCompile(`(?i)context deadline exceeded|timed out|timeout`), FailureRetryable, "the attempt ran out of time without saying why"},
}

// Classification is one classified failure, with the reasoning attached.
type Classification struct {
	Class  FailureClass `json:"class"`
	Reason string       `json:"reason"`
	// Rule identifies what decided the class, so a misclassification is
	// traceable to the rule that caused it rather than to "the classifier".
	Rule string `json:"rule"`
}

// Classify assigns a failure class to failure text.
//
// Spec-declared rules are consulted first and in order, then the built-in ones.
// Text that matches nothing is a code defect, which is the safe default here:
// it is the class that means "ordinary work continues", and misfiling a genuine
// environment fault as a defect costs a retry, whereas misfiling a defect as a
// boundary costs the rest of the run.
func Classify(text string, rules []ClassificationRule) Classification {
	for i, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			continue
		}
		if re.MatchString(text) {
			reason := r.Reason
			if reason == "" {
				reason = "matched a project-declared signature"
			}
			return Classification{Class: r.Class, Reason: reason, Rule: fmt.Sprintf("spec.classify[%d]", i)}
		}
	}
	for i, r := range builtinRules {
		if r.re.MatchString(text) {
			return Classification{Class: r.class, Reason: r.why, Rule: fmt.Sprintf("builtin[%d]", i)}
		}
	}
	return Classification{
		Class:  FailureCodeDefect,
		Reason: "no signature matched, so it is treated as ordinary work rather than as a boundary",
		Rule:   "default",
	}
}

// ValidateClassificationRules reports rules that cannot be used, so a typo in a
// spec is a preflight failure rather than a rule that silently never matches.
func ValidateClassificationRules(rules []ClassificationRule) error {
	var problems []string
	for i, r := range rules {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			problems = append(problems, fmt.Sprintf("classify[%d] pattern %q does not compile: %v", i, r.Pattern, err))
		}
		if !r.Class.Valid() {
			problems = append(problems, fmt.Sprintf("classify[%d] class %q is not a declared failure class", i, string(r.Class)))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrSpecInvalid, strings.Join(problems, "; "))
	}
	return nil
}
