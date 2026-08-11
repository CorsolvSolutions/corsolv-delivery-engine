package unattended

import (
	"errors"
	"path/filepath"
	"testing"
)

const testOrigin = "https://github.com/CorsolvSolutions/corsolv-delivery-engine.git"

func declaredFor(dir string) Ownership {
	return Ownership{
		ProjectID:      "corsolv-delivery-engine",
		Worktree:       dir,
		ExpectedOrigin: testOrigin,
		ExpectedBranch: "main",
		Role:           RoleWriter,
		Session:        "gascity-unattended-readiness",
	}
}

func outcomeOf(t *testing.T, cs Checks, id string) Check {
	t.Helper()
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %q was not emitted; got %v", id, cs)
	return Check{}
}

func TestOwnershipValidateRequiresWhatItMustProve(t *testing.T) {
	cases := []struct {
		name string
		o    Ownership
		ok   bool
	}{
		{"complete writer", declaredFor("/tmp/x"), true},
		{"no project", Ownership{Worktree: "/tmp/x", Session: "s", Role: RoleWriter, ExpectedBranch: "main"}, false},
		{"no worktree", Ownership{ProjectID: "p", Session: "s", Role: RoleWriter, ExpectedBranch: "main"}, false},
		{"no session", Ownership{ProjectID: "p", Worktree: "/tmp/x", Role: RoleWriter, ExpectedBranch: "main"}, false},
		{"bad role", Ownership{ProjectID: "p", Worktree: "/tmp/x", Session: "s", Role: "vandal", ExpectedBranch: "main"}, false},
		{"writer with no branch expectation", Ownership{ProjectID: "p", Worktree: "/tmp/x", Session: "s", Role: RoleWriter}, false},
		{"reader with no branch expectation", Ownership{ProjectID: "p", Worktree: "/tmp/x", Session: "s", Role: RoleReadOnly}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.o.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatal("Validate() = nil, want an error")
				}
				if !errors.Is(err, ErrOwnershipIncomplete) {
					t.Fatalf("Validate() = %v, want ErrOwnershipIncomplete", err)
				}
			}
		})
	}
}

func TestOwnershipAcceptsTheDeclaredRepository(t *testing.T) {
	dir := newRepo(t, testOrigin)
	_, cs := AssertOwnership(declaredFor(dir))
	if got := cs.Readiness(); got != Ready {
		t.Fatalf("readiness = %s, want READY\n%s", got, cs)
	}
}

func TestOwnershipRejectsWrongRepository(t *testing.T) {
	// A session launched against a *different* real repository: every signal
	// except the remote looks plausible, which is exactly why this failed
	// silently before.
	other := newRepo(t, "https://github.com/gastownhall/gascity.git")
	o := declaredFor(other)

	_, cs := AssertOwnership(o)
	if cs.Readiness() != NotReady {
		t.Fatalf("readiness = %s, want NOT-READY\n%s", cs.Readiness(), cs)
	}
	if c := outcomeOf(t, cs, "ownership.origin"); c.Outcome != OutcomeFail {
		t.Fatalf("origin check = %s, want fail", c.Outcome)
	}
}

func TestOwnershipRejectsAWorktreeThatIsNotTheDeclaredOne(t *testing.T) {
	dir := newRepo(t, testOrigin)
	o := declaredFor(dir)
	o.Worktree = dir // declared correctly...
	state, err := ProbeRepo(dir)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	state.Root = filepath.Join(dir, "somewhere", "else") // ...but observed elsewhere

	cs := o.Assert(state, nil)
	if c := outcomeOf(t, cs, "ownership.repository"); c.Outcome != OutcomeFail {
		t.Fatalf("repository check = %s, want fail", c.Outcome)
	}
}

func TestOwnershipRejectsWrongBranch(t *testing.T) {
	dir := newRepo(t, testOrigin)
	mustGit(t, dir, "checkout", "-q", "-b", "someone-elses-branch")

	_, cs := AssertOwnership(declaredFor(dir))
	c := outcomeOf(t, cs, "ownership.branch")
	if c.Outcome != OutcomeFail {
		t.Fatalf("branch check = %s, want fail", c.Outcome)
	}
	if c.Expected != "main" || c.Observed != "someone-elses-branch" {
		t.Fatalf("branch check must name both sides, got expected=%q observed=%q", c.Expected, c.Observed)
	}
}

func TestOwnershipRejectsDetachedHead(t *testing.T) {
	dir := newRepo(t, testOrigin)
	mustGit(t, dir, "checkout", "-q", "--detach", mustGit(t, dir, "rev-parse", "HEAD"))

	_, cs := AssertOwnership(declaredFor(dir))
	if c := outcomeOf(t, cs, "ownership.branch"); c.Outcome != OutcomeFail {
		t.Fatalf("branch check on detached HEAD = %s, want fail", c.Outcome)
	}
}

func TestWorktreeCleanlinessGate(t *testing.T) {
	dir := newRepo(t, testOrigin)
	writeRepoFile(t, dir, "scratch.txt", "uncommitted\n")

	_, cs := AssertOwnership(declaredFor(dir))
	strict := outcomeOf(t, cs, "repository.clean")
	if strict.Outcome != OutcomeFail {
		t.Fatalf("dirty tree with no declaration = %s, want fail", strict.Outcome)
	}
	if strict.Detail == "" {
		t.Fatal("a cleanliness failure must enumerate the dirt, not merely assert it")
	}

	permissive := declaredFor(dir)
	permissive.AllowDirtyWorktree = true
	_, cs = AssertOwnership(permissive)
	relaxed := outcomeOf(t, cs, "repository.clean")
	if relaxed.Outcome != OutcomePass {
		t.Fatalf("declared-dirty tree = %s, want pass", relaxed.Outcome)
	}
	if relaxed.Detail == "" {
		t.Fatal("a permitted dirty tree must still enumerate the dirt")
	}
}

func TestOwnershipRejectsAnUnfinishedOperation(t *testing.T) {
	dir := newRepo(t, testOrigin)
	mustGit(t, dir, "checkout", "-q", "-b", "side")
	writeRepoFile(t, dir, "conflict.txt", "side\n")
	commitAll(t, dir, "feat: side")
	mustGit(t, dir, "checkout", "-q", "main")
	writeRepoFile(t, dir, "conflict.txt", "main\n")
	commitAll(t, dir, "feat: main")
	if _, err := runGit(dir, "merge", "side"); err == nil {
		t.Skip("merge did not conflict on this git")
	}

	o := declaredFor(dir)
	o.AllowDirtyWorktree = true // isolate the operation check from the dirt check
	_, cs := AssertOwnership(o)
	if c := outcomeOf(t, cs, "repository.operation"); c.Outcome != OutcomeFail {
		t.Fatalf("unfinished merge = %s, want fail", c.Outcome)
	}
}

func TestOwnershipOnAnUnreadableWorktreeReportsNotReachedNotPass(t *testing.T) {
	o := declaredFor(filepath.Join(t.TempDir(), "does-not-exist"))
	_, cs := AssertOwnership(o)

	if cs.Readiness() != NotReady {
		t.Fatalf("readiness = %s, want NOT-READY", cs.Readiness())
	}
	for _, id := range []string{"ownership.repository", "ownership.origin", "ownership.branch", "repository.clean"} {
		if c := outcomeOf(t, cs, id); c.Outcome != OutcomeNotReached {
			t.Fatalf("%s = %s, want not-reached — an unprobed repository proves nothing", id, c.Outcome)
		}
	}
	if c := outcomeOf(t, cs, "ownership.worktree"); c.Outcome != OutcomeFail {
		t.Fatalf("worktree check = %s, want fail", c.Outcome)
	}
}

func TestOwnershipAcceptsEquivalentRemoteSpellings(t *testing.T) {
	dir := newRepo(t, "git@github.com:CorsolvSolutions/corsolv-delivery-engine.git")
	_, cs := AssertOwnership(declaredFor(dir))
	if c := outcomeOf(t, cs, "ownership.origin"); c.Outcome != OutcomePass {
		t.Fatalf("ssh spelling of the declared origin = %s, want pass", c.Outcome)
	}
}
