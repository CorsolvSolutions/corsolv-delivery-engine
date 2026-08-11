package unattended

import (
	"testing"
	"time"
)

func TestEveryFailureClassHasAPolicy(t *testing.T) {
	// A class with no policy would fall through to a default nobody chose.
	for class := range failureClasses {
		if _, ok := policies[class]; !ok {
			t.Errorf("failure class %q has no declared policy", class)
		}
	}
	for class, p := range policies {
		if !class.Valid() {
			t.Errorf("policy declared for %q, which is not a failure class", class)
		}
		if p.MaxAttempts < 1 {
			t.Errorf("policy for %q allows %d attempts; every failure must be attempted at least once", class, p.MaxAttempts)
		}
		if p.Why == "" {
			t.Errorf("policy for %q states no reasoning, so a stop or retry it causes would be unauditable", class)
		}
	}
}

func TestRetryIsAlwaysBounded(t *testing.T) {
	for class, p := range policies {
		if p.MaxAttempts > 8 {
			t.Errorf("policy for %q allows %d attempts, which is an unbounded loop wearing a number", class, p.MaxAttempts)
		}
	}
}

func TestUnknownFailureClassGetsTheMostCautiousPolicy(t *testing.T) {
	p := PolicyFor("something-nobody-declared")
	if p.MaxAttempts != 1 || !p.StopsRun {
		t.Fatalf("unknown class policy = %+v, want one attempt and a stop", p)
	}
}

func TestOnlyHumanResolvableClassesStopTheRun(t *testing.T) {
	// The whole difference between a nine-minute run and an overnight one is
	// which failures end it. Ordinary engineering failures must not.
	mustNotStop := []FailureClass{
		FailureRetryable, FailureCodeDefect, FailureEnvironment,
		FailureAuth, FailureGovernance, FailureDependency,
		FailureExternalService, FailureHumanDecision,
	}
	for _, c := range mustNotStop {
		if PolicyFor(c).StopsRun {
			t.Errorf("%s stops the run; the queue should move to other work instead", c)
		}
	}
	mustStop := []FailureClass{FailureConcurrentWriter, FailureIrreversibleBoundary}
	for _, c := range mustStop {
		if !PolicyFor(c).StopsRun {
			t.Errorf("%s must stop the run — continuing would do damage", c)
		}
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	p := PolicyFor(FailureExternalService)
	var previous time.Duration
	for attempt := 1; attempt <= 6; attempt++ {
		d := p.Backoff(attempt)
		if d < previous {
			t.Fatalf("backoff for attempt %d (%s) is shorter than attempt %d (%s)", attempt, d, attempt-1, previous)
		}
		if d > p.MaxBackoff {
			t.Fatalf("backoff for attempt %d = %s, exceeds the cap %s", attempt, d, p.MaxBackoff)
		}
		previous = d
	}
	if p.Backoff(1) != p.BaseBackoff {
		t.Fatalf("first retry backoff = %s, want the declared base %s", p.Backoff(1), p.BaseBackoff)
	}
	if p.Backoff(0) != 0 {
		t.Fatal("there is no delay before the first attempt")
	}
	// Far beyond any real attempt count, the cap still holds rather than
	// overflowing into a negative or absurd duration.
	if d := p.Backoff(200); d != p.MaxBackoff {
		t.Fatalf("backoff for attempt 200 = %s, want the cap %s", d, p.MaxBackoff)
	}
}

func TestPolicyWithoutBackoffNeverSleeps(t *testing.T) {
	if d := PolicyFor(FailureAuth).Backoff(3); d != 0 {
		t.Fatalf("a single-attempt policy produced a backoff of %s", d)
	}
}

func TestClassifyRecognizesTheObservedFailures(t *testing.T) {
	cases := []struct {
		name string
		text string
		want FailureClass
	}{
		{"competing writer", "unattended: the worktree already has a live owner", FailureConcurrentWriter},
		{"branch moved", "unattended: fence branch-changed — expected \"main\"", FailureConcurrentWriter},
		{"auth refused", "remote: Bad credentials\nfatal: Authentication failed", FailureAuth},
		{"http 403", "HTTP 403: Resource not accessible by integration", FailureAuth},
		{"protected branch", "remote: error: GH006: Protected branch update failed", FailureGovernance},
		{"review required", "At least 1 approving review is required by reviewers with write access", FailureGovernance},
		{"rate limited", "HTTP 429: API rate limit exceeded", FailureExternalService},
		{"gateway", "HTTP 503: Service Unavailable", FailureExternalService},
		{"missing tool", "exec: \"dolt\": executable file not found in $PATH", FailureEnvironment},
		{"disk full", "write /var/tmp/x: no space left on device", FailureEnvironment},
		{"test failure", "--- FAIL: TestSomething (0.01s)", FailureCodeDefect},
		{"compile error", "./main.go:12:2: undefined: helper", FailureCodeDefect},
		{"merge conflict", "CONFLICT (content): Merge conflict in internal/x.go", FailureCodeDefect},
		{"timeout", "context deadline exceeded", FailureRetryable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.text, nil)
			if got.Class != tc.want {
				t.Fatalf("Classify(%q) = %s (%s), want %s", tc.text, got.Class, got.Rule, tc.want)
			}
			if got.Reason == "" || got.Rule == "" {
				t.Fatalf("a classification must be traceable: %+v", got)
			}
		})
	}
}

func TestUnmatchedFailureIsOrdinaryWorkNotABoundary(t *testing.T) {
	// Misfiling an environment fault as a defect costs a retry. Misfiling a
	// defect as a boundary costs the rest of the run.
	got := Classify("something entirely novel happened", nil)
	if got.Class != FailureCodeDefect {
		t.Fatalf("unmatched failure = %s, want code-defect", got.Class)
	}
	if PolicyFor(got.Class).StopsRun {
		t.Fatal("the default class must not stop the run")
	}
}

func TestProjectRulesOutrankBuiltins(t *testing.T) {
	// A project knows what its own tooling's output means; Go does not.
	rules := []ClassificationRule{{
		Pattern: `flaky-integration-suite`,
		Class:   FailureRetryable,
		Reason:  "this suite is known flaky under parallel load",
	}}
	text := "--- FAIL: flaky-integration-suite (12.00s)"
	if got := Classify(text, nil).Class; got != FailureCodeDefect {
		t.Fatalf("without the rule, want code-defect, got %s", got)
	}
	got := Classify(text, rules)
	if got.Class != FailureRetryable {
		t.Fatalf("with the rule, got %s, want retryable", got.Class)
	}
	if got.Rule != "spec.classify[0]" {
		t.Fatalf("classification must name the deciding rule, got %q", got.Rule)
	}
}

func TestClassifyIgnoresAnUncompilablePatternRatherThanPanicking(t *testing.T) {
	rules := []ClassificationRule{{Pattern: `([`, Class: FailureRetryable}}
	if got := Classify("--- FAIL: TestX", rules); got.Class != FailureCodeDefect {
		t.Fatalf("got %s, want the builtin classification to still apply", got.Class)
	}
}

func TestValidateClassificationRulesCatchesWhatClassifySkips(t *testing.T) {
	err := ValidateClassificationRules([]ClassificationRule{
		{Pattern: `([`, Class: FailureRetryable},
		{Pattern: `ok`, Class: "invented"},
	})
	if err == nil {
		t.Fatal("an uncompilable pattern and an invented class must both be refused")
	}
	if ok := ValidateClassificationRules([]ClassificationRule{{Pattern: `ok`, Class: FailureRetryable}}); ok != nil {
		t.Fatalf("a valid rule was refused: %v", ok)
	}
}
