// Command unattended-run executes a Gas City work plan without supervision.
//
// It is the entry point for the fork-owned control layer in
// internal/unattended: it proves the ground is safe, claims the worktree, and
// drains a declared work queue, writing durable evidence as it goes.
//
// The subcommands are deliberately separable. `preflight` answers "could a run
// start now" without starting one, which is the question a person asks before
// leaving for the night; `run` does the whole thing; `status` and `owner`
// answer "what is it doing" and "who has the tree" from outside the run, without
// disturbing it.
//
// Usage:
//
//	unattended-run preflight -spec <spec.toml> [-plan <plan.toml>] [-json <out>]
//	unattended-run run       -spec <spec.toml>  -plan <plan.toml>  [-hook <cmd>]
//	unattended-run status    -state <dir>
//	unattended-run owner     -worktree <dir>
//
// Exit codes are the interface for a supervising script:
//
//	0  the run completed, or preflight said READY
//	3  a human boundary remains — the run did everything it could
//	4  NOT-READY, or the run stopped on something it could not work around
//	5  the invocation itself was wrong
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gastownhall/gascity/internal/unattended"
)

const (
	exitOK            = 0
	exitHumanBoundary = 3
	exitNotReady      = 4
	exitUsage         = 5
)

// main keeps os.Exit to itself so that run's own deferred shutdown — releasing
// the worktree lock, closing the journal — actually happens. An exit taken
// inside the command would skip every defer above it and leave a worktree
// claimed by a process that is already gone.
func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) < 2 {
		usage()
		return exitUsage
	}
	// One signal handler for the whole process: an interrupted run must reach
	// its own shutdown path rather than dying with a worktree still claimed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var code int
	switch os.Args[1] {
	case "preflight":
		code = cmdPreflight(ctx, os.Args[2:])
	case "run":
		code = cmdRun(ctx, os.Args[2:])
	case "status":
		code = cmdStatus(os.Args[2:])
	case "owner":
		code = cmdOwner(os.Args[2:])
	case "publish":
		code = cmdPublish(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unattended-run: unknown subcommand %q\n\n", os.Args[1])
		usage()
		code = exitUsage
	}
	return code
}

func usage() {
	fmt.Fprint(os.Stderr, `unattended-run — execute a Gas City work plan without supervision

  preflight -spec <spec.toml> [-plan <plan.toml>] [-json <out>] [-gh <path>]
      Answer whether a run could start now, and write the verdict. Starts nothing.

  run -spec <spec.toml> -plan <plan.toml> [-hook <cmd>] [-gh <path>]
      Preflight, claim the worktree, drain the queue, publish evidence.

  status -state <dir>
      Print what a run is doing, from its published heartbeat.

  owner -worktree <dir>
      Print who holds the worktree's writer lock.

  publish -state <dir> -repo <dir> -target <repo-relative-path>
      Install the run's delivery projection into a target repository. Refuses
      any target this projector did not write.

Exit: 0 ready/completed  3 human boundary remains  4 not ready/stopped  5 usage
`)
}

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "unattended-run: "+format+"\n", args...)
	return exitUsage
}

// load reads the spec and, when given, the plan.
func load(specPath, planPath string) (unattended.Spec, *unattended.Plan, error) {
	spec, err := unattended.LoadSpec(specPath)
	if err != nil {
		return spec, nil, err
	}
	if planPath == "" {
		return spec, nil, nil
	}
	plan, err := unattended.LoadPlan(planPath)
	if err != nil {
		return spec, nil, err
	}
	return spec, &plan, nil
}

func cmdPreflight(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	specPath := fs.String("spec", "", "path to the run spec (TOML)")
	planPath := fs.String("plan", "", "path to the work plan (TOML); optional")
	jsonPath := fs.String("json", "", "also write the report as JSON here")
	gh := fs.String("gh", "", "path to the forge CLI, when it is not `gh` on PATH")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *specPath == "" {
		return fail("preflight needs -spec")
	}
	if *gh != "" {
		unattended.GitHubCommand = *gh
	}

	spec, plan, err := load(*specPath, *planPath)
	if err != nil {
		return fail("%v", err)
	}
	report := unattended.Preflight(ctx, spec, plan)
	fmt.Print(report.String())

	if data, jerr := report.JSON(); jerr == nil {
		if *jsonPath != "" {
			if werr := os.WriteFile(*jsonPath, data, 0o644); werr != nil {
				fmt.Fprintf(os.Stderr, "unattended-run: writing %s: %v\n", *jsonPath, werr)
			}
		}
	}

	switch report.Readiness {
	case unattended.Ready:
		return exitOK
	case unattended.ReadyWithKnownHumanBoundary:
		return exitHumanBoundary
	default:
		return exitNotReady
	}
}

func cmdRun(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	specPath := fs.String("spec", "", "path to the run spec (TOML)")
	planPath := fs.String("plan", "", "path to the work plan (TOML)")
	hook := fs.String("hook", "", "command run once with the completion event on stdin")
	gh := fs.String("gh", "", "path to the forge CLI, when it is not `gh` on PATH")
	quiet := fs.Bool("quiet", false, "do not echo progress to stderr")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *specPath == "" || *planPath == "" {
		return fail("run needs -spec and -plan")
	}
	if *gh != "" {
		unattended.GitHubCommand = *gh
	}

	spec, plan, err := load(*specPath, *planPath)
	if err != nil {
		return fail("%v", err)
	}

	session, err := unattended.Begin(ctx, spec, *plan)
	if err != nil {
		switch {
		case errors.Is(err, unattended.ErrNotReady):
			fmt.Fprintf(os.Stderr, "\nNOT-READY — the run did not start.\n%v\n", err)
			return exitNotReady
		case errors.Is(err, unattended.ErrWriterHeld), errors.Is(err, unattended.ErrForeignWriter):
			fmt.Fprintf(os.Stderr, "\nREFUSED — this worktree already has an owner.\n%v\n", err)
			return exitNotReady
		default:
			return fail("%v", err)
		}
	}
	defer session.Close() //nolint:errcheck

	fmt.Print(session.Report.String())
	if session.Resumed {
		fmt.Fprintf(os.Stderr, "\nresuming an existing journal in %s\n", spec.StateDir)
	}
	if session.TruncatedTail {
		fmt.Fprintln(os.Stderr, "the previous run died mid-record; that record was never durable and is not replayed")
	}

	if !*quiet {
		session.Runner.OnProgress = func(p unattended.Progress) {
			fmt.Fprintf(os.Stderr, "[%s] %s %s %s next=%s\n",
				p.Elapsed, p.Stage, p.CurrentTask, summarize(p), p.NextAction)
		}
	}

	event, runErr := session.Runner.Run(ctx)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "unattended-run: %v\n", runErr)
	}

	// The delivery projection is published after the run, from the same queue
	// the run drained, so it can never disagree with the journal beside it.
	if _, perr := unattended.PublishDelivery(spec, session.Queue, session.Fence, time.Now()); perr != nil {
		fmt.Fprintf(os.Stderr, "unattended-run: publishing the delivery projection: %v\n", perr)
	}

	fmt.Printf("\n%s\n", event.String())
	for _, action := range event.HumanActions {
		fmt.Printf("  human: %s\n", action)
	}
	if *hook != "" {
		runHook(*hook, spec.StateDir)
	}

	switch event.Outcome {
	case unattended.RunCompleted:
		return exitOK
	case unattended.RunBlockedHuman, unattended.RunAwaitingAuth:
		return exitHumanBoundary
	default:
		return exitNotReady
	}
}

func summarize(p unattended.Progress) string {
	return fmt.Sprintf("succeeded=%d failed=%d held=%d pending=%d",
		p.Tasks[unattended.TaskSucceeded], p.Tasks[unattended.TaskFailed],
		p.Tasks[unattended.TaskHeld], p.Tasks[unattended.TaskPending])
}

// runHook hands the completion event to a notification layer.
//
// A hook that fails is reported and nothing more. The run's evidence is already
// durable in the state directory; a sound that did not play is not a reason to
// report a completed run as failed.
func runHook(command, stateDir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, filepath.Join(stateDir, unattended.CompletionName))
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "unattended-run: completion hook %q: %v\n", command, err)
	}
}

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	stateDir := fs.String("state", "", "the run's state directory")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *stateDir == "" {
		return fail("status needs -state")
	}

	p, live, perr := unattended.ReadProgress(*stateDir)
	if perr != nil {
		return fail("%v", perr)
	}
	event, finished, cerr := unattended.ReadCompletion(*stateDir)
	if cerr != nil {
		return fail("%v", cerr)
	}

	// A state directory belongs to a project and outlives any one run, so a
	// completion record from a previous run sits there while the next one is
	// still working. Reporting it would answer a question about the live run
	// with a fact about a dead one — which this command did, during the very
	// run that found it. The completion record is terminal only for the run it
	// names, and only while nothing newer has published progress.
	stale := live && (p.RunID != event.RunID || p.UpdatedAt.After(event.FinishedAt))
	if finished && !stale {
		fmt.Println(event.String())
		for _, a := range event.HumanActions {
			fmt.Printf("  human: %s\n", a)
		}
		switch event.Outcome {
		case unattended.RunCompleted:
			return exitOK
		case unattended.RunFailed:
			return exitNotReady
		default:
			return exitHumanBoundary
		}
	}
	if !live {
		return fail("no run has published state in %s", *stateDir)
	}
	fmt.Printf("run %s (%s) — %s for %s\n", p.RunID, p.ProjectID, p.Stage, p.Elapsed)
	fmt.Printf("  worktree:  %s\n", p.Worktree)
	fmt.Printf("  position:  %s@%.9s\n", p.Branch, p.Head)
	fmt.Printf("  owner:     %s (pid %d)\n", p.WriterOwner, p.WriterPID)
	fmt.Printf("  task:      %s (%s)%s\n", p.CurrentTask, p.CurrentBand, fallbackNote(p.UsingFallback))
	fmt.Printf("  progress:  %s\n", summarize(p))
	fmt.Printf("  milestone: %s\n", p.LastMilestone)
	fmt.Printf("  next:      %s\n", p.NextAction)
	for _, b := range p.Boundaries {
		fmt.Printf("  blocked:   %s\n", b)
	}
	return exitOK
}

func fallbackNote(usingFallback bool) string {
	if usingFallback {
		return " — fallback work; the primary path is blocked"
	}
	return ""
}

// cmdPublish installs the run's delivery projection into a target repository.
//
// It is a separate verb rather than something the run does implicitly, because
// writing into a project's own repository is a different kind of act from
// writing into the run's state directory, and the GUK BPM pilot showed how
// different: the projection the run publishes for itself is always safe, while
// installing it into a project can collide with a document that project
// maintains by hand.
func cmdPublish(args []string) int {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	stateDir := fs.String("state", "", "the run's state directory, holding the rendered projection")
	repo := fs.String("repo", "", "the target repository's worktree root")
	target := fs.String("target", "", "publication path, relative to the repository")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *stateDir == "" || *repo == "" || *target == "" {
		return fail("publish needs -state, -repo and -target")
	}

	projection, err := os.ReadFile(filepath.Join(*stateDir, "PROJECT-STATE.yml"))
	if err != nil {
		return fail("reading the run's projection: %v", err)
	}

	res, err := unattended.PublishProjection(*repo, *target, projection)
	switch {
	case errors.Is(err, unattended.ErrTargetNotOurs):
		fmt.Fprintf(os.Stderr, "REFUSING TO PUBLISH\n\n%v\n\n"+
			"An authorized path is not an authorized act. Choose a publication path this\n"+
			"projector owns, or have a person decide to replace the existing document.\n", err)
		return exitHumanBoundary
	case err != nil:
		return fail("%v", err)
	}

	switch {
	case res.Unchanged:
		fmt.Printf("unchanged: %s\n", res.Target)
	case res.Created:
		fmt.Printf("created: %s\n", res.Target)
	default:
		fmt.Printf("updated: %s\n", res.Target)
	}
	if res.ReplacedMalformed {
		fmt.Println("note: the previous file carried this projector's marker but was not readable; it was replaced")
	}
	return exitOK
}

func cmdOwner(args []string) int {
	fs := flag.NewFlagSet("owner", flag.ContinueOnError)
	worktree := fs.String("worktree", "", "the worktree to inspect")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *worktree == "" {
		return fail("owner needs -worktree")
	}

	state, err := unattended.ProbeRepo(*worktree)
	if err != nil {
		return fail("%v", err)
	}
	dir := unattended.WriterLockDir(state)
	owner, found, err := unattended.ReadOwner(dir)
	if err != nil {
		return fail("%v", err)
	}
	if !found {
		fmt.Printf("%s: no writer lock recorded\n", state.Root)
		return exitOK
	}
	fmt.Printf("%s\n", state.Root)
	fmt.Printf("  run:      %s\n", owner.RunID)
	fmt.Printf("  project:  %s\n", owner.ProjectID)
	fmt.Printf("  session:  %s\n", owner.Session)
	fmt.Printf("  role:     %s\n", owner.Role)
	fmt.Printf("  process:  pid %d on %s (%s)\n", owner.PID, owner.Host, owner.OSFamily)
	fmt.Printf("  since:    %s\n", owner.AcquiredAt.UTC().Format(time.RFC3339))
	if owner.Displaced != nil {
		fmt.Printf("  displaced run %s, whose record outlived its lock\n", owner.Displaced.RunID)
	}
	fmt.Println("\nA recorded owner is not proof of a live one. Only acquiring the lock decides.")
	return exitHumanBoundary
}
