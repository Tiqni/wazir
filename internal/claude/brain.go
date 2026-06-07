package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/EmadMokhtar/wazir/internal/config"
	"github.com/EmadMokhtar/wazir/internal/orchestrator"
)

// brainstormSystemPrompt is the headless contract (M2 spec §5.4), validated
// against claude 2.1.168. Tune brainstorm quality here.
const brainstormSystemPrompt = `You are the BRAINSTORM phase of an automated, human-gated dev-loop orchestrator. No live human is reachable this turn. You receive an issue transcript.

You MAY read the target repository for context using read-only tools (Read, Grep, Glob). Do NOT use AskUserQuestion or any interactive tool, do NOT edit or write files, do NOT run shell/Bash, and do NOT access the network.

In ONE response, do exactly one of:
(a) if the idea needs clarification before a spec can be written, ask ALL of your clarifying questions at once; or
(b) if the idea is already clear enough, write a complete implementation spec in markdown.

End your response with EXACTLY ONE fenced ` + "```json" + ` block and nothing after it, matching:
{"phase":"brainstorm","status":"needs_answers"|"spec_ready","questions":["..."],"spec_markdown":"..."}
Put ALL human-facing prose inside the JSON fields, never outside the block. Use "needs_answers" with a non-empty "questions" array, or "spec_ready" with a non-empty "spec_markdown".`

// brainstormAllowedTools lets the repo-aware brainstorm turn read the target repo
// (cwd = the clone) without giving it any write/exec/network capability.
var brainstormAllowedTools = []string{"Read", "Grep", "Glob"}

// brainstormDisallowedTools keeps the brainstorm turn pure reasoning (M2 spec §5.4).
var brainstormDisallowedTools = []string{"AskUserQuestion", "Bash", "Edit", "Write", "Task", "WebFetch", "WebSearch"}

// brainstormContract is the §9 brainstorm JSON contract.
type brainstormContract struct {
	Phase        string   `json:"phase"`
	Status       string   `json:"status"`
	Questions    []string `json:"questions"`
	SpecMarkdown string   `json:"spec_markdown"`
}

// Plan/Execute invoke the real Superpowers slash commands headless (validated by
// the M4 spike). The append-system-prompt forces the §9 JSON contract regardless
// of the skill's own output, and bans interactive turns.
const planSystemPrompt = `You are the PLAN phase of an automated, human-gated dev-loop orchestrator, running headless inside a git worktree of the target repository. No live human is reachable this turn: do NOT use AskUserQuestion or any interactive tool; if a skill would normally ask the human, proceed using the provided spec. Plan only — do NOT modify source files and do NOT push.

End your FINAL response with EXACTLY ONE fenced ` + "```json" + ` block and nothing after it, matching:
{"phase":"plan","status":"plan_ready"|"failed","plan_path":"<path to the plan file you wrote>","summary":"...","error":""}
Set plan_path to the actual path of the plan file created. Use "failed" with a non-empty "error" if no plan could be written. Put all prose inside the JSON fields.`

const executeSystemPrompt = `You are the EXECUTE phase of an automated, human-gated dev-loop orchestrator, running headless inside a git worktree on a fresh feature branch already checked out for you. No live human is reachable this turn: do NOT use AskUserQuestion or any interactive tool. Implement the plan, run the repository's tests, and COMMIT your work on the CURRENT branch. Do NOT push, do NOT open a pull request, do NOT change the git remote or create other branches — the orchestrator handles push/PR.

End your FINAL response with EXACTLY ONE fenced ` + "```json" + ` block and nothing after it, matching:
{"phase":"execute","status":"complete"|"failed","branch":"<current branch>","commits":["..."],"test_summary":"...","notes":"...","error":""}
Use "complete" only if the work is committed; otherwise "failed" with a non-empty "error". Put all prose inside the JSON fields.`

// planAllowedTools keeps the plan turn read-mostly (explore + write the plan file).
var planAllowedTools = []string{"Read", "Grep", "Glob", "Write", "Edit"}

type planContract struct {
	Phase    string `json:"phase"`
	Status   string `json:"status"`
	PlanPath string `json:"plan_path"`
	Summary  string `json:"summary"`
	Error    string `json:"error"`
}

type executeContract struct {
	Phase       string   `json:"phase"`
	Status      string   `json:"status"`
	Branch      string   `json:"branch"`
	Commits     []string `json:"commits"`
	TestSummary string   `json:"test_summary"`
	Notes       string   `json:"notes"`
	Error       string   `json:"error"`
}

// ClaudeBrain implements orchestrator.Brain via the headless claude CLI.
type ClaudeBrain struct {
	runner              *Runner
	model               string
	timeout             time.Duration
	planTimeout         time.Duration
	executeTimeout      time.Duration
	executeAllowedTools []string
	pluginDir           string
	settingSources      string
	log                 *zap.Logger
}

// New builds a ClaudeBrain from config. A nil logger becomes a no-op.
// Note: cfg.MaxBrainstormTurns is intentionally not read here — the question-loop
// cap is a Worker concern, applied via orchestrator Worker.WithMaxBrainstormTurns.
func New(cfg config.ClaudeConfig, log *zap.Logger) *ClaudeBrain {
	if log == nil {
		log = zap.NewNop()
	}
	return &ClaudeBrain{
		runner:              &Runner{bin: cfg.Bin, log: log},
		model:               cfg.Model,
		timeout:             cfg.Timeout,
		planTimeout:         cfg.PlanTimeout,
		executeTimeout:      cfg.ExecuteTimeout,
		executeAllowedTools: splitTools(cfg.ExecuteAllowedTools),
		pluginDir:           cfg.PluginDir,
		settingSources:      cfg.SettingSources,
		log:                 log,
	}
}

// splitTools turns the comma-separated allowlist config into argv form, trimming
// surrounding whitespace and dropping empty entries (so "Read, Edit" yields
// clean tool names the CLI accepts, not " Edit"). The default specs contain no
// commas, so splitting on comma is safe.
func splitTools(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Brainstorm runs one headless brainstorm turn. Expected failures (CLI error,
// missing/unparseable contract) are returned as a BrainstormFailed result, not a
// Go error, so the Worker routes them through its normal failure handling.
func (c *ClaudeBrain) Brainstorm(ctx context.Context, in orchestrator.BrainstormInput) (orchestrator.BrainstormResult, error) {
	res, err := c.runner.Run(ctx, RunSpec{
		Prompt:          in.Transcript,
		SystemPrompt:    brainstormSystemPrompt,
		Dir:             in.RepoPath,
		Model:           c.model,
		Timeout:         c.timeout,
		PermissionMode:  "default",
		AllowedTools:    brainstormAllowedTools,
		DisallowedTools: brainstormDisallowedTools,
		SettingSources:  c.settingSources,
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
	if ct.Phase != "brainstorm" {
		// Fail closed on CLI/prompt drift: a non-brainstorm contract that happens to
		// carry a known status must not be silently accepted (§12 CLI-drift guard).
		return orchestrator.BrainstormResult{Status: orchestrator.BrainstormFailed, Error: fmt.Sprintf("unexpected contract phase %q (want brainstorm)", ct.Phase)}, nil
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

// Plan runs one headless write-plan turn inside the worktree. Expected failures
// travel as a StatusFailed result (not a Go error), so the Worker handles them
// uniformly with its other failure paths.
func (c *ClaudeBrain) Plan(ctx context.Context, in orchestrator.PlanInput) (orchestrator.PlanResult, error) {
	res, err := c.runner.Run(ctx, RunSpec{
		Prompt:         "/superpowers:write-plan Write an implementation plan for this approved spec in the current repository.\n\nApproved spec:\n\n" + in.Spec + "\n\nIssue context:\n\n" + in.Transcript,
		SystemPrompt:   planSystemPrompt,
		Dir:            in.WorktreePath,
		Model:          c.model,
		Timeout:        c.planTimeout,
		AllowedTools:   planAllowedTools,
		PermissionMode: "acceptEdits",
		PluginDir:      c.pluginDir,
		SettingSources: c.settingSources,
	})
	if err != nil {
		return orchestrator.PlanResult{Status: orchestrator.StatusFailed, Error: err.Error()}, nil
	}
	c.log.Info("plan turn", zap.Float64("cost_usd", res.CostUSD), zap.Int("duration_ms", res.DurationMS), zap.String("session_id", res.SessionID))
	block, err := extractLastJSONBlock(res.Text)
	if err != nil {
		return orchestrator.PlanResult{Status: orchestrator.StatusFailed, Error: err.Error()}, nil
	}
	var ct planContract
	if err := json.Unmarshal([]byte(block), &ct); err != nil {
		return orchestrator.PlanResult{Status: orchestrator.StatusFailed, Error: fmt.Sprintf("unmarshal plan contract: %v", err)}, nil
	}
	if ct.Phase != "plan" {
		return orchestrator.PlanResult{Status: orchestrator.StatusFailed, Error: fmt.Sprintf("unexpected contract phase %q (want plan)", ct.Phase)}, nil
	}
	if ct.Status == "plan_ready" {
		if ct.PlanPath == "" {
			return orchestrator.PlanResult{Status: orchestrator.StatusFailed, Error: "plan_ready with empty plan_path"}, nil
		}
		return orchestrator.PlanResult{Status: orchestrator.StatusPlanReady, PlanPath: ct.PlanPath, Summary: ct.Summary}, nil
	}
	return orchestrator.PlanResult{Status: orchestrator.StatusFailed, Error: nonEmpty(ct.Error, "plan reported status "+ct.Status)}, nil
}

// Execute runs one headless execute-plan turn inside the worktree.
func (c *ClaudeBrain) Execute(ctx context.Context, in orchestrator.ExecuteInput) (orchestrator.ExecuteResult, error) {
	res, err := c.runner.Run(ctx, RunSpec{
		Prompt:         "/superpowers:execute-plan Execute the implementation plan at " + in.PlanPath + ". Commit your work on the current branch; do not push or open a PR.\n\nIssue context:\n\n" + in.Transcript,
		SystemPrompt:   executeSystemPrompt,
		Dir:            in.WorktreePath,
		Model:          c.model,
		Timeout:        c.executeTimeout,
		AllowedTools:   c.executeAllowedTools,
		PermissionMode: "acceptEdits",
		PluginDir:      c.pluginDir,
		SettingSources: c.settingSources,
	})
	if err != nil {
		return orchestrator.ExecuteResult{Status: orchestrator.StatusFailed, Error: err.Error()}, nil
	}
	c.log.Info("execute turn", zap.Float64("cost_usd", res.CostUSD), zap.Int("duration_ms", res.DurationMS), zap.String("session_id", res.SessionID))
	block, err := extractLastJSONBlock(res.Text)
	if err != nil {
		return orchestrator.ExecuteResult{Status: orchestrator.StatusFailed, Error: err.Error()}, nil
	}
	var ct executeContract
	if err := json.Unmarshal([]byte(block), &ct); err != nil {
		return orchestrator.ExecuteResult{Status: orchestrator.StatusFailed, Error: fmt.Sprintf("unmarshal execute contract: %v", err)}, nil
	}
	if ct.Phase != "execute" {
		return orchestrator.ExecuteResult{Status: orchestrator.StatusFailed, Error: fmt.Sprintf("unexpected contract phase %q (want execute)", ct.Phase)}, nil
	}
	if ct.Status == "complete" {
		return orchestrator.ExecuteResult{Status: orchestrator.StatusComplete, Branch: ct.Branch, Commits: ct.Commits, TestSummary: ct.TestSummary, Notes: ct.Notes}, nil
	}
	return orchestrator.ExecuteResult{Status: orchestrator.StatusFailed, Error: nonEmpty(ct.Error, "execute reported status "+ct.Status)}, nil
}

// nonEmpty returns a if non-empty, else fallback.
func nonEmpty(a, fallback string) string {
	if a != "" {
		return a
	}
	return fallback
}

var _ orchestrator.Brain = (*ClaudeBrain)(nil)
