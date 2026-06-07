package claude

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/EmadMokhtar/wazir/internal/config"
	"github.com/EmadMokhtar/wazir/internal/orchestrator"
)

func newTestBrain(t *testing.T, bin string) *ClaudeBrain {
	t.Helper()
	return New(config.ClaudeConfig{Bin: bin, Timeout: 5 * time.Second}, zap.NewNop())
}

func TestBrainstormNeedsAnswers(t *testing.T) {
	result := "```json\n{\"phase\":\"brainstorm\",\"status\":\"needs_answers\",\"questions\":[\"q1?\",\"q2?\"],\"spec_markdown\":\"\"}\n```"
	bin := writeFakeClaude(t, envelope(result, false, "success"), 0, 0)
	br := newTestBrain(t, bin)
	res, err := br.Brainstorm(context.Background(), orchestrator.BrainstormInput{Transcript: "# Idea\n\nbody"})
	if err != nil {
		t.Fatalf("Brainstorm: %v", err)
	}
	if res.Status != orchestrator.NeedsAnswers || len(res.Questions) != 2 || res.Questions[0] != "q1?" {
		t.Errorf("res = %+v", res)
	}
}

func TestBrainstormSpecReady(t *testing.T) {
	result := "```json\n{\"phase\":\"brainstorm\",\"status\":\"spec_ready\",\"questions\":[],\"spec_markdown\":\"# Spec\\nbody\"}\n```"
	bin := writeFakeClaude(t, envelope(result, false, "success"), 0, 0)
	br := newTestBrain(t, bin)
	res, err := br.Brainstorm(context.Background(), orchestrator.BrainstormInput{Transcript: "x"})
	if err != nil {
		t.Fatalf("Brainstorm: %v", err)
	}
	if res.Status != orchestrator.SpecReady || !strings.Contains(res.SpecMarkdown, "# Spec") {
		t.Errorf("res = %+v", res)
	}
}

func TestBrainstormFailsClosedOnBadOutput(t *testing.T) {
	// No fenced json block in the result text.
	bin := writeFakeClaude(t, envelope("I have some thoughts but no contract.", false, "success"), 0, 0)
	br := newTestBrain(t, bin)
	res, err := br.Brainstorm(context.Background(), orchestrator.BrainstormInput{Transcript: "x"})
	if err != nil {
		t.Fatalf("Brainstorm should report failure via result, not error: %v", err)
	}
	if res.Status != orchestrator.BrainstormFailed || res.Error == "" {
		t.Errorf("want failed status with Error, got %+v", res)
	}
}

func TestBrainstormFailsOnCLIError(t *testing.T) {
	bin := writeFakeClaude(t, "", 1, 0)
	br := newTestBrain(t, bin)
	res, err := br.Brainstorm(context.Background(), orchestrator.BrainstormInput{Transcript: "x"})
	if err != nil {
		t.Fatalf("unexpected error return: %v", err)
	}
	if res.Status != orchestrator.BrainstormFailed {
		t.Errorf("want failed, got %+v", res)
	}
}

func TestPlanAndExecuteDeferToWorktree(t *testing.T) {
	br := newTestBrain(t, "unused")
	if _, err := br.Plan(context.Background(), orchestrator.PlanInput{}); !errors.Is(err, orchestrator.ErrPhaseRequiresWorktree) {
		t.Errorf("Plan err = %v, want ErrPhaseRequiresWorktree", err)
	}
	if _, err := br.Execute(context.Background(), orchestrator.ExecuteInput{}); !errors.Is(err, orchestrator.ErrPhaseRequiresWorktree) {
		t.Errorf("Execute err = %v, want ErrPhaseRequiresWorktree", err)
	}
}

func TestClaudeBrainImplementsBrain(t *testing.T) {
	var _ orchestrator.Brain = (*ClaudeBrain)(nil)
}
