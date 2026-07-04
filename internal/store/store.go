// Package store persists Wazir state. Records are keyed project/card-aware
// (init-plan §4.1) so multi-board is a later config change, not a migration.
package store

import (
	"strconv"
	"time"
)

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
	WorktreePath           string // M4: absolute worktree path — Building re-entry cmd.Dir + cleanup
	Branch                 string // M4: feature/issue-<n>-<slug>, the deterministic push/PR branch
	PRNumber               int    // M5: open PR number; captured at OpenPR; enables PRStatus + PR-index
	LastReviewState        string // M5: "" | "approved" | "changes_requested" — report delta state
	LastCIConclusion       string // M5: "" | "success" | "failure" | "pending" — report delta state
	ReworkRounds           int    // M6: count of rework attempts; the cost breaker (default cap 3)
}

// Store is the persistence port.
type Store interface {
	GetBoard(projectID string) (BoardRecord, bool, error)
	PutBoard(projectID string, rec BoardRecord) error
	GetCard(issueNodeID string) (CardRecord, bool, error)
	PutCard(issueNodeID string, rec CardRecord) error

	// M5 — PR -> issue reverse index, so a PR webhook resolves to its card.
	PutPRIndex(repo string, prNumber int, issueNodeID string) error
	GetPRIndex(repo string, prNumber int) (issueNodeID string, ok bool, err error)

	// M1 — webhook idempotency (init-plan §7).
	SeenDelivery(id string) (bool, error)
	MarkDelivery(id string) error

	// M1 — cross-restart advisory lock with TTL (init-plan §8.7).
	AcquireLock(cardID, owner string, ttl time.Duration) (acquired bool, err error)
	ReleaseLock(cardID, owner string) error

	Close() error
}

// prIndexKey is the bbolt/memory key for the PR -> issue reverse index.
func prIndexKey(repo string, prNumber int) string {
	return repo + "#" + strconv.Itoa(prNumber)
}
