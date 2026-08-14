package handoff

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/gastownhall/gascity/internal/unattended"
)

// HostProfile is everything about this delivery machine that the portal does
// not know and must not be asked.
//
// It is the second half of the separation the intent contract makes. The portal
// says what to deliver; this says where things live on the host that will do
// it. Moving delivery to another machine is a change to this profile and to
// nothing else — which is the same property the run spec already has, applied
// one level up.
type HostProfile struct {
	// DeliveryRoot is where per-project delivery state lives. Each project gets
	// a directory under it holding the record, the plan and the run's state.
	DeliveryRoot string
	// Driver is the engine-owned executable every compiled task invokes. It is
	// the ONLY thing that ends up on a command line, and it comes from here —
	// never from the intent.
	Driver string
	// GitHubCommand is the forge CLI. On this host the engine runs under WSL
	// while the only authenticated gh is a Windows install, so assuming `gh` on
	// PATH is wrong and declaring it is the point.
	GitHubCommand string
	// Provider is the agent runtime workers are started under.
	Provider string
	// WindowsMountPrefix maps a Windows drive to its mount point on this host,
	// e.g. "/mnt". Empty means paths are used as given.
	WindowsMountPrefix string
}

// ErrHostProfileInvalid is returned for a profile that cannot govern a run.
var ErrHostProfileInvalid = errors.New("handoff: host profile is invalid")

// Validate refuses a profile missing something no default can supply.
func (h HostProfile) Validate() error {
	var missing []string
	if strings.TrimSpace(h.DeliveryRoot) == "" {
		missing = append(missing, "deliveryRoot")
	}
	if strings.TrimSpace(h.Driver) == "" {
		missing = append(missing, "driver")
	}
	if strings.TrimSpace(h.Provider) == "" {
		missing = append(missing, "provider")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: missing %s", ErrHostProfileInvalid, strings.Join(missing, ", "))
	}
	return nil
}

var windowsDrive = regexp.MustCompile(`^([A-Za-z]):[\\/]`)

// ResolveCheckout maps a checkout path recorded by the portal onto this host.
//
// The portal runs on Windows and records `D:\Development\thing`; the engine
// runs under WSL where that is `/mnt/d/Development/thing`. Translating here
// keeps the intent honest about what the portal knows — it really did register
// a Windows path — without making every downstream consumer aware of the mount
// layout.
func (h HostProfile) ResolveCheckout(p string) string {
	m := windowsDrive.FindStringSubmatch(p)
	if m == nil || h.WindowsMountPrefix == "" {
		return p
	}
	rest := strings.ReplaceAll(p[len(m[0]):], `\`, "/")
	return path_Join(h.WindowsMountPrefix, strings.ToLower(m[1]), rest)
}

// path_Join joins with forward slashes regardless of the host separator: the
// result addresses a POSIX mount point, so filepath.Join would corrupt it when
// this code is compiled for Windows.
//
//nolint:revive // named for what it is, not for what filepath.Join is
func path_Join(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		cleaned = append(cleaned, strings.Trim(p, "/"))
	}
	return "/" + strings.Join(cleaned, "/")
}

// ProjectDir is where a project's delivery state lives.
func (h HostProfile) ProjectDir(projectID string) string {
	return filepath.Join(h.DeliveryRoot, projectID)
}

// StateDir is the run's durable state directory.
//
// It sits beside the record rather than inside the worktree, because a checkout
// or a cleanliness check inside the worktree would touch the run's own account
// of what it was doing.
func (h HostProfile) StateDir(projectID string) string {
	return filepath.Join(h.ProjectDir(projectID), "run")
}

// ProjectionPath is where the run renders its delivery projection before
// publishing it into the project's repository.
func (h HostProfile) ProjectionPath(projectID string) string {
	return filepath.Join(h.StateDir(projectID), "PROJECT-STATE.yml")
}

// The stages a compiled delivery run executes, in order.
//
// They are named rather than numbered because they appear in the run's
// heartbeat, and "publishing wp-add" tells a person looking in at midnight
// something that "stage 4" does not.
const (
	// StageCityUp builds the city, clones the working rig and declares the
	// worker agents.
	StageCityUp = "city-up"
	// StageDispatch creates the work beads, wires their dependencies and routes
	// each to its worker.
	StageDispatch = "dispatch"
	// StageAwait waits for the agents to finish their work.
	StageAwait = "await"
	// StagePublish is per package: gate, commit, push, PR, checks, merge.
	StagePublish = "publish"
	// StageProject renders the delivery projection from the run's own evidence.
	StageProject = "project"
	// StagePublishProjection installs that projection into the project's
	// repository so the portal can read it.
	StagePublishProjection = "publish-projection"
)

// Compile turns an intent and its validated plan into the run the unattended
// layer already knows how to execute.
//
// Two properties matter more than the mechanics. First, every argv it produces
// is built from the host profile's driver and from identifiers this package has
// already validated — nothing from the intent reaches a command line as a word
// the shell could read. Second, the result is a plain declarative plan, so an
// interrupted run resumes through the journal the unattended layer already
// keeps, rather than through anything invented here.
func Compile(in Intent, plan DeliveryPlan, host HostProfile, runID string) (unattended.Spec, unattended.Plan, error) {
	var spec unattended.Spec
	var work unattended.Plan

	if err := in.Validate(); err != nil {
		return spec, work, err
	}
	if err := plan.Validate(in); err != nil {
		return spec, work, err
	}
	if err := host.Validate(); err != nil {
		return spec, work, err
	}
	if strings.TrimSpace(runID) == "" {
		return spec, work, fmt.Errorf("%w: a run needs an id", ErrHostProfileInvalid)
	}

	worktree := host.ResolveCheckout(in.Checkout)
	stateDir := host.StateDir(in.ProjectID)

	spec = unattended.Spec{
		ProjectID: in.ProjectID,
		Ownership: unattended.Ownership{
			ProjectID:      in.ProjectID,
			Worktree:       worktree,
			ExpectedOrigin: in.Repository.Origin,
			ExpectedBranch: in.Repository.DefaultBranch,
			Role:           unattended.RoleController,
			Session:        "managed-delivery-" + in.ProjectID,
			// The registered checkout is the project's, not the run's. Delivery
			// happens in a working clone the run makes for itself, so the
			// checkout is read for identity and never mutated — and a person's
			// half-finished edit in it is not this run's business to refuse.
			AllowDirtyWorktree: true,
		},
		StateDir:    stateDir,
		PublishPath: host.ProjectionPath(in.ProjectID),
		Tools: []unattended.ToolRequirement{
			{Name: "git", MinVersion: "2.30", VersionArgs: []string{"--version"}, Purpose: "every repository operation delivery performs"},
			{Name: "tmux", MinVersion: "3.0", VersionArgs: []string{"-V"}, Purpose: "the runtime provider worker sessions run under"},
			{Name: host.forgeCLI(), MinVersion: "2.40", VersionArgs: []string{"--version"}, Purpose: "pull requests, checks and merges"},
		},
		Paths: []unattended.PathRequirement{
			{Path: worktree, Kind: unattended.PathDir, Purpose: "the registered checkout this delivery is for"},
			{Path: host.ProjectDir(in.ProjectID), Kind: unattended.PathDir, Writable: true, Create: true, Purpose: "durable delivery state: record, plan, journal, projection"},
			{Path: stateDir, Kind: unattended.PathDir, Writable: true, Create: true, Purpose: "the run's journal, heartbeat and completion record"},
		},
		Env: []unattended.EnvRequirement{
			{Name: "HOME", Purpose: "resolves the agent runtime's configuration"},
			{Name: "PATH", Purpose: "resolves every declared tool"},
		},
		GitHub: &unattended.GitHubRequirement{
			Repo:             in.Repository.Slug,
			Command:          host.GitHubCommand,
			Branch:           in.Repository.DefaultBranch,
			NeedPush:         in.Policy.NeedPush,
			NeedPR:           in.Policy.NeedPR,
			NeedChecks:       in.Policy.NeedChecks,
			NeedMerge:        in.Policy.NeedMerge,
			MergeHumanAction: in.Policy.MergeHumanAction,
		},
	}

	work = unattended.Plan{RunID: runID}
	stage := func(id, title string, band unattended.Band, needs []string, mutates bool, timeout int, args ...string) unattended.Task {
		return unattended.Task{
			ID:             id,
			Title:          title,
			Band:           band,
			Argv:           append([]string{host.Driver}, args...),
			Needs:          needs,
			Mutates:        mutates,
			TimeoutSeconds: timeout,
			MaxAttempts:    1,
		}
	}

	// Every argument the driver receives comes from the host profile or from an
	// identifier this package has already validated. The forge CLI is here for
	// the same reason it is in the spec: where `gh` lives is machine-specific,
	// and a driver left to find it on PATH fails at the clone with an
	// authentication error that names nothing.
	project := []string{"-project", in.ProjectID, "-state", host.ProjectDir(in.ProjectID)}
	if cli := strings.TrimSpace(host.GitHubCommand); cli != "" {
		project = append(project, "-gh", cli)
	}

	work.Tasks = append(work.Tasks,
		stage(StageCityUp, "build the city, clone the working rig and declare the workers",
			unattended.BandPrimary, nil, false, 1800,
			append([]string{StageCityUp}, project...)...),
		stage(StageDispatch, "create the work beads, wire their dependencies and route them",
			unattended.BandPrimary, []string{StageCityUp}, false, 900,
			append([]string{StageDispatch}, project...)...),
	)

	awaitTimeout := in.Policy.WorkDeadlineSeconds
	if awaitTimeout <= 0 {
		awaitTimeout = 5400
	}
	work.Tasks = append(work.Tasks,
		stage(StageAwait, "wait for the workers to finish",
			unattended.BandPrimary, []string{StageDispatch}, false, awaitTimeout,
			append([]string{StageAwait}, project...)...),
	)

	// One publication task per package, in dependency order. Keeping them
	// separate is what lets an interrupted run resume at the package it was on
	// rather than replaying every merge that already landed.
	for _, wp := range orderPackages(plan) {
		needs := []string{StageAwait}
		for _, dep := range wp.DependsOn {
			needs = append(needs, StagePublish+"-"+dep)
		}
		work.Tasks = append(work.Tasks, unattended.Task{
			ID:             StagePublish + "-" + wp.ID,
			Title:          "publish " + wp.ID + ": " + wp.Title,
			Band:           unattended.BandPrimary,
			Argv:           append([]string{host.Driver, StagePublish, "-package", wp.ID}, project...),
			Needs:          needs,
			Mutates:        true,
			TimeoutSeconds: 3600,
			MaxAttempts:    1,
			DeliveryStatus: publishedStatus(in.Policy),
			CompletionGate: "required CI passed + independent assurance passed + merged through repository governance",
			Phase:          wp.Phase,
		})
	}

	publishNeeds := []string{StageProject}
	projectNeeds := []string{StageAwait}
	for _, wp := range plan.Packages {
		projectNeeds = append(projectNeeds, StagePublish+"-"+wp.ID)
	}

	work.Tasks = append(work.Tasks,
		stage(StageProject, "render the delivery projection from the run's evidence",
			unattended.BandEvidence, projectNeeds, false, 600,
			append([]string{StageProject}, project...)...),
		stage(StagePublishProjection, "publish the projection into the project's repository",
			unattended.BandEvidence, publishNeeds, true, 900,
			append([]string{StagePublishProjection}, project...)...),
	)

	if err := work.Validate(); err != nil {
		return spec, work, fmt.Errorf("compiling the delivery run: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return spec, work, fmt.Errorf("compiling the delivery run: %w", err)
	}
	return spec, work, nil
}

// publishedStatus is the projection status a successful publication
// establishes.
//
// It is the policy's honest ceiling, not an aspiration. A run forbidden to
// merge reaches an open pull request and no further, and saying `merged` there
// would score delivery for something that did not happen.
func publishedStatus(p Policy) string {
	if p.NeedMerge {
		return "merged"
	}
	return "pr-open"
}

func (h HostProfile) forgeCLI() string {
	if strings.TrimSpace(h.GitHubCommand) != "" {
		return h.GitHubCommand
	}
	return "gh"
}

// orderPackages returns the packages in dependency order.
//
// The plan has already been proved acyclic, so this is a plain topological
// sort with a deterministic tiebreak: two runs of the same plan must compile to
// the same task list, or a resumed run would not match its own journal.
func orderPackages(plan DeliveryPlan) []WorkPackage {
	remaining := make(map[string]WorkPackage, len(plan.Packages))
	var ids []string
	for _, wp := range plan.Packages {
		remaining[wp.ID] = wp
		ids = append(ids, wp.ID)
	}
	sortStrings(ids)

	done := map[string]bool{}
	out := make([]WorkPackage, 0, len(plan.Packages))
	for len(out) < len(plan.Packages) {
		progressed := false
		for _, id := range ids {
			if done[id] {
				continue
			}
			wp := remaining[id]
			ready := true
			for _, dep := range wp.DependsOn {
				if _, known := remaining[dep]; known && !done[dep] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			out = append(out, wp)
			done[id] = true
			progressed = true
		}
		if !progressed {
			// Unreachable: Validate proved the graph acyclic. Appending the
			// remainder keeps the compiler total rather than looping forever if
			// that ever stops being true.
			for _, id := range ids {
				if !done[id] {
					out = append(out, remaining[id])
					done[id] = true
				}
			}
		}
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// WriteRunFiles renders the compiled spec and plan into the project's delivery
// directory, where the driver and a person can both read them.
func WriteRunFiles(host HostProfile, spec unattended.Spec, work unattended.Plan) (specPath, planPath string, err error) {
	dir := host.ProjectDir(spec.ProjectID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("creating delivery directory: %w", err)
	}

	specPath = filepath.Join(dir, "run-spec.toml")
	data, err := spec.Encode()
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(specPath, data, 0o644); err != nil { //nolint:gosec // run evidence
		return "", "", fmt.Errorf("writing %q: %w", specPath, err)
	}

	planPath = filepath.Join(dir, "run-plan.toml")
	encoded, err := encodeWorkPlan(work)
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(planPath, encoded, 0o644); err != nil { //nolint:gosec // run evidence
		return "", "", fmt.Errorf("writing %q: %w", planPath, err)
	}
	return specPath, planPath, nil
}

// encodeWorkPlan renders the compiled work plan as the TOML the unattended
// layer loads.
//
// The plan is round-tripped rather than handed over in memory because an
// interrupted run is restarted as a new process, and the plan it resumes must
// be the one on disk — the same bytes a person can read to see what the run
// believed it was doing.
func encodeWorkPlan(work unattended.Plan) ([]byte, error) {
	var buf strings.Builder
	if err := toml.NewEncoder(&buf).Encode(work); err != nil {
		return nil, fmt.Errorf("encoding the delivery work plan: %w", err)
	}
	return []byte(buf.String()), nil
}
