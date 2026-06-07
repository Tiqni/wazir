package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
)

func TestBuildTranscriptTagsAuthors(t *testing.T) {
	c := board.Card{
		Title: "Add login",
		Body:  "We need auth.",
		Comments: []board.Comment{
			{Author: "alice", IsBot: false, Body: "use OAuth?"},
			{Author: "wazir-bot", IsBot: true, Body: "Which provider?"},
		},
	}
	got := BuildTranscript(c)
	if !strings.Contains(got, "# Add login") || !strings.Contains(got, "We need auth.") {
		t.Errorf("transcript missing title/body:\n%s", got)
	}
	if !strings.Contains(got, "HUMAN: use OAuth?") {
		t.Errorf("human comment not tagged:\n%s", got)
	}
	if !strings.Contains(got, "SYSTEM: Which provider?") {
		t.Errorf("bot comment not tagged:\n%s", got)
	}
}

func TestBrainstormResultHasFailureChannel(t *testing.T) {
	r := BrainstormResult{Status: BrainstormFailed, Error: "claude exited 1"}
	if r.Status != BrainstormFailed || r.Error == "" {
		t.Errorf("BrainstormResult failure channel missing: %+v", r)
	}
}

func TestErrPhaseRequiresWorktreeIsSentinel(t *testing.T) {
	// The Worker wraps the sentinel with %w; errors.Is must survive that chain.
	wrapped := fmt.Errorf("plan: %w", ErrPhaseRequiresWorktree)
	if !errors.Is(wrapped, ErrPhaseRequiresWorktree) {
		t.Fatal("ErrPhaseRequiresWorktree must survive errors.Is through a wrapping chain")
	}
}

func TestBuildTranscriptTrimsTrailingBodyNewlines(t *testing.T) {
	c := board.Card{Title: "T", Body: "body\n\n\n", Comments: []board.Comment{{Body: "hi", IsBot: false}}}
	got := BuildTranscript(c)
	if strings.Contains(got, "body\n\n\nHUMAN") || strings.Contains(got, "body\n\n\n\n") {
		t.Errorf("trailing body newlines not trimmed:\n%q", got)
	}
	if !strings.Contains(got, "body\n") || !strings.Contains(got, "HUMAN: hi") {
		t.Errorf("transcript malformed:\n%q", got)
	}
}

func TestCannedBrainAdvances(t *testing.T) {
	ctx := context.Background()
	var b Brain = CannedBrain{}

	bs, err := b.Brainstorm(ctx, BrainstormInput{})
	if err != nil || bs.Status != SpecReady || bs.SpecMarkdown == "" {
		t.Errorf("Brainstorm = %+v err=%v, want spec_ready with a spec", bs, err)
	}
	pl, err := b.Plan(ctx, PlanInput{})
	if err != nil || pl.Status != StatusPlanReady {
		t.Errorf("Plan = %+v err=%v, want plan_ready", pl, err)
	}
	ex, err := b.Execute(ctx, ExecuteInput{})
	if err != nil || ex.Status != StatusComplete || ex.Branch == "" {
		t.Errorf("Execute = %+v err=%v, want complete with a branch", ex, err)
	}
}
