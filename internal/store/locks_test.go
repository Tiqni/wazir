package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLockMemory(t *testing.T) { testLock(t, NewMemory(), func(s Store, f func() time.Time) { s.(*Memory).now = f }) }

func TestLockBbolt(t *testing.T) {
	s, err := OpenBbolt(filepath.Join(t.TempDir(), "wazir.db"))
	if err != nil {
		t.Fatalf("OpenBbolt: %v", err)
	}
	defer s.Close()
	testLock(t, s, func(s Store, f func() time.Time) { s.(*Bbolt).now = f })
}

// testLock drives the lock contract with a controllable clock.
func testLock(t *testing.T, s Store, setClock func(Store, func() time.Time)) {
	t.Helper()
	base := time.Unix(1_000_000, 0)
	setClock(s, func() time.Time { return base })

	// First acquire succeeds.
	if ok, err := s.AcquireLock("A", "w1", time.Minute); err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	// A different owner cannot take a live lock.
	if ok, _ := s.AcquireLock("A", "w2", time.Minute); ok {
		t.Error("w2 should not acquire a live lock held by w1")
	}
	// The same owner re-acquires (re-entrant).
	if ok, _ := s.AcquireLock("A", "w1", time.Minute); !ok {
		t.Error("w1 should re-acquire its own lock")
	}
	// After the TTL expires, another owner may steal it.
	setClock(s, func() time.Time { return base.Add(2 * time.Minute) })
	if ok, _ := s.AcquireLock("A", "w2", time.Minute); !ok {
		t.Error("w2 should steal an expired lock")
	}
	// Only the owner can release; a non-owner release is a no-op.
	if err := s.ReleaseLock("A", "w1"); err != nil {
		t.Fatalf("non-owner release: %v", err)
	}
	if ok, _ := s.AcquireLock("A", "w1", time.Minute); ok {
		t.Error("lock should still be held by w2 after a non-owner release")
	}
	// The owner releases; the lock is free.
	if err := s.ReleaseLock("A", "w2"); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	if ok, _ := s.AcquireLock("A", "w1", time.Minute); !ok {
		t.Error("lock should be free after the owner released it")
	}
}
