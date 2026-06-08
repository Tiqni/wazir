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
	"os"
	"os/exec"
	"path/filepath"
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
	PluginsDir      string // M5: when set, seed the per-run config dir (symlink registry + settings.json) so the plugin's skills load; empty for brainstorm
	EnabledPlugin   string // M5: plugin id enabled in the seeded settings.json (e.g. "superpowers@claude-plugins-official")
	SettingSources  string // M5: --setting-sources <v> (stops a repo's .claude/settings.json widening tools)
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
	if spec.SettingSources != "" {
		args = append(args, "--setting-sources", spec.SettingSources)
	}

	cmd := exec.CommandContext(ctx, r.bin, args...)
	// Always run in an explicit directory. Worktree phases pass the worktree;
	// worktree-less phases (brainstorm) leave Dir empty — give them a fresh temp
	// dir so claude never inherits the daemon's cwd and auto-loads an unrelated
	// project CLAUDE.md / plugin config, which would contaminate the turn with the
	// orchestrator's own repo context instead of the card's.
	dir := spec.Dir
	if dir == "" {
		tmp, err := os.MkdirTemp("", "wazir-run-")
		if err != nil {
			return RunResult{}, fmt.Errorf("create isolated run dir: %w", err)
		}
		defer os.RemoveAll(tmp)
		dir = tmp
	}
	cmd.Dir = dir
	// Per-run isolated config dir: an empty CLAUDE_CONFIG_DIR means no global
	// ~/.claude/CLAUDE.md, no globally-enabled plugins, no global MCP, and isolated
	// session state (parallel-safe). Removed when the run returns, on every path.
	cfgDir, err := os.MkdirTemp("", "wazir-cfg-")
	if err != nil {
		return RunResult{}, fmt.Errorf("create isolated config dir: %w", err)
	}
	// Resolve any OS-level symlinks (e.g. /var → /private/var on macOS) so that
	// callers and tests can compare the path without EvalSymlinks on a deleted dir.
	if resolved, resolveErr := filepath.EvalSymlinks(cfgDir); resolveErr == nil {
		cfgDir = resolved
	}
	defer func() {
		if rmErr := os.RemoveAll(cfgDir); rmErr != nil {
			r.log.Warn("remove per-run config dir", zap.String("dir", cfgDir), zap.Error(rmErr))
		}
	}()
	// Plan/execute need the Superpowers skills, which a relocated config dir doesn't
	// have. Seed it: symlink the real plugin registry in + enable only the configured
	// plugin (M5 spike: --plugin-dir does not register a marketplace plugin's skills).
	// Brainstorm leaves PluginsDir empty, so it stays a bare, plugin-free config dir.
	if spec.PluginsDir != "" {
		if err := seedConfigDir(cfgDir, spec.PluginsDir, spec.EnabledPlugin); err != nil {
			return RunResult{}, fmt.Errorf("seed config dir: %w", err)
		}
	}
	cmd.Env = append(curatedEnv(), "CLAUDE_CONFIG_DIR="+cfgDir)
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

// seedConfigDir makes a relocated CLAUDE_CONFIG_DIR usable for a plan/execute turn:
// it symlinks the real plugin registry in and writes a settings.json that enables
// ONLY enabledPlugin — so the Superpowers skills load while the global
// ~/.claude/CLAUDE.md and other plugins stay out. (M5 spike: --plugin-dir does not
// register a marketplace plugin's skills under a relocated config dir; the
// registration must be present.)
func seedConfigDir(cfgDir, pluginsDir, enabledPlugin string) error {
	if err := os.Symlink(pluginsDir, filepath.Join(cfgDir, "plugins")); err != nil {
		return fmt.Errorf("symlink plugins: %w", err)
	}
	settings, err := json.Marshal(map[string]any{
		"enabledPlugins": map[string]bool{enabledPlugin: true},
	})
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "settings.json"), settings, 0o600); err != nil {
		return fmt.Errorf("write settings.json: %w", err)
	}
	return nil
}

// curatedEnv returns a secret-free environment for the claude child: the vars
// the CLI needs to run + authenticate (HOME/PATH + ANTHROPIC_*/CLAUDE_*/XDG_*),
// with WAZIR_* host secrets dropped so card-controlled tool runs can't read them.
func curatedEnv() []string {
	keepExact := map[string]bool{
		"HOME": true, "PATH": true, "USER": true, "LOGNAME": true,
		"SHELL": true, "LANG": true, "TERM": true, "TMPDIR": true,
	}
	keepPrefix := []string{"ANTHROPIC_", "CLAUDE_", "XDG_", "LC_", "SSL_CERT"}
	keep := func(k string) bool {
		if k == "CLAUDE_CONFIG_DIR" {
			return false // set per-run by Run, never inherited
		}
		if keepExact[k] {
			return true
		}
		for _, p := range keepPrefix {
			if strings.HasPrefix(k, p) {
				return true
			}
		}
		return false
	}
	var out []string
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 && keep(kv[:i]) {
			out = append(out, kv)
		}
	}
	return out
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
