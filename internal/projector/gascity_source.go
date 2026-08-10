package projector

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// This file is the ADAPTER between Gas City's event names and the projector's
// vocabulary. The split is deliberate: the projector's semantics
// (started/finished/failed, idempotence, cursor ordering) are stable and
// testable on their own, while the engine's event names are the engine's to
// change. Folding the names into Apply would make every rename a semantics
// change.

// ReadCityEvents parses a Gas City events.jsonl into projector events, mapping
// engine event names onto the projector's vocabulary.
//
// MAPPING, and why each choice is the honest one:
//
//   - bead.closed  -> work.finished. Closure is the engine's terminal fact.
//   - bead.updated -> work.started, but ONLY from an actor that is not the
//     cache reconciler. The reconciler rewrites bead rows as a cache-coherence
//     side effect; treating those as execution would date a task's start from
//     housekeeping rather than from work. First-write-wins in Apply then makes
//     the earliest genuine update the start.
//   - everything else is ignored rather than guessed at. An event whose meaning
//     the projection does not know is not evidence of anything.
//
// Events for subjects with no task in the state are skipped by Apply, so a city
// log containing unrelated work projects nothing for it.
func ReadCityEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening event log %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only

	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var env struct {
			Seq     uint64 `json:"seq"`
			Type    string `json:"type"`
			Ts      string `json:"ts"`
			Actor   string `json:"actor"`
			Subject string `json:"subject"`
			// Only the one documented field the mapping needs is read out of
			// the payload. This is not a general map[string]any crawl: the
			// status is what distinguishes a start from any other update.
			Payload struct {
				Status string `json:"status"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue // a malformed line is not a fact; it is noise
		}
		mapped, ok := mapCityEventType(env.Type, env.Payload.Status)
		if !ok || env.Subject == "" {
			continue
		}
		ts, err := parseEventTime(env.Ts)
		if err != nil {
			continue
		}
		out = append(out, Event{Seq: env.Seq, Type: mapped, Ts: ts, Subject: env.Subject, Actor: env.Actor})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading event log %s: %w", path, err)
	}
	return out, nil
}

// parseEventTime accepts the engine's RFC3339 timestamps, which carry a zone
// offset. A timestamp that will not parse makes the event unusable rather than
// dateable to now: guessing a time would put a fabricated date into a durable
// projection.
func parseEventTime(raw string) (time.Time, error) {
	return time.Parse(time.RFC3339, strings.TrimSpace(raw))
}

// mapCityEventType maps an engine event onto the projector's vocabulary using
// the STATUS the event carries, not the actor that published it.
//
// The actor is the wrong discriminator, and trying it proved so: in a real city
// every bead event on the log is published by the cache reconciler, which
// reports the store's transitions rather than performing the work. Filtering on
// the actor left ZERO events and produced a projection of a completed run with
// no execution times at all — a silently empty result, which is the worst kind
// of wrong.
//
// The status the event carries is the fact. A bead moving to in_progress is a
// start; a bead closing is a finish. Who published it is provenance, not
// meaning.
func mapCityEventType(eventType, payloadStatus string) (string, bool) {
	switch eventType {
	case "bead.closed":
		return "work.finished", true
	case "bead.updated":
		if strings.TrimSpace(payloadStatus) == "in_progress" {
			return "work.started", true
		}
		return "", false
	default:
		return "", false
	}
}
