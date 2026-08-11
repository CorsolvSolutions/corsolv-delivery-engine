package unattended

import "testing"

func ghOutcome(t *testing.T, cs Checks, id string) Check {
	t.Helper()
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %q was not emitted", id)
	return Check{}
}

// fullyCapable is the probe of an account that can do everything, against a
// repository with no blocking protection.
func fullyCapable() GitHubProbe {
	return GitHubProbe{
		Authenticated: true, Account: "Corsolv",
		RepoReadable: true, ViewerPermission: "ADMIN", DefaultBranch: "main",
		ProtectionReadable: true,
	}
}

func needsEverything() GitHubRequirement {
	return GitHubRequirement{
		Repo: "CorsolvSolutions/corsolv-delivery-engine", Branch: "main",
		NeedPush: true, NeedPR: true, NeedChecks: true, NeedMerge: true,
	}
}

func TestGitHubChecksAcceptAFullyCapableAccount(t *testing.T) {
	cs := GitHubChecks(needsEverything(), fullyCapable())
	if got := cs.Readiness(); got != Ready {
		t.Fatalf("readiness = %s, want READY\n%s", got, cs)
	}
}

func TestGitHubUnauthenticatedIsABoundaryAndStopsTheRestBeingClaimed(t *testing.T) {
	// This is acceptance TEST 4: missing auth is caught at preflight, before a
	// long run, and everything downstream of it reports NOT REACHED rather than
	// a pass it never earned.
	cs := GitHubChecks(needsEverything(), GitHubProbe{})

	auth := ghOutcome(t, cs, "github.auth")
	if auth.Outcome != OutcomeHumanBoundary {
		t.Fatalf("unauthenticated = %s, want human-boundary", auth.Outcome)
	}
	if auth.Boundary == "" {
		t.Fatal("the boundary must name the login action")
	}
	for _, id := range []string{"github.repo", "github.push", "github.pr", "github.checks", "github.merge"} {
		if c := ghOutcome(t, cs, id); c.Outcome != OutcomeNotReached {
			t.Fatalf("%s = %s, want not-reached", id, c.Outcome)
		}
	}
	if cs.Readiness() != NotReady {
		t.Fatalf("readiness = %s, want NOT-READY — unrun checks cannot license a run", cs.Readiness())
	}
}

func TestGitHubWrongAccountIsAFailure(t *testing.T) {
	// An authenticated session on the wrong account is the ownership failure in
	// forge form, and no other check here would see it.
	req := needsEverything()
	req.Account = "SomeoneElse"
	cs := GitHubChecks(req, fullyCapable())
	if c := ghOutcome(t, cs, "github.account"); c.Outcome != OutcomeFail {
		t.Fatalf("wrong account = %s, want fail", c.Outcome)
	}
}

func TestGitHubUnreadableRepositoryIsAFailure(t *testing.T) {
	p := fullyCapable()
	p.RepoReadable = false
	p.Notes = []string{"repo view refused: Could not resolve to a Repository"}
	cs := GitHubChecks(needsEverything(), p)
	if c := ghOutcome(t, cs, "github.repo"); c.Outcome != OutcomeFail {
		t.Fatalf("unreadable repo = %s, want fail", c.Outcome)
	}
	if c := ghOutcome(t, cs, "github.merge"); c.Outcome != OutcomeNotReached {
		t.Fatalf("merge without a readable repo = %s, want not-reached", c.Outcome)
	}
}

func TestGitHubInsufficientPermissionIsABoundaryNotAFailure(t *testing.T) {
	// A read-only account is not a broken run. It is a run that ends at a pull
	// request, and saying so lets the queue finish everything short of it.
	for _, permission := range []string{"READ", "TRIAGE", ""} {
		p := fullyCapable()
		p.ViewerPermission = permission
		cs := GitHubChecks(needsEverything(), p)

		for _, id := range []string{"github.push", "github.pr", "github.merge"} {
			c := ghOutcome(t, cs, id)
			if c.Outcome != OutcomeHumanBoundary {
				t.Fatalf("permission %q: %s = %s, want human-boundary", permission, id, c.Outcome)
			}
			if c.Boundary == "" {
				t.Fatalf("permission %q: %s boundary names no human action", permission, id)
			}
		}
		if got := cs.Readiness(); got != ReadyWithKnownHumanBoundary {
			t.Fatalf("permission %q: readiness = %s, want READY-WITH-KNOWN-HUMAN-BOUNDARY", permission, got)
		}
	}
}

func TestGitHubRequiredReviewIsTheMergeBoundaryDiscoveredEarly(t *testing.T) {
	// The exact observed failure: a merge restriction found at merge time,
	// after all the work. Here it is found before any of it.
	p := fullyCapable()
	p.ProtectionPresent = true
	p.RequiresReview = true
	p.RequiredApprovals = 2

	c := ghOutcome(t, GitHubChecks(needsEverything(), p), "github.merge")
	if c.Outcome != OutcomeHumanBoundary {
		t.Fatalf("required review = %s, want human-boundary", c.Outcome)
	}
	if c.Observed == "" || c.Boundary == "" {
		t.Fatalf("the boundary must say what is required and who clears it: %+v", c)
	}
}

func TestGitHubProtectionPresentButUnblockingStillPasses(t *testing.T) {
	p := fullyCapable()
	p.ProtectionPresent = true
	p.RequiresReview = false
	if c := ghOutcome(t, GitHubChecks(needsEverything(), p), "github.merge"); c.Outcome != OutcomePass {
		t.Fatalf("protection requiring no review = %s, want pass", c.Outcome)
	}
}

func TestGitHubUnreadableProtectionIsAFailureNotAnAssumption(t *testing.T) {
	// "No protection" and "cannot see the protection" arrive identically from a
	// failed API call and mean opposite things. Assuming the harmless one is how
	// a run walks into a wall it was told about.
	p := fullyCapable()
	p.ProtectionReadable = false
	p.Notes = []string{"branch protection unreadable: HTTP 403"}
	c := ghOutcome(t, GitHubChecks(needsEverything(), p), "github.merge")
	if c.Outcome != OutcomeFail {
		t.Fatalf("unreadable protection = %s, want fail", c.Outcome)
	}
}

func TestGitHubUnclaimedMergeIsAPlannedBoundary(t *testing.T) {
	req := needsEverything()
	req.NeedMerge = false
	req.MergeHumanAction = "the delivery owner merges after reviewing the run's evidence"

	c := ghOutcome(t, GitHubChecks(req, fullyCapable()), "github.merge")
	if c.Outcome != OutcomeHumanBoundary {
		t.Fatalf("unclaimed merge = %s, want human-boundary", c.Outcome)
	}
	if c.Boundary != req.MergeHumanAction {
		t.Fatalf("boundary = %q, want the declared action", c.Boundary)
	}
}

func TestGitHubCapabilitiesNotRequiredAreNotDemanded(t *testing.T) {
	// A run that only reads must not be refused for lacking write access.
	req := GitHubRequirement{Repo: "o/r", Branch: "main"}
	p := fullyCapable()
	p.ViewerPermission = "READ"

	cs := GitHubChecks(req, p)
	for _, id := range []string{"github.push", "github.pr", "github.checks"} {
		if c := ghOutcome(t, cs, id); c.Outcome != OutcomePass {
			t.Fatalf("%s = %s, want pass for a run that does not need it", id, c.Outcome)
		}
	}
}
