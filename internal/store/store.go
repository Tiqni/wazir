// Package store persists Wazir state. Records are keyed project/card-aware
// (init-plan §4.1) so multi-board is a later config change, not a migration.
package store

import "time"

// BoardRecord is the cached identity of one provisioned board.
type BoardRecord struct {
	ProjectNumber int
	ProjectNodeID string
	StatusFieldID string
	Options       map[string]string // phase token -> single-select option id
	Owner         string
	OwnerType     string
}

// CardRecord maps a card's opaque id to its forge coordinates.
type CardRecord struct {
	Repo                   string // "owner/name"
	IssueNumber            int
	ProjectItemID          string
	LastProcessedCommentID string // M1: skip re-delivered comment events (§8.7)
	PlanPath               string // M1: persisted so a Building re-entry/replay still has the plan
	BrainstormTurns        int    // M2: count of clarifying-question rounds, for the MAX cap
}

// Store is the persistence port.
type Store interface {
	GetBoard(projectID string) (BoardRecord, bool, error)
	PutBoard(projectID string, rec BoardRecord) error
	GetCard(issueNodeID string) (CardRecord, bool, error)
	PutCard(issueNodeID string, rec CardRecord) error

	// M1 — webhook idempotency (init-plan §7).
	SeenDelivery(id string) (bool, error)
	MarkDelivery(id string) error

	// M1 — cross-restart advisory lock with TTL (init-plan §8.7).
	AcquireLock(cardID, owner string, ttl time.Duration) (acquired bool, err error)
	ReleaseLock(cardID, owner string) error

	Close() error
}
