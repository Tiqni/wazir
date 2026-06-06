package store

import "testing"

// The memory store must snapshot BoardRecord.Options so callers can't mutate
// stored state by reference (matching bbolt, which round-trips through JSON).
func TestMemoryDeepCopiesOptions(t *testing.T) {
	s := NewMemory()
	orig := BoardRecord{ProjectNodeID: "P1", Options: map[string]string{"Inbox": "o1"}}
	if err := s.PutBoard("P1", orig); err != nil {
		t.Fatalf("PutBoard: %v", err)
	}

	// Mutating the caller's map after Put must not affect the store.
	orig.Options["Inbox"] = "mutated"
	got, _, _ := s.GetBoard("P1")
	if got.Options["Inbox"] != "o1" {
		t.Errorf("PutBoard should snapshot Options; got %q", got.Options["Inbox"])
	}

	// Mutating the returned map must not affect the store either.
	got.Options["Inbox"] = "mutated2"
	again, _, _ := s.GetBoard("P1")
	if again.Options["Inbox"] != "o1" {
		t.Errorf("GetBoard should return a copy; got %q", again.Options["Inbox"])
	}
}
