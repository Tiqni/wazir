package board

import "testing"

func TestAllPhasesOrderedAndComplete(t *testing.T) {
	got := AllPhases()
	want := []Phase{
		PhaseInbox, PhaseBrainstorming, PhaseAwaitingAnswers, PhaseSpecReview,
		PhasePlanning, PhaseBuilding, PhasePRReview, PhaseDone, PhaseFailed,
	}
	if len(got) != len(want) {
		t.Fatalf("AllPhases len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AllPhases[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPhaseValid(t *testing.T) {
	if !PhaseInbox.Valid() {
		t.Error("PhaseInbox should be valid")
	}
	if Phase("Nope").Valid() {
		t.Error("unknown phase should be invalid")
	}
}
