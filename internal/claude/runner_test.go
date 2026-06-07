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

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
