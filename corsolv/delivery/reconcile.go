package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gastownhall/gascity/internal/handoff"
)

// THE TWO OPERATIONS THAT LET A FINISHED DELIVERY BE WRONG.
//
// `invalidate` records that a criterion reported met was not actually
// satisfied. `remediate` authorizes the additive work that repairs it. Between
// them they are the only supported route from Complete back to incomplete and
// forward to Complete again, and both are deliberately separate commands rather
// than flags on anything a run invokes — for the same reason `accept` is.
//
// Nothing the engine schedules can reach either. A run that finds its own work
// wanting cannot un-complete the project it just finished, and an AI worker
// whose implementation failed cannot dismiss the acceptance that would have
// caught it. What reaches durable state is who decided, why, and against what.

// cmdInvalidate records that a delivery-owned criterion reported met was not
// actually satisfied.
//
// It refreshes the published projection before reporting, for the same reason
// acceptance does: the record and the document the portal reads are two places
// one fact lives, and a command that updated the first and left the second
// would recreate the split that the acceptance packet closed. Refreshing BEFORE
// observing is deliberate — the assessment reads the projection for what the
// packages did, so deriving status first would report it from the document this
// invalidation has just made stale.
func cmdInvalidate(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("invalidate", flag.ContinueOnError)
	projectID := fs.String("project", "", "the project whose criterion is disproved")
	criterion := fs.String("criterion", "", "the acceptance criterion whose earlier result is withdrawn")
	by := fs.String("by", "", "the actor recording the finding")
	reason := fs.String("reason", "", "why the earlier result is wrong")
	evidence := fs.String("evidence", "", "what proves it: an issue, a commit, a report, a file")
	hostPath := fs.String("host", defaultHostPath(), "path to the delivery host profile")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	switch {
	case *projectID == "":
		return fail("invalidate needs -project")
	case *criterion == "":
		return fail("invalidate needs -criterion")
	case *by == "":
		return fail("invalidate needs -by — an unattributed invalidation is an invisible mutation of a finished project")
	case *reason == "":
		return fail("invalidate needs -reason — what a later reader needs is why the earlier conclusion was wrong")
	case *evidence == "":
		return fail("invalidate needs -evidence — an assertion with nothing behind it may not un-complete a project")
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

	// The assessment being withdrawn, read now rather than assumed. It is what
	// decides whether there is a conclusion here to correct at all.
	prior, err := observe(host, *projectID)
	if err != nil {
		return refuse(err)
	}

	updated, err := record.Invalidate(*criterion, prior.Evidence, *by, *reason, *evidence, time.Now())
	if err != nil {
		return refuse(err)
	}
	if err := handoff.SaveRecord(host.DeliveryRoot, updated); err != nil {
		return refuse(err)
	}

	inv, _ := updated.LatestInvalidation(*criterion)
	fmt.Printf("INVALIDATED %s (invalidation %d) by %s\n", *criterion, inv.Seq, inv.By)

	if err := refreshProjection(ctx, host, updated); err != nil {
		return refuse(fmt.Errorf(
			"%s is invalidated and the record says so, but the published projection could not be refreshed, "+
				"so the portal will keep reporting the project complete: %w", *criterion, err))
	}

	status, err := observe(host, *projectID)
	if err != nil {
		return refuse(err)
	}
	printStatus(status)
	// An invalidated criterion is work delivery has to do again, so the honest
	// exit is the one that says delivery is not finished. It is never 0: a
	// script that treated this as success would have un-completed a project and
	// reported nothing happened.
	return exitHumanBoundary
}

// remediationInput is the document a person writes to authorize corrective work.
//
// It is the Remediation type itself, decoded with unknown fields refused, so
// there is exactly one shape and no second schema to keep in step. What the
// document may NOT carry — seq, authorizedAt, authorizedBy — is refused by
// AuthorizeRemediation rather than filtered here, so the refusal says why.

// cmdRemediate authorizes additive work against a standing invalidation, or
// prints the remediations a project already has.
//
// IT DOES NOT REPLACE THE PLAN, AND CANNOT. The original plan.json is never
// opened for writing on this path; each remediation is its own document, and
// `delivery plan` goes on printing exactly what was planned before anything was
// disproved. That separation is what keeps the original plan usable as the
// historical evidence its merged work was measured against.
func cmdRemediate(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("remediate", flag.ContinueOnError)
	projectID := fs.String("project", "", "the project whose criterion is being repaired")
	from := fs.String("from", "", "authorize the remediation in this JSON file")
	by := fs.String("by", "", "the actor authorizing the corrective work")
	hostPath := fs.String("host", defaultHostPath(), "path to the delivery host profile")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *projectID == "" {
		return fail("remediate needs -project")
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

	if *from == "" {
		return showRemediations(host, *projectID)
	}
	if *by == "" {
		return fail("remediate needs -by — additive work against a finished project is a decision, and a decision with no author is a mutation")
	}

	plan, hasPlan, err := handoff.LoadPlan(host.DeliveryRoot, record.Intent)
	if err != nil {
		return refuse(err)
	}
	if !hasPlan {
		return refuse(fmt.Errorf("delivery for %q has no plan — there is nothing for corrective work to be added to", *projectID))
	}

	prior, err := observe(host, *projectID)
	if err != nil {
		return refuse(err)
	}

	raw, err := os.ReadFile(*from) //nolint:gosec // an operator-supplied path
	if err != nil {
		return refuse(fmt.Errorf("reading the remediation: %w", err))
	}
	proposed, err := handoff.DecodeRemediation(raw)
	if err != nil {
		return refuse(err)
	}

	authorized, err := handoff.AuthorizeRemediation(
		host.DeliveryRoot, record, plan, prior.Evidence, proposed, *by, time.Now())
	if err != nil {
		return refuse(err)
	}

	fmt.Printf("REMEDIATION %d authorized by %s: %d work package(s) repairing %v\n",
		authorized.Seq, authorized.AuthorizedBy, len(authorized.Packages), authorized.Criteria())

	// The document has to learn about the new work as well as the record. A
	// remedial package the projection does not carry is a package the
	// completion assessment reads as outstanding with nothing to say why.
	if err := refreshProjection(ctx, host, record); err != nil {
		return refuse(fmt.Errorf(
			"remediation %d is authorized and durable, but the published projection could not be refreshed, "+
				"so the portal will not show the corrective work: %w", authorized.Seq, err))
	}

	status, err := observe(host, *projectID)
	if err != nil {
		return refuse(err)
	}
	printStatus(status)
	if status.State == handoff.StateCompleted {
		return exitOK
	}
	// Authorized work that has not run yet is delivery that is not finished.
	return exitHumanBoundary
}

// showRemediations prints the corrective work a project has had authorized, in
// the order it was authorized.
func showRemediations(host handoff.HostProfile, projectID string) int {
	remediations, err := handoff.LoadRemediations(host.DeliveryRoot, projectID)
	if err != nil {
		return refuse(err)
	}
	if len(remediations) == 0 {
		fmt.Fprintf(os.Stderr, "delivery for %q has had no remediation authorized\n", projectID)
		return exitOK
	}
	data, err := json.MarshalIndent(remediations, "", "  ")
	if err != nil {
		return refuse(fmt.Errorf("encoding the remediations: %w", err))
	}
	fmt.Println(string(data))
	return exitOK
}
