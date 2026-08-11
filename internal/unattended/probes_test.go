package unattended

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func requirePOSIXProbes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the probe fixtures assume a POSIX command environment")
	}
}

func TestToolChecks(t *testing.T) {
	cs := ToolChecks(context.Background(), []ToolRequirement{
		{Name: "git", MinVersion: "1.0", VersionArgs: []string{"--version"}},
		{Name: "git"},
		{Name: "a-tool-that-does-not-exist-anywhere", Purpose: "proving absence is detected"},
		{Name: "git", MinVersion: "99.0", VersionArgs: []string{"--version"}},
		{Name: "another-absent-tool", HumanAction: "install it from the vendor's site"},
	})
	want := []Outcome{OutcomePass, OutcomePass, OutcomeFail, OutcomeFail, OutcomeHumanBoundary}
	for i, w := range want {
		if cs[i].Outcome != w {
			t.Fatalf("check %d (%s) = %s, want %s — observed %q", i, cs[i].Title, cs[i].Outcome, w, cs[i].Observed)
		}
	}
	if !strings.Contains(cs[3].Expected, "99.0") {
		t.Fatalf("a version failure must name the requirement, got %q", cs[3].Expected)
	}
	if cs[4].Boundary == "" {
		t.Fatal("a tool boundary must carry the human action that clears it")
	}
}

func TestToolChecksRefuseAnUnreadableVersion(t *testing.T) {
	// `git --help` succeeds and prints no version. Treating that as "new enough"
	// would let an unknown toolchain into an unattended run.
	cs := ToolChecks(context.Background(), []ToolRequirement{
		{Name: "git", MinVersion: "2.0", VersionArgs: []string{"rev-parse", "--is-inside-work-tree"}},
	})
	if cs[0].Outcome != OutcomeFail {
		t.Fatalf("unreadable version = %s, want fail", cs[0].Outcome)
	}
}

func TestPathChecks(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dir, "made-by-the-run")

	cs := PathChecks([]PathRequirement{
		{Path: dir, Kind: PathDir},
		{Path: file, Kind: PathFile},
		{Path: file, Kind: PathDir},
		{Path: filepath.Join(dir, "absent"), Kind: PathFile},
		{Path: dir},
		{Path: created, Kind: PathDir, Create: true},
		{Path: dir, Kind: PathDir, Writable: true},
	})
	want := []Outcome{OutcomePass, OutcomePass, OutcomeFail, OutcomeFail, OutcomePass, OutcomePass, OutcomePass}
	for i, w := range want {
		if cs[i].Outcome != w {
			t.Fatalf("check %d (%s) = %s, want %s", i, cs[i].Title, cs[i].Outcome, w)
		}
	}
	if fi, err := os.Stat(created); err != nil || !fi.IsDir() {
		t.Fatal("a create=true directory must actually be created")
	}
}

func TestPathChecksDoNotConjureProjectDirectories(t *testing.T) {
	// Only state directories the run owns may be created. A missing project
	// path is a misconfiguration to report, not something to paper over with an
	// empty directory.
	absent := filepath.Join(t.TempDir(), "project-that-should-exist")
	cs := PathChecks([]PathRequirement{{Path: absent, Kind: PathDir}})
	if cs[0].Outcome != OutcomeFail {
		t.Fatalf("absent project path = %s, want fail", cs[0].Outcome)
	}
	if _, err := os.Stat(absent); err == nil {
		t.Fatal("a non-create path requirement must not create the directory")
	}
}

func TestEnvChecksNeverRecordAValue(t *testing.T) {
	t.Setenv("GC_UNATTENDED_TEST_PRESENT", "an-ordinary-value")
	t.Setenv("GC_UNATTENDED_TEST_EMPTY", "   ")
	t.Setenv("GC_UNATTENDED_TEST_SECRET", "ghp_0123456789abcdefghijklmnopqrstuvwxyz")
	os.Unsetenv("GC_UNATTENDED_TEST_ABSENT") //nolint:errcheck

	cs := EnvChecks([]EnvRequirement{
		{Name: "GC_UNATTENDED_TEST_PRESENT"},
		{Name: "GC_UNATTENDED_TEST_EMPTY"},
		{Name: "GC_UNATTENDED_TEST_ABSENT"},
		{Name: "GC_UNATTENDED_TEST_SECRET", Secret: true},
	})
	want := []Outcome{OutcomePass, OutcomeFail, OutcomeFail, OutcomePass}
	for i, w := range want {
		if cs[i].Outcome != w {
			t.Fatalf("check %d (%s) = %s, want %s", i, cs[i].Title, cs[i].Outcome, w)
		}
	}
	// Not marked secret, and still withheld: a spec author must not be able to
	// leak a value by forgetting the flag.
	for _, c := range cs {
		if strings.Contains(c.Observed, "an-ordinary-value") || strings.Contains(c.Observed, "ghp_") {
			t.Fatalf("an environment value reached the report: %q", c.Observed)
		}
	}
}

func TestPortChecks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	defer ln.Close() //nolint:errcheck
	live := ln.Addr().String()

	spare, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	dead := spare.Addr().String()
	spare.Close() //nolint:errcheck

	cs := PortChecks([]PortRequirement{
		{Address: live},
		{Address: dead, TimeoutSeconds: 1},
	})
	if cs[0].Outcome != OutcomePass {
		t.Fatalf("reachable port = %s, want pass", cs[0].Outcome)
	}
	if cs[1].Outcome != OutcomeFail {
		t.Fatalf("unreachable port = %s, want fail", cs[1].Outcome)
	}
}

func TestCommandChecks(t *testing.T) {
	requirePOSIXProbes(t)
	cs := CommandChecks(context.Background(), []CommandRequirement{
		{ID: "git-responds", Title: "git responds", Argv: []string{"git", "--version"}},
		{ID: "git-version-text", Title: "git reports a version", Argv: []string{"git", "--version"}, ExpectOutput: "git version"},
		{ID: "wrong-output", Title: "output narrowing works", Argv: []string{"git", "--version"}, ExpectOutput: "mercurial"},
		{ID: "nonzero", Title: "a refusing probe", Argv: []string{"git", "not-a-real-subcommand"}},
		{ID: "absent", Title: "an absent binary", Argv: []string{"a-binary-that-does-not-exist"}},
	})
	want := []Outcome{OutcomePass, OutcomePass, OutcomeFail, OutcomeFail, OutcomeFail}
	for i, w := range want {
		if cs[i].Outcome != w {
			t.Fatalf("check %d (%s) = %s, want %s — observed %q", i, cs[i].Title, cs[i].Outcome, w, cs[i].Observed)
		}
	}
}

func TestClassifyCredential(t *testing.T) {
	requirePOSIXProbes(t)
	now := time.Now().UTC()
	dir := t.TempDir()
	full := filepath.Join(dir, "full")
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(full, []byte("material"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_UNATTENDED_CRED_SET", "value")

	cases := []struct {
		name string
		req  CredentialRequirement
		want CredentialState
	}{
		{"env present", CredentialRequirement{ID: "a", Env: "GC_UNATTENDED_CRED_SET"}, CredentialReady},
		{"env absent", CredentialRequirement{ID: "b", Env: "GC_UNATTENDED_CRED_UNSET"}, CredentialMissing},
		{"file present", CredentialRequirement{ID: "c", File: full}, CredentialReady},
		{"file empty", CredentialRequirement{ID: "d", File: empty}, CredentialMissing},
		{"file absent", CredentialRequirement{ID: "e", File: filepath.Join(dir, "nope")}, CredentialMissing},
		{"declared expiry passed", CredentialRequirement{ID: "f", Env: "GC_UNATTENDED_CRED_SET", NotAfter: now.Add(-time.Hour)}, CredentialExpired},
		{"declared expiry future", CredentialRequirement{ID: "g", Env: "GC_UNATTENDED_CRED_SET", NotAfter: now.Add(time.Hour)}, CredentialReady},
		{"probe accepts", CredentialRequirement{ID: "h", Probe: []string{"git", "--version"}}, CredentialReady},
		{"probe refuses, unrecognized", CredentialRequirement{ID: "i", Probe: []string{"git", "not-a-real-subcommand"}}, CredentialHumanAuthRequired},
		{"probe refuses, expiry recognized", CredentialRequirement{
			ID: "j", Probe: []string{"git", "not-a-real-subcommand"}, ExpiredPattern: `is not a git command`,
		}, CredentialExpired},
		{"probe cannot run", CredentialRequirement{ID: "k", Probe: []string{"a-binary-that-does-not-exist"}}, CredentialHumanAuthRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := ClassifyCredential(context.Background(), tc.req, now)
			if got != tc.want {
				t.Fatalf("ClassifyCredential = %s (%s), want %s", got, why, tc.want)
			}
			if why == "" {
				t.Fatal("a credential state must carry its reasoning")
			}
		})
	}
}

func TestDeclaredExpiryOutranksAProbeThatMayBeAnsweringFromCache(t *testing.T) {
	requirePOSIXProbes(t)
	req := CredentialRequirement{
		ID:       "cached",
		Probe:    []string{"git", "--version"}, // would answer "ready"
		NotAfter: time.Now().Add(-time.Minute),
	}
	got, _ := ClassifyCredential(context.Background(), req, time.Now().UTC())
	if got != CredentialExpired {
		t.Fatalf("got %s, want expired — a known expiry outranks any probe", got)
	}
}

func TestCredentialTroubleIsABoundaryWhenAHumanActionIsNamed(t *testing.T) {
	requirePOSIXProbes(t)
	named := CredentialChecks(context.Background(), []CredentialRequirement{{
		ID: "named", Env: "GC_UNATTENDED_CRED_DEFINITELY_UNSET",
		HumanAction: "authenticate the provider CLI",
	}})
	if named[0].Outcome != OutcomeHumanBoundary {
		t.Fatalf("named credential trouble = %s, want human-boundary", named[0].Outcome)
	}
	if named[0].Boundary == "" {
		t.Fatal("a boundary must name the human action")
	}

	// A state nobody can be told how to clear is a defect in the spec, not a
	// boundary a queue can route around.
	unnamed := CredentialChecks(context.Background(), []CredentialRequirement{{
		ID: "unnamed", Env: "GC_UNATTENDED_CRED_DEFINITELY_UNSET",
	}})
	if unnamed[0].Outcome != OutcomeFail {
		t.Fatalf("unnamed credential trouble = %s, want fail", unnamed[0].Outcome)
	}
}

func TestRedactRemovesCredentialMaterial(t *testing.T) {
	secrets := []string{
		"gho_16charsandmoreaaaaaaaaaaaaaaaaaaaaaa",
		"ghp_0123456789abcdefghijklmnopqrstuvwxyz",
		"github_pat_11ABCDEFG0abcdefghijklmnopqrstuvwxyz",
		"Bearer abcdef0123456789xyz",
		"AKIAIOSFODNN7EXAMPLE",
		"token: hunter2",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N",
	}
	for _, s := range secrets {
		got := Redact("output before " + s + " output after")
		if strings.Contains(got, s) {
			t.Fatalf("Redact left %q intact: %q", s, got)
		}
		if !strings.Contains(got, "[redacted]") {
			t.Fatalf("Redact removed %q without marking the removal: %q", s, got)
		}
	}
	if got := Redact("Logged in to github.com account Corsolv"); !strings.Contains(got, "Corsolv") {
		t.Fatalf("Redact destroyed ordinary text: %q", got)
	}
}

func TestRunProbeTreatsNonZeroExitAsAnAnswerNotAnError(t *testing.T) {
	requirePOSIXProbes(t)
	out, ok, err := runProbe(context.Background(), defaultProbeTimeout, "", []string{"git", "not-a-real-subcommand"})
	if err != nil {
		t.Fatalf("a non-zero exit must not be an error: %v", err)
	}
	if ok {
		t.Fatal("a non-zero exit must report ok=false")
	}
	if out == "" {
		t.Fatal("probe output must be captured so a refusal can be diagnosed")
	}
}

func TestRunProbeIsBounded(t *testing.T) {
	// A preflight that hangs is worse than one that fails: a failure at least
	// says something, and an unattended run has nobody to notice the silence.
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep is not available")
	}
	start := time.Now()
	_, _, err := runProbe(context.Background(), 200*time.Millisecond, "", []string{"sleep", "30"})
	if err == nil {
		t.Fatal("a probe that outlives its timeout must return an error, not hang")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("a timeout error must say so: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the probe took %s to give up, want roughly its timeout", elapsed)
	}
}

func TestWithinPath(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	if !withinPath(root, inside) {
		t.Fatal("a nested directory must read as inside")
	}
	if !withinPath(root, root) {
		t.Fatal("a directory must read as inside itself")
	}
	if withinPath(root, outside) {
		t.Fatal("a sibling directory must not read as inside")
	}
	// The prefix trap: /tmp/x must not contain /tmp/x-other.
	if withinPath(root, root+"-other") {
		t.Fatal("a path sharing a string prefix is not inside")
	}
}
