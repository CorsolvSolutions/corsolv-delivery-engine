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
	"os/exec"
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
	case "accept":
		return cmdAccept(ctx, os.Args[2:])
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

  accept -project <id> -criterion <id> -by <person> [-note <text>] [-host <file>]
      Record a person's acceptance of a criterion the intent reserved for one.
      Refused for anything delivery is expected to satisfy and prove itself.

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
	spec, _, err := handoff.Compile(in, preflightPlan(in), host, "preflight")
	if err != nil {
		return nil, err
	}
	if host.GitHubCommand != "" {
		unattended.GitHubCommand = host.GitHubCommand
	}
	return unattended.Preflight(ctx, spec, nil), nil
}

// preflightPlan is the synthetic plan preflight compiles with.
//
// It never runs, and it is not a proposal: the real work is written by a
// planning agent as the first thing a run does. Its only job is to be a plan
// the compiler will accept, so that the checks which do not depend on what the
// work turns out to be — ownership, tools, forge, durable state — can be asked
// before anyone waits on a planner.
func preflightPlan(in handoff.Intent) handoff.DeliveryPlan {
	return handoff.DeliveryPlan{
		SchemaVersion: handoff.PlanSchemaVersion,
		ProjectID:     in.ProjectID,
		PlannedBy:     "preflight",
		Packages:      preflightPackages(in),
	}
}

// preflightPackages is a minimal plan shaped only to satisfy the compiler.
//
// One package per criterion DELIVERY OWNS is the smallest shape that passes
// plan validation, and using the criteria rather than an invented package keeps
// the preflight spec's phase list honest.
//
// A criterion the intent reserved to a person is deliberately absent. The plan
// validator refuses any package that claims one — a package may prepare what a
// reviewer reads and may never claim their acceptance — so a placeholder that
// claimed every criterion refused every intent with a human boundary at the
// front door, before the planner that would have written a lawful plan was ever
// asked. Nothing is lost by leaving it out: the validator does not demand
// coverage of a reserved criterion, because that answer comes from outside any
// plan, and Compile declares it as a known human boundary so preflight still
// reports it. Ownership is read here and never rewritten.
func preflightPackages(in handoff.Intent) []handoff.WorkPackage {
	phase := "preflight"
	if len(in.Lifecycle) > 0 {
		phase = in.Lifecycle[0]
	}
	out := make([]handoff.WorkPackage, 0, len(in.Acceptance))
	for _, c := range in.Acceptance {
		if c.IsHuman() {
			continue
		}
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

	startedAt := time.Now().UTC()
	runID := fmt.Sprintf("%s-%s", projectID, startedAt.Format("20060102T150405Z"))
	reason := handoff.ReasonInitial
	if len(record.Runs) > 0 {
		reason = handoff.ReasonResumed
	}

	plan, planned, err := handoff.EnsurePlan(ctx, host.DeliveryRoot, in, planner)
	if err != nil {
		// Planning happens BEFORE the run layer starts, so a failure here leaves
		// nothing behind that anything downstream can read — and a delivery whose
		// planner died reads to the portal exactly like one still thinking. That
		// is the worst possible answer: it is indistinguishable from progress,
		// and it never changes.
		//
		// So the failure is published through the record the run layer already
		// owns, rather than through a new one. Nothing here invents a second
		// authority; it fills in the authority that would have existed a moment
		// later.
		recordPreRunBoundary(host, projectID, runID, startedAt, err)
		return refuse(err)
	}
	if planned {
		fmt.Fprintf(os.Stderr, "planned %d work package(s) for %s\n", len(plan.Packages), projectID)
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
	// The projection is published under the same progression decision the
	// completion event carries. A projection that could claim more than the
	// run's own gates licensed would be the reassuring account of two, and the
	// one a person reads.
	if _, perr := unattended.PublishDelivery(spec, session.Queue, session.Fence, event.QA, time.Now()); perr != nil {
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

// recordPreRunBoundary publishes a failure that happened before the run layer
// could publish one for itself.
//
// It is reported as a human boundary rather than as a plain failure, because
// that is what it always is: whatever stopped planning — an exhausted spend
// limit, an expired login, a brief the planner could not turn into safe work —
// needs a person, and delivery genuinely did everything it could without one.
// The reason carries the planner's own words, because a boundary nobody can
// read is not actionable.
//
// A failure to write the record is reported and nothing more. The run has
// already failed; losing the note about it must not also lose the exit code
// that says so.
func recordPreRunBoundary(host handoff.HostProfile, projectID, runID string, startedAt time.Time, cause error) {
	stateDir := host.StateDir(projectID)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "delivery: recording the planning boundary: %v\n", err)
		return
	}

	finished := time.Now().UTC()
	event := unattended.CompletionEvent{
		RunID:        runID,
		ProjectID:    projectID,
		Session:      "managed-delivery-" + projectID,
		SessionLabel: "managed delivery — " + projectID,
		Outcome:      unattended.RunBlockedHuman,
		Reason:       "delivery could not plan the work",
		StartedAt:    startedAt,
		FinishedAt:   finished,
		Duration:     finished.Sub(startedAt).Round(time.Second).String(),
		HumanActions: []string{cause.Error()},
	}
	if err := unattended.WriteCompletion(stateDir, event); err != nil {
		fmt.Fprintf(os.Stderr, "delivery: recording the planning boundary: %v\n", err)
	}
}

// --- status -----------------------------------------------------------------

// --- accept -----------------------------------------------------------------

// cmdAccept records a person's answer to a criterion only a person may give.
//
// It is a separate command, and not a flag on anything a run invokes, because
// the boundary is the point: nothing the engine schedules can reach this, and
// what it writes says who answered and when. An acceptance the machine could
// produce for itself would leave the same evidence as one a person gave, and
// then the boundary is decoration.
func cmdAccept(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("accept", flag.ContinueOnError)
	projectID := fs.String("project", "", "the project being accepted")
	criterion := fs.String("criterion", "", "the acceptance criterion being answered")
	by := fs.String("by", "", "the person accepting it")
	note := fs.String("note", "", "what they said, if anything")
	hostPath := fs.String("host", defaultHostPath(), "path to the delivery host profile")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	switch {
	case *projectID == "":
		return fail("accept needs -project")
	case *criterion == "":
		return fail("accept needs -criterion")
	case *by == "":
		return fail("accept needs -by — an unattributed acceptance is not one")
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
		return refuse(fmt.Errorf("no managed delivery has been started for %s", *projectID))
	}

	updated, err := record.Accept(*criterion, *by, *note, time.Now())
	if err != nil {
		return refuse(err)
	}
	if err := handoff.SaveRecord(host.DeliveryRoot, updated); err != nil {
		return refuse(err)
	}

	fmt.Printf("ACCEPTED %s by %s\n", *criterion, *by)

	// The answer has to reach the document as well as the record. Refreshing
	// BEFORE observing is deliberate: the assessment reads the projection for
	// what the packages did, so deriving the status first would report it from
	// the document this acceptance has just made stale.
	if err := refreshProjection(ctx, host, updated); err != nil {
		return refuse(fmt.Errorf(
			"%s is accepted and the record says so, but the published projection could not be "+
				"refreshed, so the portal will keep reporting it outstanding: %w", *criterion, err))
	}

	status, err := observe(host, *projectID)
	if err != nil {
		return refuse(err)
	}
	printStatus(status)
	if status.State == handoff.StateCompleted {
		return exitOK
	}
	return exitHumanBoundary
}

// projectionStages are the run stages that render the delivery projection and
// install it in the project's repository, in the order they must run.
//
// They are the run's OWN stages, named here rather than reimplemented. A second
// renderer would be a second answer to "what did this delivery do", and the
// whole point of this refresh is that there is only one.
var projectionStages = []string{handoff.StageProject, handoff.StagePublishProjection}

// refreshProjection re-renders and republishes the delivery projection from
// canonical state, without starting a run.
//
// WHY THIS EXISTS. The projection is produced by two stages of a run, and a
// criterion reserved to a person is answered after that run has finished — so
// the document went on reporting the deliverable outstanding while the engine's
// own state said complete. One project, two answers.
//
// It re-runs those two stages and nothing else. The stages are evidence-band
// and idempotent by construction: they read the run's durable ledger, the
// record and the forge, and write one document. No worker is started, no bead
// is routed, no merged package is reopened. The run identity comes from the
// record, so nothing here mints a run id, and the queue and journal are never
// opened — this is not a run, it is the same run's last two stages asked again
// over state that has since changed.
//
// The argv is the argv the compiler already produces for those stages, so the
// refresh cannot drift from what the run does.
func refreshProjection(ctx context.Context, host handoff.HostProfile, record handoff.Record) error {
	plan, planned, err := handoff.LoadPlan(host.DeliveryRoot, record.Intent)
	if err != nil {
		return err
	}
	run, ran := record.LatestRun()
	if !planned || !ran {
		// Nothing has ever rendered a projection for this delivery, so there is
		// nothing to bring up to date. Not an error: a delivery can be accepted
		// before it has run.
		return nil
	}

	_, work, err := handoff.Compile(record.Intent, plan, host, run.RunID)
	if err != nil {
		return err
	}

	for _, stage := range projectionStages {
		task, found := taskByID(work, stage)
		if !found {
			return fmt.Errorf("the compiled run has no %q stage to refresh the projection with", stage)
		}
		cmd := exec.CommandContext(ctx, task.Argv[0], task.Argv[1:]...) //nolint:gosec // argv the compiler built
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		// A stage states its outcome only when a run is supervising it, and no
		// run is. Clearing the contract's variables keeps this refresh from
		// writing a stage result into the evidence of a run that has already
		// finished — which would make a finished run appear to be moving.
		cmd.Env = append(os.Environ(),
			"GC_UNATTENDED_RESULT_PATH=",
			"GC_UNATTENDED_STATE_DIR=")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("re-running the %s stage: %w", stage, err)
		}
	}
	return nil
}

// taskByID finds a compiled task by its stage id.
func taskByID(work unattended.Plan, id string) (unattended.Task, bool) {
	for _, t := range work.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return unattended.Task{}, false
}

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
		// The DELIVERY projection, not the run-progress one. Assess is keyed by
		// package id and reads each package's completion gate; the run publisher's
		// document has neither, so reading it scored a fully merged, fully gated
		// delivery as entirely outstanding.
		ev, err = handoff.Assess(plan, record.Intent, host.DeliveryProjectionPath(projectID), record.Acceptances)
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
	//
	// But a run's OWN last heartbeat is not a later run, and it is not work
	// either: `finished` is published once, microseconds after the completion
	// event, and it says the run stopped. Comparing timestamps alone treated it
	// as progress that superseded the completion — so a delivery that had
	// finished every package reported as `queued`, "no run is currently
	// executing it", exit 0, over evidence whose only outstanding clause was a
	// person's acceptance. The boundary the whole design exists to surface was
	// the one thing the status did not say.
	carriedOn := progress.Stage != unattended.StageFinished &&
		progress.UpdatedAt.After(event.FinishedAt)
	superseded := hasProgress && finished &&
		(progress.RunID != event.RunID || carriedOn)

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
