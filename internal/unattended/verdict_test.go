package unattended

import "testing"

func TestOutcomeSeverityOrdersWorstLast(t *testing.T) {
	ordered := []Outcome{OutcomePass, OutcomeHumanBoundary, OutcomeNotReached, OutcomeFail}
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1].severity() >= ordered[i].severity() {
			t.Fatalf("%s must be less severe than %s", ordered[i-1], ordered[i])
		}
	}
}

func TestWorstOutcomeIsTheWorstConstituent(t *testing.T) {
	cases := []struct {
		name string
		in   []Outcome
		want Outcome
	}{
		{"all pass", []Outcome{OutcomePass, OutcomePass}, OutcomePass},
		{"one boundary", []Outcome{OutcomePass, OutcomeHumanBoundary}, OutcomeHumanBoundary},
		{"boundary loses to not-reached", []Outcome{OutcomeHumanBoundary, OutcomeNotReached}, OutcomeNotReached},
		{"fail dominates", []Outcome{OutcomeNotReached, OutcomeFail, OutcomePass}, OutcomeFail},
		{"empty proves nothing", nil, OutcomeNotReached},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WorstOutcome(tc.in); got != tc.want {
				t.Fatalf("WorstOutcome(%v) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestNotReachedNeverPasses(t *testing.T) {
	// The single rule the whole reporting layer exists to protect: a check that
	// did not execute must never license a run.
	for _, mix := range [][]Outcome{
		{OutcomeNotReached},
		{OutcomePass, OutcomeNotReached},
		{OutcomePass, OutcomePass, OutcomeHumanBoundary, OutcomeNotReached},
	} {
		if got := ReadinessOf(mix); got != NotReady {
			t.Fatalf("ReadinessOf(%v) = %s, want %s", mix, got, NotReady)
		}
	}
}

func TestReadinessMapping(t *testing.T) {
	cases := []struct {
		in   []Outcome
		want Readiness
	}{
		{[]Outcome{OutcomePass}, Ready},
		{[]Outcome{OutcomePass, OutcomeHumanBoundary}, ReadyWithKnownHumanBoundary},
		{[]Outcome{OutcomeFail}, NotReady},
		{[]Outcome{OutcomeNotReached}, NotReady},
		{nil, NotReady},
	}
	for _, tc := range cases {
		if got := ReadinessOf(tc.in); got != tc.want {
			t.Fatalf("ReadinessOf(%v) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestReadinessPermitsAnUnattendedRun(t *testing.T) {
	if !Ready.PermitsUnattendedRun() {
		t.Fatal("READY must permit an unattended run")
	}
	if !ReadyWithKnownHumanBoundary.PermitsUnattendedRun() {
		t.Fatal("a known, named human boundary must not block the run it does not gate")
	}
	if NotReady.PermitsUnattendedRun() {
		t.Fatal("NOT-READY must never permit an unattended run")
	}
}

func TestUnknownOutcomeIsTreatedAsUnproven(t *testing.T) {
	// Defense in depth against a future outcome constant added without a
	// severity: it must degrade to the unproven end, never to pass.
	var bogus Outcome = "invented"
	if got := WorstOutcome([]Outcome{OutcomePass, bogus}); got != OutcomeNotReached {
		t.Fatalf("unknown outcome degraded to %s, want %s", got, OutcomeNotReached)
	}
}
