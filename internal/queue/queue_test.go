package queue

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/store"
)

func evt(card, dedup string) board.Event {
	return board.Event{Kind: board.EventPhaseChanged, CardID: card, Dedup: dedup}
}

// Same-card events must never run concurrently.
func TestQueueSerializesSameCard(t *testing.T) {
	var mu sync.Mutex
	active, maxActive := 0, 0
	h := func(ctx context.Context, ev board.Event) error {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	}
	q := New(store.NewMemory(), h, Options{Workers: 4})
	q.Start(context.Background())
	q.Enqueue(evt("A", "1"))
	q.Enqueue(evt("A", "2"))
	q.Shutdown()
	if maxActive != 1 {
		t.Errorf("max concurrent same-card handlers = %d, want 1", maxActive)
	}
}

// Different cards must run concurrently.
func TestQueueConcurrentDifferentCards(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	h := func(ctx context.Context, ev board.Event) error {
		started <- ev.CardID
		<-release
		return nil
	}
	q := New(store.NewMemory(), h, Options{Workers: 2})
	q.Start(context.Background())
	q.Enqueue(evt("A", "1"))
	q.Enqueue(evt("B", "2"))

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case id := <-started:
			got[id] = true
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for both cards to start concurrently")
		}
	}
	close(release)
	q.Shutdown()
	if !got["A"] || !got["B"] {
		t.Errorf("expected both A and B to run concurrently, got %v", got)
	}
}

// A card locked by another owner (cross-restart) is skipped, not processed.
func TestQueueSkipsForeignLock(t *testing.T) {
	st := store.NewMemory()
	if ok, _ := st.AcquireLock("A", "other-process", time.Hour); !ok {
		t.Fatal("precondition: foreign lock not acquired")
	}
	called := false
	h := func(ctx context.Context, ev board.Event) error {
		called = true
		return nil
	}
	q := New(st, h, Options{Workers: 1, Owner: "wazir"})
	q.Start(context.Background())
	q.Enqueue(evt("A", "1"))
	q.Shutdown()
	if called {
		t.Error("handler ran despite a live foreign lock")
	}
}
