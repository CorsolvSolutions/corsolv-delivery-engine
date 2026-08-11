package unattended

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// GitHubProbe is what a live interrogation of the forge found.
//
// It is a plain value with no behavior so that the rules that matter — which
// permission licenses a merge, when a required review is a boundary rather than
// a failure — can be table-tested against recorded facts. Asserting those rules
// against live GitHub would only ever test what this account happens to be able
// to do today.
type GitHubProbe struct {
	Authenticated bool   `json:"authenticated"`
	Account       string `json:"account"`

	RepoReadable bool `json:"repoReadable"`
	// ViewerPermission is GitHub's own answer: ADMIN, MAINTAIN, WRITE, TRIAGE
	// or READ.
	ViewerPermission string `json:"viewerPermission"`
	DefaultBranch    string `json:"defaultBranch"`

	// ProtectionReadable distinguishes "this branch has no protection" from
	// "this account cannot see whether it has protection". Both come back as a
	// failed API call and they mean opposite things.
	ProtectionReadable bool `json:"protectionReadable"`
	ProtectionPresent  bool `json:"protectionPresent"`
	RequiredApprovals  int  `json:"requiredApprovals"`
	RequiresReview     bool `json:"requiresReview"`

	// Notes records probe-level trouble that did not itself decide an outcome.
	Notes []string `json:"notes,omitempty"`
}

// writeCapablePermissions license pushing a branch and opening a pull request.
var writeCapablePermissions = map[string]bool{
	"ADMIN": true, "MAINTAIN": true, "WRITE": true,
}

// mergeCapablePermissions license merging a pull request. WRITE is enough on
// GitHub's side: what usually stops a merge is branch protection, not the role,
// which is why protection is probed separately rather than inferred from this.
var mergeCapablePermissions = map[string]bool{
	"ADMIN": true, "MAINTAIN": true, "WRITE": true,
}

// GitHubCommand is the executable used to talk to the forge. It is a package
// variable rather than a constant so a spec on a host where `gh` lives
// somewhere unusual — a Windows install reached from WSL, for instance — can
// point at it without this package knowing anything about hosts.
var GitHubCommand = "gh"

// ProbeGitHub asks the forge, before the run starts, every question the run
// will later depend on.
//
// Each of these was previously answered by attempting the thing and seeing
// whether it worked — am I logged in, can I push, may I merge — spread across
// the length of a run. That is the worst possible schedule for these questions:
// the merge permission is discovered at merge time, after all the work.
func ProbeGitHub(ctx context.Context, req GitHubRequirement) GitHubProbe {
	var p GitHubProbe
	gh := GitHubCommand

	out, ok, err := runProbe(ctx, defaultProbeTimeout, "", []string{gh, "auth", "status"})
	switch {
	case err != nil:
		p.Notes = append(p.Notes, "auth status probe failed: "+Redact(err.Error()))
		return p
	case !ok:
		p.Notes = append(p.Notes, "not authenticated: "+Redact(firstLine(out)))
		return p
	}
	p.Authenticated = true

	if out, ok, err := runProbe(ctx, defaultProbeTimeout, "", []string{gh, "api", "user", "--jq", ".login"}); err == nil && ok {
		p.Account = firstLine(out)
	}

	if req.Repo == "" {
		return p
	}
	out, ok, err = runProbe(ctx, defaultProbeTimeout, "",
		[]string{gh, "repo", "view", req.Repo, "--json", "viewerPermission,defaultBranchRef"})
	switch {
	case err != nil:
		p.Notes = append(p.Notes, "repo view probe failed: "+Redact(err.Error()))
		return p
	case !ok:
		p.Notes = append(p.Notes, "repo view refused: "+Redact(firstLine(out)))
		return p
	}
	var view struct {
		ViewerPermission string `json:"viewerPermission"`
		DefaultBranchRef struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	}
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		p.Notes = append(p.Notes, "repo view returned unparseable JSON")
		return p
	}
	p.RepoReadable = true
	p.ViewerPermission = strings.ToUpper(view.ViewerPermission)
	p.DefaultBranch = view.DefaultBranchRef.Name

	base := req.Branch
	if base == "" {
		base = p.DefaultBranch
	}
	if base == "" {
		return p
	}
	out, ok, err = runProbe(ctx, defaultProbeTimeout, "",
		[]string{gh, "api", fmt.Sprintf("repos/%s/branches/%s/protection", req.Repo, base)})
	switch {
	case err != nil:
		p.Notes = append(p.Notes, "branch protection probe failed: "+Redact(err.Error()))
	case ok:
		p.ProtectionReadable = true
		p.ProtectionPresent = true
		var protection struct {
			RequiredPullRequestReviews *struct {
				RequiredApprovingReviewCount int `json:"required_approving_review_count"`
			} `json:"required_pull_request_reviews"`
		}
		if err := json.Unmarshal([]byte(out), &protection); err == nil && protection.RequiredPullRequestReviews != nil {
			p.RequiredApprovals = protection.RequiredPullRequestReviews.RequiredApprovingReviewCount
			p.RequiresReview = p.RequiredApprovals > 0
		}
	case strings.Contains(out, "Branch not protected"), strings.Contains(out, "Not Found"):
		// An unprotected branch answers 404. That is a readable answer meaning
		// "no protection", not an unreadable one.
		p.ProtectionReadable = true
	default:
		p.Notes = append(p.Notes, "branch protection unreadable: "+Redact(firstLine(out)))
	}
	return p
}

// GitHubChecks maps probe facts onto readiness checks.
//
// Anything a person must grant is a boundary rather than a failure. A run whose
// account cannot merge is not broken; it is a run that ends at a pull request.
// Saying that at the start is what lets the queue finish everything short of the
// merge, instead of discovering the wall by walking into it.
func GitHubChecks(req GitHubRequirement, p GitHubProbe) Checks {
	var cs Checks

	if !p.Authenticated {
		cs = append(cs, Check{
			ID: "github.auth", Category: CategoryGitHub, Title: "the forge CLI is authenticated",
			Outcome: OutcomeHumanBoundary, Expected: "authenticated", Observed: "not authenticated",
			Boundary: "run `" + GitHubCommand + " auth login` — an interactive login cannot be performed by the run",
		})
		return append(cs, ghNotReached("the forge CLI is not authenticated, so this could not be asked")...)
	}
	cs = append(cs, pass("github.auth", CategoryGitHub, "the forge CLI is authenticated", "as "+orNone(p.Account)))

	// An authenticated session on the wrong account is the ownership failure in
	// forge form, and it is invisible to every other check here.
	if req.Account != "" {
		if strings.EqualFold(req.Account, p.Account) {
			cs = append(cs, pass("github.account", CategoryGitHub, "authenticated as the declared account", p.Account))
		} else {
			cs = append(cs, fail("github.account", CategoryGitHub, "authenticated as the declared account",
				req.Account, orNone(p.Account),
				"switch accounts with `"+GitHubCommand+" auth switch` before starting a run that pushes"))
		}
	}

	if !p.RepoReadable {
		cs = append(cs, fail("github.repo", CategoryGitHub, "repository is readable",
			req.Repo, "not readable"+notesSuffix(p.Notes),
			"check the repository name, and that this account has access to it"))
		return append(cs, ghNotReached("the repository could not be read, so this could not be asked")...)
	}
	cs = append(cs, pass("github.repo", CategoryGitHub, "repository is readable",
		req.Repo+" ("+p.ViewerPermission+")"))

	canWrite := writeCapablePermissions[p.ViewerPermission]

	cs = append(cs, capabilityCheck("github.push", "push authority on the working branch",
		req.NeedPush, canWrite, p.ViewerPermission,
		"ask an administrator of "+req.Repo+" for write access"))

	cs = append(cs, capabilityCheck("github.pr", "pull requests may be opened",
		req.NeedPR, canWrite, p.ViewerPermission,
		"use a fork-and-pull workflow, or obtain write access to "+req.Repo))

	cs = append(cs, capabilityCheck("github.checks", "CI results are readable",
		req.NeedChecks, true, p.ViewerPermission,
		"obtain read access to Actions on "+req.Repo))

	cs = append(cs, mergeCheck(req, p))
	return cs
}

func ghNotReached(why string) Checks {
	return Checks{
		notReached("github.repo", CategoryGitHub, "repository is readable", why),
		notReached("github.push", CategoryGitHub, "push authority on the working branch", why),
		notReached("github.pr", CategoryGitHub, "pull requests may be opened", why),
		notReached("github.checks", CategoryGitHub, "CI results are readable", why),
		notReached("github.merge", CategoryGitHub, "pull requests may be merged", why),
	}
}

func capabilityCheck(id, title string, needed, held bool, permission, humanAction string) Check {
	switch {
	case !needed:
		return pass(id, CategoryGitHub, title, "not required by this run")
	case held:
		return pass(id, CategoryGitHub, title, permission)
	default:
		return Check{
			ID: id, Category: CategoryGitHub, Title: title, Outcome: OutcomeHumanBoundary,
			Expected: "write-capable permission", Observed: "permission " + orNone(permission),
			Boundary: humanAction,
		}
	}
}

// mergeCheck is separate because merge authority has three distinct ways of
// being absent — the run does not claim it, the account does not have it, or a
// policy withholds it — and a report that collapsed them would send a person to
// the wrong place.
func mergeCheck(req GitHubRequirement, p GitHubProbe) Check {
	const (
		id    = "github.merge"
		title = "pull requests may be merged"
	)
	humanAction := req.MergeHumanAction
	if humanAction == "" {
		humanAction = "a person with merge rights on " + req.Repo + " merges the pull request this run opens"
	}

	if !req.NeedMerge {
		return Check{
			ID: id, Category: CategoryGitHub, Title: title, Outcome: OutcomeHumanBoundary,
			Expected: "a merge this run performs", Observed: "this run does not claim merge authority",
			Boundary: humanAction,
		}
	}
	if !mergeCapablePermissions[p.ViewerPermission] {
		return Check{
			ID: id, Category: CategoryGitHub, Title: title, Outcome: OutcomeHumanBoundary,
			Expected: "merge-capable permission", Observed: "permission " + orNone(p.ViewerPermission),
			Boundary: humanAction,
		}
	}
	if !p.ProtectionReadable {
		return fail(id, CategoryGitHub, title,
			"branch protection readable", "unreadable"+notesSuffix(p.Notes),
			"grant this account permission to read branch protection, or set needMerge=false and plan for a human merge")
	}
	if p.RequiresReview {
		return Check{
			ID: id, Category: CategoryGitHub, Title: title, Outcome: OutcomeHumanBoundary,
			Expected: "no blocking review requirement",
			Observed: fmt.Sprintf("branch protection requires %d approving review(s)", p.RequiredApprovals),
			Boundary: "a human reviewer approves the pull request before it can merge",
		}
	}
	observed := p.ViewerPermission + ", no branch protection"
	if p.ProtectionPresent {
		observed = p.ViewerPermission + ", branch protection present but requires no approving review"
	}
	return pass(id, CategoryGitHub, title, observed)
}

func notesSuffix(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return " (" + strings.Join(notes, "; ") + ")"
}
