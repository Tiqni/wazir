// Package claude implements the orchestrator.Brain port by shelling out to the
// `claude` CLI headlessly (init-plan §8.4). The CLI emits a JSON array of events
// for --output-format json; the final {"type":"result"} element carries the text,
// cost, and session id (verified against claude 2.1.168 — M2 spec §2).
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Runner executes one headless `claude` invocation and returns the result event.
type Runner struct {
	bin string
	log *zap.Logger
}

// RunSpec is one invocation's inputs.
type RunSpec struct {
	Prompt          string
	SystemPrompt    string
	Dir             string // cmd.Dir; empty for brainstorm, a worktree in M4
	Model           string
	Timeout         time.Duration
	AllowedTools    []string
	DisallowedTools []string
	PermissionMode  string
}

// RunResult is the parsed {"type":"result"} envelope element.
type RunResult struct {
	Text       string
	SessionID  string
	CostUSD    float64
	NumTurns   int
	DurationMS int
	IsError    bool
	Subtype    string
}

// resultEvent decodes the fields we read off the result element. Other event
// types in the array decode into a mostly-zero value and are skipped.
type resultEvent struct {
	Type         string  `json:"type"`
	Subtype      string  `json:"subtype"`
	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	SessionID    string  `json:"session_id"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	NumTurns     int     `json:"num_turns"`
	DurationMS   int     `json:"duration_ms"`
}

// Run builds the claude command, executes it with a timeout, and parses stdout.
// It fails loudly on exec error, timeout, unparseable output, or a CLI-reported
// failure (the §12 CLI-drift guard).
func (r *Runner) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	args := []string{"-p", spec.Prompt, "--output-format", "json"}
	if spec.PermissionMode != "" {
		args = append(args, "--permission-mode", spec.PermissionMode)
	}
	if spec.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", spec.SystemPrompt)
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if len(spec.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(spec.AllowedTools, ","))
	}
	if len(spec.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(spec.DisallowedTools, ","))
	}

	cmd := exec.CommandContext(ctx, r.bin, args...)
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	// After a context-driven kill, grandchild processes can keep the stdout pipe
	// open, blocking cmd.Wait past the deadline. WaitDelay bounds that: Go force-
	// closes the pipes shortly after the kill. It only fires once ctx is done, so
	// the ctx.Err() checks below still attribute the failure correctly.
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return RunResult{}, fmt.Errorf("claude timed out after %s (stderr: %s)", spec.Timeout, strings.TrimSpace(stderr.String()))
	case context.Canceled:
		return RunResult{}, fmt.Errorf("claude cancelled (stderr: %s)", strings.TrimSpace(stderr.String()))
	}
	if runErr != nil {
		return RunResult{}, fmt.Errorf("claude exec: %w (stderr: %s)", runErr, strings.TrimSpace(stderr.String()))
	}

	ev, err := parseEnvelope(stdout.Bytes())
	if err != nil {
		return RunResult{}, fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	res := RunResult{
		Text: ev.Result, SessionID: ev.SessionID, CostUSD: ev.TotalCostUSD,
		NumTurns: ev.NumTurns, DurationMS: ev.DurationMS, IsError: ev.IsError, Subtype: ev.Subtype,
	}
	if ev.IsError || (ev.Subtype != "" && ev.Subtype != "success") {
		return res, fmt.Errorf("claude reported failure: subtype=%q is_error=%v", ev.Subtype, ev.IsError)
	}
	r.log.Debug("claude run", zap.Int("duration_ms", res.DurationMS), zap.Float64("cost_usd", res.CostUSD), zap.String("subtype", res.Subtype))
	return res, nil
}

// parseEnvelope finds the result element in the JSON array `claude` prints.
// Defensive: if a future/older CLI returns a single object, decode it directly.
func parseEnvelope(stdout []byte) (resultEvent, error) {
	var arr []resultEvent
	if err := json.Unmarshal(stdout, &arr); err == nil {
		for i := len(arr) - 1; i >= 0; i-- {
			if arr[i].Type == "result" {
				return arr[i], nil
			}
		}
		return resultEvent{}, fmt.Errorf("no result element in claude envelope array")
	}
	var obj resultEvent
	if err := json.Unmarshal(stdout, &obj); err != nil {
		return resultEvent{}, fmt.Errorf("parse claude envelope: %w", err)
	}
	if obj.Type != "" && obj.Type != "result" {
		return resultEvent{}, fmt.Errorf("unexpected claude envelope object type %q", obj.Type)
	}
	return obj, nil
}

// extractLastJSONBlock returns the body of the last fenced ```json block in text.
// It matches on whole physical lines, so fences embedded inside a JSON string
// value (where newlines are escaped as \n) never trigger a false match.
func extractLastJSONBlock(text string) (string, error) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "```json" {
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "```" {
					return strings.Join(lines[i+1:j], "\n"), nil
				}
			}
		}
	}
	return "", fmt.Errorf("no fenced ```json block found in claude output")
}
