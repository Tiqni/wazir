package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/EmadMokhtar/wazir/internal/config"
	"github.com/EmadMokhtar/wazir/internal/orchestrator"
)

// brainstormSystemPrompt is the headless contract (M2 spec §5.4), validated
// against claude 2.1.168. Tune brainstorm quality here.
const brainstormSystemPrompt = `You are the BRAINSTORM phase of an automated, human-gated dev-loop orchestrator. No live human is reachable this turn. You receive an issue transcript.

Do NOT use AskUserQuestion or any interactive, file, or shell tool.

In ONE response, do exactly one of:
(a) if the idea needs clarification before a spec can be written, ask ALL of your clarifying questions at once; or
(b) if the idea is already clear enough, write a complete implementation spec in markdown.

End your response with EXACTLY ONE fenced ` + "```json" + ` block and nothing after it, matching:
{"phase":"brainstorm","status":"needs_answers"|"spec_ready","questions":["..."],"spec_markdown":"..."}
Put ALL human-facing prose inside the JSON fields, never outside the block. Use "needs_answers" with a non-empty "questions" array, or "spec_ready" with a non-empty "spec_markdown".`

// brainstormDisallowedTools keeps the brainstorm turn pure reasoning (M2 spec §5.4).
var brainstormDisallowedTools = []string{"AskUserQuestion", "Bash", "Edit", "Write", "Task", "WebFetch", "WebSearch"}

// brainstormContract is the §9 brainstorm JSON contract.
type brainstormContract struct {
	Phase        string   `json:"phase"`
	Status       string   `json:"status"`
	Questions    []string `json:"questions"`
	SpecMarkdown string   `json:"spec_markdown"`
}

// ClaudeBrain implements orchestrator.Brain via the headless claude CLI.
type ClaudeBrain struct {
	runner  *Runner
	model   string
	timeout time.Duration
	log     *zap.Logger
}

// New builds a ClaudeBrain from config. A nil logger becomes a no-op.
func New(cfg config.ClaudeConfig, log *zap.Logger) *ClaudeBrain {
	if log == nil {
		log = zap.NewNop()
	}
	return &ClaudeBrain{
		runner:  &Runner{bin: cfg.Bin, log: log},
		model:   cfg.Model,
		timeout: cfg.Timeout,
		log:     log,
	}
}

// Brainstorm runs one headless brainstorm turn. Expected failures (CLI error,
// missing/unparseable contract) are returned as a BrainstormFailed result, not a
// Go error, so the Worker routes them through its normal failure handling.
func (c *ClaudeBrain) Brainstorm(ctx context.Context, in orchestrator.BrainstormInput) (orchestrator.BrainstormResult, error) {
	res, err := c.runner.Run(ctx, RunSpec{
		Prompt:          in.Transcript,
		SystemPrompt:    brainstormSystemPrompt,
		Model:           c.model,
		Timeout:         c.timeout,
		PermissionMode:  "default",
		DisallowedTools: brainstormDisallowedTools,
	})
	if err != nil {
		return orchestrator.BrainstormResult{Status: orchestrator.BrainstormFailed, Error: err.Error()}, nil
	}
	c.log.Info("brainstorm turn",
		zap.Float64("cost_usd", res.CostUSD),
		zap.Int("duration_ms", res.DurationMS),
		zap.String("session_id", res.SessionID))

	block, err := extractLastJSONBlock(res.Text)
	if err != nil {
		return orchestrator.BrainstormResult{Status: orchestrator.BrainstormFailed, Error: err.Error()}, nil
	}
	var ct brainstormContract
	if err := json.Unmarshal([]byte(block), &ct); err != nil {
		return orchestrator.BrainstormResult{Status: orchestrator.BrainstormFailed, Error: fmt.Sprintf("unmarshal contract: %v", err)}, nil
	}
	switch ct.Status {
	case "needs_answers":
		if len(ct.Questions) == 0 {
			return orchestrator.BrainstormResult{Status: orchestrator.BrainstormFailed, Error: "needs_answers with no questions"}, nil
		}
		return orchestrator.BrainstormResult{Status: orchestrator.NeedsAnswers, Questions: ct.Questions}, nil
	case "spec_ready":
		if ct.SpecMarkdown == "" {
			return orchestrator.BrainstormResult{Status: orchestrator.BrainstormFailed, Error: "spec_ready with empty spec_markdown"}, nil
		}
		return orchestrator.BrainstormResult{Status: orchestrator.SpecReady, SpecMarkdown: ct.SpecMarkdown}, nil
	default:
		return orchestrator.BrainstormResult{Status: orchestrator.BrainstormFailed, Error: fmt.Sprintf("unknown contract status %q", ct.Status)}, nil
	}
}

// Plan and Execute need an isolated worktree; they land in M4. Until then they
// return the sentinel the Worker recognizes for a friendly deferral.
func (c *ClaudeBrain) Plan(ctx context.Context, in orchestrator.PlanInput) (orchestrator.PlanResult, error) {
	return orchestrator.PlanResult{}, orchestrator.ErrPhaseRequiresWorktree
}

func (c *ClaudeBrain) Execute(ctx context.Context, in orchestrator.ExecuteInput) (orchestrator.ExecuteResult, error) {
	return orchestrator.ExecuteResult{}, orchestrator.ErrPhaseRequiresWorktree
}

var _ orchestrator.Brain = (*ClaudeBrain)(nil)
