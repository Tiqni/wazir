// Package memory is an in-memory board.Board for running the whole state
// machine with no network (init-plan §4.2 honest-abstraction test).
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/EmadMokhtar/wazir/internal/board"
)

// Board is an in-memory board.Board.
type Board struct {
	mu    sync.Mutex
	cards map[string]*board.Card
}

// New returns an empty in-memory board.
func New() *Board { return &Board{cards: map[string]*board.Card{}} }

// Seed inserts or replaces a card (test/demo helper).
func (b *Board) Seed(c board.Card) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := cloneCard(c)
	b.cards[c.ID] = &cp
}

// AddComment appends an arbitrary comment (test helper for simulating a human
// reply; PostComment is bot-only).
func (b *Board) AddComment(cardID string, c board.Comment) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cc, ok := b.cards[cardID]; ok {
		cc.Comments = append(cc.Comments, c)
	}
}

func (b *Board) EnsureProvisioned(ctx context.Context, spec board.BoardSpec) error { return nil }

func (b *Board) GetCard(ctx context.Context, cardID string) (board.Card, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.cards[cardID]
	if !ok {
		return board.Card{}, fmt.Errorf("memory: card %s not found", cardID)
	}
	return cloneCard(*c), nil
}

func (b *Board) ListCards(ctx context.Context, phase board.Phase) ([]board.Card, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []board.Card
	for _, c := range b.cards {
		if c.Phase == phase {
			out = append(out, cloneCard(*c))
		}
	}
	return out, nil
}

func (b *Board) PostComment(ctx context.Context, cardID, body string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.cards[cardID]
	if !ok {
		return fmt.Errorf("memory: card %s not found", cardID)
	}
	c.Comments = append(c.Comments, board.Comment{
		ID:      fmt.Sprintf("bot%d", len(c.Comments)+1),
		Author:  "wazir-bot",
		IsBot:   true,
		Body:    body,
		Created: time.Time{},
	})
	return nil
}

func (b *Board) SetBody(ctx context.Context, cardID, markdown string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.cards[cardID]
	if !ok {
		return fmt.Errorf("memory: card %s not found", cardID)
	}
	c.Body = markdown
	return nil
}

func (b *Board) MoveTo(ctx context.Context, cardID string, phase board.Phase) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.cards[cardID]
	if !ok {
		return fmt.Errorf("memory: card %s not found", cardID)
	}
	c.Phase = phase
	return nil
}

// ParseEvent decodes a JSON-encoded board.Event (the memory transport).
func (b *Board) ParseEvent(headers map[string]string, payload []byte) (board.Event, error) {
	var ev board.Event
	if err := json.Unmarshal(payload, &ev); err != nil {
		return board.Event{}, fmt.Errorf("memory: parse event: %w", err)
	}
	return ev, nil
}

func cloneCard(c board.Card) board.Card {
	if c.Comments != nil {
		cc := make([]board.Comment, len(c.Comments))
		copy(cc, c.Comments)
		c.Comments = cc
	}
	return c
}

var _ board.Board = (*Board)(nil)
