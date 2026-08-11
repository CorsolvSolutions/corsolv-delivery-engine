package unattended

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func journalPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state", JournalName)
}

func TestJournalAppendsDurablyAndNumbersInOrder(t *testing.T) {
	path := journalPath(t)
	j, err := OpenJournal(path, "run-1")
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	for _, k := range []RecordKind{RecordRunStarted, RecordTaskStarted, RecordTaskSucceeded} {
		if _, err := j.Append(Record{Kind: k, TaskID: "t"}); err != nil {
			t.Fatalf("Append(%s): %v", k, err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records, truncated, err := ReadJournal(path)
	if err != nil || truncated {
		t.Fatalf("ReadJournal: truncated=%v err=%v", truncated, err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3", len(records))
	}
	for i, r := range records {
		if r.Seq != i+1 {
			t.Fatalf("record %d has seq %d", i, r.Seq)
		}
		if r.RunID != "run-1" {
			t.Fatalf("record %d lost its run id", i)
		}
		if r.At.IsZero() {
			t.Fatalf("record %d has no timestamp", i)
		}
	}
}

func TestJournalContinuesRatherThanReplacing(t *testing.T) {
	// A resumed run must add to the record of what happened, not overwrite it.
	path := journalPath(t)
	first, err := OpenJournal(path, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	first.Append(Record{Kind: RecordRunStarted})  //nolint:errcheck
	first.Append(Record{Kind: RecordTaskStarted}) //nolint:errcheck
	first.Close()                                 //nolint:errcheck

	second, err := OpenJournal(path, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := second.Append(Record{Kind: RecordTaskSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	second.Close() //nolint:errcheck

	if rec.Seq != 3 {
		t.Fatalf("resumed sequence = %d, want 3 — a resume must not restart numbering", rec.Seq)
	}
	records, _, err := ReadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records after resume, want 3 — history was lost", len(records))
	}
}

func TestJournalToleratesATruncatedTailRecord(t *testing.T) {
	// The signature of a crash between write and sync. It means precisely "this
	// record never became durable", which is not corruption.
	path := journalPath(t)
	j, err := OpenJournal(path, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	j.Append(Record{Kind: RecordRunStarted})               //nolint:errcheck
	j.Append(Record{Kind: RecordTaskStarted, TaskID: "t"}) //nolint:errcheck
	j.Close()                                              //nolint:errcheck

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"seq":3,"kind":"task-succ`) //nolint:errcheck
	f.Close()                                   //nolint:errcheck

	records, truncated, err := ReadJournal(path)
	if err != nil {
		t.Fatalf("a truncated tail must not be an error: %v", err)
	}
	if !truncated {
		t.Fatal("a truncated tail must be reported, not silently dropped")
	}
	if len(records) != 2 {
		t.Fatalf("got %d durable records, want 2", len(records))
	}
}

func TestJournalRefusesCorruptionInTheMiddle(t *testing.T) {
	// A bad line that is not the last one means something rewrote history. A
	// resume built on it would silently skip or repeat real work.
	path := journalPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"seq":1,"kind":"run-started","runId":"r"}` + "\n" +
		`not json at all` + "\n" +
		`{"seq":3,"kind":"run-finished","runId":"r"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadJournal(path); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("ReadJournal = %v, want ErrJournalCorrupt", err)
	}
}

func TestReadingAnAbsentJournalIsNotAnError(t *testing.T) {
	records, truncated, err := ReadJournal(filepath.Join(t.TempDir(), "never-written.jsonl"))
	if err != nil || truncated || len(records) != 0 {
		t.Fatalf("absent journal: records=%d truncated=%v err=%v", len(records), truncated, err)
	}
}

func TestReplayReconstructsWhatWasDurablyTrue(t *testing.T) {
	records := []Record{
		{Seq: 1, Kind: RecordRunStarted},
		{Seq: 2, Kind: RecordTaskStarted, TaskID: "a"},
		{Seq: 3, Kind: RecordTaskSucceeded, TaskID: "a"},
		{Seq: 4, Kind: RecordTaskStarted, TaskID: "b"},
		{Seq: 5, Kind: RecordTaskFailed, TaskID: "b"},
		{Seq: 6, Kind: RecordTaskStarted, TaskID: "c"}, // crashed here
	}
	st := Replay(records, "")

	if !st.Succeeded["a"] {
		t.Fatal("a durably succeeded task must be known succeeded")
	}
	if st.Interrupted["a"] || st.Interrupted["b"] {
		t.Fatal("a task that reached a terminal record is not interrupted")
	}
	if !st.Interrupted["c"] {
		t.Fatal("a task that started and never finished must be reported interrupted")
	}
	if st.Attempts["c"] != 1 {
		t.Fatalf("attempts for c = %d, want 1", st.Attempts["c"])
	}
	if st.Finished {
		t.Fatal("a run with no finish record must not read as finished")
	}
	if st.LastSeq != 6 {
		t.Fatalf("last seq = %d, want 6", st.LastSeq)
	}
}

func TestReplayIsIdempotent(t *testing.T) {
	records := []Record{
		{Seq: 1, Kind: RecordTaskStarted, TaskID: "a"},
		{Seq: 2, Kind: RecordTaskSucceeded, TaskID: "a"},
	}
	once := Replay(records, "")
	twice := Replay(append([]Record{}, records...), "")
	if once.Attempts["a"] != twice.Attempts["a"] || once.Succeeded["a"] != twice.Succeeded["a"] {
		t.Fatal("replaying the same records twice produced different state")
	}
}

func TestReplayIgnoresOtherRunsInTheSameJournal(t *testing.T) {
	// A state directory belongs to a project, not to a run, so one journal
	// accumulates every run the project has had. The endurance run found this
	// the hard way: it shared a state directory with the run before it, three
	// task IDs matched, and it began by skipping work it had never done.
	records := []Record{
		{Seq: 1, Kind: RecordTaskSucceeded, RunID: "an-earlier-run", TaskID: "shared-id"},
		{Seq: 2, Kind: RecordTaskStarted, RunID: "an-earlier-run", TaskID: "also-shared"},
		{Seq: 3, Kind: RecordTaskSucceeded, RunID: "this-run", TaskID: "mine"},
	}
	st := Replay(records, "this-run")

	if st.Succeeded["shared-id"] {
		t.Fatal("another run's success was credited to this one; it would skip work it never did")
	}
	if st.Attempts["also-shared"] != 0 || st.Interrupted["also-shared"] {
		t.Fatal("another run's attempts were charged to this one")
	}
	if !st.Succeeded["mine"] {
		t.Fatal("this run's own success was lost")
	}
	// The sequence is a property of the file, not of a run: continuing must not
	// re-use numbers another run already wrote.
	if st.LastSeq != 3 {
		t.Fatalf("last seq = %d, want 3 across the whole journal", st.LastSeq)
	}
}

func TestReplayCountsEveryAttemptSoACrashLoopIsBounded(t *testing.T) {
	// Three crashes mid-task must consume three attempts, or a run that crashes
	// during the same task forever retries forever, one crash at a time.
	var records []Record
	for i := 1; i <= 3; i++ {
		records = append(records, Record{Seq: i, Kind: RecordTaskStarted, TaskID: "crashy"})
	}
	if got := Replay(records, "").Attempts["crashy"]; got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestJournalRecordsAreOneLineEach(t *testing.T) {
	// The truncated-tail recovery depends on it: a record spanning lines would
	// make a partial write indistinguishable from corruption.
	path := journalPath(t)
	j, err := OpenJournal(path, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	j.Append(Record{Kind: RecordTaskFailed, TaskID: "t", Detail: "a detail\nwith an embedded newline"}) //nolint:errcheck
	j.Close()                                                                                           //nolint:errcheck

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.TrimRight(string(data), "\n"), "\n"); n != 0 {
		t.Fatalf("one record produced %d line breaks; multiline records break tail recovery", n+1)
	}
}
