package unattended

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// defaultProbeTimeout bounds any probe that does not declare its own. Every
// probe here is a subprocess or a socket, and both can hang. An unattended
// preflight that hangs is worse than one that fails: a failure at least says
// something, and there is nobody watching the silence.
const defaultProbeTimeout = 20 * time.Second

// orphanWaitDelay is how long a canceled command is given to let go of its
// output pipes before the caller stops waiting on them.
//
// It closes a hole this package's own timeout test found: a task that spawns a
// descendant which outlives it keeps the pipes open, and the run waits on a
// process nobody is watching, long past the timeout it declared.
const orphanWaitDelay = 5 * time.Second

func timeoutOr(seconds int, fallback time.Duration) time.Duration {
	if seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}

// runProbe executes a command and reports its combined output and exit status.
//
// A non-zero exit is an answer, not an error: "the probe ran and said no" and
// "the probe could not run" need different responses, so they are returned
// differently.
func runProbe(ctx context.Context, timeout time.Duration, dir string, argv []string) (out string, ok bool, err error) {
	if len(argv) == 0 {
		return "", false, fmt.Errorf("probe has no command")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	// Nothing probed here may ask a person a question: an interactive prompt in
	// an unattended preflight is an unbounded hang wearing a question mark.
	cmd.Stdin = nil
	superviseProcess(cmd)
	cmd.WaitDelay = orphanWaitDelay
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	raw, runErr := cmd.CombinedOutput()
	out = strings.TrimSpace(string(raw))
	if ctx.Err() != nil {
		return out, false, fmt.Errorf("probe %q timed out after %s", strings.Join(argv, " "), timeout)
	}
	if runErr != nil {
		var ee *exec.ExitError
		if asExitError(runErr, &ee) {
			return out, false, nil
		}
		return out, false, runErr
	}
	return out, true, nil
}

func notes(purpose string) string {
	if purpose == "" {
		return ""
	}
	return " — " + purpose
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// --- tools ------------------------------------------------------------------

// ToolChecks resolves each declared executable and, where a minimum version is
// declared, reads and compares it.
//
// A tool whose requirement names a HumanAction is a boundary when absent rather
// than a failure: installing software is a person's job, and a run that can
// still do useful work without that tool should not be refused outright.
func ToolChecks(ctx context.Context, tools []ToolRequirement) Checks {
	var cs Checks
	for _, tool := range tools {
		id := "tool." + tool.Name
		title := "tool available: " + tool.Name

		path, err := exec.LookPath(tool.Name)
		if err != nil {
			if tool.HumanAction != "" {
				cs = append(cs, Check{
					ID: id, Category: CategoryTools, Title: title, Outcome: OutcomeHumanBoundary,
					Expected: tool.Name + " on PATH", Observed: "not found",
					Boundary: tool.HumanAction,
				})
				continue
			}
			cs = append(cs, fail(id, CategoryTools, title,
				tool.Name+" on PATH", "not found",
				"install "+tool.Name+notes(tool.Purpose)))
			continue
		}
		if tool.MinVersion == "" {
			cs = append(cs, pass(id, CategoryTools, title, path))
			continue
		}

		args := tool.VersionArgs
		if len(args) == 0 {
			args = []string{"--version"}
		}
		out, _, probeErr := runProbe(ctx, defaultProbeTimeout, "", append([]string{tool.Name}, args...))
		if probeErr != nil {
			cs = append(cs, fail(id, CategoryTools, title,
				tool.Name+" >= "+tool.MinVersion, "version could not be read: "+probeErr.Error(),
				"check that `"+tool.Name+" "+strings.Join(args, " ")+"` runs"))
			continue
		}
		atLeast, verr := VersionAtLeast(out, tool.MinVersion)
		switch {
		case verr != nil:
			cs = append(cs, fail(id, CategoryTools, title,
				tool.Name+" >= "+tool.MinVersion, "unreadable version: "+verr.Error(),
				"correct tool.versionArgs so the banner contains a version number"))
		case !atLeast:
			cs = append(cs, fail(id, CategoryTools, title,
				tool.Name+" >= "+tool.MinVersion, firstLine(out),
				"upgrade "+tool.Name+" to at least "+tool.MinVersion+notes(tool.Purpose)))
		default:
			cs = append(cs, pass(id, CategoryTools, title, path+" ("+firstLine(out)+")"))
		}
	}
	return cs
}

// --- paths ------------------------------------------------------------------

// PathChecks verifies every declared path, and creates the state directories
// the run owns.
//
// Creating is confined to `create = true` directories on purpose: a run may
// make the place it keeps its own journal, and may not conjure the project
// directories it was pointed at. A missing project path is a misconfiguration
// that must be reported, not papered over with an empty directory.
func PathChecks(paths []PathRequirement) Checks {
	var cs Checks
	for _, p := range paths {
		id := "path." + p.Path
		title := "path present: " + p.Path

		if p.Create {
			if err := os.MkdirAll(p.Path, 0o755); err != nil {
				cs = append(cs, fail(id, CategoryEnvironment, title,
					"a directory this run may create", err.Error(),
					"give the run permission to create "+p.Path+notes(p.Purpose)))
				continue
			}
		}

		fi, err := os.Stat(p.Path)
		if err != nil {
			cs = append(cs, fail(id, CategoryEnvironment, title,
				p.Path, "absent",
				"create it, or correct the path in the run spec"+notes(p.Purpose)))
			continue
		}
		switch {
		case p.Kind == PathDir && !fi.IsDir():
			cs = append(cs, fail(id, CategoryEnvironment, title, "a directory", "a file",
				"correct path.kind or the path itself"))
			continue
		case p.Kind == PathFile && fi.IsDir():
			cs = append(cs, fail(id, CategoryEnvironment, title, "a file", "a directory",
				"correct path.kind or the path itself"))
			continue
		}
		if p.Writable {
			if err := probeWritable(p.Path, fi.IsDir()); err != nil {
				cs = append(cs, fail(id, CategoryEnvironment, title, "writable", err.Error(),
					"grant write permission on "+p.Path+notes(p.Purpose)))
				continue
			}
		}
		cs = append(cs, pass(id, CategoryEnvironment, title, p.Path))
	}
	return cs
}

// probeWritable answers by writing, because permission bits do not decide the
// question on every filesystem this runs on — a read-only mount, an ACL and a
// full disk all present as writable metadata.
func probeWritable(path string, isDir bool) error {
	if !isDir {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("not writable: %w", err)
		}
		return f.Close()
	}
	f, err := os.CreateTemp(path, ".gc-unattended-writable-*")
	if err != nil {
		return fmt.Errorf("not writable: %w", err)
	}
	name := f.Name()
	f.Close() //nolint:errcheck
	return os.Remove(name)
}

// --- environment ------------------------------------------------------------

// EnvChecks verifies declared environment variables are present and non-empty.
//
// No value is ever recorded, secret or not. Presence is the entire question a
// preflight is entitled to ask, and answering it needs no disclosure — which
// also means a spec author cannot leak a value by forgetting to mark it secret.
func EnvChecks(env []EnvRequirement) Checks {
	var cs Checks
	for _, e := range env {
		id := "env." + e.Name
		title := "environment value set: " + e.Name
		val, set := os.LookupEnv(e.Name)
		switch {
		case !set:
			cs = append(cs, fail(id, CategoryEnvironment, title, "set", "unset",
				"export "+e.Name+notes(e.Purpose)))
		case strings.TrimSpace(val) == "":
			cs = append(cs, fail(id, CategoryEnvironment, title, "a non-empty value", "set but empty",
				"set "+e.Name+" to a real value"+notes(e.Purpose)))
		default:
			cs = append(cs, pass(id, CategoryEnvironment, title, "set (value withheld)"))
		}
	}
	return cs
}

// --- ports ------------------------------------------------------------------

// PortChecks dials each declared endpoint.
func PortChecks(ports []PortRequirement) Checks {
	var cs Checks
	for _, p := range ports {
		id := "port." + p.Address
		title := "service reachable: " + p.Address
		conn, err := net.DialTimeout("tcp", p.Address, timeoutOr(p.TimeoutSeconds, 5*time.Second))
		if err != nil {
			cs = append(cs, fail(id, CategoryEnvironment, title,
				p.Address+" accepting connections", "unreachable: "+err.Error(),
				"start the service"+notes(p.Purpose)))
			continue
		}
		conn.Close() //nolint:errcheck
		cs = append(cs, pass(id, CategoryEnvironment, title, p.Address))
	}
	return cs
}

// --- external dependencies --------------------------------------------------

// CommandChecks probes each declared external dependency by running it.
//
// Exit status is the verdict, optionally narrowed by ExpectOutput: a tool that
// exits zero while reporting a problem is common enough that exit status alone
// is a weak signal, and the person declaring the dependency is the one who
// knows what its healthy output looks like.
func CommandChecks(ctx context.Context, cmds []CommandRequirement) Checks {
	var cs Checks
	for _, c := range cmds {
		id := "command." + c.ID
		title := c.Title
		if title == "" {
			title = "dependency responds: " + strings.Join(c.Argv, " ")
		}
		remedy := c.Remedy
		if remedy == "" {
			remedy = "check `" + strings.Join(c.Argv, " ") + "`"
		}

		out, ok, err := runProbe(ctx, timeoutOr(c.TimeoutSeconds, defaultProbeTimeout), c.Dir, c.Argv)
		switch {
		case err != nil:
			cs = append(cs, fail(id, CategoryDependencies, title, "the probe runs", err.Error(), remedy))
		case !ok:
			cs = append(cs, fail(id, CategoryDependencies, title, "exit status 0", firstLine(out), remedy))
		case c.ExpectOutput != "" && !strings.Contains(out, c.ExpectOutput):
			cs = append(cs, fail(id, CategoryDependencies, title,
				"output containing "+c.ExpectOutput, firstLine(out), remedy))
		default:
			cs = append(cs, pass(id, CategoryDependencies, title, firstLine(out)))
		}
	}
	return cs
}

// --- credentials ------------------------------------------------------------

// secretPatterns match credential material that must never reach a durable
// report.
//
// The list is deliberately broad. A probe's output is arbitrary text from a
// third-party tool, over-redacting costs a reader a little context, and
// under-redacting writes a live token into evidence that outlives the run.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._\-]{12,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`(?i)\b(token|secret|password|passwd|api[_-]?key)\b\s*[:=]\s*\S+`),
	regexp.MustCompile(`\bey[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`),
	regexp.MustCompile(`\b[A-Za-z0-9_\-]{40,}\b`),
}

// Redact removes anything that looks like credential material from text bound
// for a report.
func Redact(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	return s
}

// CredentialState is what a credential probe concluded.
type CredentialState string

// The four credential states. They are distinguished because they need four
// different responses, and a run that cannot tell them apart cannot say whether
// waiting would help.
const (
	CredentialReady             CredentialState = "ready"
	CredentialMissing           CredentialState = "missing"
	CredentialExpired           CredentialState = "expired"
	CredentialHumanAuthRequired CredentialState = "human-auth-required"
)

// ClassifyCredential decides a credential's state from everything declared
// about it.
//
// A declared expiry is checked before any probe. A probe can be answering from
// a cache, and a credential that is known to have expired has expired whatever
// the cache says.
func ClassifyCredential(ctx context.Context, c CredentialRequirement, now time.Time) (CredentialState, string) {
	if !c.NotAfter.IsZero() && now.After(c.NotAfter) {
		return CredentialExpired, "declared expiry " + c.NotAfter.UTC().Format(time.RFC3339) + " has passed"
	}
	if c.Env != "" {
		if v, ok := os.LookupEnv(c.Env); !ok || strings.TrimSpace(v) == "" {
			return CredentialMissing, "environment value " + c.Env + " is unset or empty"
		}
	}
	if c.File != "" {
		fi, err := os.Stat(c.File)
		switch {
		case err != nil:
			return CredentialMissing, "credential file " + c.File + " is absent"
		case fi.Size() == 0:
			return CredentialMissing, "credential file " + c.File + " is empty"
		}
	}
	if len(c.Probe) == 0 {
		return CredentialReady, "declared material is present"
	}

	out, ok, err := runProbe(ctx, timeoutOr(c.TimeoutSeconds, defaultProbeTimeout), "", c.Probe)
	switch {
	case err != nil:
		return CredentialHumanAuthRequired, "the probe could not run: " + Redact(err.Error())
	case ok:
		return CredentialReady, "the probe accepted the credential"
	}
	if c.ExpiredPattern != "" {
		if re, cerr := regexp.Compile(c.ExpiredPattern); cerr == nil && re.MatchString(out) {
			return CredentialExpired, "the probe reported expiry: " + Redact(firstLine(out))
		}
	}
	// The probe refused and said nothing this spec recognizes as expiry. A
	// person has to look — which is exactly what human-auth-required means.
	return CredentialHumanAuthRequired, "the probe refused: " + Redact(firstLine(out))
}

// CredentialChecks classifies every declared credential.
//
// Anything but ready is a human boundary rather than a failure, provided the
// requirement names the human action. None of these states can be resolved by
// the run, and calling them failures would make a run NOT-READY when it may
// have a great deal of work that never touches that credential. A requirement
// with no named action is a genuine failure: an unfixable state nobody can be
// told how to fix is a defect in the spec.
func CredentialChecks(ctx context.Context, creds []CredentialRequirement) Checks {
	var cs Checks
	now := time.Now().UTC()
	for _, c := range creds {
		id := "credential." + c.ID
		title := c.Title
		if title == "" {
			title = "credential ready: " + c.ID
		}
		state, why := ClassifyCredential(ctx, c, now)
		if state == CredentialReady {
			cs = append(cs, pass(id, CategoryCredentials, title, string(CredentialReady)))
			continue
		}
		if c.HumanAction == "" {
			cs = append(cs, fail(id, CategoryCredentials, title,
				string(CredentialReady), string(state)+": "+why,
				"declare credential.humanAction so this state can be reported as a boundary a person can clear"))
			continue
		}
		cs = append(cs, Check{
			ID: id, Category: CategoryCredentials, Title: title, Outcome: OutcomeHumanBoundary,
			Expected: string(CredentialReady), Observed: string(state) + ": " + why,
			Boundary: c.HumanAction,
		})
	}
	return cs
}

// stateDirPath joins a run state directory with a name, for the journal,
// heartbeat and reports.
func stateDirPath(stateDir, name string) string { return filepath.Join(stateDir, name) }

func osMkdirAll(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %q: %w", dir, err)
	}
	return nil
}

// withinPath reports whether child lies inside parent, comparing canonicalized
// paths so that a symlinked or differently spelled route to the same directory
// is still recognized as inside it.
func withinPath(parent, child string) bool {
	canon := func(p string) string {
		p = filepath.Clean(p)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return p
	}
	p, c := canon(parent), canon(child)
	if p == c {
		return true
	}
	rel, err := filepath.Rel(p, c)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
