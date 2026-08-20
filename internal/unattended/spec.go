package unattended

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Spec is the declarative description of everything a run needs before it may
// begin.
//
// It is a file, not code, for one reason: every machine-specific fact this
// layer exists to check — an absolute path, a port, a repository URL, a
// required binary — was previously embedded in the logic of a harness script,
// which is what made those harnesses unrunnable anywhere but the machine they
// were written on. Nothing here is inferred from the host. If the run needs it,
// the spec names it.
type Spec struct {
	// ProjectID identifies the delivery project the run belongs to. It is the
	// key the Dashboard projection is published under, so it is not decorative.
	ProjectID string `toml:"projectId" json:"projectId"`

	// Ownership is what this session may touch, proved before any mutation.
	Ownership Ownership `toml:"ownership" json:"ownership"`

	Tools       []ToolRequirement       `toml:"tool,omitempty" json:"tools,omitempty"`
	Paths       []PathRequirement       `toml:"path,omitempty" json:"paths,omitempty"`
	Env         []EnvRequirement        `toml:"env,omitempty" json:"env,omitempty"`
	Ports       []PortRequirement       `toml:"port,omitempty" json:"ports,omitempty"`
	Commands    []CommandRequirement    `toml:"command,omitempty" json:"commands,omitempty"`
	Credentials []CredentialRequirement `toml:"credential,omitempty" json:"credentials,omitempty"`

	// GitHub is the forge readiness the run needs. Absent means the run makes
	// no GitHub claim at all, which is only true of a purely local run.
	GitHub *GitHubRequirement `toml:"github,omitempty" json:"github,omitempty"`

	// Classification carries the run's declared failure-classification rules.
	// They live in the spec rather than in Go because deciding that a
	// particular stderr line means "environment" rather than "code defect" is a
	// judgement about a specific project's tooling.
	Classification []ClassificationRule `toml:"classify,omitempty" json:"classification,omitempty"`

	// QA is the project's half of mandatory-gate selection. It may add gates
	// the packet's risk class does not already require; it cannot remove one.
	QA QAPolicy `toml:"qa,omitempty" json:"qa,omitempty"`

	// StateDir is where durable run evidence — journal, heartbeat, checkpoint —
	// is written. It must be outside the mutable worktree so that a checkout,
	// a branch switch or a cleanliness check never touches it.
	StateDir string `toml:"stateDir" json:"stateDir"`

	// PublishPath is the delivery projection the Dashboard reads. Empty means
	// this run publishes nothing, and the report says so rather than pretending
	// publication happened.
	PublishPath string `toml:"publishPath,omitempty" json:"publishPath,omitempty"`

	// Boundaries are the human limits this run already knows about before any
	// probe runs.
	//
	// Every other boundary in a report is DISCOVERED — a missing credential, a
	// merge permission the account does not have — because a probe can ask the
	// world about it. Some cannot be asked: whether a person will accept a
	// release is not a fact about this machine, and the only authority on it is
	// the configuration that stated the limit. Declaring one here puts it in the
	// same report, under the same outcome, as the ones a probe found, so a limit
	// no probe can see is not silently missing from the answer to "may this run
	// start".
	Boundaries []KnownBoundary `toml:"boundary,omitempty" json:"boundaries,omitempty"`
}

// KnownBoundary is a limit only a person can lift, stated by the run's
// configuration rather than found by a probe.
type KnownBoundary struct {
	// ID is the check id this boundary is published under. Tasks name it in
	// RequiresChecks to be held behind it.
	ID string `toml:"id" json:"id"`
	// Title is the question the check answers, in the report's voice.
	Title string `toml:"title,omitempty" json:"title,omitempty"`
	// Detail says why the limit exists, for the person reading the report.
	Detail string `toml:"detail,omitempty" json:"detail,omitempty"`
	// Action names what only a person can do about it. A boundary nobody can
	// act on is indistinguishable from a vague failure, so it is required.
	Action string `toml:"action" json:"action"`
}

// ToolRequirement is an executable the run cannot proceed without.
type ToolRequirement struct {
	Name string `toml:"name" json:"name"`
	// MinVersion, when set, is compared against the version the tool reports.
	MinVersion string `toml:"minVersion,omitempty" json:"minVersion,omitempty"`
	// VersionArgs override the default `--version` probe for tools that spell
	// it differently.
	VersionArgs []string `toml:"versionArgs,omitempty" json:"versionArgs,omitempty"`
	// Purpose says what the run needs it for, so a failure report is actionable
	// without reading the spec.
	Purpose string `toml:"purpose,omitempty" json:"purpose,omitempty"`
	// HumanAction, when set, downgrades absence from a failure to a named human
	// boundary: the tool is genuinely missing, and installing it is a person's
	// job, not this run's.
	HumanAction string `toml:"humanAction,omitempty" json:"humanAction,omitempty"`
}

// PathKind is what a required path must be.
type PathKind string

// The path kinds.
const (
	PathAny  PathKind = ""
	PathDir  PathKind = "dir"
	PathFile PathKind = "file"
)

// PathRequirement is a filesystem location the run depends on.
type PathRequirement struct {
	Path     string   `toml:"path" json:"path"`
	Kind     PathKind `toml:"kind,omitempty" json:"kind,omitempty"`
	Purpose  string   `toml:"purpose,omitempty" json:"purpose,omitempty"`
	Writable bool     `toml:"writable,omitempty" json:"writable,omitempty"`
	// Create allows preflight to make a missing directory rather than fail on
	// it. Only ever true for state directories the run owns.
	Create bool `toml:"create,omitempty" json:"create,omitempty"`
}

// EnvRequirement is an environment value the run reads.
//
// The value is never recorded — see envCheck. Only presence is a fact this
// report is entitled to carry.
type EnvRequirement struct {
	Name    string `toml:"name" json:"name"`
	Purpose string `toml:"purpose,omitempty" json:"purpose,omitempty"`
	// Secret marks a value that must never appear in any output. Presence is
	// still reported; the value never is.
	Secret bool `toml:"secret,omitempty" json:"secret,omitempty"`
}

// PortRequirement is a TCP endpoint that must answer before the run starts.
type PortRequirement struct {
	Address string `toml:"address" json:"address"`
	Purpose string `toml:"purpose,omitempty" json:"purpose,omitempty"`
	// TimeoutSeconds bounds the dial. Zero uses the package default.
	TimeoutSeconds int `toml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
}

// CommandRequirement is an external dependency probed by running something,
// where the exit status is the verdict.
type CommandRequirement struct {
	ID             string   `toml:"id" json:"id"`
	Title          string   `toml:"title" json:"title"`
	Argv           []string `toml:"argv" json:"argv"`
	Dir            string   `toml:"dir,omitempty" json:"dir,omitempty"`
	TimeoutSeconds int      `toml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
	// ExpectOutput, when set, must appear in the combined output for the probe
	// to pass. Exit zero alone is a weak signal for tools that succeed while
	// reporting a problem.
	ExpectOutput string `toml:"expectOutput,omitempty" json:"expectOutput,omitempty"`
	Remedy       string `toml:"remedy,omitempty" json:"remedy,omitempty"`
}

// CredentialRequirement is a secret the run needs, classified without ever
// being read into the report.
type CredentialRequirement struct {
	ID    string `toml:"id" json:"id"`
	Title string `toml:"title" json:"title"`
	// Env names an environment variable that must be present and non-empty.
	Env string `toml:"env,omitempty" json:"env,omitempty"`
	// File names a file that must exist and be non-empty.
	File string `toml:"file,omitempty" json:"file,omitempty"`
	// NotAfter is a declared expiry. A credential whose expiry is known and
	// past is expired regardless of what a probe says, because the probe may be
	// reading a cached answer.
	NotAfter time.Time `toml:"notAfter,omitempty" json:"notAfter,omitempty"`
	// Probe is a command whose exit status decides live validity.
	Probe          []string `toml:"probe,omitempty" json:"probe,omitempty"`
	TimeoutSeconds int      `toml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
	// ExpiredPattern distinguishes an expired credential from an absent one in
	// the probe's output. Without it a failed probe is reported as unusable
	// rather than as expired, which is the honest answer.
	ExpiredPattern string `toml:"expiredPattern,omitempty" json:"expiredPattern,omitempty"`
	// HumanAction names what a person must do — re-authenticate, complete MFA,
	// request access. Its presence is what turns a missing credential from a
	// failure into a *named* human boundary the work queue can route around.
	HumanAction string `toml:"humanAction,omitempty" json:"humanAction,omitempty"`
}

// GitHubRequirement is the forge authority the run needs, declared up front so
// a merge permission is discovered before the work rather than at merge time.
type GitHubRequirement struct {
	// Repo is `owner/name`.
	Repo string `toml:"repo" json:"repo"`
	// Command is the forge CLI to use. It belongs here because where `gh` lives
	// is machine-specific — on this host the execution environment is WSL and
	// the only authenticated `gh` is a Windows install reached through /mnt/c —
	// and the whole purpose of this file is that such facts are declared rather
	// than assumed. Empty means `gh` on PATH.
	Command string `toml:"command,omitempty" json:"command,omitempty"`
	// Account, when set, is the login the run must be authenticated as. A
	// session authenticated as the wrong account is the ownership failure in
	// forge form.
	Account string `toml:"account,omitempty" json:"account,omitempty"`
	Branch  string `toml:"branch,omitempty" json:"branch,omitempty"`

	NeedPush   bool `toml:"needPush,omitempty" json:"needPush,omitempty"`
	NeedPR     bool `toml:"needPr,omitempty" json:"needPr,omitempty"`
	NeedChecks bool `toml:"needChecks,omitempty" json:"needChecks,omitempty"`
	NeedMerge  bool `toml:"needMerge,omitempty" json:"needMerge,omitempty"`

	// MergeHumanAction names the person-side action when merge authority is
	// absent or gated by review. Declaring it converts an eventual hard stop
	// into a boundary the queue can plan around.
	MergeHumanAction string `toml:"mergeHumanAction,omitempty" json:"mergeHumanAction,omitempty"`
}

// ClassificationRule maps an observed failure signature onto a failure class.
type ClassificationRule struct {
	// Pattern is a regular expression matched against the failure text.
	Pattern string `toml:"pattern" json:"pattern"`
	// Class is the class a match assigns.
	Class FailureClass `toml:"class" json:"class"`
	// Reason is recorded on the classified failure so the report says why.
	Reason string `toml:"reason,omitempty" json:"reason,omitempty"`
}

// LoadSpec reads a spec from a TOML file.
func LoadSpec(path string) (Spec, error) {
	var s Spec
	data, err := os.ReadFile(path)
	if err != nil {
		return s, fmt.Errorf("reading run spec %q: %w", path, err)
	}
	if err := toml.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parsing run spec %q: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return s, fmt.Errorf("run spec %q: %w", path, err)
	}
	return s, nil
}

// Encode renders a spec back to TOML. Round-tripping is a requirement, not a
// convenience: the spec that governed a run is part of the run's evidence, and
// evidence that cannot be re-read is not evidence.
func (s Spec) Encode() ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(s); err != nil {
		return nil, fmt.Errorf("encoding run spec: %w", err)
	}
	return buf.Bytes(), nil
}

// ErrSpecInvalid is returned when a spec cannot govern a run.
var ErrSpecInvalid = fmt.Errorf("unattended: run spec is invalid")

// Validate refuses a spec that would produce an unreadable verdict.
func (s Spec) Validate() error {
	var problems []string
	if strings.TrimSpace(s.ProjectID) == "" {
		problems = append(problems, "projectId is required")
	}
	if strings.TrimSpace(s.StateDir) == "" {
		problems = append(problems, "stateDir is required — a run with nowhere durable to write cannot be resumed")
	}
	if err := s.Ownership.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if s.Ownership.ProjectID != "" && s.ProjectID != "" && s.Ownership.ProjectID != s.ProjectID {
		problems = append(problems, fmt.Sprintf("ownership.projectId %q does not match projectId %q", s.Ownership.ProjectID, s.ProjectID))
	}
	for i, t := range s.Tools {
		if strings.TrimSpace(t.Name) == "" {
			problems = append(problems, fmt.Sprintf("tool[%d] has no name", i))
		}
	}
	for i, p := range s.Paths {
		if strings.TrimSpace(p.Path) == "" {
			problems = append(problems, fmt.Sprintf("path[%d] has no path", i))
		}
		switch p.Kind {
		case PathAny, PathDir, PathFile:
		default:
			problems = append(problems, fmt.Sprintf("path[%d] kind %q is not one of dir, file", i, string(p.Kind)))
		}
		if p.Create && p.Kind != PathDir {
			problems = append(problems, fmt.Sprintf("path[%d] may only be created when kind is dir", i))
		}
	}
	for i, e := range s.Env {
		if strings.TrimSpace(e.Name) == "" {
			problems = append(problems, fmt.Sprintf("env[%d] has no name", i))
		}
	}
	for i, p := range s.Ports {
		if !strings.Contains(p.Address, ":") {
			problems = append(problems, fmt.Sprintf("port[%d] address %q is not host:port", i, p.Address))
		}
	}
	for i, c := range s.Commands {
		if strings.TrimSpace(c.ID) == "" {
			problems = append(problems, fmt.Sprintf("command[%d] has no id", i))
		}
		if len(c.Argv) == 0 {
			problems = append(problems, fmt.Sprintf("command[%d] has no argv", i))
		}
	}
	for i, c := range s.Credentials {
		if strings.TrimSpace(c.ID) == "" {
			problems = append(problems, fmt.Sprintf("credential[%d] has no id", i))
		}
		if c.Env == "" && c.File == "" && len(c.Probe) == 0 && c.NotAfter.IsZero() {
			problems = append(problems, fmt.Sprintf("credential[%d] (%s) declares nothing to check", i, c.ID))
		}
	}
	for _, id := range s.QA.RequireGates {
		if _, ok := LookupGate(id); !ok {
			problems = append(problems, fmt.Sprintf("qa.requireGates names %q, which is not a catalog gate (%s)", id, joinGateIDs()))
		}
	}
	if s.GitHub != nil && !strings.Contains(s.GitHub.Repo, "/") {
		problems = append(problems, fmt.Sprintf("github.repo %q is not owner/name", s.GitHub.Repo))
	}
	seenBoundary := map[string]bool{}
	for i, b := range s.Boundaries {
		switch {
		case strings.TrimSpace(b.ID) == "":
			problems = append(problems, fmt.Sprintf("boundary[%d] has no id", i))
		case seenBoundary[b.ID]:
			// The report reduces boundaries to a map keyed by id, so a duplicate
			// would quietly replace one human action with another.
			problems = append(problems, fmt.Sprintf("boundary[%d] id %q is duplicated", i, b.ID))
		default:
			seenBoundary[b.ID] = true
		}
		if strings.TrimSpace(b.Action) == "" {
			problems = append(problems, fmt.Sprintf("boundary[%d] (%s) names no action only a person can take", i, b.ID))
		}
	}
	for i, r := range s.Classification {
		if strings.TrimSpace(r.Pattern) == "" {
			problems = append(problems, fmt.Sprintf("classify[%d] has no pattern", i))
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
