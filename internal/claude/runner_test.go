package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		// Capture what the runner seeded into the per-run config dir (empty when unseeded).
		"readlink \"$CLAUDE_CONFIG_DIR/plugins\" > \"$0.plugins\" 2>/dev/null || true\n" +
		"cat \"$CLAUDE_CONFIG_DIR/settings.json\" > \"$0.settings\" 2>/dev/null || true\n" +
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

func TestRunnerSeedsConfigDirForPlanExecute(t *testing.T) {
	pluginsDir := t.TempDir() // stand-in for the real ~/.claude/plugins registry
	bin := writeFakeClaude(t, envelope("ok", false, "success"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{
		Prompt: "x", Timeout: 5 * time.Second,
		PluginsDir: pluginsDir, EnabledPlugin: "superpowers@claude-plugins-official", SettingSources: "user",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The child saw a CLAUDE_CONFIG_DIR/plugins symlink pointing at the registry...
	link, _ := os.ReadFile(bin + ".plugins")
	if strings.TrimSpace(string(link)) != pluginsDir {
		t.Errorf("seeded plugins symlink = %q, want %q", strings.TrimSpace(string(link)), pluginsDir)
	}
	// ...and a settings.json enabling only the configured plugin.
	settings, _ := os.ReadFile(bin + ".settings")
	if !strings.Contains(string(settings), `"superpowers@claude-plugins-official":true`) {
		t.Errorf("seeded settings.json missing enabledPlugins entry; got:\n%s", settings)
	}
	args, _ := os.ReadFile(bin + ".args")
	if !strings.Contains(string(args), "--setting-sources") || !strings.Contains(string(args), "user") {
		t.Errorf("argv missing --setting-sources user; got:\n%s", args)
	}
}

func TestRunnerDoesNotSeedWhenPluginsDirEmpty(t *testing.T) {
	bin := writeFakeClaude(t, envelope("ok", false, "success"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// brainstorm-style run (no PluginsDir): the config dir stays bare — no plugins symlink.
	link, _ := os.ReadFile(bin + ".plugins")
	if strings.TrimSpace(string(link)) != "" {
		t.Errorf("unseeded run must not symlink plugins; got %q", strings.TrimSpace(string(link)))
	}
}

func TestSeedConfigDir(t *testing.T) {
	cfgDir := t.TempDir()
	pluginsDir := t.TempDir()
	if err := seedConfigDir(cfgDir, pluginsDir, "superpowers@claude-plugins-official"); err != nil {
		t.Fatalf("seedConfigDir: %v", err)
	}
	target, err := os.Readlink(filepath.Join(cfgDir, "plugins"))
	if err != nil || target != pluginsDir {
		t.Errorf("plugins symlink = %q (err %v), want %q", target, err, pluginsDir)
	}
	settings, err := os.ReadFile(filepath.Join(cfgDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	if !strings.Contains(string(settings), `"superpowers@claude-plugins-official":true`) {
		t.Errorf("settings.json missing enabledPlugins entry; got:\n%s", settings)
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

// Parallel safety is the point of the per-run config dir: concurrent runs must
// each get their own CLAUDE_CONFIG_DIR (spec §12). Run under -race.
func TestRunnerConfigDirsDistinctUnderConcurrency(t *testing.T) {
	const n = 8
	// Pre-create the fake bins on the test goroutine (writeFakeClaude calls
	// t.TempDir/t.Fatalf, which must not run in a spawned goroutine).
	bins := make([]string, n)
	for i := range bins {
		bins[i] = writeFakeClaude(t, envelope("ok", false, "success"), 0, 0)
	}
	dirs := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := &Runner{bin: bins[i], log: zap.NewNop()}
			if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err != nil {
				t.Errorf("run %d: %v", i, err)
				return
			}
			env, err := os.ReadFile(bins[i] + ".env")
			if err != nil {
				t.Errorf("run %d read env: %v", i, err)
				return
			}
			for _, line := range strings.Split(string(env), "\n") {
				if v, ok := strings.CutPrefix(line, "CLAUDE_CONFIG_DIR="); ok {
					dirs[i] = v
				}
			}
		}(i)
	}
	wg.Wait()
	seen := make(map[string]bool, n)
	for i, d := range dirs {
		if d == "" {
			t.Fatalf("run %d had no CLAUDE_CONFIG_DIR", i)
		}
		if seen[d] {
			t.Errorf("config dir %q reused across concurrent runs", d)
		}
		seen[d] = true
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("config dir %q not removed after concurrent run", d)
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// countingClaude writes a fake `claude` that exits non-zero with a chosen stderr
// on its first failFirst calls, then prints a success envelope. Counts via a
// marker file.
func countingClaude(t *testing.T, failFirst int, failStderr, okText string) string {
	t.Helper()
	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"n=$(cat '" + countPath + "' 2>/dev/null || echo 0)\n" +
		"n=$((n+1)); echo $n > '" + countPath + "'\n" +
		"if [ \"$n\" -le " + fmt.Sprint(failFirst) + " ]; then\n" +
		"  echo '" + failStderr + "' >&2; exit 1\n" +
		"fi\n" +
		"cat <<'EOF'\n" + envelope(okText, false, "success") + "\nEOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTransientClaudeClassifier(t *testing.T) {
	// No work happened (empty result) + a transport-ish error => retry.
	if !transientClaude(RunResult{}, errors.New("claude exec: exec: \"claude\": executable file not found in $PATH")) {
		t.Error("spawn failure must be transient")
	}
	if !transientClaude(RunResult{}, errors.New("claude exec: exit status 1 (stderr: overloaded_error 529)")) {
		t.Error("overloaded 529 with no work must be transient")
	}
	// Work happened / model-reported outcome => never retry.
	if transientClaude(RunResult{IsError: true, Subtype: "error_during_execution", SessionID: "s1"}, errors.New("claude reported failure")) {
		t.Error("a model-reported failure must NOT be transient")
	}
	if transientClaude(RunResult{Text: "partial"}, errors.New("claude exec: overloaded")) {
		t.Error("any produced result means work happened; must NOT retry")
	}
	// A timeout is not retried (work likely ran the full duration).
	if transientClaude(RunResult{}, errors.New("claude timed out after 5m")) {
		t.Error("timeout must NOT be transient")
	}
	if transientClaude(RunResult{}, nil) {
		t.Error("nil error must NOT be transient")
	}
	// A wrapped context sentinel must be caught even without the magic string.
	if transientClaude(RunResult{}, fmt.Errorf("some wrapper: %w", context.DeadlineExceeded)) {
		t.Error("a wrapped DeadlineExceeded must NOT be transient")
	}
	// The string fallback must match Go's own single-l "canceled" spelling, not
	// just "cancelled" (for an unwrapped error that doesn't carry the sentinel).
	if transientClaude(RunResult{}, errors.New("boom: context canceled")) {
		t.Error("an unwrapped \"context canceled\" must NOT be transient")
	}
}

func TestRunnerRetriesTransportFailure(t *testing.T) {
	oldDelay := transportBaseDelay
	transportBaseDelay = time.Millisecond
	defer func() { transportBaseDelay = oldDelay }()

	bin := countingClaude(t, 1, "overloaded_error 529", "recovered")
	r := &Runner{bin: bin, log: zap.NewNop(), maxTransportRetries: 2}
	res, err := r.Run(context.Background(), RunSpec{Prompt: "hi"})
	if err != nil || res.Text != "recovered" {
		t.Fatalf("res=%+v err=%v, want a retry then success", res, err)
	}
}

func TestRunnerDoesNotRetryModelFailure(t *testing.T) {
	// is_error=true is a real turn outcome; runOnce returns a populated result +
	// error, so Run must return immediately without a second invocation.
	bin := writeFakeClaude(t, envelope("boom", true, "error_during_execution"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop(), maxTransportRetries: 3}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "hi"}); err == nil {
		t.Fatal("want the model-reported failure surfaced")
	}
}
