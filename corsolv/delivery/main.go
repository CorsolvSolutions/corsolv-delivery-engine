// Command delivery is the entry point a management portal starts managed
// delivery through.
//
// It is the only surface the portal touches, and it accepts exactly one thing:
// a delivery intent. Identity, repository, registered checkout, business
// objective, lifecycle, acceptance and the delivery authorities granted. No
// commands, no paths into this machine's internals, no run mechanics. What
// happens as a result — which city gets built, which worktree a worker is given,
// when a branch is cut — is this engine's decision and is made from the host
// profile, which the portal never sees.
//
// Usage:
//
//	delivery preflight -intent <file>     could delivery start? starts nothing
//	delivery start     -intent <file>     admit, plan, compile and begin
//	delivery run       -project <id>      execute the compiled run in the foreground
//	delivery status    -project <id>      the canonical delivery state
//
// Exit codes are the interface for the portal and for a supervising script:
//
//	0  done, ready, or accepted
//	3  a human boundary remains — delivery did everything it could
//	4  refused, or stopped on something it could not work around
//	5  the invocation itself was wrong
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gastownhall/gascity/internal/handoff"
	"github.com/gastownhall/gascity/internal/unattended"
)

const (
	exitOK            = 0
	exitHumanBoundary = 3
	exitRefused       = 4
	exitUsage         = 5
)

func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) < 2 {
		usage()
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "preflight":
		return cmdPreflight(ctx, os.Args[2:])
	case "start":
		return cmdStart(ctx, os.Args[2:])
	case "run":
		return cmdRun(ctx, os.Args[2:])
	case "plan":
		return cmdPlan(os.Args[2:])
	case "status":
		return cmdStatus(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "delivery: unknown subcommand %q\n\n", os.Args[1])
		usage()
		return exitUsage
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `delivery — start and observe managed delivery for a project

  preflight -intent <file|-> [-host <file>]
      Answer whether managed delivery could start for this intent. Starts nothing.

  start -intent <file|-> [-host <file>] [-foreground]
      Admit the intent, plan the work, compile the run and begin it. Idempotent:
      starting an already-started delivery reports its state and starts nothing.

  run -project <id> [-host <file>]
      Execute the compiled run in the foreground. This is what start detaches.

  plan -project <id> [-from <file>] [-host <file>]
      Print the delivery's plan, or install one written by hand with -from.
      An installed plan faces the same validator an agent's would.

  status -project <id> [-host <file>]
      Print the canonical delivery state as JSON.

Exit: 0 ready/accepted/complete  3 human boundary remains  4 refused/stopped  5 usage
`)
}

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "delivery: "+format+"\n", args...)
	return exitUsage
}

func refuse(err error) int {
	fmt.Fprintf(os.Stderr, "REFUSED\n\n%v\n", err)
	return exitRefused
}

// readIntent loads an intent from a file, or from stdin when the path is "-".
func readIntent(path string) (handoff.Intent, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path) //nolint:gosec // an operator-supplied path
	}
	if err != nil {
		return handoff.Intent{}, fmt.Errorf("reading the delivery intent: %w", err)
	}
	return handoff.DecodeIntent(data)
}

// --- preflight --------------------------------------------------------------

func cmdPreflight(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	intentPath := fs.String("intent", "", "path to the delivery intent JSON, or - for stdin")
	hostPath := fs.String("host", defaultHostPath(), "path to the delivery host profile")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *intentPath == "" {
		return fail("preflight needs -intent")
	}

	in, err := readIntent(*intentPath)
	if err != nil {
		return refuse(err)
	}
	host, _, err := loadHost(*hostPath)
	if err != nil {
		return refuse(err)
	}

	// Safety is proved against the intent alone, before any plan exists: the
	// questions that must fail closed — is this repository registered here, does
	// the checkout exist, does its origin match, does someone else hold it —
	// are all answerable without knowing what the work is.
	report, err := preflightDelivery(ctx, in, host)
	if err != nil {
		return refuse(err)
	}

	if *asJSON {
		data, jerr := json.MarshalIndent(report, "", "  ")
		if jerr != nil {
			return fail("encoding the report: %v", jerr)
		}
		fmt.Println(string(data))
	} else {
		fmt.Print(report.String())
	}

	readiness := deliveryReadiness(report)
	if !*asJSON {
		fmt.Printf("\nMANAGED DELIVERY: %s\n", readiness)
		fmt.Println("The work plan is not adjudicated here — planning is the first thing a run does.")
	}

	switch readiness {
	case unattended.Ready:
		return exitOK
	case unattended.ReadyWithKnownHumanBoundary:
		return exitHumanBoundary
	default:
		return exitRefused
	}
}

// planCheckPrefix identifies the run-layer checks that adjudicate a work queue.
//
// Managed delivery has none at preflight, and cannot: the work is produced by a
// planning agent as the FIRST thing a run does, so a plan exists only after
// Start. The run layer reports those checks as not-reached, which is honest but
// would make every delivery preflight say NOT-READY for the one reason that is
// never a reason to refuse.
const planCheckPrefix = "plan."

// deliveryReadiness reduces a preflight report to a verdict about whether
// managed delivery may begin.
//
// It is the run layer's own reduction over every check EXCEPT the plan ones.
// Excluding them is a statement about when planning happens, not a relaxation
// of a gate: nothing about ownership, the forge, the tools or the durable state
// is skipped, and those are what "is it safe to start" actually means here.
func deliveryReadiness(report *unattended.Report) unattended.Readiness {
	var answerable unattended.Checks
	for _, c := range report.Checks {
		if strings.HasPrefix(c.ID, planCheckPrefix) {
			continue
		}
		answerable = append(answerable, c)
	}
	return answerable.Readiness()
}

// preflightDelivery proves the ground before anything is created.
//
// It compiles a spec from the intent with a placeholder plan, because the
// ownership, tool and forge checks do not depend on what the work turns out to
// be — and a person asking "could this start tonight" must not have to wait for
// a planning agent to answer.
func preflightDelivery(ctx context.Context, in handoff.Intent, host handoff.HostProfile) (*unattended.Report, error) {
	placeholder := handoff.DeliveryPlan{
		SchemaVersion: handoff.PlanSchemaVersion,
		ProjectID:     in.ProjectID,
		PlannedBy:     "preflight",
		Packages:      preflightPackages(in),
	}
	spec, _, err := handoff.Compile(in, placeholder, host, "preflight")
	if err != nil {
		return nil, err
	}
	if host.GitHubCommand != "" {
		unattended.GitHubCommand = host.GitHubCommand
	}
	return unattended.Preflight(ctx, spec, nil), nil
}

// preflightPackages is a minimal plan shaped only to satisfy the compiler.
//
// It never runs. One package per acceptance criterion is the smallest shape
// that passes plan validation, and using the criteria rather than an invented
// package keeps the preflight spec's phase list honest.
func preflightPackages(in handoff.Intent) []handoff.WorkPackage {
	phase := "preflight"
	if len(in.Lifecycle) > 0 {
		phase = in.Lifecycle[0]
	}
	out := make([]handoff.WorkPackage, 0, len(in.Acceptance))
	for _, c := range in.Acceptance {
		out = append(out, handoff.WorkPackage{
			ID:              "wp-" + c.ID,
			Title:           "placeholder for " + c.ID,
			Phase:           phase,
			Objective:       "Placeholder used only to preflight the ground; never executed.",
			Artifact:        "preflight/" + c.ID + ".placeholder",
			AuthorizedPaths: []string{"preflight/" + c.ID + ".placeholder"},
			Satisfies:       []string{c.ID},
		})
	}
	return out
}

// --- start ------------------------------------------------------------------

func cmdStart(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	intentPath := fs.String("intent", "", "path to the delivery intent JSON, or - for stdin")
	hostPath := fs.String("host", defaultHostPath(), "path to the delivery host profile")
	foreground := fs.Bool("foreground", false, "execute the run here instead of detaching it")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *intentPath == "" {
		return fail("start needs -intent")
	}

	in, err := readIntent(*intentPath)
	if err != nil {
		return refuse(err)
	}
	host, planner, err := loadHost(*hostPath)
	if err != nil {
		return refuse(err)
	}

	// Idempotency first, before any side effect. A portal button gets pressed
	// twice, a browser resubmits, and a user who sees nothing happen clicks
	// again; none of those may produce a second delivery.
	adm, err := handoff.Admit(host.DeliveryRoot, in, time.Now())
	if err != nil {
		return refuse(err)
	}

	if adm.AlreadyStarted {
		status, serr := observe(host, in.ProjectID)
		if serr != nil {
			return refuse(serr)
		}
		printStatus(status)
		return exitOK
	}

	// The ground is proved before anything durable is written, so a refused
	// start leaves no record claiming a delivery exists.
	report, err := preflightDelivery(ctx, in, host)
	if err != nil {
		return refuse(err)
	}
	if deliveryReadiness(report) == unattended.NotReady {
		fmt.Fprint(os.Stderr, report.String())
		return refuse(fmt.Errorf("managed delivery cannot start for %q: the ground is not ready", in.ProjectID))
	}

	if err := handoff.SaveRecord(host.DeliveryRoot, adm.Record); err != nil {
		return refuse(err)
	}

	if *foreground {
		return executeRun(ctx, host, planner, in.ProjectID)
	}

	if err := detachRun(host, in.ProjectID); err != nil {
		return refuse(err)
	}
	status, err := observe(host, in.ProjectID)
	if err != nil {
		return refuse(err)
	}
	printStatus(status)
	return exitOK
}

// detachRun starts the run as an independent process.
//
// Managed delivery must outlive the request that asked for it. A portal request
// that held the connection for the hours a real delivery takes would time out,
// and the run would die with it — which is the failure the whole durable-state
// design exists to prevent, reintroduced at the front door.
func detachRun(host handoff.HostProfile, projectID string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating this executable to detach the run: %w", err)
	}
	logPath := filepath.Join(host.ProjectDir(projectID), "run.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("creating the delivery directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) //nolint:gosec // run evidence
	if err != nil {
		return fmt.Errorf("opening the run log: %w", err)
	}
	defer logFile.Close() //nolint:errcheck // the child holds its own descriptor

	return spawnDetached(self, []string{"run", "-project", projectID}, logFile)
}

// --- run --------------------------------------------------------------------

func cmdRun(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	projectID := fs.String("project", "", "the project whose delivery to execute")
	hostPath := fs.String("host", defaultHostPath(), "path to the delivery host profile")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *projectID == "" {
		return fail("run needs -project")
	}
	if err := handoff.SanitizeProjectID(*projectID); err != nil {
		return refuse(err)
	}
	host, planner, err := loadHost(*hostPath)
	if err != nil {
		return refuse(err)
	}
	return executeRun(ctx, host, planner, *projectID)
}

// executeRun plans if necessary, compiles, and drains the work queue.
//
// Every step is idempotent against what is already on disk, because this is the
// function an interrupted delivery re-enters. Planning is skipped when a plan
// exists, compilation is deterministic so a resumed run recognizes its own
// journal, and the queue itself resumes from that journal rather than replaying
// work that already landed.
func executeRun(ctx context.Context, host handoff.HostProfile, planner handoff.Planner, projectID string) int {
	record, found, err := handoff.LoadRecord(host.DeliveryRoot, projectID)
	if err != nil {
		return refuse(err)
	}
	if !found {
		return refuse(fmt.Errorf("no managed delivery has been started for %q", projectID))
	}
	in := record.Intent

	plan, planned, err := handoff.EnsurePlan(ctx, host.DeliveryRoot, in, planner)
	if err != nil {
		return refuse(err)
	}
	if planned {
		fmt.Fprintf(os.Stderr, "planned %d work package(s) for %s\n", len(plan.Packages), projectID)
	}

	runID := fmt.Sprintf("%s-%s", projectID, time.Now().UTC().Format("20060102T150405Z"))
	reason := handoff.ReasonInitial
	if len(record.Runs) > 0 {
		reason = handoff.ReasonResumed
	}

	spec, work, err := handoff.Compile(in, plan, host, runID)
	if err != nil {
		return refuse(err)
	}
	if _, _, err := handoff.WriteRunFiles(host, spec, work); err != nil {
		return refuse(err)
	}
	// The driver is a separate program and reads the two validated documents
	// directly. The intent comes from the RECORD, so what executes is provably
	// what was admitted rather than a later request that was refused.
	if err := handoff.SaveIntent(host.DeliveryRoot, in); err != nil {
		return refuse(err)
	}
	if host.GitHubCommand != "" {
		unattended.GitHubCommand = host.GitHubCommand
	}

	record = record.AppendRun(runID, reason, time.Now())
	record.StateDir = host.StateDir(projectID)
	record.Worktree = spec.Ownership.Worktree
	if err := handoff.SaveRecord(host.DeliveryRoot, record); err != nil {
		return refuse(err)
	}

	session, err := unattended.Begin(ctx, spec, work)
	if err != nil {
		switch {
		case errors.Is(err, unattended.ErrNotReady):
			return refuse(fmt.Errorf("delivery did not start: %w", err))
		case errors.Is(err, unattended.ErrWriterHeld), errors.Is(err, unattended.ErrForeignWriter):
			// Not an error condition. Another run already owns this delivery,
			// which is exactly what the lock is for.
			fmt.Fprintf(os.Stderr, "delivery for %q is already running: %v\n", projectID, err)
			return exitOK
		default:
			return refuse(err)
		}
	}
	defer session.Close() //nolint:errcheck

	if session.Resumed {
		fmt.Fprintf(os.Stderr, "resuming the existing journal in %s\n", spec.StateDir)
	}
	session.Runner.OnProgress = func(p unattended.Progress) {
		fmt.Fprintf(os.Stderr, "[%s] %s %s next=%s\n", p.Elapsed, p.Stage, p.CurrentTask, p.NextAction)
	}

	event, runErr := session.Runner.Run(ctx)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "delivery: %v\n", runErr)
	}
	if _, perr := unattended.PublishDelivery(spec, session.Queue, session.Fence, time.Now()); perr != nil {
		fmt.Fprintf(os.Stderr, "delivery: publishing the run projection: %v\n", perr)
	}

	fmt.Fprintf(os.Stderr, "\n%s\n", event.String())

	// The run's own outcome is reported, but it does not decide the delivery's
	// state. That is the evidence assessment's job, and status is where a
	// caller reads it.
	switch event.Outcome {
	case unattended.RunCompleted:
		return exitOK
	case unattended.RunBlockedHuman, unattended.RunAwaitingAuth:
		return exitHumanBoundary
	default:
		return exitRefused
	}
}

// --- plan -------------------------------------------------------------------

// cmdPlan shows a delivery's plan, or installs one a person wrote.
//
// Planning is normally an agent's job, and this does not change that. What it
// changes is who may be the author when a person already knows the answer — or
// when the agent runtime is unavailable, which on a real machine is a Tuesday
// rather than a hypothetical. An installed plan goes through exactly the same
// validator an agent's would: the containment rules are not a property of who
// wrote the plan.
//
// It refuses to replace an existing plan. A delivery part-way through has
// merged work against the plan it started with, and swapping in a new one would
// leave the run reconciling two.
func cmdPlan(args []string) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	projectID := fs.String("project", "", "the project whose plan to show or install")
	from := fs.String("from", "", "install the plan in this JSON file")
	hostPath := fs.String("host", defaultHostPath(), "path to the delivery host profile")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *projectID == "" {
		return fail("plan needs -project")
	}
	if err := handoff.SanitizeProjectID(*projectID); err != nil {
		return refuse(err)
	}
	host, _, err := loadHost(*hostPath)
	if err != nil {
		return refuse(err)
	}

	record, found, err := handoff.LoadRecord(host.DeliveryRoot, *projectID)
	if err != nil {
		return refuse(err)
	}
	if !found {
		return refuse(fmt.Errorf("no managed delivery has been started for %q", *projectID))
	}

	existing, hasPlan, err := handoff.LoadPlan(host.DeliveryRoot, record.Intent)
	if err != nil {
		return refuse(err)
	}

	if *from == "" {
		if !hasPlan {
			fmt.Fprintf(os.Stderr, "delivery for %q has no plan yet\n", *projectID)
			return exitHumanBoundary
		}
		data, merr := handoff.MarshalPlan(existing)
		if merr != nil {
			return refuse(merr)
		}
		fmt.Println(string(data))
		return exitOK
	}

	if hasPlan {
		return refuse(fmt.Errorf(
			"delivery for %q already has a plan of %d work package(s) — a delivery part-way through has "+
				"merged work against the plan it started with, so it is not replaced",
			*projectID, len(existing.Packages)))
	}

	raw, err := os.ReadFile(*from) //nolint:gosec // an operator-supplied path
	if err != nil {
		return refuse(fmt.Errorf("reading the plan: %w", err))
	}
	plan, err := handoff.StaticPlanner{Raw: raw}.Plan(context.Background(), record.Intent)
	if err != nil {
		return refuse(err)
	}
	if err := handoff.SavePlan(host.DeliveryRoot, plan); err != nil {
		return refuse(err)
	}
	fmt.Fprintf(os.Stderr, "installed a plan of %d work package(s) for %s\n", len(plan.Packages), *projectID)
	return exitOK
}

// --- status -----------------------------------------------------------------

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	projectID := fs.String("project", "", "the project to report on")
	hostPath := fs.String("host", defaultHostPath(), "path to the delivery host profile")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *projectID == "" {
		return fail("status needs -project")
	}
	if err := handoff.SanitizeProjectID(*projectID); err != nil {
		return refuse(err)
	}
	host, _, err := loadHost(*hostPath)
	if err != nil {
		return refuse(err)
	}

	status, err := observe(host, *projectID)
	if err != nil {
		return refuse(err)
	}
	printStatus(status)

	switch status.State {
	case handoff.StateCompleted:
		return exitOK
	case handoff.StateBlocked:
		return exitHumanBoundary
	case handoff.StateFailed:
		return exitRefused
	default:
		return exitOK
	}
}

// observe derives the delivery's canonical state from the authorities that own
// it: the record, the plan, the run's own published files and the projection.
func observe(host handoff.HostProfile, projectID string) (handoff.Status, error) {
	record, found, err := handoff.LoadRecord(host.DeliveryRoot, projectID)
	if err != nil {
		return handoff.Status{}, err
	}
	if !found {
		return handoff.Derive(projectID, false, handoff.DeliveryPlan{}, false,
			handoff.RunObservation{}, handoff.Evidence{}, time.Now()), nil
	}

	plan, planFound, err := handoff.LoadPlan(host.DeliveryRoot, record.Intent)
	if err != nil {
		return handoff.Status{}, err
	}

	ev := handoff.Evidence{}
	if planFound {
		ev, err = handoff.Assess(plan, record.Intent, host.ProjectionPath(projectID))
		if err != nil {
			return handoff.Status{}, err
		}
	}

	obs, err := observeRun(host.StateDir(projectID))
	if err != nil {
		return handoff.Status{}, err
	}
	return handoff.Derive(projectID, true, plan, planFound, obs, ev, time.Now()), nil
}

// observeRun reads what the run layer publishes about itself.
//
// Liveness is decided from the process table rather than from the heartbeat
// alone. A run killed mid-flight leaves its last heartbeat behind, and reading
// that file as "still running" is exactly how an interrupted delivery would
// look healthy forever.
func observeRun(stateDir string) (handoff.RunObservation, error) {
	var obs handoff.RunObservation

	progress, hasProgress, err := unattended.ReadProgress(stateDir)
	if err != nil {
		return obs, err
	}
	event, finished, err := unattended.ReadCompletion(stateDir)
	if err != nil {
		return obs, err
	}

	if hasProgress {
		obs.RunID = progress.RunID
		obs.Stage = progress.Stage
		obs.Boundaries = progress.Boundaries
	}

	// A completion record belongs to the run it names. One left by a previous
	// run must not answer a question about the run happening now.
	superseded := hasProgress && finished &&
		(progress.RunID != event.RunID || progress.UpdatedAt.After(event.FinishedAt))

	if finished && !superseded {
		obs.Finished = true
		obs.Outcome = string(event.Outcome)
		obs.RunID = event.RunID
		if len(event.HumanActions) > 0 {
			obs.Boundaries = event.HumanActions
		}
		return obs, nil
	}

	if hasProgress && progress.WriterPID > 0 {
		obs.Live = processAlive(progress.WriterPID)
	}
	return obs, nil
}

func printStatus(s handoff.Status) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "delivery: encoding status: %v\n", err)
		return
	}
	fmt.Println(string(data))
}
