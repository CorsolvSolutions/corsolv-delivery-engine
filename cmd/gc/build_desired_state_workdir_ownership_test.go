package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
)

// TestWorkDirStampHasOwnershipEvidenceIsFailClosed pins the guard that decides
// whether reconciliation may mirror an observed session workdir onto a
// pool-managed bead's canonical gc.work_dir.
//
// The contract it enforces: the worktree CREATOR writes the legacy artifact
// path first, and reconciliation may only mirror that value. A pool session
// merely observes a slot cwd — it neither creates nor owns a managed worktree —
// so manufacturing gc.work_dir from that observation would turn a slot label
// into incomplete worktree-ownership evidence and make demand fail closed.
//
// This is load-bearing for per-task worktree acceptance: gc.work_dir appearing
// on a pool-managed bead is itself the proof that the controller's pre-dispatch
// legacy stamp matched the workdir the live session was actually started in.
// If the guard ever softened to "any observed workdir is evidence", that proof
// would evaporate silently and every downstream ownership assertion would keep
// reporting PASS. Hence a test that fails on ANY loosening, not just on the
// obvious one.
func TestWorkDirStampHasOwnershipEvidenceIsFailClosed(t *testing.T) {
	const owned = "/city/.gc/worktrees/rig/worker-a"

	tests := []struct {
		name     string
		metadata map[string]string
		workDir  string
		want     bool
	}{
		{
			name:     "legacy stamp matches the observed workdir",
			metadata: map[string]string{beadmeta.LegacyWorkDirMetadataKey: owned},
			workDir:  owned,
			want:     true,
		},
		{
			name:     "no metadata at all",
			metadata: nil,
			workDir:  owned,
			want:     false,
		},
		{
			name:     "legacy stamp absent",
			metadata: map[string]string{},
			workDir:  owned,
			want:     false,
		},
		{
			name:     "legacy stamp empty",
			metadata: map[string]string{beadmeta.LegacyWorkDirMetadataKey: ""},
			workDir:  owned,
			want:     false,
		},
		{
			name:     "legacy stamp is whitespace only",
			metadata: map[string]string{beadmeta.LegacyWorkDirMetadataKey: "   "},
			workDir:  owned,
			want:     false,
		},
		{
			// The attempt-freshness case: a work_dir left behind by an earlier
			// attempt must not license a stamp for the directory this session
			// actually landed in.
			name:     "legacy stamp names a different (stale) directory",
			metadata: map[string]string{beadmeta.LegacyWorkDirMetadataKey: "/city/.gc/worktrees/rig/worker-a-previous"},
			workDir:  owned,
			want:     false,
		},
		{
			// A canonical gc.work_dir already present is NOT ownership
			// evidence for itself; only the legacy creator-written key is.
			name:     "canonical key present but legacy key absent",
			metadata: map[string]string{beadmeta.WorkDirMetadataKey: owned},
			workDir:  owned,
			want:     false,
		},
		{
			name:     "observed workDir is empty",
			metadata: map[string]string{beadmeta.LegacyWorkDirMetadataKey: owned},
			workDir:  "",
			want:     false,
		},
		{
			name:     "prefix match is not a match",
			metadata: map[string]string{beadmeta.LegacyWorkDirMetadataKey: owned},
			workDir:  owned + "/nested",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workDirStampHasOwnershipEvidence(tt.metadata, tt.workDir); got != tt.want {
				t.Fatalf("workDirStampHasOwnershipEvidence(%v, %q) = %v, want %v",
					tt.metadata, tt.workDir, got, tt.want)
			}
		})
	}
}
