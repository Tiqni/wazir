// Package server is the HTTP webhook receiver. It is provider-agnostic: it
// hands raw headers+body to board.ParseEvent and enqueues survivors (§8.1).
package server

import (
	"io"
	"net/http"

	"go.uber.org/zap"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// Enqueuer accepts an event for asynchronous processing.
type Enqueuer interface {
	Enqueue(ev board.Event)
}

// Handler receives webhooks, normalizes, dedupes, and enqueues.
type Handler struct {
	board board.Board
	store store.Store
	queue Enqueuer
	log   *zap.Logger
}

// New builds a receiver. A nil logger is replaced with a no-op.
func New(b board.Board, st store.Store, q Enqueuer, log *zap.Logger) *Handler {
	if log == nil {
		log = zap.NewNop()
	}
	return &Handler{board: b, store: st, queue: q, log: log}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	headers := make(map[string]string, len(r.Header))
	for k := range r.Header {
		headers[k] = r.Header.Get(k)
	}

	ev, err := h.board.ParseEvent(headers, body)
	if err != nil {
		// Signature failure or malformed payload is a client error.
		h.log.Warn("parse event", zap.Error(err))
		http.Error(w, "bad webhook", http.StatusBadRequest)
		return
	}
	if h.drop(ev) {
		w.WriteHeader(http.StatusOK)
		return
	}

	seen, err := h.store.SeenDelivery(ev.Dedup)
	if err != nil {
		h.log.Error("dedupe check", zap.Error(err))
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	if seen {
		h.log.Debug("duplicate delivery dropped", zap.String("delivery", ev.Dedup))
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := h.store.MarkDelivery(ev.Dedup); err != nil {
		h.log.Error("mark delivery", zap.Error(err))
	}

	h.queue.Enqueue(ev)
	w.WriteHeader(http.StatusOK)
}

// drop reports whether an event should be ignored (no-op kinds and bot events).
func (h *Handler) drop(ev board.Event) bool {
	if ev.Kind == board.EventIgnore || ev.CardID == "" {
		return true
	}
	if ev.Comment != nil && ev.Comment.IsBot {
		return true
	}
	return false
}
