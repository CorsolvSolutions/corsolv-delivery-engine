package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE GAP THESE TESTS PIN. A plan is never rewritten because merged work was
// measured against it — but a mis-planned delivery whose run failed before
// dispatch has a plan that measured NOTHING, and it had no governed way
// forward: invalidate and remediate repair criteria that were reported met,
// and none ever was. SupersedeUnexecutedPlan is the one governed exception,
// guarded by evidence and archiving what it replaces.

func supersedeStatus(state DeliveryState) Status {
	return Status{
		ProjectID: reconTestIntent().ProjectID,
		State:     state,
		Live:      false,
	}
}

// A failed delivery with zero package evidence may have its plan superseded:
// the new plan installs, and the superseded one is archived beside it rather
// than destroyed.
func TestAnUnexecutedPlanMayBeSupersededAndIsArchived(t *testing.T) {
	root := t.TempDir()
	in := reconTestIntent()
	first := reconTestPlan()
	if err := SavePlan(root, first); err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(PlanPath(root, in.ProjectID))
	if err != nil {
		t.Fatal(err)
	}

	next := reconTestPlan()
	next.Packages = append([]WorkPackage(nil), next.Packages...)
	next.Packages[0].Title = "the corrected first package"

	if err := SupersedeUnexecutedPlan(root, next, supersedeStatus(StateFailed)); err != nil {
		t.Fatalf("superseding an unexecuted plan: %v", err)
	}

	archived, err := os.ReadFile(filepath.Join(root, in.ProjectID, SupersededPlanName(1)))
	if err != nil {
		t.Fatalf("the superseded plan was not archived: %v", err)
	}
	if string(archived) != string(firstBytes) {
		t.Fatal("the archive is not byte-identical to the plan that was superseded")
	}

	loaded, hasPlan, err := LoadPlan(root, in)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPlan {
		t.Fatal("the superseding plan did not install")
	}
	if loaded.Packages[0].Title != "the corrected first package" {
		t.Fatalf("LoadPlan returned %q, want the superseding plan", loaded.Packages[0].Title)
	}
}

// A second supersession archives at the next slot — nothing is overwritten.
func TestEachSupersededPlanKeepsItsOwnArchiveSlot(t *testing.T) {
	root := t.TempDir()
	in := reconTestIntent()
	if err := SavePlan(root, reconTestPlan()); err != nil {
		t.Fatal(err)
	}
	second := reconTestPlan()
	second.Packages = append([]WorkPackage(nil), second.Packages...)
	second.Packages[0].Title = "second"
	if err := SupersedeUnexecutedPlan(root, second, supersedeStatus(StateFailed)); err != nil {
		t.Fatal(err)
	}
	third := reconTestPlan()
	third.Packages = append([]WorkPackage(nil), third.Packages...)
	third.Packages[0].Title = "third"
	if err := SupersedeUnexecutedPlan(root, third, supersedeStatus(StateFailed)); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 2; n++ {
		if _, err := os.Stat(filepath.Join(root, in.ProjectID, SupersededPlanName(n))); err != nil {
			t.Fatalf("archive slot %d: %v", n, err)
		}
	}
}

// Evidence of execution refuses the swap: a complete package, a merged-but-
// ungated package, a live run, or a delivery whose run finished at all.
func TestExecutedOrLiveDeliveriesRefuseSupersession(t *testing.T) {
	cases := []struct {
		name string
		st   Status
		want string
	}{
		{
			name: "a complete package",
			st: func() Status {
				s := supersedeStatus(StateFailed)
				s.Evidence.CompletePackages = []string{"wp-anything"}
				return s
			}(),
			want: "completed against the standing plan",
		},
		{
			name: "a merged package without its gate",
			st: func() Status {
				s := supersedeStatus(StateFailed)
				s.Evidence.GateNotMet = []string{"wp-anything"}
				return s
			}(),
			want: "merged without their completion gate",
		},
		{
			name: "a live run",
			st: func() Status {
				s := supersedeStatus(StateFailed)
				s.Live = true
				return s
			}(),
			want: "may be mid-dispatch",
		},
		{
			name: "a blocked delivery",
			st:   supersedeStatus(StateBlocked),
			want: "run finished executed its packages",
		},
		{
			name: "a completed delivery",
			st:   supersedeStatus(StateCompleted),
			want: "run finished executed its packages",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			first := reconTestPlan()
			if err := SavePlan(root, first); err != nil {
				t.Fatal(err)
			}
			next := reconTestPlan()
			next.Packages = append([]WorkPackage(nil), next.Packages...)
			next.Packages[0].Title = "corrected"

			err := SupersedeUnexecutedPlan(root, next, tc.st)
			if !errors.Is(err, ErrRecordConflict) {
				t.Fatalf("err = %v, want ErrRecordConflict", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not say %q", err.Error(), tc.want)
			}

			// And the standing plan is untouched.
			loaded, _, lerr := LoadPlan(root, reconTestIntent())
			if lerr != nil {
				t.Fatal(lerr)
			}
			if loaded.Packages[0].Title != first.Packages[0].Title {
				t.Fatal("a refused supersession changed the standing plan")
			}
		})
	}
}

// SavePlan's own refusal is untouched: the direct path still never rewrites.
func TestSavePlanStillRefusesToRewrite(t *testing.T) {
	root := t.TempDir()
	if err := SavePlan(root, reconTestPlan()); err != nil {
		t.Fatal(err)
	}
	changed := reconTestPlan()
	changed.Packages = append([]WorkPackage(nil), changed.Packages...)
	changed.Packages[0].Title = "changed"
	if err := SavePlan(root, changed); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("err = %v, want ErrRecordConflict", err)
	}
}
