// Command projector-gen produces a delivery projection from a completed Gas
// City run.
//
// It is the controller-owned producer. Execution facts come from the city's
// event log (starts) and the bead store's terminal records (finishes); the
// completion gate is derived from the run's own control ledger; the document it
// writes is a projection of those. It has no authority of its own, and anything
// no authority can supply is left explicitly absent.
//
// Usage:
//
//	projector-gen -city <city> -evidence <evidence-dir> -facts <facts.json> \
//	              -out <PROJECT-STATE.yml> -cursor <cursor.json>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/projector"
)

type factsFile struct {
	Project struct {
		ProjectID             string `json:"projectId"`
		Strategy              string `json:"strategy"`
		AuthoritativeRef      string `json:"authoritativeRef"`
		CurrentPhase          string `json:"currentPhase"`
		CurrentMilestone      string `json:"currentMilestone"`
		OverallRag            string `json:"overallRag"`
		OverallRagReason      string `json:"overallRagReason"`
		LatestAcceptedMainSha string `json:"latestAcceptedMainSha"`
	} `json:"project"`
	RunID string `json:"runId"`
	Tasks []struct {
		TaskID            string   `json:"taskId"`
		BeadID            string   `json:"beadId"`
		Title             string   `json:"title"`
		Phase             string   `json:"phase"`
		TaskType          string   `json:"taskType"`
		Status            string   `json:"status"`
		Priority          string   `json:"priority"`
		OwnerType         string   `json:"ownerType"`
		Branch            string   `json:"branch"`
		PullRequest       int      `json:"pullRequest"`
		Dependencies      []string `json:"dependencies"`
		ParallelGroup     string   `json:"parallelGroup"`
		CriticalPath      bool     `json:"criticalPath"`
		ImplementationSha string   `json:"implementationSha"`
		AgentSession      string   `json:"agentSession"`
		WorktreePath      string   `json:"worktreePath"`
		// GateLabel keys this task to its rows in the run's control ledger. The
		// gate verdict is DERIVED from those rows, never asserted here.
		GateLabel string `json:"gateLabel"`
	} `json:"tasks"`

	// Deliverables are what the PROJECT agreed to produce, and which packages
	// claimed each one. Both are facts the driver reads from the two validated
	// documents — the intent's acceptance criteria and the plan's `satisfies`
	// — and neither is a verdict. Whether a deliverable is MET is derived from
	// the task rows above, by the projector, and never asserted here.
	Deliverables []struct {
		ID          string   `json:"id"`
		Statement   string   `json:"statement"`
		SatisfiedBy []string `json:"satisfiedBy"`
	} `json:"deliverables"`
}

// beadRecord is the slice of a bead's terminal record the projection needs.
// closed_at is the authoritative finish fact.
type beadRecord struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	ClosedAt string `json:"closed_at"`
}

// completionGateControls are the three control identities that together
// constitute the promoted route's completion gate. All three must be PASS for
// that workstream, or the gate is not met.
//
// This is why the gate cannot be inferred from status: `merged` says a PR
// landed, while these say the exact head was tested by required CI, that an
// independent verifier re-derived the result before the merge, and that the
// merge went through repository governance. A merge without them is publication
// without acceptance.
var completionGateControls = []string{
	"required CI passed",
	"independent assurance passed",
	"merged through repository governance",
}

func main() {
	city := flag.String("city", "", "city directory holding .gc/events.jsonl")
	evidence := flag.String("evidence", "", "run evidence directory (controls.tsv, final-*.json)")
	factsPath := flag.String("facts", "", "reconciliation facts JSON")
	out := flag.String("out", "", "generated PROJECT-STATE.yml path")
	cursorPath := flag.String("cursor", "", "durable projector cursor path")
	flag.Parse()

	if *city == "" || *evidence == "" || *factsPath == "" || *out == "" || *cursorPath == "" {
		fmt.Fprintln(os.Stderr, "projector-gen: -city, -evidence, -facts, -out and -cursor are all required")
		os.Exit(2)
	}

	raw, err := os.ReadFile(*factsPath)
	if err != nil {
		fatal("reading facts: %v", err)
	}
	var facts factsFile
	if err := json.Unmarshal(raw, &facts); err != nil {
		fatal("parsing facts: %v", err)
	}

	controls, err := os.ReadFile(filepath.Join(*evidence, "controls.tsv"))
	if err != nil {
		fatal("reading the run control ledger: %v", err)
	}

	state := projector.NewState(facts.Project.ProjectID)
	state.Project.Strategy = facts.Project.Strategy
	state.Project.AuthoritativeRef = facts.Project.AuthoritativeRef
	state.Project.CurrentPhase = facts.Project.CurrentPhase
	state.Project.CurrentMilestone = facts.Project.CurrentMilestone
	state.Project.OverallRag = facts.Project.OverallRag
	state.Project.OverallRagReason = facts.Project.OverallRagReason
	state.Project.LatestAcceptedMainSha = facts.Project.LatestAcceptedMainSha

	for _, fd := range facts.Deliverables {
		state.Deliverables = append(state.Deliverables, projector.Deliverable{
			ID:          fd.ID,
			Statement:   fd.Statement,
			SatisfiedBy: fd.SatisfiedBy,
		})
	}

	byBead := map[string]string{}
	terminal := map[string]time.Time{}

	for _, ft := range facts.Tasks {
		status := projector.TaskStatus(ft.Status)
		if err := projector.ValidateTaskStatus(status); err != nil {
			fatal("task %s: %v", ft.TaskID, err)
		}

		var ev []string
		if ft.AgentSession != "" {
			ev = append(ev, "session="+ft.AgentSession)
		}
		if ft.WorktreePath != "" {
			ev = append(ev, "worktree="+ft.WorktreePath)
		}
		if facts.RunID != "" {
			ev = append(ev, "run="+facts.RunID)
		}
		if ft.BeadID != "" {
			ev = append(ev, "bead="+ft.BeadID)
		}

		gate, gateStatus := deriveCompletionGate(string(controls), ft.GateLabel)

		state.Tasks[ft.TaskID] = &projector.Task{
			TaskID: ft.TaskID, Title: ft.Title, Phase: ft.Phase,
			TaskType: ft.TaskType, Status: status, Priority: ft.Priority,
			OwnerType: ft.OwnerType, Branch: ft.Branch, PullRequest: ft.PullRequest,
			Dependencies: ft.Dependencies, ParallelGroup: ft.ParallelGroup,
			CriticalPath:      ft.CriticalPath,
			ImplementationSha: ft.ImplementationSha,
			CompletionGate:    gate, CompletionGateStatus: gateStatus,
			Evidence: ev,
		}
		if ft.BeadID != "" {
			byBead[ft.BeadID] = ft.TaskID
			if closed, ok := readTerminalRecord(*evidence, ft.BeadID); ok {
				terminal[ft.TaskID] = closed
			}
		}
	}

	events, err := projector.ReadCityEvents(filepath.Join(*city, ".gc", "events.jsonl"))
	if err != nil {
		fatal("reading city events: %v", err)
	}

	var mapped []projector.Event
	var newest time.Time
	for _, e := range events {
		taskID, ok := byBead[e.Subject]
		if !ok {
			continue
		}
		e.Subject = taskID
		mapped = append(mapped, e)
		if e.Ts.After(newest) {
			newest = e.Ts
		}
	}
	state.Project.LastUpdateTimestamp = newest

	cur, err := projector.Project(mapped, state, *out, *cursorPath, nil)
	if err != nil {
		fatal("projecting: %v", err)
	}

	// The terminal join happens AFTER the event fold, then the document is
	// re-rendered: closures are a bead-store fact and the event log does not
	// carry them, so without this every completed task would project
	// actualFinish: null.
	for taskID, closedAt := range terminal {
		state.SetTerminalFinish(taskID, closedAt)
	}
	state.RecomputeBlockers()
	rendered, err := projector.Render(state)
	if err != nil {
		fatal("rendering: %v", err)
	}
	if err := os.WriteFile(*out, rendered, 0o644); err != nil { //nolint:gosec // generated, world-readable by design
		fatal("writing state: %v", err)
	}

	finished := 0
	for _, t := range state.Tasks {
		if !t.ActualFinish.IsZero() {
			finished++
		}
	}
	fmt.Printf("projected %d event(s) for %d task(s)\n", len(mapped), len(state.Tasks))
	fmt.Printf("cursor seq:        %d\n", cur.Seq)
	fmt.Printf("terminal finishes: %d/%d\n", finished, len(state.Tasks))
	fmt.Printf("state:             %s\n", *out)
}

// readTerminalRecord reads a bead's captured terminal record and returns its
// closed_at. A record that is not closed yields no finish rather than a guess.
func readTerminalRecord(evidenceDir, beadID string) (time.Time, bool) {
	raw, err := os.ReadFile(filepath.Join(evidenceDir, "final-"+beadID+".json"))
	if err != nil {
		return time.Time{}, false
	}
	var rows []beadRecord
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) == 0 {
		return time.Time{}, false
	}
	r := rows[0]
	if r.Status != "closed" || strings.TrimSpace(r.ClosedAt) == "" {
		return time.Time{}, false
	}
	closed, err := time.Parse(time.RFC3339, r.ClosedAt)
	if err != nil {
		return time.Time{}, false
	}
	return closed, true
}

// deriveCompletionGate reads the run's control ledger and reports the gate only
// when every constituent control passed FOR THIS WORKSTREAM.
//
// It never consults status. A merged task whose assurance control is missing or
// failed gets not-met, which is the whole point: the consumer reserves 100% for
// a met gate, and manufacturing one from a merge would score acceptance that
// nothing verified.
func deriveCompletionGate(ledger, label string) (string, projector.CompletionGateStatus) {
	if strings.TrimSpace(label) == "" {
		return "", projector.GateNotMet
	}
	passed := 0
	for _, want := range completionGateControls {
		for _, line := range strings.Split(ledger, "\n") {
			cols := strings.Split(line, "\t")
			if len(cols) < 2 {
				continue
			}
			name, status := cols[0], cols[1]
			if strings.HasPrefix(name, label+" ") && strings.Contains(name, want) && status == "PASS" {
				passed++
				break
			}
		}
	}
	gate := strings.Join(completionGateControls, " + ")
	switch {
	case passed == len(completionGateControls):
		return gate, projector.GateMet
	case passed > 0:
		return gate, projector.GatePartiallyMet
	default:
		return gate, projector.GateNotMet
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "projector-gen: "+format+"\n", args...)
	os.Exit(1)
}
