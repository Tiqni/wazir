package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
	memboard "github.com/EmadMokhtar/wazir/internal/board/memory"
	"github.com/EmadMokhtar/wazir/internal/orchestrator"
	"github.com/EmadMokhtar/wazir/internal/queue"
	"github.com/EmadMokhtar/wazir/internal/server"
	"github.com/EmadMokhtar/wazir/internal/store"
)

type capture struct {
	mu  sync.Mutex
	evs []board.Event
}

func (c *capture) Enqueue(ev board.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evs = append(c.evs, ev)
}
func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.evs)
}

func post(t *testing.T, h http.Handler, ev board.Event) int {
	t.Helper()
	body, _ := json.Marshal(ev)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code
}

func TestReceiverDedupesAndDrops(t *testing.T) {
	b := memboard.New()
	st := store.NewMemory()
	cap := &capture{}
	h := server.New(b, st, cap, nil)

	// A normal event enqueues once; a replay is dropped.
	good := board.Event{Kind: board.EventPhaseChanged, CardID: "A", NewPhase: board.PhasePlanning, Dedup: "d1"}
	if code := post(t, h, good); code != http.StatusOK {
		t.Fatalf("first post code = %d, want 200", code)
	}
	if code := post(t, h, good); code != http.StatusOK {
		t.Fatalf("replay post code = %d, want 200", code)
	}
	if cap.count() != 1 {
		t.Errorf("enqueued %d events, want 1 (dedupe)", cap.count())
	}

	// A bot comment is dropped.
	bot := board.Event{Kind: board.EventCommentAdded, CardID: "A", Dedup: "d2", Comment: &board.Comment{ID: "b1", IsBot: true}}
	post(t, h, bot)
	// An ignore event is dropped.
	post(t, h, board.Event{Kind: board.EventIgnore, Dedup: "d3"})
	if cap.count() != 1 {
		t.Errorf("enqueued %d events, want still 1 after bot+ignore", cap.count())
	}
}

// End-to-end: a webhook drives a card through the real queue + worker.
func TestReceiverPipelineMovesCard(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "A", Repo: "o/r", Phase: board.PhaseBrainstorming})
	st := store.NewMemory()
	brain := orchestrator.CannedBrain{} // Brainstorm -> spec_ready
	w := orchestrator.NewWorker(b, noForge{}, brain, st, nil)
	q := queue.New(st, w.Process, queue.Options{Workers: 2})
	q.Start(ctx)

	h := server.New(b, st, q, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	ev := board.Event{Kind: board.EventPhaseChanged, CardID: "A", NewPhase: board.PhaseBrainstorming, Dedup: "d1"}
	body, _ := json.Marshal(ev)
	resp, err := http.Post(ts.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	q.Shutdown() // drain in-flight work deterministically

	card, _ := b.GetCard(ctx, "A")
	if card.Phase != board.PhaseSpecReview {
		t.Errorf("phase = %q, want SpecReview after the pipeline ran", card.Phase)
	}
}

// noForge satisfies forge.CodeForge; nothing is exercised in the spec-ready path.
type noForge struct{}

func (noForge) EnsureClone(ctx context.Context, repo string) error                          { return nil }
func (noForge) CreateWorktree(ctx context.Context, repo, branch string) (string, error)     { return "", nil }
func (noForge) RemoveWorktree(ctx context.Context, repo, path string) error                 { return nil }
func (noForge) PushBranch(ctx context.Context, repo, branch string) error                   { return nil }
func (noForge) OpenPR(ctx context.Context, repo, branch, base, t, b string) (string, error) {
	return "", nil
}

func TestReceiverRejectsGet(t *testing.T) {
	h := server.New(memboard.New(), store.NewMemory(), &capture{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("code = %d, want 405", rr.Code)
	}
}

func TestReceiverRejectsOversizeBody(t *testing.T) {
	h := server.New(memboard.New(), store.NewMemory(), &capture{}, nil)
	big := bytes.Repeat([]byte("x"), 1<<20+1)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(big))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("code = %d, want 413", rr.Code)
	}
}
