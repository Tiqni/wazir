package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/forge"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// defaultMaxBrainstormTurns caps the clarifying-question loop (M2 spec §6).
const defaultMaxBrainstormTurns = 8

// Worker executes a Resolver Decision against the ports. It owns the
// deterministic mapping from a Brain result to board writes.
type Worker struct {
	board              board.Board
	forge              forge.CodeForge
	brain              Brain
	store              store.Store
	resolver           Resolver
	log                *zap.Logger
	base               string       // PR base branch
	maxBrainstormTurns atomic.Int64 // cap on the clarifying-question loop (M2); hot-reloadable
}

// NewWorker builds a Worker. A nil logger is replaced with a no-op.
func NewWorker(b board.Board, f forge.CodeForge, br Brain, st store.Store, log *zap.Logger) *Worker {
	if log == nil {
		log = zap.NewNop()
	}
	w := &Worker{board: b, forge: f, brain: br, store: st, log: log, base: "main"}
	w.maxBrainstormTurns.Store(defaultMaxBrainstormTurns)
	return w
}

// WithMaxBrainstormTurns overrides the question-loop cap (e.g. from config).
// A non-positive n is ignored. Returns w for chaining.
func (w *Worker) WithMaxBrainstormTurns(n int) *Worker {
	w.SetMaxBrainstormTurns(n)
	return w
}

// SetMaxBrainstormTurns hot-swaps the question-loop cap (config reload). Ignores n <= 0.
func (w *Worker) SetMaxBrainstormTurns(n int) {
	if n > 0 {
		w.maxBrainstormTurns.Store(int64(n))
	}
}

// WithBase overrides the PR base branch (from config). Empty is ignored.
func (w *Worker) WithBase(base string) *Worker {
	if base != "" {
		w.base = base
	}
	return w
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
		// Re-brainstorm from Awaiting Answers (a human reply) or Spec Review (a
		// revision request): move the card back to Brainstorming first so the rework
		// is visible on the board (§3 loops back to Brainstorming) instead of the
		// turn running silently in place. A card already in Brainstorming is left put
		// to avoid a redundant self-move event.
		if card.Phase != board.PhaseBrainstorming {
			if err := w.board.MoveTo(ctx, card.ID, board.PhaseBrainstorming); err != nil {
				return err
			}
			card.Phase = board.PhaseBrainstorming
		}
		return w.brainstorm(ctx, card)
	case ActPlan:
		return w.plan(ctx, card)
	case ActExecute:
		// Direct Building re-entry (crash recovery / re-delivered Building event):
		// recover the worktree path, branch, and plan path persisted by plan().
		rec, _, err := w.store.GetCard(card.ID)
		if err != nil {
			return fmt.Errorf("read card record %s: %w", card.ID, err)
		}
		if rec.WorktreePath == "" {
			// A real Building card always has a worktree persisted by plan(); an empty
			// one means the card reached Building without going through Planning (e.g.
			// a manual column drag). Fail fast rather than run execute in the daemon's
			// cwd outside any worktree.
			return fmt.Errorf("card is in Building without a recorded worktree (was Planning skipped?); move it back to Spec Review to re-plan")
		}
		return w.executePhase(ctx, card, rec.WorktreePath, rec.Branch, rec.PlanPath)
	default:
		return fmt.Errorf("unknown action %v", d.Action)
	}
}

func (w *Worker) brainstorm(ctx context.Context, card board.Card) error {
	rec, _, err := w.store.GetCard(card.ID)
	if err != nil {
		return fmt.Errorf("read card record %s: %w", card.ID, err)
	}
	// Cost circuit breaker: once the question loop has hit the cap, escalate to a
	// human *without* another (paid) model call. Checked before brain.Brainstorm
	// so the cap actually prevents spend (spec §6: "no further spend").
	maxTurns := int(w.maxBrainstormTurns.Load())
	if rec.BrainstormTurns >= maxTurns {
		msg := fmt.Sprintf("I've reached the question limit (%d rounds) on this card without a clear spec. It needs a human to decide the direction.", maxTurns)
		if err := w.board.PostComment(ctx, card.ID, msg); err != nil {
			return err
		}
		return w.board.MoveTo(ctx, card.ID, board.PhaseAwaitingAnswers)
	}

	// Repo-aware brainstorm (M5): clone the target repo so the turn runs with cwd =
	// the clone and loads the repo's own CLAUDE.md/AGENTS.md. Done *after* the cap
	// check so an about-to-escalate card never triggers a clone.
	clonePath, err := w.forge.EnsureClone(ctx, card.Repo)
	if err != nil {
		return fmt.Errorf("ensure clone: %w", err)
	}
	res, err := w.brain.Brainstorm(ctx, BrainstormInput{Transcript: BuildTranscript(card), RepoPath: clonePath})
	if err != nil {
		return fmt.Errorf("brainstorm: %w", err)
	}
	switch res.Status {
	case BrainstormFailed:
		return fmt.Errorf("brainstorm failed: %s", res.Error)
	case NeedsAnswers:
		rec.BrainstormTurns++
		if err := w.store.PutCard(card.ID, rec); err != nil {
			return fmt.Errorf("persist brainstorm turn: %w", err)
		}
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
		priorTurns := rec.BrainstormTurns
		rec.BrainstormTurns = 0
		if err := w.store.PutCard(card.ID, rec); err != nil {
			w.log.Error("reset brainstorm turns", zap.String("card", card.ID), zap.Error(err))
		}
		if err := w.board.SetBody(ctx, card.ID, res.SpecMarkdown); err != nil {
			return err
		}
		// Make the "jumped straight to a spec" decision visible on the board, so a
		// human isn't left wondering why no clarifying questions were asked.
		if err := w.board.PostComment(ctx, card.ID, specReadyNote(priorTurns)); err != nil {
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
	rec, _, err := w.store.GetCard(card.ID)
	if err != nil {
		return fmt.Errorf("read card record %s: %w", card.ID, err)
	}
	branch := branchName(rec.IssueNumber, card.Title)
	if _, err := w.forge.EnsureClone(ctx, card.Repo); err != nil {
		return fmt.Errorf("ensure clone: %w", err)
	}
	wt, err := w.forge.CreateWorktree(ctx, card.Repo, branch)
	if err != nil {
		return fmt.Errorf("create worktree: %w", err)
	}
	if wt == "" {
		// Fail closed: a worktreeless plan/execute would run claude in the daemon's
		// own cwd, outside any isolation. A real forge never returns ("", nil).
		return fmt.Errorf("create worktree returned an empty path for %s", card.Repo)
	}
	rec.Branch = branch
	rec.WorktreePath = wt
	if err := w.store.PutCard(card.ID, rec); err != nil {
		// The worktree exists but its path won't be persisted; remove it best-effort
		// so it isn't orphaned, and surface the path in case the removal also fails.
		_ = w.forge.RemoveWorktree(ctx, card.Repo, wt)
		return fmt.Errorf("persist worktree coords (removed worktree %s): %w", wt, err)
	}

	res, err := w.brain.Plan(ctx, PlanInput{Transcript: BuildTranscript(card), Spec: card.Body, WorktreePath: wt})
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if res.Status != StatusPlanReady {
		return fmt.Errorf("plan failed: %s", res.Error)
	}
	w.persistPlanPath(card.ID, res.PlanPath)
	if err := w.board.MoveTo(ctx, card.ID, board.PhaseBuilding); err != nil {
		return err
	}
	card.Phase = board.PhaseBuilding
	return w.executePhase(ctx, card, wt, branch, res.PlanPath)
}

func (w *Worker) executePhase(ctx context.Context, card board.Card, worktreePath, branch, planPath string) error {
	res, err := w.brain.Execute(ctx, ExecuteInput{Transcript: BuildTranscript(card), PlanPath: planPath, WorktreePath: worktreePath})
	if err != nil {
		return fmt.Errorf("execute: %w", err)
	}
	if res.Status != StatusComplete {
		return fmt.Errorf("execute failed: %s", res.Error)
	}
	// Push the orchestrator-owned branch (not res.Branch — the model doesn't pick it).
	if err := w.forge.PushBranch(ctx, card.Repo, branch); err != nil {
		return fmt.Errorf("push branch: %w", err)
	}
	url, err := w.forge.OpenPR(ctx, card.Repo, branch, w.base, card.Title, prBody(res))
	if err != nil {
		return fmt.Errorf("open pr: %w", err)
	}
	if err := w.board.PostComment(ctx, card.ID, "Opened PR: "+url); err != nil {
		return err
	}
	if err := w.board.MoveTo(ctx, card.ID, board.PhasePRReview); err != nil {
		return err
	}
	// Success: drop the worktree (best-effort; keep it on failure for debugging).
	if worktreePath != "" {
		if err := w.forge.RemoveWorktree(ctx, card.Repo, worktreePath); err != nil {
			w.log.Warn("remove worktree", zap.String("card", card.ID), zap.String("path", worktreePath), zap.Error(err))
		}
	}
	return nil
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

// specReadyNote explains why the card advanced to Spec Review — either the idea was
// clear enough to spec directly (no question round), or the spec came after N rounds.
// Posted as a board comment so the transition is transparent to a human reviewer.
func specReadyNote(priorTurns int) string {
	if priorTurns == 0 {
		return "📝 The idea looked clear enough to spec directly, so I skipped the clarifying-question round. " +
			"The spec is in the issue description above — review it, then move the card to Planning to approve, or comment with changes."
	}
	rounds := "round"
	if priorTurns != 1 {
		rounds += "s"
	}
	return fmt.Sprintf("📝 Spec ready after %d clarifying %s. "+
		"The spec is in the issue description above — review it, then move the card to Planning to approve, or comment with changes.", priorTurns, rounds)
}

// branchName is the deterministic, orchestrator-owned feature branch for a card.
func branchName(issueNumber int, title string) string {
	return fmt.Sprintf("feature/issue-%d-%s", issueNumber, branchSlug(title))
}

// branchSlug lowercases title, keeps [a-z0-9], collapses runs to single dashes,
// trims dashes, and caps length. Empty input yields "card".
func branchSlug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	if out == "" {
		return "card"
	}
	return out
}
