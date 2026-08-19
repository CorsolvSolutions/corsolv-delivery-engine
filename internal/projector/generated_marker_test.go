package projector

import (
	"strings"
	"testing"
)

// The marker is the consumer's admission test, so it is asserted as a first
// line and not merely as "present somewhere in the header".
//
// THE DEFECT. The dashboard refuses to read a document at the projection path
// unless it begins with exactly this line, because that path can also hold a
// hand-maintained governance record. This package wrote a different banner, so
// every projection it ever published was declined — and declined silently, in
// the only way a reader can decline a file it does not recognize. A pilot
// project sat at "0 of 7 deliverables complete" over a current, correct
// projection saying six were done.
func TestRenderBeginsWithTheMarkerTheConsumerAdmits(t *testing.T) {
	data, err := Render(NewState("p"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	const want = "# generated-by: gas-city-delivery-projector\n"
	if GeneratedMarker != want {
		t.Errorf("GeneratedMarker = %q, want %q — this is the dashboard's constant, not ours to reword", GeneratedMarker, want)
	}
	if !strings.HasPrefix(string(data), want) {
		first := strings.SplitN(string(data), "\n", 2)[0]
		t.Fatalf("the projection's first line is %q; the consumer admits a document only when it starts with %q",
			first, strings.TrimSuffix(want, "\n"))
	}
}

// The banner a person reads survives beside the marker a machine reads. Losing
// it would trade one audience for the other.
func TestRenderStillTellsAReaderNotToEditIt(t *testing.T) {
	data, err := Render(NewState("p"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(data), "DO NOT EDIT") {
		t.Error("the projection must still say, in words, that hand edits are overwritten")
	}
}
