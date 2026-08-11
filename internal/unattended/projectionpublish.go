package unattended

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GeneratedMarker is the first line of every delivery projection this engine
// writes into a target repository.
//
// It exists because of what the GUK BPM pilot found. The pilot was authorized to
// write `delivery/PROJECT-STATE.yml` on its branch, and by execution time that
// path held a hand-maintained canonical governance record whose own header said
// a human must be able to determine current phase from it. Publishing over it
// would have destroyed a governance document from inside an authorized path.
//
// An authorized path is not an authorized act. This marker is how the publisher
// tells a file it owns from a file it would be taking, and the distinction is
// made by reading the target rather than by trusting configuration.
const GeneratedMarker = "# generated-by: gas-city-delivery-projector"

// Errors the publisher distinguishes. A caller — and the failure classifier —
// needs to tell "somebody else's file is in the way" from "the thing I was
// given to publish is not publishable".
var (
	// ErrTargetNotOurs — the target exists and carries no generated marker, so
	// it belongs to the project. Only a person can decide to replace it.
	ErrTargetNotOurs = errors.New("unattended: the publication target was not written by this projector")

	// ErrProjectionUnusable — the projection to be published is absent, empty or
	// not a project-state document. Publishing it would put something the
	// dashboard cannot read where it expects something it can.
	ErrProjectionUnusable = errors.New("unattended: the projection is not a publishable project-state document")
)

// PublishResult records what a publication did, for the run's journal.
type PublishResult struct {
	// Target is the absolute path written.
	Target string
	// Created is true when the target did not previously exist.
	Created bool
	// ReplacedMalformed is true when the previous target carried this
	// projector's marker but was not a readable project-state document. It is
	// replaced rather than refused — it is this projector's own artifact, and
	// leaving a corrupt file where the dashboard reads one helps nobody — but
	// the fact is recorded rather than silently swallowed.
	ReplacedMalformed bool
	// Unchanged is true when the target already held exactly this content.
	// Publication is structurally idempotent, so a re-run of an unchanged run
	// produces no commit.
	Unchanged bool
}

// looksLikeProjectState is a deliberately shallow structural check.
//
// It is not a schema validator: the projector owns the schema and the dashboard
// owns the parser, and duplicating either here would create a third opinion to
// disagree with. It answers only "is this the kind of document that belongs at
// this path", which is what the publisher needs to avoid installing rubbish.
func looksLikeProjectState(content string) bool {
	return strings.Contains(content, "schemaVersion:") && strings.Contains(content, "project:")
}

// PublishProjection installs a rendered delivery projection into a target
// repository at a repo-relative path, refusing to overwrite anything it did not
// write.
//
// The path is a parameter rather than a constant because projects differ: one
// with no governance layer may want the projection at `delivery/PROJECT-STATE.yml`,
// while GUK BPM already keeps a hand-maintained record there and needs the
// projection somewhere of its own. Baking either choice into the engine would
// make the engine wrong for the other kind of project.
func PublishProjection(repoRoot, relPath string, projection []byte) (PublishResult, error) {
	var res PublishResult

	if strings.TrimSpace(relPath) == "" {
		return res, fmt.Errorf("%w: no publication path was given", ErrProjectionUnusable)
	}
	if filepath.IsAbs(relPath) {
		return res, fmt.Errorf("%w: publication path %q must be relative to the repository", ErrProjectionUnusable, relPath)
	}
	// A path that climbs out of the repository would write somewhere the run
	// was never authorized to touch, which is the whole failure class this
	// package exists to prevent.
	clean := filepath.Clean(relPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return res, fmt.Errorf("%w: publication path %q escapes the repository", ErrProjectionUnusable, relPath)
	}

	body := strings.TrimSpace(string(projection))
	if body == "" {
		return res, fmt.Errorf("%w: the projection is empty", ErrProjectionUnusable)
	}
	if !looksLikeProjectState(body) {
		return res, fmt.Errorf("%w: it has no schemaVersion or project section", ErrProjectionUnusable)
	}

	target := filepath.Join(repoRoot, clean)
	existing, err := os.ReadFile(target)
	switch {
	case err == nil:
		if !strings.Contains(firstLines(string(existing), 5), GeneratedMarker) {
			return res, fmt.Errorf("%w: %s\n\nfirst lines of the existing file:\n%s",
				ErrTargetNotOurs, target, indent(firstLines(string(existing), 5)))
		}
		// It is ours. If it is also unreadable, say so and replace it.
		if !looksLikeProjectState(string(existing)) {
			res.ReplacedMalformed = true
		}
	case os.IsNotExist(err):
		res.Created = true
	default:
		return res, fmt.Errorf("reading publication target %q: %w", target, err)
	}

	content := GeneratedMarker + "\n" + body + "\n"
	if !res.Created && string(existing) == content {
		res.Target = target
		res.Unchanged = true
		return res, nil
	}
	if err := writeFileAtomic(target, []byte(content)); err != nil {
		return res, err
	}
	res.Target = target
	return res, nil
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func indent(s string) string {
	out := strings.Split(s, "\n")
	for i := range out {
		out[i] = "    " + out[i]
	}
	return strings.Join(out, "\n")
}
