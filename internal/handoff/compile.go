package handoff

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	// GasCityCommand is the Gas City CLI the driver builds the city with. It is
	// declared for the same reason the forge CLI and the planner are: a run
	// detached into its own process group does not inherit an interactive
	// shell's PATH, and a driver left to find `gc` there fails at the very
	// first stage with "command not found".
	GasCityCommand string
	// BeadsCommand is the beads CLI the city's bead stores are made with.
	//
	// The driver never invokes it. Gas City does, by PATH lookup, from a script
	// it shells out to — so unlike the others this one is declared not to be
	// executed but to be findable, and the driver puts it where that lookup
	// will find it.
	BeadsCommand string
	// Provider is the agent runtime workers are started under.
	Provider string
	// ProviderCommand is where that runtime's binary actually is.
	//
	// Gas City resolves a provider by name on PATH, and refuses to finish
	// building a city whose provider it cannot resolve — which leaves the city
	// half-made: created, but with its pack imports never installed, so every
	// later command fails on a missing packs.lock. Declared here, exposed by
	// name, for the same reason as the two above.
	ProviderCommand string
	// ProjectToolPath is where the PROJECT's own toolchain lives on this host:
	// the directories prepended to PATH when the controller runs a project's own
	// commands — its declared gates, and the dependency install that precedes
	// them. Empty means the run's inherited PATH is used unchanged.
	//
	// This is the engine's own doctrine applied to the last thing still relying
	// on PATH. A run detached into its own process group inherits no interactive
	// shell's PATH, and on a host where the engine runs under WSL beside a
	// Windows install the inherited one resolves `npm` to the WINDOWS npm — which
	// then operates on a Linux worktree through a \\wsl.localhost\... UNC path —
	// while `node` does not resolve at all. The controller's re-run of a
	// package's declared gates is the evidence publication rests on, so it must
	// be run by the toolchain the project is actually built with, and which one
	// that is is a fact about this machine.
	ProjectToolPath []string
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

// ProjectionPath is where the run publisher renders RUN progress, refreshed
// alongside the heartbeat so a dashboard sees movement during a long run.
//
// Its rows are keyed by run task id — `publish-wp-add`, not `wp-add` — and it
// carries no per-package completion gate, because the run layer does not
// adjudicate one. It is a view of the queue, not of the delivery.
func (h HostProfile) ProjectionPath(projectID string) string {
	return filepath.Join(h.StateDir(projectID), "PROJECT-STATE.yml")
}

// DeliveryProjectionPath is the delivery projection: the document the driver's
// project stage renders from the forge and the run's control ledger, and the
// one its publish-projection stage commits into the project's repository.
//
// It is keyed by PACKAGE id and carries each package's completion gate, which is
// what makes it the document an acceptance assessment can read. Assess only ever
// understood this shape, and was being handed the run-progress document instead:
// no row could match by construction, so a delivery that had merged all four
// packages with every gate met reported every package outstanding. A mismatch
// that reads as "nothing is done" rather than as an error is the worst kind, so
// the two documents are named apart here rather than distinguished by comment.
func (h HostProfile) DeliveryProjectionPath(projectID string) string {
	return filepath.Join(h.ProjectDir(projectID), "PROJECT-STATE.yml")
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
	// StageAwait waits for one package's agent to finish its work. It is
	// compiled per package as `await-<id>`, because a single plan-wide wait
	// cannot be satisfied by a plan whose packages depend on each other.
	StageAwait = "await"
	// StagePublish is per package: gate, commit, push, PR, checks, merge.
	StagePublish = "publish"
	// StageVerify is what a verification package gets INSTEAD of await and
	// publish: it cuts a clean tree at the named merged commit, runs the
	// declared gates against it and requires the artifact to be really there.
	//
	// One stage rather than two because there is no worker to wait for and
	// nothing to publish. Naming it separately is also what makes the record
	// legible — a reader of the run plan can see which packages changed the
	// repository and which only checked it.
	StageVerify = "verify"
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
			// No minimum version: the run is built against whichever gc this
			// host has, and a version comparison here would refuse a delivery
			// over a banner rather than over a capability. Presence is the
			// property that was missing — a preflight that reported READY while
			// the binary the first stage runs was nowhere on PATH.
			{Name: host.gasCityCLI(), Purpose: "building the city, cloning the rig and every bead operation delivery performs"},
			{Name: host.beadsCLI(), Purpose: "the bead store Gas City creates in the rig; Gas City resolves it by PATH lookup"},
			{Name: host.providerCLI(), Purpose: "the agent runtime the city's provider readiness and every worker session resolve by name"},
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
		// A run's own tooling knows what its stderr means, and this is the one
		// sentence the builtin rules would read wrongly. "The supervisor cannot
		// be asked to reconcile" is not a code defect to retry and not an
		// environment fault to shrug at: restarting a machine-wide process that
		// other work may depend on is a judgement, and this run is not entitled
		// to make it.
		Classification: []unattended.ClassificationRule{{
			Pattern: `supervisor cannot be asked to reconcile`,
			Class:   unattended.FailureHumanDecision,
			Reason:  "the machine-wide supervisor is not answering, and restarting a shared process is its owner's decision",
		}},
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
		Boundaries: acceptanceBoundaries(in),
	}

	// A declared toolchain directory that is not there is the same class of fact
	// as a declared binary that is not there, and preflight is where both are
	// caught — before a run starts rather than by the stage that needed it. It
	// is never created: a toolchain preflight conjured into existence would be
	// an empty directory, which is a worse answer than saying it is absent.
	for _, dir := range host.ProjectToolPath {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		spec.Paths = append(spec.Paths, unattended.PathRequirement{
			Path:    dir,
			Kind:    unattended.PathDir,
			Purpose: "the project's own toolchain, which the controller runs a package's declared gates with",
		})
	}

	// Risk classification — the packet's half of mandatory-gate selection.
	//
	// This packet authors no code and compiles nothing. Every line of code a
	// managed delivery produces is written inside a WORKER's own packet, and is
	// certified there: by the gates that package declared, re-run independently
	// by the controller before it publishes, by required CI on the exact
	// pull-request head, and by a governed merge — the three controls the run's
	// own ledger records and the projection's completion gate is derived from.
	// So no code-certifying gate applies to this packet, and Q0 requires none.
	//
	// Q0 rather than Q2 is a deliberate reading of what this packet IS, not a
	// way around Q2's gates. The compiled run's every task invokes the declared
	// driver and nothing else — TestCompiledArgvComesOnlyFromTheHostProfile
	// holds that line — so it structurally cannot run a build, a test suite or
	// a linter of its own to satisfy them. Declaring Q2 here would not make the
	// delivery safer; it would make `Begin` refuse every delivery for want of
	// evidence the packet is forbidden from producing, which is the same
	// precedent guk-bpm-publication.plan.toml settled: classify by what the
	// packet authors, not by what its product touches.
	//
	// Changing what this packet does is a change to this classification first.
	work = unattended.Plan{RunID: runID, Risk: unattended.RiskQ0}
	stage := func(id, title string, band unattended.Band, needs []string, mutates bool, timeout int, args ...string) unattended.Task {
		return unattended.Task{
			ID:             id,
			Title:          title,
			Band:           band,
			Argv:           append([]string{host.Driver}, args...),
			Needs:          needs,
			Mutates:        mutates,
			TimeoutSeconds: timeout,
			// EVERY STAGE IS SUPERVISED. Declaring where a stage states its
			// outcome is what makes that statement the verdict and takes the
			// residual exit status out of the decision — and the driver produces
			// both directions of that residue. `gc init` exits non-zero for a
			// condition that is correct on any host that already has a
			// supervisor; a stage that waits out its deadline over unfinished
			// work is not a stage that failed.
			ResultPath: driverResultPath(id),
			// No MaxAttempts override. It used to be one, which capped every
			// stage at a single attempt whatever went wrong — so a rate limit
			// from the forge ended a delivery that the external-service policy
			// would have carried through five attempts and a long backoff. The
			// override was the right caution while a stage's failure class came
			// from guessing at its output; now the stage NAMES its terminal
			// reason, the class is its own declaration, and the class policy is
			// what should govern. Every stage is idempotent by design, which is
			// what makes a second attempt safe to allow.
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
	if cli := strings.TrimSpace(host.GasCityCommand); cli != "" {
		project = append(project, "-gc", cli)
	}
	if cli := strings.TrimSpace(host.BeadsCommand); cli != "" {
		project = append(project, "-bd", cli)
	}
	// The provider is named as well as located: the name is what Gas City looks
	// up, so it is also the name the located binary has to be exposed under. A
	// driver that hard-coded one of them would build a city under a runtime the
	// host profile did not declare.
	project = append(project, "-provider", host.Provider)
	if cli := strings.TrimSpace(host.ProviderCommand); cli != "" {
		project = append(project, "-provider-bin", cli)
	}
	// The project's own toolchain, declared for the same reason and carried by
	// the same route. The driver prepends these to PATH for a project's own
	// commands only; the engine's binaries stay exactly as declared above.
	if dirs := host.projectToolPath(); dirs != "" {
		project = append(project, "-project-path", dirs)
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
	waitBudget := strconv.Itoa(awaitTimeout)

	// WAITING IS PER PACKAGE, AND SO IS PUBLICATION.
	//
	// One `await` for the whole plan deadlocks any plan whose packages depend on
	// each other, which is every real plan. The dependency the driver wires runs
	// from an upstream's MERGE bead — a package waits for repository state, not
	// for a sibling worker's filesystem — and that merge bead is closed by the
	// controller inside `publish`. So a single await waited for work beads that
	// could not open until a publication that could not start until the await
	// finished. The first pilot never saw it because it had one package; the
	// second sat for its full deadline with three workers that were never
	// eligible to run.
	//
	// Per package, the same graph runs in the order it describes:
	//
	//	await-A → publish-A (closes A's merge bead) → await-B → publish-B → …
	//
	// A package with no upstreams waits only on dispatch, so genuinely
	// independent packages stay free to proceed as soon as they are ready. No
	// new scheduler: this is the existing queue over the existing bead graph.
	for _, wp := range orderPackages(plan) {
		// A verification package has no worker to wait for and nothing to
		// publish, so it compiles to ONE task rather than two. It still waits on
		// dispatch, because dispatch is where the run's own facts are settled.
		if wp.IsVerification() {
			verifyTask := stage(StageVerify+"-"+wp.ID, "verify "+wp.ID+": "+wp.Title,
				unattended.BandPrimary, []string{StageDispatch}, false,
				awaitTimeout+driverResultMargin,
				append([]string{StageVerify, "-package", wp.ID, "-deadline", waitBudget}, project...)...)
			verifyTask.MaxResumes = driverMaxResumes
			// `verified`, never `merged`. The completion assessment accepts both,
			// and the difference is the whole record: this package reconciled a
			// criterion by checking evidence that was already there, and a reader
			// who cannot tell that from a merge has been told the wrong thing.
			verifyTask.DeliveryStatus = "verified"
			verifyTask.CompletionGate = "declared verification gates passed against the authoritative merged commit + required evidence present at that commit"
			verifyTask.Phase = wp.Phase
			work.Tasks = append(work.Tasks, verifyTask)
			continue
		}

		upstream := make([]string, 0, len(wp.DependsOn))
		for _, dep := range wp.DependsOn {
			upstream = append(upstream, StagePublish+"-"+dep)
		}

		awaitID := StageAwait + "-" + wp.ID
		awaitNeeds := append([]string{StageDispatch}, upstream...)
		publishNeeds := append([]string{awaitID}, upstream...)

		awaitTask := stage(awaitID, "wait for "+wp.ID+": "+wp.Title,
			unattended.BandPrimary, awaitNeeds, false, awaitTimeout+driverResultMargin,
			append([]string{StageAwait, "-package", wp.ID, "-deadline", waitBudget}, project...)...)
		awaitTask.MaxResumes = driverMaxResumes

		publishTask := stage(StagePublish+"-"+wp.ID, "publish "+wp.ID+": "+wp.Title,
			unattended.BandPrimary, publishNeeds, true,
			awaitTimeout+publishCIBudget+driverResultMargin,
			append([]string{StagePublish, "-package", wp.ID, "-deadline", waitBudget}, project...)...)
		publishTask.MaxResumes = driverMaxResumes
		publishTask.DeliveryStatus = publishedStatus(in.Policy)
		publishTask.CompletionGate = "required CI passed + independent assurance passed + merged through repository governance"
		publishTask.Phase = wp.Phase

		work.Tasks = append(work.Tasks, awaitTask, publishTask)
	}

	publishProjectionNeeds := []string{StageProject}
	// Every package the delivery now has, including the corrective work any
	// remediation added. The projection is what the completion assessment reads
	// back, so a remedial package missing from these dependencies would let the
	// document be rendered before the work that repairs the criterion had run.
	all := plan.AllPackages()
	projectNeeds := make([]string, 0, len(all))
	for _, wp := range all {
		// Each package by the stage that actually finishes it. A verification
		// package named here as `publish-<id>` would be a dependency on a task
		// the run does not contain, and the projection would be rendered before
		// the check that repairs the criterion had run.
		if wp.IsVerification() {
			projectNeeds = append(projectNeeds, StageVerify+"-"+wp.ID)
			continue
		}
		projectNeeds = append(projectNeeds, StagePublish+"-"+wp.ID)
	}

	work.Tasks = append(work.Tasks,
		stage(StageProject, "render the delivery projection from the run's evidence",
			unattended.BandEvidence, projectNeeds, false, 600,
			append([]string{StageProject}, project...)...),
		stage(StagePublishProjection, "publish the projection into the project's repository",
			unattended.BandEvidence, publishProjectionNeeds, true, 900,
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

// acceptanceBoundaryPrefix namespaces the check id a reserved criterion is
// published under, so it can never collide with a probe's check.
const acceptanceBoundaryPrefix = "acceptance."

// acceptanceBoundaries declares every criterion the intent reserved to a
// person as a boundary the run knows about before it starts.
//
// No work package may claim one — the plan validator refuses that — so without
// this declaration a reserved criterion would be invisible everywhere the run
// is inspected before its first projection: absent from the plan because it is
// not delivery's to do, and absent from the preflight report because no probe
// can ask a person whether they will accept. Declaring it makes the omission
// from the plan legible as a stated boundary rather than as a gap.
func acceptanceBoundaries(in Intent) []unattended.KnownBoundary {
	var out []unattended.KnownBoundary
	for _, c := range in.Acceptance {
		if !c.IsHuman() {
			continue
		}
		out = append(out, unattended.KnownBoundary{
			ID:     acceptanceBoundaryPrefix + c.ID,
			Title:  "acceptance criterion " + c.ID + " is a person's to answer",
			Detail: c.Statement,
			Action: "a person accepts " + c.ID + " — delivery may prepare what they read and may never claim their answer",
		})
	}
	return out
}

// The bounds a compiled delivery stage runs under.
const (
	// driverResultMargin is how much longer a stage's task timeout is than the
	// deadline the stage itself is given to wait for work.
	//
	// The run KILLS a task that exceeds its timeout, and a killed stage states
	// nothing — which is an absence of knowledge, and fails safe. So a stage
	// whose own deadline expired at the same moment its task timed out could
	// never say "the work is unfinished, not failed": it would be killed a
	// moment before saying so, and an interruption would reach the run as
	// silence. This margin is the stage's room to speak.
	driverResultMargin = 300

	// publishCIBudget is the time a publication may spend waiting for required
	// CI on its exact head, on top of any wait for the work itself. It matches
	// the driver's own DELIVERY_CI_DEADLINE default, because a task timeout that
	// did not cover the wait the stage is about to perform is a stage killed
	// part-way through publishing.
	publishCIBudget = 2700

	// driverMaxResumes bounds how many times a stage that reports unfinished
	// work is re-offered.
	//
	// One. The deadline a stage waits under is the policy's declared budget for
	// the work, and multiplying it silently would spend a night on a decision
	// nobody took. A single further pass is worth having because the stage
	// re-derives on the way back in which packages have no worker and routes
	// them again; beyond that the task is HELD, which puts an unfinished
	// delivery in front of a person instead of failing it as though something
	// had been proved wrong.
	driverMaxResumes = 1
)

// driverResultPath is where a stage states what happened to it, relative to the
// run's state directory.
//
// One file per task, named for the task: two stages sharing a path would let
// one stage's statement be adjudicated as another's, and the run clears the
// path before each attempt so a stale statement can never survive into the next
// one.
func driverResultPath(taskID string) string {
	return "results/" + taskID + ".json"
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

// gasCityCLI is the Gas City command this run will use, declared or defaulted.
//
// The default is the bare name, which is what the driver falls back to. It is a
// real answer on a host that has gc on PATH, and naming it here means preflight
// reports its absence instead of leaving city-up to discover it.
func (h HostProfile) gasCityCLI() string {
	if strings.TrimSpace(h.GasCityCommand) != "" {
		return h.GasCityCommand
	}
	return "gc"
}

// beadsCLI is the beads command this run will make findable, declared or
// defaulted, on the same terms as the two above.
func (h HostProfile) beadsCLI() string {
	if strings.TrimSpace(h.BeadsCommand) != "" {
		return h.BeadsCommand
	}
	return "bd"
}

// providerCLI is the agent runtime binary, declared or defaulted to the
// provider's own name — which is what Gas City looks up when nothing says
// otherwise.
func (h HostProfile) providerCLI() string {
	if strings.TrimSpace(h.ProviderCommand) != "" {
		return h.ProviderCommand
	}
	return h.Provider
}

// projectToolPath is the declared project toolchain as one PATH fragment, or
// empty when the host declared none.
//
// Unlike the four above there is no default, and there must not be: a guess at
// where a project's toolchain lives is exactly the guess that put the Windows
// npm in front of a Linux worktree. A host that declares nothing gets the run's
// inherited PATH, which is what it had before this existed.
func (h HostProfile) projectToolPath() string {
	dirs := make([]string, 0, len(h.ProjectToolPath))
	for _, dir := range h.ProjectToolPath {
		if dir = strings.TrimSpace(dir); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return strings.Join(dirs, ":")
}

// orderPackages returns the packages in dependency order.
//
// The plan has already been proved acyclic, so this is a plain topological
// sort with a deterministic tiebreak: two runs of the same plan must compile to
// the same task list, or a resumed run would not match its own journal.
func orderPackages(plan DeliveryPlan) []WorkPackage {
	packages := plan.AllPackages()
	remaining := make(map[string]WorkPackage, len(packages))
	var ids []string
	for _, wp := range packages {
		remaining[wp.ID] = wp
		ids = append(ids, wp.ID)
	}
	sortStrings(ids)

	done := map[string]bool{}
	out := make([]WorkPackage, 0, len(packages))
	for len(out) < len(packages) {
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
