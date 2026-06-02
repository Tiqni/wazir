package store

import (
	"path/filepath"
	"testing"
)

func TestBboltPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wazir.db")

	s1, err := OpenBbolt(path)
	if err != nil {
		t.Fatalf("OpenBbolt: %v", err)
	}
	if err := s1.PutBoard("P1", BoardRecord{ProjectNodeID: "P1", Options: map[string]string{"Done": "o9"}}); err != nil {
		t.Fatalf("PutBoard: %v", err)
	}
	if err := s1.PutCard("I1", CardRecord{Repo: "o/r", IssueNumber: 3}); err != nil {
		t.Fatalf("PutCard: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := OpenBbolt(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	b, ok, _ := s2.GetBoard("P1")
	if !ok || b.Options["Done"] != "o9" {
		t.Errorf("GetBoard after reopen = %+v ok=%v", b, ok)
	}
	c, ok, _ := s2.GetCard("I1")
	if !ok || c.Repo != "o/r" {
		t.Errorf("GetCard after reopen = %+v ok=%v", c, ok)
	}
}
