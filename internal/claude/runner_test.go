package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// writeFakeClaude writes an executable shell script that acts as a fake `claude`:
// it records its argv to "<path>.args", optionally sleeps, prints stdout, and
// exits exitCode. Unix-only, which matches this project's targets.
func writeFakeClaude(t *testing.T, stdout string, exitCode, sleepSecs int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	sleepLine := ""
	if sleepSecs > 0 {
		sleepLine = fmt.Sprintf("sleep %d\n", sleepSecs)
	}
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$0.args\"\n" +
		"pwd > \"$0.pwd\"\n" +
		"env > \"$0.env\"\n" +
		sleepLine +
		"cat <<'WAZIR_EOF'\n" + stdout + "\nWAZIR_EOF\n" +
		fmt.Sprintf("exit %d\n", exitCode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

// envelope builds the JSON-array stdout shape that `claude --output-format json`
// emits (system event + result event), per the M2 spec §2.
func envelope(resultText string, isError bool, subtype string) string {
	rt, _ := json.Marshal(resultText) // quoted + escaped JSON string value
	return `[{"type":"system"},` +
		`{"type":"result","subtype":"` + subtype + `","is_error":` + boolStr(isError) +
		`,"result":` + string(rt) +
		`,"session_id":"sess-1","total_cost_usd":0.12,"num_turns":1,"duration_ms":2345}]`
}

func TestRunnerParsesArrayEnvelope(t *testing.T) {
	bin := writeFakeClaude(t, envelope("hello world", false, "success"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	res, err := r.Run(context.Background(), RunSpec{Prompt: "hi", Timeout: 5 * time.Second, PermissionMode: "default"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "hello world" || res.SessionID != "sess-1" || res.CostUSD != 0.12 || res.DurationMS != 2345 {
		t.Errorf("RunResult = %+v", res)
	}
}

func TestRunnerBuildsExpectedArgs(t *testing.T) {
	bin := writeFakeClaude(t, envelope("ok", false, "success"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	_, err := r.Run(context.Background(), RunSpec{
		Prompt: "TRANSCRIPT", SystemPrompt: "SYS", Model: "opus",
		PermissionMode: "default", DisallowedTools: []string{"AskUserQuestion", "Bash"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := os.ReadFile(bin + ".args")
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	for _, want := range []string{"-p", "TRANSCRIPT", "--append-system-prompt", "SYS",
		"--output-format", "json", "--permission-mode", "default", "--model", "opus",
		"--disallowedTools", "AskUserQuestion,Bash"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("argv missing %q; got:\n%s", want, got)
		}
	}
}

func TestRunnerFailsOnIsError(t *testing.T) {
	bin := writeFakeClaude(t, envelope("boom", true, "error_during_execution"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err == nil {
		t.Fatal("expected error on is_error=true")
	}
}

func TestRunnerFailsOnNonZeroExit(t *testing.T) {
	bin := writeFakeClaude(t, "", 2, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err == nil {
		t.Fatal("expected error on non-zero exit")
	}
}

func TestRunnerFailsOnGarbageStdout(t *testing.T) {
	bin := writeFakeClaude(t, "not json at all", 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err == nil {
		t.Fatal("expected error on unparseable stdout")
	}
}

func TestRunnerTimesOut(t *testing.T) {
	bin := writeFakeClaude(t, envelope("late", false, "success"), 0, 3)
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 150 * time.Millisecond}); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestExtractLastJSONBlock(t *testing.T) {
	text := "blah\n```json\n{\"a\":1}\n```\ntrailing"
	got, err := extractLastJSONBlock(text)
	if err != nil || strings.TrimSpace(got) != `{"a":1}` {
		t.Fatalf("got %q err %v", got, err)
	}
	// Two blocks: take the last.
	text2 := "```json\n{\"first\":true}\n```\nmid\n```json\n{\"second\":true}\n```"
	got2, _ := extractLastJSONBlock(text2)
	if !strings.Contains(got2, "second") || strings.Contains(got2, "first") {
		t.Errorf("want the last block, got %q", got2)
	}
	if _, err := extractLastJSONBlock("no block here"); err == nil {
		t.Error("expected error when no block present")
	}
}

func TestParseEnvelopeSingleObjectFallback(t *testing.T) {
	ev, err := parseEnvelope([]byte(`{"type":"result","subtype":"success","result":"hi","session_id":"s"}`))
	if err != nil || ev.Result != "hi" || ev.SessionID != "s" {
		t.Fatalf("fallback failed: %+v err %v", ev, err)
	}
}

func TestRunnerIsolatesEmptyDir(t *testing.T) {
	bin := writeFakeClaude(t, envelope("ok", false, "success"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	pwdBytes, err := os.ReadFile(bin + ".pwd")
	if err != nil {
		t.Fatalf("read pwd: %v", err)
	}
	// The shell's pwd already prints the resolved physical path; the run dir is
	// removed by the time we read this, so don't EvalSymlinks it (that would fail).
	got := strings.TrimSpace(string(pwdBytes))
	cwd, _ := os.Getwd()
	cwdResolved, _ := filepath.EvalSymlinks(cwd)
	if got == cwd || got == cwdResolved {
		t.Errorf("claude ran in the daemon cwd %q; want an isolated dir (else it auto-loads the repo's CLAUDE.md)", got)
	}
	tmpRoot, _ := filepath.EvalSymlinks(os.TempDir())
	if !strings.HasPrefix(got, tmpRoot) {
		t.Errorf("isolated run dir %q is not under the temp root %q", got, tmpRoot)
	}
}

func TestRunnerCuratesEnvDropsSecrets(t *testing.T) {
	t.Setenv("WAZIR_SECRET", "topsecret")
	bin := writeFakeClaude(t, envelope("ok", false, "success"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	env, err := os.ReadFile(bin + ".env")
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if strings.Contains(string(env), "WAZIR_SECRET") || strings.Contains(string(env), "topsecret") {
		t.Errorf("curated env leaked a WAZIR_* secret:\n%s", env)
	}
	if !strings.Contains(string(env), "HOME=") {
		t.Errorf("curated env dropped HOME:\n%s", env)
	}
}

func TestRunnerRelocatesConfigDirPerRun(t *testing.T) {
	bin := writeFakeClaude(t, envelope("ok", false, "success"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}

	readConfigDir := func() string {
		if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		env, err := os.ReadFile(bin + ".env")
		if err != nil {
			t.Fatalf("read env: %v", err)
		}
		for _, line := range strings.Split(string(env), "\n") {
			if v, ok := strings.CutPrefix(line, "CLAUDE_CONFIG_DIR="); ok {
				return v
			}
		}
		t.Fatal("CLAUDE_CONFIG_DIR not set in claude child env")
		return ""
	}

	first := readConfigDir()
	tmpRoot, _ := filepath.EvalSymlinks(os.TempDir())
	// The runner resolves cfgDir via EvalSymlinks before removal, so first is
	// already the canonical path. Use it directly rather than calling EvalSymlinks
	// on an already-deleted directory (which returns "" on all platforms).
	if !strings.HasPrefix(first, tmpRoot) {
		t.Errorf("config dir %q not under temp root %q", first, tmpRoot)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Errorf("config dir %q must be removed after the run (stat err=%v)", first, err)
	}
	if second := readConfigDir(); first == second {
		t.Errorf("config dir must be distinct per run; got %q twice", first)
	}
}

func TestRunnerKeepsOAuthTokenAndRealHome(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok-123")
	bin := writeFakeClaude(t, envelope("ok", false, "success"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	env, _ := os.ReadFile(bin + ".env")
	if !strings.Contains(string(env), "CLAUDE_CODE_OAUTH_TOKEN=tok-123") {
		t.Errorf("curated env dropped the OAuth token:\n%s", env)
	}
	if !strings.Contains(string(env), "HOME="+os.Getenv("HOME")) {
		t.Errorf("curated env must preserve the real HOME=%q:\n%s", os.Getenv("HOME"), env)
	}
}

func TestRunnerPassesPluginAndSettingFlags(t *testing.T) {
	bin := writeFakeClaude(t, envelope("ok", false, "success"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{
		Prompt: "x", Timeout: 5 * time.Second,
		PluginDir: "/plugins/superpowers", SettingSources: "user",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	args, _ := os.ReadFile(bin + ".args")
	for _, want := range []string{"--plugin-dir", "/plugins/superpowers", "--setting-sources", "user"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("argv missing %q; got:\n%s", want, args)
		}
	}
}

func TestRunnerRemovesConfigDirOnFailure(t *testing.T) {
	bin := writeFakeClaude(t, "", 2, 0) // non-zero exit, but env is written first
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	env, err := os.ReadFile(bin + ".env")
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	var cfgDir string
	for _, line := range strings.Split(string(env), "\n") {
		if v, ok := strings.CutPrefix(line, "CLAUDE_CONFIG_DIR="); ok {
			cfgDir = v
		}
	}
	if cfgDir == "" {
		t.Fatal("CLAUDE_CONFIG_DIR not captured")
	}
	if _, err := os.Stat(cfgDir); !os.IsNotExist(err) {
		t.Errorf("config dir %q must be removed even after a failed run (stat err=%v)", cfgDir, err)
	}
}

// An inherited CLAUDE_CONFIG_DIR in the daemon's own environment must never reach
// the claude child: the per-run isolated dir is authoritative. This guards the
// curatedEnv drop against a future reorder of the prefix check.
func TestRunnerDropsInheritedConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/attacker/config")
	bin := writeFakeClaude(t, envelope("ok", false, "success"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	env, err := os.ReadFile(bin + ".env")
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if strings.Contains(string(env), "/attacker/config") {
		t.Errorf("inherited CLAUDE_CONFIG_DIR must be dropped, not passed to the child:\n%s", env)
	}
	if !strings.Contains(string(env), "CLAUDE_CONFIG_DIR=") {
		t.Errorf("the per-run CLAUDE_CONFIG_DIR must still be set in the child env:\n%s", env)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
