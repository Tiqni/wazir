package store

import (
	"path/filepath"
	"testing"
)

func TestCardRecordReworkRoundsPersist(t *testing.T) {
	s, err := OpenBbolt(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenBbolt: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.PutCard("I1", CardRecord{Repo: "octocat/hello", ReworkRounds: 2}); err != nil {
		t.Fatalf("PutCard: %v", err)
	}
	got, ok, err := s.GetCard("I1")
	if err != nil || !ok {
		t.Fatalf("GetCard: ok=%v err=%v", ok, err)
	}
	if got.ReworkRounds != 2 {
		t.Errorf("ReworkRounds = %d, want 2", got.ReworkRounds)
	}
}
