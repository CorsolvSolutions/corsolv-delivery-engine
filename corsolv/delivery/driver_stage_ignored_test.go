//go:build integration

// Publication of an authorized path that .gitignore ignores.
//
// Found on aws-infrastructure-intelligence: the plan named docs/build/ci.md,
// the project's own .gitignore ignored `build/`, and `git add` refused the
// whole staging invocation with "The following paths are ignored". A package
// whose gates had all passed failed publication three times, reported only as
// "staging authorized paths", and forty-four tasks were held behind it.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/handoff"
)

// ignoreInRig makes `pattern` ignored for every worktree of the rig without
// changing any tracked file — the same effect as a committed .gitignore, but
// leaving the scope check nothing unrelated to report.
func ignoreInRig(t *testing.T, s *sendbackEnv, pattern string) {
	t.Helper()
	common := gitOut(t, s.recoveryEnv, s.worktree, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(common) {
		common = filepath.Join(s.worktree, common)
	}
	if err := os.MkdirAll(filepath.Join(common, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(common, "info", "exclude"), []byte(pattern+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePlanPaths(t *testing.T, s *sendbackEnv, paths ...string) {
	t.Helper()
	plan := fixturePlan()
	plan.Packages[0].AuthorizedPaths = paths
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handoff.PlanPath(s.root, s.project), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeWorkFile(t *testing.T, s *sendbackEnv, rel, body string) {
	t.Helper()
	full := filepath.Join(s.worktree, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A FILE THE PLAN NAMES is published even though .gitignore ignores it: the
// plan asked for exactly that file.
func TestAnIgnoredFileThePlanNamesIsPublished(t *testing.T) {
	s := newSendbackEnv(t)
	ignoreInRig(t, s, "build/")
	writePlanPaths(t, s, "src/one.ts", "docs/build/ci.md")
	writeWorkFile(t, s, "docs/build/ci.md", "# CI\n")

	code, out := s.runPublish()

	if strings.Contains(out, "are ignored by one of your .gitignore files") ||
		strings.Contains(out, "staging wp-one's authorized paths") {
		t.Fatalf("an explicitly authorized ignored file failed the publication:\n%s", out)
	}
	if !strings.Contains(out, "force-staging") || !strings.Contains(out, "docs/build/ci.md") {
		t.Errorf("the run must say it force-staged the ignored file, got:\n%s", out)
	}

	branch := "delivery/20260814T164300Z/wp-one"
	origin := filepath.Join(s.root, "origin.git")
	files := gitOut(t, s.recoveryEnv, origin, "ls-tree", "-r", "--name-only", "refs/heads/"+branch)
	for _, want := range []string{"src/one.ts", "docs/build/ci.md"} {
		if !strings.Contains(files, want) {
			t.Errorf("the published commit is missing %s (exit %d); it holds:\n%s\n%s", want, code, files, out)
		}
	}
	// This fixture's CI is red, so publication still stops — at the CI verdict,
	// which proves it got past staging, committing and pushing.
	if code == 0 || !strings.Contains(out, "required CI") {
		t.Errorf("publication must reach the CI verdict (exit %d):\n%s", code, out)
	}
}

// A DIRECTORY that .gitignore ignores is never force-added: -f would publish
// whatever ignored content lies beneath it, which no scope check ever saw.
func TestAnIgnoredDirectoryIsRefusedByNameNotForceAdded(t *testing.T) {
	s := newSendbackEnv(t)
	ignoreInRig(t, s, "build/")
	writePlanPaths(t, s, "src/one.ts", "docs/build")
	writeWorkFile(t, s, "docs/build/ci.md", "# CI\n")
	writeWorkFile(t, s, "docs/build/.env", "SECRET=1\n")

	code, out := s.runPublish()

	if code == 0 {
		t.Fatalf("an ignored authorized directory must stop publication:\n%s", out)
	}
	if !strings.Contains(out, "docs/build") || !strings.Contains(out, "never force-added") {
		t.Errorf("the refusal must name the directory and why, got:\n%s", out)
	}
	origin := filepath.Join(s.root, "origin.git")
	if refs := gitOut(t, s.recoveryEnv, origin, "branch", "--list", "delivery/20260814T164300Z/wp-one"); refs != "" {
		files := gitOut(t, s.recoveryEnv, origin, "ls-tree", "-r", "--name-only", "refs/heads/delivery/20260814T164300Z/wp-one")
		if strings.Contains(files, ".env") {
			t.Fatalf("ignored content under an authorized directory was published:\n%s", files)
		}
	}
}
