package store

import "sync"

// Memory is an in-memory Store for tests.
type Memory struct {
	mu     sync.Mutex
	boards map[string]BoardRecord
	cards  map[string]CardRecord
}

// NewMemory returns an empty in-memory Store.
func NewMemory() *Memory {
	return &Memory{boards: map[string]BoardRecord{}, cards: map[string]CardRecord{}}
}

func (m *Memory) GetBoard(projectID string) (BoardRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.boards[projectID]
	return r, ok, nil
}

func (m *Memory) PutBoard(projectID string, rec BoardRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.boards[projectID] = rec
	return nil
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

func (m *Memory) Close() error { return nil }
