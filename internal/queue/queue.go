// Package queue serializes work per card (an in-process keyed mutex plus a
// cross-restart TTL lock) while running different cards concurrently (§8.7).
package queue

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// Handler processes one event for a card.
type Handler func(ctx context.Context, ev board.Event) error

// Options configures a Queue.
type Options struct {
	Workers int           // pool size (default 4)
	Buffer  int           // channel buffer (default 128)
	Owner   string        // lock owner token (default "wazir")
	LockTTL time.Duration // advisory lock TTL (default 5m)
	Logger  *zap.Logger
}

// Queue dispatches events to a bounded worker pool, serialized per card.
type Queue struct {
	store   store.Store
	handler Handler
	log     *zap.Logger
	owner   string
	lockTTL time.Duration
	workers int

	events chan board.Event
	wg     sync.WaitGroup

	closeMu sync.RWMutex
	closed  bool

	mu    sync.Mutex
	// keyMu holds one mutex per distinct CardID, never evicted. Acceptable for
	// M1 (card count is bounded by the project board); revisit if boards grow large.
	keyMu map[string]*sync.Mutex
}

// New builds a Queue. Defaults fill any zero-valued option.
func New(st store.Store, h Handler, opts Options) *Queue {
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.Buffer <= 0 {
		opts.Buffer = 128
	}
	if opts.LockTTL <= 0 {
		opts.LockTTL = 5 * time.Minute
	}
	if opts.Owner == "" {
		opts.Owner = "wazir"
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	return &Queue{
		store:   st,
		handler: h,
		log:     opts.Logger,
		owner:   opts.Owner,
		lockTTL: opts.LockTTL,
		workers: opts.Workers,
		events:  make(chan board.Event, opts.Buffer),
		keyMu:   map[string]*sync.Mutex{},
	}
}

// Start launches the worker pool.
func (q *Queue) Start(ctx context.Context) {
	for i := 0; i < q.workers; i++ {
		q.wg.Add(1)
		go q.loop(ctx)
	}
}

// Enqueue submits an event. Blocks if the buffer is full (Buffer events, 128
// by default) until a worker drains one. Calls after Shutdown are dropped
// (logged), never panicking.
func (q *Queue) Enqueue(ev board.Event) {
	q.closeMu.RLock()
	defer q.closeMu.RUnlock()
	if q.closed {
		q.log.Warn("enqueue after shutdown; dropping event", zap.String("card", ev.CardID))
		return
	}
	q.events <- ev
}

// Shutdown stops accepting work and waits for in-flight events to finish.
// Safe to call once; subsequent Enqueue calls drop their events.
func (q *Queue) Shutdown() {
	q.closeMu.Lock()
	if q.closed {
		q.closeMu.Unlock()
		return
	}
	q.closed = true
	close(q.events)
	q.closeMu.Unlock()
	q.wg.Wait()
}

func (q *Queue) loop(ctx context.Context) {
	defer q.wg.Done()
	for ev := range q.events {
		q.process(ctx, ev)
	}
}

func (q *Queue) process(ctx context.Context, ev board.Event) {
	// In-process per-card serialization.
	km := q.keyMutex(ev.CardID)
	km.Lock()
	defer km.Unlock()

	// Cross-restart advisory lock (a crashed peer's lock self-heals via TTL).
	ok, err := q.store.AcquireLock(ev.CardID, q.owner, q.lockTTL)
	if err != nil {
		q.log.Error("acquire lock", zap.String("card", ev.CardID), zap.Error(err))
		return
	}
	if !ok {
		q.log.Info("card locked by another worker; skipping", zap.String("card", ev.CardID))
		return
	}
	defer func() {
		if err := q.store.ReleaseLock(ev.CardID, q.owner); err != nil {
			q.log.Error("release lock", zap.String("card", ev.CardID), zap.Error(err))
		}
	}()

	// Worker.Process already logs+handles its own failures; this is a safety net
	// for handlers that propagate infrastructure errors.
	if err := q.handler(ctx, ev); err != nil {
		q.log.Error("handler", zap.String("card", ev.CardID), zap.Error(err))
	}
}

func (q *Queue) keyMutex(cardID string) *sync.Mutex {
	q.mu.Lock()
	defer q.mu.Unlock()
	m, ok := q.keyMu[cardID]
	if !ok {
		m = &sync.Mutex{}
		q.keyMu[cardID] = m
	}
	return m
}
