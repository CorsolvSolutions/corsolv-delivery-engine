// Command projector-gen produces a delivery projection from a completed Gas
// City run.
//
// It is the controller-owned producer: it reads execution facts from a city's
// event log and bead records, takes PR/CI/merge facts from a GitHub
// reconciliation file, and writes the generated PROJECT-STATE.yml plus its
// durable cursor. It writes state before advancing the cursor, so a crash
// between the two replays rather than skips.
//
// It deliberately has no authority of its own: every value it emits comes from
// one of those two sources, and anything neither can supply is left absent.
//
// Usage:
//
//	projector-gen -city <city-dir> -facts <facts.json> -out <PROJECT-STATE.yml> -cursor <cursor.json>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gastownhall/gascity/internal/projector"
)

// factsFile is the reconciliation input: the identity of the run, the tasks it
// executed, and the GitHub facts observed for each. Execution TIMES are not in
// here — those come from the event log, so this file cannot silently become a
// second source of execution truth.
type factsFile struct {
	Project string `json:"project"`
	RunID   string `json:"run_id"`
	Tasks   []struct {
		ID           string   `json:"id"`
		BeadID       string   `json:"bead_id"`
		Title        string   `json:"title"`
		Phase        string   `json:"phase"`
		Milestone    string   `json:"milestone"`
		Workstream   string   `json:"workstream"`
		Status       string   `json:"status"`
		DependsOn    []string `json:"depends_on"`
		AgentSession string   `json:"agent_session"`
		WorktreePath string   `json:"worktree_path"`
		SourceCommit string   `json:"source_commit"`
		PRNumber     int      `json:"pr_number"`
		PRState      string   `json:"pr_state"`
		PRHeadSHA    string   `json:"pr_head_sha"`
		CIState      string   `json:"ci_state"`
		CITestedSHA  string   `json:"ci_tested_sha"`
		MergeState   string   `json:"merge_state"`
		MergeSHA     string   `json:"merge_sha"`
	} `json:"tasks"`
}

func main() {
	city := flag.String("city", "", "city directory holding .gc/events.jsonl")
	factsPath := flag.String("facts", "", "reconciliation facts JSON")
	out := flag.String("out", "", "generated PROJECT-STATE.yml path")
	cursorPath := flag.String("cursor", "", "durable projector cursor path")
	flag.Parse()

	if *city == "" || *factsPath == "" || *out == "" || *cursorPath == "" {
		fmt.Fprintln(os.Stderr, "projector-gen: -city, -facts, -out and -cursor are all required")
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

	state := projector.NewState(facts.Project)
	// Generated is stamped from the newest event actually projected, not from
	// wall clock: a projection of the same authoritative state must render
	// identically however many times it is run.
	byBead := map[string]string{}
	for _, ft := range facts.Tasks {
		status := projector.Status(ft.Status)
		if err := projector.ValidateStatus(status); err != nil {
			fatal("task %s: %v", ft.ID, err)
		}
		state.Tasks[ft.ID] = &projector.Task{
			ID: ft.ID, Title: ft.Title, Phase: ft.Phase,
			Milestone: ft.Milestone, Workstream: ft.Workstream,
			Status: status, DependsOn: ft.DependsOn,
			Evidence: projector.Evidence{
				AgentSession: ft.AgentSession,
				WorktreePath: ft.WorktreePath,
				SourceCommit: ft.SourceCommit,
				Ref:          facts.RunID,
			},
			GitHub: projector.GitHubFacts{
				PRNumber: ft.PRNumber, PRState: ft.PRState, PRHeadSHA: ft.PRHeadSHA,
				CIState: ft.CIState, CITestedSHA: ft.CITestedSHA,
				MergeState: ft.MergeState, MergeSHA: ft.MergeSHA,
			},
		}
		if ft.BeadID != "" {
			byBead[ft.BeadID] = ft.ID
		}
	}

	events, err := projector.ReadCityEvents(filepath.Join(*city, ".gc", "events.jsonl"))
	if err != nil {
		fatal("reading city events: %v", err)
	}

	// Re-key bead-subject events onto task ids so the projection speaks the
	// delivery vocabulary rather than the store's internal ids.
	var mapped []projector.Event
	var newest time.Time
	for _, ev := range events {
		taskID, ok := byBead[ev.Subject]
		if !ok {
			continue
		}
		ev.Subject = taskID
		mapped = append(mapped, ev)
		if ev.Ts.After(newest) {
			newest = ev.Ts
		}
	}
	state.Generated = newest

	cur, err := projector.Project(mapped, state, *out, *cursorPath, nil)
	if err != nil {
		fatal("projecting: %v", err)
	}

	fmt.Printf("projected %d event(s) for %d task(s)\n", len(mapped), len(state.Tasks))
	fmt.Printf("cursor seq: %d\n", cur.Seq)
	fmt.Printf("state:      %s\n", *out)
	fmt.Printf("cursor:     %s\n", *cursorPath)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "projector-gen: "+format+"\n", args...)
	os.Exit(1)
}
