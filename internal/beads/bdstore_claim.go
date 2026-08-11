package beads

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Claim is the assignee-aware compare-and-swap acquire of a bead, and the
// acquire dual of [BdStore.ReleaseIfCurrent].
//
// WHY THIS EXISTS SEPARATELY FROM ClaimAsActor. bd's own `update --claim` takes
// no assignee: it claims for whatever actor the invocation is configured with
// (typically BEADS_ACTOR). That is a different operation from "compare-and-swap
// this bead's assignee to X", and the graph-class front door needs the second
// one — its contract promises a single winner under concurrency, an idempotent
// same-holder re-claim that consumes no revision, and a conflict reported as
// ok=false rather than as an error. The storebinding adapter deliberately
// refuses to emulate that with a read-then-write, because doing so would open
// exactly the race the contract exists to close. So the semantic is implemented
// here, on the store, where the datastore's own conditional mutation can carry
// it.
//
// THE ACQUISITION IS THE CONDITIONAL UPDATE, not the read below it. The
// pre-read only CLASSIFIES three cases the SQL cannot distinguish on its own —
// a bead that does not exist, one in a terminal state, and a same-holder
// re-claim that must not write. Acquisition itself is one conditional UPDATE
// whose WHERE clause carries the precondition, and whose affected-row count is
// the CAS result. A pre-read that has gone stale cannot manufacture a win: if
// another holder claimed in the meantime, the UPDATE matches zero rows and this
// reports a conflict.
//
// The predicate admits a bead that is unassigned OR already assigned to this
// same assignee, so a worker can claim work that was routed to it without
// having been started. It never admits a bead held by someone else, and never
// resurrects a closed one.
func (s *BdStore) Claim(id, assignee string) (Bead, bool, error) {
	assignee = strings.TrimSpace(assignee)
	if assignee == "" {
		return Bead{}, false, fmt.Errorf("claiming bead %q: empty assignee", id)
	}

	current, err := s.Get(id)
	if err != nil {
		return Bead{}, false, err
	}
	switch current.Status {
	case "open", "in_progress":
	default:
		// Terminal states are never resurrected by a claim. A conflict, not an
		// error: the caller asked whether it could own this, and the answer is
		// no.
		return current, false, nil
	}

	held := strings.TrimSpace(current.Assignee)
	if held != "" && held != assignee {
		return current, false, nil
	}
	if held == assignee && current.Status == "in_progress" {
		// Same-holder re-claim is a TRUE no-op. Running the UPDATE anyway would
		// move updated_at and burn a revision, and the contract says an
		// idempotent re-claim consumes neither. Returning the stored snapshot
		// is the whole operation.
		return current, true, nil
	}

	rows, err := s.claimCAS(id, assignee)
	if err != nil {
		return Bead{}, false, err
	}
	if rows <= 0 {
		// Lost the race between the read and the swap. Re-read so the caller
		// can report who won rather than guessing.
		if latest, getErr := s.Get(id); getErr == nil {
			return latest, false, nil
		}
		return Bead{}, false, nil
	}
	claimed, err := s.Get(id)
	if err != nil {
		return Bead{}, false, fmt.Errorf("claiming bead %q: reading back the claim: %w", id, err)
	}
	return claimed, true, nil
}

// claimCAS runs the conditional acquire and returns the affected-row count.
//
// It mirrors ReleaseIfCurrent's transport exactly, including the embedded-Dolt
// fallback: the sqlite backend refuses raw DB access and embedded dolt WITHOUT
// a configured directory surfaces ErrConditionalReleaseUnsupported, while
// embedded dolt WITH a directory services the CAS directly and returns real
// rows-affected.
//
// SEAM (bd conditional-claim verb): this rides raw `bd sql` for the same reason
// the release side does. When bd ships a native issueops CAS claim verb, consume
// it HERE as the first attempt and fall back to this path when bd reports the
// command unknown.
func (s *BdStore) claimCAS(id, assignee string) (int, error) {
	query := "UPDATE issues SET status = 'in_progress', assignee = " + bdSQLStringLiteral(assignee) +
		", updated_at = CURRENT_TIMESTAMP" +
		" WHERE id = " + bdSQLStringLiteral(id) +
		" AND status IN ('open', 'in_progress')" +
		" AND (assignee IS NULL OR assignee = '' OR assignee = " + bdSQLStringLiteral(assignee) + ")"

	out, err := s.runBDTransientWriteOutput("sql", "--json", query)
	if err != nil {
		if isBdSQLUnsupportedInEmbeddedMode(err) {
			return s.claimCASViaEmbeddedDoltSQL(id, assignee)
		}
		return 0, fmt.Errorf("bd claim-if-unheld: %w", err)
	}
	var result struct {
		RowsAffected int `json:"rows_affected"`
	}
	if err := json.Unmarshal(extractJSON(out), &result); err != nil {
		return 0, fmt.Errorf("bd claim-if-unheld: parsing SQL result: %w", err)
	}
	return result.RowsAffected, nil
}

func (s *BdStore) claimCASViaEmbeddedDoltSQL(id, assignee string) (int, error) {
	doltDir, ok, err := s.embeddedDoltDir()
	if err != nil {
		return 0, fmt.Errorf("bd claim-if-unheld embedded fallback: %w", err)
	}
	if !ok {
		return 0, fmt.Errorf("bd claim-if-unheld embedded fallback: %w", ErrConditionalReleaseUnsupported)
	}
	query := "UPDATE issues SET status = 'in_progress', assignee = " + bdSQLStringLiteral(assignee) +
		", updated_at = CURRENT_TIMESTAMP" +
		" WHERE id = " + bdSQLStringLiteral(id) +
		" AND status IN ('open', 'in_progress')" +
		" AND (assignee IS NULL OR assignee = '' OR assignee = " + bdSQLStringLiteral(assignee) + ")" +
		"; SELECT ROW_COUNT() AS rows_affected"
	out, err := s.runner(doltDir, "dolt", "sql", "-r", "json", "-q", query)
	if err != nil {
		return 0, fmt.Errorf("bd claim-if-unheld embedded fallback: dolt sql: %w", err)
	}
	rowsAffected, err := parseDoltRowsAffected(out)
	if err != nil {
		return 0, fmt.Errorf("bd claim-if-unheld embedded fallback: parsing SQL result: %w", err)
	}
	return rowsAffected, nil
}
