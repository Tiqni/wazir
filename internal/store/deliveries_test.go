package store

import (
	"path/filepath"
	"testing"
)

func TestDeliveryDedupeMemory(t *testing.T) { testDeliveryDedupe(t, NewMemory()) }

func TestDeliveryDedupeBbolt(t *testing.T) {
	s, err := OpenBbolt(filepath.Join(t.TempDir(), "wazir.db"))
	if err != nil {
		t.Fatalf("OpenBbolt: %v", err)
	}
	defer s.Close()
	testDeliveryDedupe(t, s)
}

func testDeliveryDedupe(t *testing.T, s Store) {
	t.Helper()
	seen, err := s.SeenDelivery("d1")
	if err != nil || seen {
		t.Fatalf("fresh delivery: seen=%v err=%v", seen, err)
	}
	if err := s.MarkDelivery("d1"); err != nil {
		t.Fatalf("MarkDelivery: %v", err)
	}
	if seen, _ := s.SeenDelivery("d1"); !seen {
		t.Error("d1 should be seen after MarkDelivery")
	}
	if seen, _ := s.SeenDelivery("d2"); seen {
		t.Error("d2 should not be seen")
	}
}

func TestDeliveryDedupeBboltPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wazir.db")

	s1, err := OpenBbolt(path)
	if err != nil {
		t.Fatalf("OpenBbolt: %v", err)
	}
	if err := s1.MarkDelivery("d1"); err != nil {
		t.Fatalf("MarkDelivery: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := OpenBbolt(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	seen, err := s2.SeenDelivery("d1")
	if err != nil {
		t.Fatalf("SeenDelivery after reopen: %v", err)
	}
	if !seen {
		t.Error("d1 should be seen after bbolt reopen")
	}
}

func TestLastProcessedCommentIDRoundTrips(t *testing.T) {
	s := NewMemory()
	if err := s.PutCard("I1", CardRecord{Repo: "o/r", LastProcessedCommentID: "c9"}); err != nil {
		t.Fatalf("PutCard: %v", err)
	}
	got, ok, _ := s.GetCard("I1")
	if !ok || got.LastProcessedCommentID != "c9" {
		t.Errorf("LastProcessedCommentID = %q ok=%v, want c9", got.LastProcessedCommentID, ok)
	}
}
