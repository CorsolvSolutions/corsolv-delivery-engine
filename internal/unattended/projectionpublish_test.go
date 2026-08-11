package unattended

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The GUK BPM pilot's central finding, made permanent. Every case below is a
// shape the pilot actually met or could have met, and case 1 is the one that
// would have destroyed a governance document.

const sampleProjection = "schemaVersion: 1\nproject:\n  projectId: \"p\"\n"

// The real thing: GUK BPM's hand-maintained record. It parses as a project-state
// document, which is exactly why a naive "is this the right shape" check would
// have happily overwritten it.
const humanGovernanceFile = `# GUK BPM canonical active-state record.
#
# Purpose: a new Claude chat (or any human) must be able to determine
# current phase, active task, active branch/PR from THIS FILE.
schemaVersion: 1
project:
  projectId: GUKBPM-1
  currentPhase: "2-critical-security-and-uat-blockers"
`

func TestPublishRefusesAHumanOwnedGovernanceFile(t *testing.T) {
	// Case 1. The path is authorized; the act is not.
	repo := t.TempDir()
	target := filepath.Join(repo, "delivery", "PROJECT-STATE.yml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(humanGovernanceFile), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := PublishProjection(repo, "delivery/PROJECT-STATE.yml", []byte(sampleProjection))
	if !errors.Is(err, ErrTargetNotOurs) {
		t.Fatalf("publish over a human-owned file = %v, want ErrTargetNotOurs", err)
	}
	// The refusal must name what it would have taken, or nobody can judge it.
	if !strings.Contains(err.Error(), "canonical active-state record") {
		t.Fatalf("the refusal must quote the existing file:\n%v", err)
	}

	after, rerr := os.ReadFile(target)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(after) != humanGovernanceFile {
		t.Fatal("the governance file was modified by a refused publication")
	}
}

func TestPublishCreatesAnAbsentTarget(t *testing.T) {
	// Case 2. Including the parent directory, which will not exist the first
	// time a project publishes.
	repo := t.TempDir()
	res, err := PublishProjection(repo, "delivery/gascity/PROJECT-STATE.yml", []byte(sampleProjection))
	if err != nil {
		t.Fatalf("PublishProjection: %v", err)
	}
	if !res.Created {
		t.Fatal("a first publication must report that it created the target")
	}
	content, err := os.ReadFile(filepath.Join(repo, "delivery", "gascity", "PROJECT-STATE.yml"))
	if err != nil {
		t.Fatalf("target not written: %v", err)
	}
	if !strings.HasPrefix(string(content), GeneratedMarker) {
		t.Fatalf("the marker must be the first line, got:\n%s", content)
	}
	if !strings.Contains(string(content), "schemaVersion: 1") {
		t.Fatal("the projection body was lost")
	}
}

func TestPublishUpdatesItsOwnPreviousArtifact(t *testing.T) {
	// Case 3. A second run must be able to update what the first run wrote.
	repo := t.TempDir()
	if _, err := PublishProjection(repo, "delivery/gascity/PROJECT-STATE.yml", []byte(sampleProjection)); err != nil {
		t.Fatal(err)
	}
	next := "schemaVersion: 1\nproject:\n  projectId: \"p\"\n  currentPhase: \"later\"\n"
	res, err := PublishProjection(repo, "delivery/gascity/PROJECT-STATE.yml", []byte(next))
	if err != nil {
		t.Fatalf("updating our own artifact: %v", err)
	}
	if res.Created || res.Unchanged {
		t.Fatalf("an update must report neither created nor unchanged: %+v", res)
	}
	content, _ := os.ReadFile(filepath.Join(repo, "delivery", "gascity", "PROJECT-STATE.yml"))
	if !strings.Contains(string(content), "later") {
		t.Fatal("the update did not land")
	}
}

func TestPublishIsStructurallyIdempotent(t *testing.T) {
	// Re-publishing identical content must produce no change, so an unchanged
	// run produces no commit and no dashboard churn.
	repo := t.TempDir()
	if _, err := PublishProjection(repo, "delivery/gascity/PROJECT-STATE.yml", []byte(sampleProjection)); err != nil {
		t.Fatal(err)
	}
	res, err := PublishProjection(repo, "delivery/gascity/PROJECT-STATE.yml", []byte(sampleProjection))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Unchanged {
		t.Fatal("republishing identical content must report unchanged")
	}
}

func TestPublishRefusesATargetWithoutTheMarker(t *testing.T) {
	// Case 4. Anything at the configured path that this projector did not write
	// is somebody else's, whatever it looks like.
	repo := t.TempDir()
	target := filepath.Join(repo, "delivery", "gascity", "PROJECT-STATE.yml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("schemaVersion: 1\nproject:\n  projectId: \"someone-else\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishProjection(repo, "delivery/gascity/PROJECT-STATE.yml", []byte(sampleProjection)); !errors.Is(err, ErrTargetNotOurs) {
		t.Fatalf("unmarked target = %v, want ErrTargetNotOurs", err)
	}
}

func TestPublishReplacesItsOwnMalformedArtifactAndSaysSo(t *testing.T) {
	// Case 5. A corrupt file bearing our marker is ours to repair — leaving it
	// where the dashboard reads helps nobody — but the repair is recorded
	// rather than silently performed.
	repo := t.TempDir()
	target := filepath.Join(repo, "delivery", "gascity", "PROJECT-STATE.yml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(GeneratedMarker+"\n<<<<<<< truncated mid-write\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := PublishProjection(repo, "delivery/gascity/PROJECT-STATE.yml", []byte(sampleProjection))
	if err != nil {
		t.Fatalf("replacing our own malformed artifact: %v", err)
	}
	if !res.ReplacedMalformed {
		t.Fatal("replacing a malformed artifact must be reported, not swallowed")
	}
	content, _ := os.ReadFile(target)
	if strings.Contains(string(content), "truncated") {
		t.Fatal("the malformed content survived")
	}
}

func TestPublishRefusesAnUnusableProjection(t *testing.T) {
	// Publishing something the dashboard cannot read, where it expects
	// something it can, is worse than publishing nothing.
	repo := t.TempDir()
	for _, tc := range []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"whitespace", "   \n\n"},
		{"not a state document", "hello: world\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PublishProjection(repo, "delivery/gascity/PROJECT-STATE.yml", []byte(tc.body)); !errors.Is(err, ErrProjectionUnusable) {
				t.Fatalf("publish(%q) = %v, want ErrProjectionUnusable", tc.name, err)
			}
		})
	}
}

func TestPublishRefusesAPathThatEscapesTheRepository(t *testing.T) {
	repo := t.TempDir()
	for _, p := range []string{"../outside.yml", "/etc/passwd", "delivery/../../escape.yml", ""} {
		if _, err := PublishProjection(repo, p, []byte(sampleProjection)); !errors.Is(err, ErrProjectionUnusable) {
			t.Fatalf("publish to %q = %v, want refusal", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(repo), "outside.yml")); err == nil {
		t.Fatal("a path escaping the repository was written")
	}
}

func TestPublishNeverTouchesTheGovernanceFileWhilePublishingItsOwn(t *testing.T) {
	// Case 6's precondition, at the filesystem level: the two authorities
	// coexist, and writing one does not disturb the other.
	repo := t.TempDir()
	governance := filepath.Join(repo, "delivery", "PROJECT-STATE.yml")
	if err := os.MkdirAll(filepath.Dir(governance), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(governance, []byte(humanGovernanceFile), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := PublishProjection(repo, "delivery/gascity/PROJECT-STATE.yml", []byte(sampleProjection)); err != nil {
		t.Fatalf("PublishProjection: %v", err)
	}

	after, _ := os.ReadFile(governance)
	if string(after) != humanGovernanceFile {
		t.Fatal("publishing the execution projection modified the governance file")
	}
	if _, err := os.Stat(filepath.Join(repo, "delivery", "gascity", "PROJECT-STATE.yml")); err != nil {
		t.Fatalf("the execution projection was not written: %v", err)
	}
}
