package store

import (
	"path/filepath"
	"testing"
)

func openTempBbolt(t *testing.T) *Bbolt {
	t.Helper()
	s, err := OpenBbolt(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenBbolt: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPRIndexRoundTripBbolt(t *testing.T) { runPRIndexSuite(t, openTempBbolt(t)) }
func TestPRIndexRoundTripMemory(t *testing.T) { runPRIndexSuite(t, NewMemory()) }

func runPRIndexSuite(t *testing.T, s Store) {
	t.Helper()
	// Miss before write.
	if _, ok, err := s.GetPRIndex("octocat/hello", 9); err != nil || ok {
		t.Fatalf("pre-write GetPRIndex: ok=%v err=%v, want ok=false", ok, err)
	}
	// Write then hit.
	if err := s.PutPRIndex("octocat/hello", 9, "ISSUE_NODE_1"); err != nil {
		t.Fatalf("PutPRIndex: %v", err)
	}
	id, ok, err := s.GetPRIndex("octocat/hello", 9)
	if err != nil || !ok || id != "ISSUE_NODE_1" {
		t.Fatalf("GetPRIndex = (%q, %v, %v), want (ISSUE_NODE_1, true, nil)", id, ok, err)
	}
	// Different PR number does not collide.
	if _, ok, _ := s.GetPRIndex("octocat/hello", 10); ok {
		t.Errorf("PR 10 should miss")
	}
	// Different repo with the same PR number does not collide.
	if _, ok, _ := s.GetPRIndex("octocat/world", 9); ok {
		t.Errorf("different repo PR 9 should miss")
	}
}

func TestCardRecordDeltaFieldsPersist(t *testing.T) {
	s := openTempBbolt(t)
	rec := CardRecord{Repo: "octocat/hello", PRNumber: 9, LastReviewState: "changes_requested", LastCIConclusion: "failure"}
	if err := s.PutCard("ISSUE_NODE_1", rec); err != nil {
		t.Fatalf("PutCard: %v", err)
	}
	got, ok, err := s.GetCard("ISSUE_NODE_1")
	if err != nil || !ok {
		t.Fatalf("GetCard: ok=%v err=%v", ok, err)
	}
	if got.PRNumber != 9 || got.LastReviewState != "changes_requested" || got.LastCIConclusion != "failure" {
		t.Errorf("round-trip = %+v", got)
	}
}
