// Package store persists Wazir state. Records are keyed project/card-aware
// (init-plan §4.1) so multi-board is a later config change, not a migration.
package store

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

	Close() error
}
