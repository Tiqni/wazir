package orchestrator

import (
	"context"
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

func TestPlanExecuteInputsCarryWorktreePath(t *testing.T) {
	p := PlanInput{Transcript: "t", Spec: "s", WorktreePath: "/wt"}
	e := ExecuteInput{Transcript: "t", PlanPath: "P", WorktreePath: "/wt"}
	if p.WorktreePath != "/wt" || e.WorktreePath != "/wt" {
		t.Errorf("WorktreePath not carried: %+v %+v", p, e)
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
