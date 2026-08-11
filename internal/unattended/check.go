package unattended

import (
	"fmt"
	"sort"
	"strings"
)

// Category groups checks by the kind of blocker they eliminate. The grouping is
// how a NOT-READY report is read: the category says which of the eight observed
// failure classes just fired.
type Category string

// The check categories.
const (
	CategoryConcurrency  Category = "concurrency"
	CategoryOwnership    Category = "ownership"
	CategoryRepository   Category = "repository"
	CategoryGitHub       Category = "github"
	CategoryTools        Category = "tools"
	CategoryEnvironment  Category = "environment"
	CategoryCredentials  Category = "credentials"
	CategoryProject      Category = "project"
	CategoryDependencies Category = "dependencies"
)

// Check is one executed readiness question and its answer.
//
// Expected and Observed are separate fields rather than a prose sentence
// because the whole value of a preflight report is being able to see, without
// interpretation, what was wanted and what was there.
type Check struct {
	ID       string   `json:"id"`
	Category Category `json:"category"`
	Title    string   `json:"title"`
	Outcome  Outcome  `json:"outcome"`
	Expected string   `json:"expected,omitempty"`
	Observed string   `json:"observed,omitempty"`
	Detail   string   `json:"detail,omitempty"`

	// Remedy is what a human would do about a failure. It is written at the
	// point the check is defined, where what "fixed" means is actually known.
	Remedy string `json:"remedy,omitempty"`

	// Boundary is set only when Outcome is OutcomeHumanBoundary, and names the
	// action only a human can take. A boundary without this field would be
	// indistinguishable from a vague failure.
	Boundary string `json:"boundary,omitempty"`
}

func pass(id string, c Category, title string, observed string) Check {
	return Check{ID: id, Category: c, Title: title, Outcome: OutcomePass, Observed: observed}
}

func fail(id string, c Category, title, expected, observed, remedy string) Check {
	return Check{
		ID: id, Category: c, Title: title, Outcome: OutcomeFail,
		Expected: expected, Observed: observed, Remedy: remedy,
	}
}

func notReached(id string, c Category, title, why string) Check {
	return Check{ID: id, Category: c, Title: title, Outcome: OutcomeNotReached, Detail: why}
}

// Checks is an ordered set of executed checks.
type Checks []Check

// Outcomes projects the set onto its outcomes, for aggregation.
func (cs Checks) Outcomes() []Outcome {
	out := make([]Outcome, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Outcome)
	}
	return out
}

// Readiness reduces the set to one verdict.
func (cs Checks) Readiness() Readiness { return ReadinessOf(cs.Outcomes()) }

// Failures returns only the checks that failed or did not run — the two
// outcomes that make a run unsafe to start.
func (cs Checks) Failures() Checks {
	var out Checks
	for _, c := range cs {
		if c.Outcome == OutcomeFail || c.Outcome == OutcomeNotReached {
			out = append(out, c)
		}
	}
	return out
}

// Boundaries returns the checks that found a named human boundary.
func (cs Checks) Boundaries() Checks {
	var out Checks
	for _, c := range cs {
		if c.Outcome == OutcomeHumanBoundary {
			out = append(out, c)
		}
	}
	return out
}

// String renders the set as a stable, human-readable report body.
func (cs Checks) String() string {
	byCat := map[Category]Checks{}
	var cats []string
	for _, c := range cs {
		if _, seen := byCat[c.Category]; !seen {
			cats = append(cats, string(c.Category))
		}
		byCat[c.Category] = append(byCat[c.Category], c)
	}
	sort.Strings(cats)

	var b strings.Builder
	for _, cat := range cats {
		fmt.Fprintf(&b, "\n--- %s ---\n", cat)
		for _, c := range byCat[Category(cat)] {
			fmt.Fprintf(&b, "  %-12s %-52s %s\n", strings.ToUpper(string(c.Outcome)), c.Title, c.Observed)
			if c.Outcome == OutcomeFail {
				if c.Expected != "" {
					fmt.Fprintf(&b, "  %-12s   expected: %s\n", "", c.Expected)
				}
				if c.Remedy != "" {
					fmt.Fprintf(&b, "  %-12s   remedy:   %s\n", "", c.Remedy)
				}
			}
			if c.Boundary != "" {
				fmt.Fprintf(&b, "  %-12s   human:    %s\n", "", c.Boundary)
			}
			if c.Outcome == OutcomeNotReached && c.Detail != "" {
				fmt.Fprintf(&b, "  %-12s   why:      %s\n", "", c.Detail)
			}
		}
	}
	return b.String()
}
