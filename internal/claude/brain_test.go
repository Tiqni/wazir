package claude

import (
	"context"
	"os"
	"path/filepath"
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

func TestBrainstormFailsOnWrongPhase(t *testing.T) {
	// Valid status, but the contract's phase drifted to "plan" — must fail closed.
	result := "```json\n{\"phase\":\"plan\",\"status\":\"needs_answers\",\"questions\":[\"q?\"],\"spec_markdown\":\"\"}\n```"
	bin := writeFakeClaude(t, envelope(result, false, "success"), 0, 0)
	br := newTestBrain(t, bin)
	res, err := br.Brainstorm(context.Background(), orchestrator.BrainstormInput{Transcript: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != orchestrator.BrainstormFailed || !strings.Contains(res.Error, "phase") {
		t.Errorf("want failed on wrong phase, got %+v", res)
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

func TestPlanReady(t *testing.T) {
	result := "```json\n{\"phase\":\"plan\",\"status\":\"plan_ready\",\"plan_path\":\"docs/plan.md\",\"summary\":\"ok\",\"error\":\"\"}\n```"
	bin := writeFakeClaude(t, envelope(result, false, "success"), 0, 0)
	br := New(config.ClaudeConfig{Bin: bin, PlanTimeout: 5 * time.Second, ExecuteTimeout: 5 * time.Second,
		ExecuteAllowedTools: "Read,Edit,Write,Bash(git:*)"}, zap.NewNop())
	wt := t.TempDir()
	res, err := br.Plan(context.Background(), orchestrator.PlanInput{Transcript: "t", Spec: "s", WorktreePath: wt})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Status != orchestrator.StatusPlanReady || res.PlanPath != "docs/plan.md" {
		t.Errorf("res = %+v", res)
	}
	if pwd, _ := os.ReadFile(bin + ".pwd"); func() bool {
		got := strings.TrimSpace(string(pwd))
		// Resolve symlinks on both sides so the macOS /var→/private/var symlink doesn't
		// cause spurious mismatches (t.TempDir returns /var/…; pwd resolves to /private/var/…).
		gotReal, _ := filepath.EvalSymlinks(got)
		wtReal, _ := filepath.EvalSymlinks(wt)
		return gotReal != wtReal
	}() {
		t.Errorf("cmd.Dir = %q, want worktree %q", strings.TrimSpace(string(pwd)), wt)
	}
	args, _ := os.ReadFile(bin + ".args")
	if !strings.Contains(string(args), "/superpowers:write-plan") {
		t.Errorf("plan prompt must invoke the write-plan slash command; args:\n%s", args)
	}
	if strings.Contains(string(args), "Bash(") {
		t.Errorf("plan must not allow Bash; args:\n%s", args)
	}
	if !strings.Contains(string(args), "acceptEdits") {
		t.Errorf("plan must use acceptEdits; args:\n%s", args)
	}
}

func TestExecuteComplete(t *testing.T) {
	result := "```json\n{\"phase\":\"execute\",\"status\":\"complete\",\"branch\":\"feature/issue-7-x\",\"commits\":[\"abc\"],\"test_summary\":\"ok\",\"notes\":\"n\",\"error\":\"\"}\n```"
	bin := writeFakeClaude(t, envelope(result, false, "success"), 0, 0)
	br := New(config.ClaudeConfig{Bin: bin, PlanTimeout: 5 * time.Second, ExecuteTimeout: 5 * time.Second,
		ExecuteAllowedTools: "Read,Edit,Write,Bash(git:*)"}, zap.NewNop())
	wt := t.TempDir()
	res, err := br.Execute(context.Background(), orchestrator.ExecuteInput{Transcript: "t", PlanPath: "docs/plan.md", WorktreePath: wt})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != orchestrator.StatusComplete || res.Branch != "feature/issue-7-x" || len(res.Commits) != 1 {
		t.Errorf("res = %+v", res)
	}
	args, _ := os.ReadFile(bin + ".args")
	if !strings.Contains(string(args), "/superpowers:execute-plan") {
		t.Errorf("execute prompt must invoke the execute-plan slash command; args:\n%s", args)
	}
	if !strings.Contains(string(args), "Bash(git:*)") {
		t.Errorf("execute must carry the configured allowlist; args:\n%s", args)
	}
}

func TestPlanFailsClosedOnBadOutput(t *testing.T) {
	bin := writeFakeClaude(t, envelope("no contract here", false, "success"), 0, 0)
	br := New(config.ClaudeConfig{Bin: bin, PlanTimeout: 5 * time.Second}, zap.NewNop())
	res, err := br.Plan(context.Background(), orchestrator.PlanInput{WorktreePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Plan should report failure via result, not error: %v", err)
	}
	if res.Status != orchestrator.StatusFailed || res.Error == "" {
		t.Errorf("want failed+error, got %+v", res)
	}
}

func TestExecuteFailsOnCLIError(t *testing.T) {
	bin := writeFakeClaude(t, "", 1, 0)
	br := New(config.ClaudeConfig{Bin: bin, ExecuteTimeout: 5 * time.Second}, zap.NewNop())
	res, err := br.Execute(context.Background(), orchestrator.ExecuteInput{WorktreePath: t.TempDir()})
	if err != nil {
		t.Fatalf("unexpected error return: %v", err)
	}
	if res.Status != orchestrator.StatusFailed {
		t.Errorf("want failed, got %+v", res)
	}
}

func TestClaudeBrainImplementsBrain(t *testing.T) {
	var _ orchestrator.Brain = (*ClaudeBrain)(nil)
}
