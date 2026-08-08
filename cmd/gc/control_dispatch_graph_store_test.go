package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/coordclass"
)

// controlSplitRoutes builds the routes a converged split city runs on: work
// keeps its own ledger and every infrastructure class is served by one shared
// binding — the arrangement storageSplitWhole names and openStorageRoutes
// produces.
func controlSplitRoutes(infra beads.Store) *storageRoutes {
	routes := &storageRoutes{stores: make(map[coordclass.Class]beads.Store), binding: "infra"}
	for _, class := range coordclass.Classes() {
		if class.IsInfrastructure() {
			routes.stores[class] = infra
		}
	}
	return routes
}

// seedCLIStorageRoutes installs routes for cityPath in the one-shot memo, so a
// test can drive the CLI class resolvers without standing up a real binding.
func seedCLIStorageRoutes(t *testing.T, cityPath string, routes *storageRoutes) {
	t.Helper()
	resetCLIStorageRoutes(t)
	entry := cliStorageRoutesEntryFor(filepath.Clean(cityPath))
	entry.once.Do(func() { entry.routes = routes })
}

// newOrphanControlFixture writes a control bead whose workflow root was
// canceled. Both are graph class. The returned ids are identical in whichever
// store they are written to, so the same fixture models the live binding copy
// and the stale copy the migration retained in the work ledger.
func newControlBead(t *testing.T, store beads.Store, rootID string) beads.Bead {
	t.Helper()
	bead, err := store.Create(beads.Bead{
		Title: "check 1",
		Type:  "task",
		Metadata: map[string]string{
			beadmeta.KindMetadataKey:          "check",
			beadmeta.RootBeadIDMetadataKey:    rootID,
			beadmeta.RootStoreRefMetadataKey:  "city:test-city",
			beadmeta.StepIDMetadataKey:        "implement",
			beadmeta.CheckModeMetadataKey:     "exec",
			beadmeta.MaxAttemptsMetadataKey:   "1",
			beadmeta.LogicalBeadIDMetadataKey: "logical-1",
		},
	})
	if err != nil {
		t.Fatalf("create control bead: %v", err)
	}
	return bead
}

func beadByID(t *testing.T, store beads.Store, id string) beads.Bead {
	t.Helper()
	b, err := store.Get(id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return b
}

// TestControlDispatchReadsAndWritesTheGraphStoreOnSplitCity pins both halves of
// the control-dispatch class hop on a split city.
//
// The fixture is the shape a converged city actually has: the migration COPIES
// infrastructure into the binding and retains the source, so the same control
// bead id exists in both stores. Only the binding's copy is live.
//
// Both directions are asserted, and the disposition is what separates them.
// The dispatcher closes a control bead differently depending on where it finds
// the workflow root: root present and canceled means gc.outcome=canceled, root
// absent means gc.final_disposition=orphaned_workflow. Writing the canceled root
// ONLY into the graph store therefore makes the outcome a direct readout of
// which store the dispatcher READ. Which copy ends up closed is the readout of
// which store it WROTE.
func TestControlDispatchReadsAndWritesTheGraphStoreOnSplitCity(t *testing.T) {
	cityPath := t.TempDir()
	scopeStore := beads.NewMemStore()
	graphStore := beads.NewMemStore()
	seedCLIStorageRoutes(t, cityPath, controlSplitRoutes(graphStore))

	// Both stores hold the workflow root, because the migration copied it and
	// retained the source. Only the BINDING's copy carries the cancellation:
	// the operator canceled after cutover, through the graph-routed API, so the
	// retained source still holds the pre-cutover version.
	staleRoot, err := scopeStore.Create(beads.Bead{
		Title:    "workflow",
		Type:     "task",
		Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWorkflow},
	})
	if err != nil {
		t.Fatalf("create retained workflow root: %v", err)
	}
	root, err := graphStore.Create(beads.Bead{
		Title: "workflow",
		Type:  "task",
		Metadata: map[string]string{
			beadmeta.KindMetadataKey:            beadmeta.KindWorkflow,
			beadmeta.CancelRequestedMetadataKey: "operator",
		},
	})
	if err != nil {
		t.Fatalf("create workflow root: %v", err)
	}
	if staleRoot.ID != root.ID {
		t.Fatalf("fixture root ids diverged (%s vs %s)", staleRoot.ID, root.ID)
	}
	stale := newControlBead(t, scopeStore, root.ID)
	live := newControlBead(t, graphStore, root.ID)
	if live.ID != stale.ID {
		t.Fatalf("fixture ids diverged (%s vs %s); the retained copy must share the binding copy's id", live.ID, stale.ID)
	}

	cfg := &config.City{Workspace: config.Workspace{Name: "test-city"}}
	var stdout, stderr bytes.Buffer
	if err := runControlDispatcherWithStoreAndConfig(cityPath, cityPath, scopeStore, live, live.ID, cfg, &stdout, &stderr); err != nil {
		t.Fatalf("control dispatch: %v", err)
	}

	// READ: the root was resolved from the graph store, so the control bead was
	// closed as canceled rather than as an orphan of a missing root.
	got := beadByID(t, graphStore, live.ID)
	if disposition := got.Metadata[beadmeta.FinalDispositionMetadataKey]; disposition == beadmeta.DispositionOrphanedWorkflow {
		t.Fatalf("control bead closed as %q; the dispatcher read the scope store, where the canceled root does not exist", disposition)
	}
	if outcome := got.Metadata[beadmeta.OutcomeMetadataKey]; outcome != beadmeta.OutcomeCanceled {
		t.Fatalf("gc.outcome = %q, want %q from the canceled root in the graph binding", outcome, beadmeta.OutcomeCanceled)
	}

	// WRITE: the binding's copy advanced.
	if got.Status != "closed" {
		t.Fatalf("graph-store control bead status = %q, want closed; the dispatcher's mutation went somewhere else", got.Status)
	}

	// And the retained source did NOT: a write there is invisible to every
	// graph-routed reader and becomes a strand at the next boot.
	if stayed := beadByID(t, scopeStore, stale.ID); stayed.Status == "closed" {
		t.Fatalf("retained work-store copy was closed too; control mutations must not land in the source the migration left behind")
	}
}

// TestControlDispatchSingleStoreUsesTheOneStore is the compatibility guarantee.
// A city that relocates nothing routes nothing, so control dispatch runs against
// the exact scope store it always did — same instance, so the bd command runner,
// the scope issue prefix and the optional-capability assertions the scope-skip
// paths make (DepListBatch, UpdateAll) all still land on the store they used to.
//
// Green before and after by design; its teeth are proven by mutation.
func TestControlDispatchSingleStoreUsesTheOneStore(t *testing.T) {
	cityPath := t.TempDir()
	store := beads.NewMemStore()
	seedCLIStorageRoutes(t, cityPath, nil)

	if got := controlGraphStore(cityPath, nil, store); got != beads.Store(store) {
		t.Fatalf("controlGraphStore returned %T(%p), want the identical scope store %p", got, got, store)
	}

	root, err := store.Create(beads.Bead{
		Title: "workflow",
		Type:  "task",
		Metadata: map[string]string{
			beadmeta.KindMetadataKey:            beadmeta.KindWorkflow,
			beadmeta.CancelRequestedMetadataKey: "operator",
		},
	})
	if err != nil {
		t.Fatalf("create workflow root: %v", err)
	}
	control := newControlBead(t, store, root.ID)

	cfg := &config.City{Workspace: config.Workspace{Name: "test-city"}}
	var stdout, stderr bytes.Buffer
	if err := runControlDispatcherWithStoreAndConfig(cityPath, cityPath, store, control, control.ID, cfg, &stdout, &stderr); err != nil {
		t.Fatalf("control dispatch: %v", err)
	}
	got := beadByID(t, store, control.ID)
	if got.Status != "closed" {
		t.Fatalf("control bead status = %q, want closed in the single store", got.Status)
	}
	if outcome := got.Metadata[beadmeta.OutcomeMetadataKey]; outcome != beadmeta.OutcomeCanceled {
		t.Fatalf("gc.outcome = %q, want %q", outcome, beadmeta.OutcomeCanceled)
	}
}
