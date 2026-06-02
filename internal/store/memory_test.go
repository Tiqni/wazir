package store

import "testing"

func TestMemoryBoardRoundTrip(t *testing.T) {
	s := NewMemory()
	if _, ok, _ := s.GetBoard("P1"); ok {
		t.Fatal("expected board absent")
	}
	rec := BoardRecord{ProjectNodeID: "P1", StatusFieldID: "F1", Options: map[string]string{"Inbox": "o1"}}
	if err := s.PutBoard("P1", rec); err != nil {
		t.Fatalf("PutBoard: %v", err)
	}
	got, ok, err := s.GetBoard("P1")
	if err != nil || !ok {
		t.Fatalf("GetBoard ok=%v err=%v", ok, err)
	}
	if got.Options["Inbox"] != "o1" {
		t.Errorf("Options[Inbox] = %q, want o1", got.Options["Inbox"])
	}
}

func TestMemoryCardRoundTrip(t *testing.T) {
	s := NewMemory()
	if err := s.PutCard("I1", CardRecord{Repo: "o/r", IssueNumber: 5, ProjectItemID: "PI1"}); err != nil {
		t.Fatalf("PutCard: %v", err)
	}
	got, ok, _ := s.GetCard("I1")
	if !ok || got.Repo != "o/r" || got.IssueNumber != 5 {
		t.Errorf("GetCard = %+v ok=%v", got, ok)
	}
}
