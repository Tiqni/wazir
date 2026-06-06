package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/forge"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// Worker executes a Resolver Decision against the ports. It owns the
// deterministic mapping from a Brain result to board writes.
type Worker struct {
	board    board.Board
	forge    forge.CodeForge
	brain    Brain
	store    store.Store
	resolver Resolver
	log      *zap.Logger
	base     string // PR base branch
}

// NewWorker builds a Worker. A nil logger is replaced with a no-op.
func NewWorker(b board.Board, f forge.CodeForge, br Brain, st store.Store, log *zap.Logger) *Worker {
	if log == nil {
		log = zap.NewNop()
	}
	return &Worker{board: b, forge: f, brain: br, store: st, log: log, base: "main"}
}

// Process resolves and executes one event for a card. A handled failure moves
// the card to Failed and returns nil; only infrastructure errors propagate.
func (w *Worker) Process(ctx context.Context, ev board.Event) error {
	card, err := w.board.GetCard(ctx, ev.CardID)
	if err != nil {
		return fmt.Errorf("get card %s: %w", ev.CardID, err)
	}
	rec, _, err := w.store.GetCard(ev.CardID)
	if err != nil {
		return fmt.Errorf("read card record %s: %w", ev.CardID, err)
	}
	d := w.resolver.Resolve(card, ev, rec.LastProcessedCommentID)
	w.log.Debug("resolved",
		zap.String("card", ev.CardID),
		zap.String("phase", string(card.Phase)),
		zap.String("action", d.Action.String()))

	if err := w.execute(ctx, card, d); err != nil {
		w.fail(ctx, ev.CardID, err)
		return nil
	}
	if d.Action != ActNone {
		w.advanceComment(ev)
	}
	return nil
}

func (w *Worker) execute(ctx context.Context, card board.Card, d Decision) error {
	switch d.Action {
	case ActNone:
		return nil
	case ActPickUp:
		if err := w.board.MoveTo(ctx, card.ID, board.PhaseBrainstorming); err != nil {
			return err
		}
		card.Phase = board.PhaseBrainstorming
		return w.brainstorm(ctx, card)
	case ActBrainstorm:
		return w.brainstorm(ctx, card)
	case ActPlan:
		return w.plan(ctx, card)
	case ActExecute:
		// Direct Building re-entry (crash recovery / re-delivered Building event):
		// load the plan path persisted by plan() so a real brain can still execute.
		rec, _, err := w.store.GetCard(card.ID)
		if err != nil {
			return fmt.Errorf("read card record %s: %w", card.ID, err)
		}
		return w.executePhase(ctx, card, rec.PlanPath)
	default:
		return fmt.Errorf("unknown action %v", d.Action)
	}
}

func (w *Worker) brainstorm(ctx context.Context, card board.Card) error {
	res, err := w.brain.Brainstorm(ctx, BrainstormInput{Transcript: BuildTranscript(card)})
	if err != nil {
		return fmt.Errorf("brainstorm: %w", err)
	}
	switch res.Status {
	case NeedsAnswers:
		var sb strings.Builder
		sb.WriteString("I need a few answers before writing the spec:\n")
		for _, q := range res.Questions {
			sb.WriteString("\n- ")
			sb.WriteString(q)
		}
		if err := w.board.PostComment(ctx, card.ID, sb.String()); err != nil {
			return err
		}
		return w.board.MoveTo(ctx, card.ID, board.PhaseAwaitingAnswers)
	case SpecReady:
		if err := w.board.SetBody(ctx, card.ID, res.SpecMarkdown); err != nil {
			return err
		}
		return w.board.MoveTo(ctx, card.ID, board.PhaseSpecReview)
	default:
		return fmt.Errorf("brainstorm returned unknown status %q", res.Status)
	}
}

func (w *Worker) plan(ctx context.Context, card board.Card) error {
	if card.Phase != board.PhasePlanning {
		if err := w.board.MoveTo(ctx, card.ID, board.PhasePlanning); err != nil {
			return err
		}
		card.Phase = board.PhasePlanning
	}
	res, err := w.brain.Plan(ctx, PlanInput{Transcript: BuildTranscript(card), Spec: card.Body})
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if res.Status != StatusPlanReady {
		return fmt.Errorf("plan failed: %s", res.Error)
	}
	// Persist the plan path so a later Building re-entry (ActExecute) can recover it.
	w.persistPlanPath(card.ID, res.PlanPath)
	if err := w.board.MoveTo(ctx, card.ID, board.PhaseBuilding); err != nil {
		return err
	}
	card.Phase = board.PhaseBuilding
	return w.executePhase(ctx, card, res.PlanPath)
}

func (w *Worker) executePhase(ctx context.Context, card board.Card, planPath string) error {
	res, err := w.brain.Execute(ctx, ExecuteInput{Transcript: BuildTranscript(card), PlanPath: planPath})
	if err != nil {
		return fmt.Errorf("execute: %w", err)
	}
	if res.Status != StatusComplete {
		return fmt.Errorf("execute failed: %s", res.Error)
	}
	// PushBranch is an M4 forge stub today: live it returns ErrNotImplemented and
	// drops the card to Failed (honest deferral). In tests the fake forge succeeds.
	if err := w.forge.PushBranch(ctx, card.Repo, res.Branch); err != nil {
		return fmt.Errorf("push branch: %w", err)
	}
	url, err := w.forge.OpenPR(ctx, card.Repo, res.Branch, w.base, card.Title, prBody(res))
	if err != nil {
		return fmt.Errorf("open pr: %w", err)
	}
	if err := w.board.PostComment(ctx, card.ID, "Opened PR: "+url); err != nil {
		return err
	}
	return w.board.MoveTo(ctx, card.ID, board.PhasePRReview)
}

func prBody(res ExecuteResult) string {
	return "Automated PR by Wazir.\n\n" + res.Notes + "\n\nTests: " + res.TestSummary
}

func (w *Worker) fail(ctx context.Context, cardID string, cause error) {
	w.log.Warn("phase failed", zap.String("card", cardID), zap.Error(cause))
	if err := w.board.PostComment(ctx, cardID, "⚠️ Wazir hit an error: "+cause.Error()); err != nil {
		w.log.Error("post failure comment", zap.Error(err))
	}
	if err := w.board.MoveTo(ctx, cardID, board.PhaseFailed); err != nil {
		w.log.Error("move to Failed", zap.Error(err))
	}
}

func (w *Worker) advanceComment(ev board.Event) {
	if ev.Kind != board.EventCommentAdded || ev.Comment == nil {
		return
	}
	rec, _, err := w.store.GetCard(ev.CardID)
	if err != nil {
		w.log.Error("read card record for watermark", zap.String("card", ev.CardID), zap.Error(err))
		return
	}
	rec.LastProcessedCommentID = ev.Comment.ID
	if err := w.store.PutCard(ev.CardID, rec); err != nil {
		w.log.Error("advance last_processed_comment_id", zap.Error(err))
	}
}

// persistPlanPath stores the plan path on the card record so a Building re-entry
// (a re-delivered Building event, or recovery after a crash between Planning and
// Building) can execute against the right plan. Best-effort: a write failure only
// affects the durability of a replay, not the in-flight turn (which has the path
// in hand). Re-reads to merge, preserving the other CardRecord fields.
func (w *Worker) persistPlanPath(cardID, planPath string) {
	if planPath == "" {
		return
	}
	rec, _, err := w.store.GetCard(cardID)
	if err != nil {
		w.log.Error("read card record for plan path", zap.String("card", cardID), zap.Error(err))
		return
	}
	rec.PlanPath = planPath
	if err := w.store.PutCard(cardID, rec); err != nil {
		w.log.Error("persist plan path", zap.Error(err))
	}
}
