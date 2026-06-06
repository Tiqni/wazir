package store

import (
	"sync"
	"time"
)

// Memory is an in-memory Store for tests.
type Memory struct {
	mu         sync.Mutex
	boards     map[string]BoardRecord
	cards      map[string]CardRecord
	deliveries map[string]bool
	locks      map[string]lockEntry
	now        func() time.Time
}

type lockEntry struct {
	owner     string
	expiresAt time.Time
}

// NewMemory returns an empty in-memory Store.
func NewMemory() *Memory {
	return &Memory{
		boards:     map[string]BoardRecord{},
		cards:      map[string]CardRecord{},
		deliveries: map[string]bool{},
		locks:      map[string]lockEntry{},
		now:        time.Now,
	}
}

func (m *Memory) GetBoard(projectID string) (BoardRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.boards[projectID]
	if ok {
		r = copyBoardRecord(r)
	}
	return r, ok, nil
}

func (m *Memory) PutBoard(projectID string, rec BoardRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.boards[projectID] = copyBoardRecord(rec)
	return nil
}

// copyBoardRecord deep-copies the Options map so callers can't mutate stored
// state by reference — matching the bbolt impl, which round-trips through JSON.
func copyBoardRecord(rec BoardRecord) BoardRecord {
	if rec.Options != nil {
		opts := make(map[string]string, len(rec.Options))
		for k, v := range rec.Options {
			opts[k] = v
		}
		rec.Options = opts
	}
	return rec
}

func (m *Memory) GetCard(issueNodeID string) (CardRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.cards[issueNodeID]
	return r, ok, nil
}

func (m *Memory) PutCard(issueNodeID string, rec CardRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cards[issueNodeID] = rec
	return nil
}

func (m *Memory) SeenDelivery(id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deliveries[id], nil
}

func (m *Memory) MarkDelivery(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deliveries[id] = true
	return nil
}

func (m *Memory) Close() error { return nil }

func (m *Memory) AcquireLock(cardID, owner string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if e, ok := m.locks[cardID]; ok && e.owner != owner && now.Before(e.expiresAt) {
		return false, nil
	}
	m.locks[cardID] = lockEntry{owner: owner, expiresAt: now.Add(ttl)}
	return true, nil
}

func (m *Memory) ReleaseLock(cardID, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.locks[cardID]; ok && e.owner == owner {
		delete(m.locks, cardID)
	}
	return nil
}
