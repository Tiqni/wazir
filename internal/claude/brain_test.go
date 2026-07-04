package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/EmadMokhtar/wazir/internal/config"
	"github.com/EmadMokhtar/wazir/internal/forge"
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
	if !strings.Contains(string(args), "writing-plans") {
		t.Errorf("plan prompt must name the writing-plans skill; args:\n%s", args)
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
	if !strings.Contains(string(args), "executing-plans") {
		t.Errorf("execute prompt must name the executing-plans skill; args:\n%s", args)
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

func TestPlanFailsOnWrongPhase(t *testing.T) {
	// Valid status but the contract's phase is "brainstorm" — must fail closed.
	result := "```json\n{\"phase\":\"brainstorm\",\"status\":\"plan_ready\",\"plan_path\":\"docs/plan.md\"}\n```"
	bin := writeFakeClaude(t, envelope(result, false, "success"), 0, 0)
	br := New(config.ClaudeConfig{Bin: bin, PlanTimeout: 5 * time.Second}, zap.NewNop())
	res, err := br.Plan(context.Background(), orchestrator.PlanInput{WorktreePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Plan should report failure via result, not error: %v", err)
	}
	if res.Status != orchestrator.StatusFailed || res.Error == "" {
		t.Errorf("wrong phase must fail closed, got %+v", res)
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

func TestBrainstormRunsInRepoCloneReadOnly(t *testing.T) {
	result := "```json\n{\"phase\":\"brainstorm\",\"status\":\"needs_answers\",\"questions\":[\"q?\"],\"spec_markdown\":\"\"}\n```"
	bin := writeFakeClaude(t, envelope(result, false, "success"), 0, 0)
	br := New(config.ClaudeConfig{Bin: bin, Timeout: 5 * time.Second, PluginsDir: t.TempDir(), PluginID: "superpowers@claude-plugins-official", SettingSources: "user"}, zap.NewNop())
	clone := t.TempDir()
	if _, err := br.Brainstorm(context.Background(), orchestrator.BrainstormInput{Transcript: "x", RepoPath: clone}); err != nil {
		t.Fatalf("Brainstorm: %v", err)
	}
	pwd, _ := os.ReadFile(bin + ".pwd")
	gotReal, _ := filepath.EvalSymlinks(strings.TrimSpace(string(pwd)))
	cloneReal, _ := filepath.EvalSymlinks(clone)
	if gotReal != cloneReal {
		t.Errorf("brainstorm cmd.Dir = %q, want the repo clone %q", strings.TrimSpace(string(pwd)), clone)
	}
	args, _ := os.ReadFile(bin + ".args")
	if !strings.Contains(string(args), "Read,Grep,Glob") {
		t.Errorf("brainstorm must allow read-only exploration; args:\n%s", args)
	}
	if !strings.Contains(string(args), "--setting-sources") || !strings.Contains(string(args), "user") {
		t.Errorf("brainstorm must carry --setting-sources; args:\n%s", args)
	}
	// Brainstorm needs no plugin: the config dir must NOT be seeded even though the
	// brain is configured with a plugins dir.
	if link, _ := os.ReadFile(bin + ".plugins"); strings.TrimSpace(string(link)) != "" {
		t.Errorf("brainstorm must NOT seed a plugin; got symlink %q", strings.TrimSpace(string(link)))
	}
}

func TestPlanAndExecuteSeedPluginAndUseSkill(t *testing.T) {
	pluginsDir := t.TempDir()
	cfg := func(bin string) config.ClaudeConfig {
		return config.ClaudeConfig{
			Bin: bin, PlanTimeout: 5 * time.Second, ExecuteTimeout: 5 * time.Second,
			ExecuteAllowedTools: "Skill,Read,Edit", PluginsDir: pluginsDir,
			PluginID: "superpowers@claude-plugins-official", SettingSources: "user",
		}
	}

	// Plan: seeds the registry, allows the Skill tool, and names the writing-plans skill.
	planJSON := "```json\n{\"phase\":\"plan\",\"status\":\"plan_ready\",\"plan_path\":\"docs/plan.md\",\"summary\":\"s\",\"error\":\"\"}\n```"
	planBin := writeFakeClaude(t, envelope(planJSON, false, "success"), 0, 0)
	if _, err := New(cfg(planBin), zap.NewNop()).Plan(context.Background(), orchestrator.PlanInput{Spec: "s", WorktreePath: t.TempDir()}); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if link, _ := os.ReadFile(planBin + ".plugins"); strings.TrimSpace(string(link)) != pluginsDir {
		t.Errorf("plan must seed the plugins registry; symlink = %q, want %q", strings.TrimSpace(string(link)), pluginsDir)
	}
	planArgs, _ := os.ReadFile(planBin + ".args")
	if !strings.Contains(string(planArgs), "Skill") {
		t.Errorf("plan must allow the Skill tool; args:\n%s", planArgs)
	}
	if !strings.Contains(string(planArgs), "writing-plans") {
		t.Errorf("plan prompt must name the writing-plans skill; args:\n%s", planArgs)
	}

	// Execute (the highest-risk phase): same seeding + Skill, names executing-plans.
	execJSON := "```json\n{\"phase\":\"execute\",\"status\":\"complete\",\"branch\":\"b\",\"commits\":[\"a\"],\"test_summary\":\"ok\",\"notes\":\"n\",\"error\":\"\"}\n```"
	execBin := writeFakeClaude(t, envelope(execJSON, false, "success"), 0, 0)
	if _, err := New(cfg(execBin), zap.NewNop()).Execute(context.Background(), orchestrator.ExecuteInput{PlanPath: "docs/plan.md", WorktreePath: t.TempDir()}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if link, _ := os.ReadFile(execBin + ".plugins"); strings.TrimSpace(string(link)) != pluginsDir {
		t.Errorf("execute must seed the plugins registry; symlink = %q, want %q", strings.TrimSpace(string(link)), pluginsDir)
	}
	execArgs, _ := os.ReadFile(execBin + ".args")
	if !strings.Contains(string(execArgs), "Skill") {
		t.Errorf("execute must allow the Skill tool; args:\n%s", execArgs)
	}
	if !strings.Contains(string(execArgs), "executing-plans") {
		t.Errorf("execute prompt must name the executing-plans skill; args:\n%s", execArgs)
	}
}

func TestReworkComplete(t *testing.T) {
	result := "```json\n{\"phase\":\"rework\",\"status\":\"complete\",\"commits\":[\"def\"],\"test_summary\":\"ok\",\"notes\":\"fixed lint\",\"error\":\"\"}\n```"
	bin := writeFakeClaude(t, envelope(result, false, "success"), 0, 0)
	br := New(config.ClaudeConfig{Bin: bin, ExecuteTimeout: 5 * time.Second,
		ReworkTimeout: 5 * time.Second, ReworkAllowedTools: "Read,Edit,Write,Bash(git:*)"}, zap.NewNop())
	wt := t.TempDir()
	res, err := br.Rework(context.Background(), orchestrator.ReworkInput{
		Transcript:    "t",
		WorktreePath:  wt,
		Feedback:      forge.ReviewFeedback{Summary: "wrap the error", Comments: []forge.InlineComment{{Path: "main.go", Line: 42, Body: "here"}}},
		FailingChecks: []string{"lint"},
		Annotations:   []forge.CheckAnnotation{{Check: "lint", Path: "main.go", Line: 10, Level: "failure", Message: "undefined: foo"}},
	})
	if err != nil {
		t.Fatalf("Rework: %v", err)
	}
	if res.Status != orchestrator.StatusComplete {
		t.Errorf("res = %+v", res)
	}
	args, _ := os.ReadFile(bin + ".args")
	if !strings.Contains(string(args), "wrap the error") || !strings.Contains(string(args), "undefined: foo") {
		t.Errorf("rework prompt must carry feedback + annotations; args:\n%s", args)
	}
	if !strings.Contains(string(args), "Bash(git:*)") {
		t.Errorf("rework must carry the configured allowlist; args:\n%s", args)
	}
	if res.Notes != "fixed lint" {
		t.Errorf("Notes = %q, want fixed lint", res.Notes)
	}
}

func TestReworkFailsOnCLIError(t *testing.T) {
	bin := writeFakeClaude(t, "", 1, 0)
	br := New(config.ClaudeConfig{Bin: bin, ExecuteTimeout: 5 * time.Second}, zap.NewNop())
	res, err := br.Rework(context.Background(), orchestrator.ReworkInput{WorktreePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Rework should report failure via result, not error: %v", err)
	}
	if res.Status != orchestrator.StatusFailed {
		t.Errorf("CLI error must fail closed, got %+v", res)
	}
}

func TestReworkFailsOnWrongPhase(t *testing.T) {
	// Valid status but the contract's phase is "execute" — must fail closed.
	result := "```json\n{\"phase\":\"execute\",\"status\":\"complete\"}\n```"
	bin := writeFakeClaude(t, envelope(result, false, "success"), 0, 0)
	br := New(config.ClaudeConfig{Bin: bin, ExecuteTimeout: 5 * time.Second}, zap.NewNop())
	res, err := br.Rework(context.Background(), orchestrator.ReworkInput{WorktreePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Rework should report failure via result, not error: %v", err)
	}
	if res.Status != orchestrator.StatusFailed || res.Error == "" {
		t.Errorf("wrong phase must fail closed, got %+v", res)
	}
}

func TestReworkFailedStatusCarriesError(t *testing.T) {
	result := "```json\n{\"phase\":\"rework\",\"status\":\"failed\",\"error\":\"tests still red\"}\n```"
	bin := writeFakeClaude(t, envelope(result, false, "success"), 0, 0)
	br := New(config.ClaudeConfig{Bin: bin, ExecuteTimeout: 5 * time.Second}, zap.NewNop())
	res, err := br.Rework(context.Background(), orchestrator.ReworkInput{WorktreePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Rework: %v", err)
	}
	if res.Status != orchestrator.StatusFailed || res.Error != "tests still red" {
		t.Errorf("failed status must surface the error, got %+v", res)
	}
}

func TestSplitToolsTrimsAndDropsEmpties(t *testing.T) {
	got := splitTools("Read, Edit ,, Bash(git:*) ")
	want := []string{"Read", "Edit", "Bash(git:*)"}
	if len(got) != len(want) {
		t.Fatalf("splitTools = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("splitTools[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if splitTools("") != nil || splitTools(" , ") != nil {
		t.Errorf("empty/whitespace-only input must yield nil, got %#v / %#v", splitTools(""), splitTools(" , "))
	}
}

func TestBrainReloadSwapsModel(t *testing.T) {
	planJSON := "```json\n{\"phase\":\"plan\",\"status\":\"plan_ready\",\"plan_path\":\"p.md\",\"summary\":\"s\",\"error\":\"\"}\n```"
	bin := writeFakeClaude(t, envelope(planJSON, false, "success"), 0, 0)
	br := New(config.ClaudeConfig{Bin: bin, Model: "model-a", PlanTimeout: 5 * time.Second}, zap.NewNop())

	if _, err := br.Plan(context.Background(), orchestrator.PlanInput{Spec: "s", WorktreePath: t.TempDir()}); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if args, _ := os.ReadFile(bin + ".args"); !strings.Contains(string(args), "model-a") {
		t.Fatalf("first turn argv missing model-a:\n%s", args)
	}

	// Bin is intentionally omitted: Reload does not change the binary (restart-only);
	// the fake binary is already wired into the runner from New.
	br.Reload(config.ClaudeConfig{Model: "model-b", PlanTimeout: 5 * time.Second})
	if _, err := br.Plan(context.Background(), orchestrator.PlanInput{Spec: "s", WorktreePath: t.TempDir()}); err != nil {
		t.Fatalf("Plan after reload: %v", err)
	}
	args, _ := os.ReadFile(bin + ".args")
	if !strings.Contains(string(args), "model-b") {
		t.Errorf("after Reload, argv must carry model-b; got:\n%s", args)
	}
}

func TestBrainReloadRace(t *testing.T) {
	br := New(config.ClaudeConfig{Bin: "true", Model: "a"}, zap.NewNop())
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); for j := 0; j < 1000; j++ { _ = br.settings.Load().model } }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); for j := 0; j < 1000; j++ { br.Reload(config.ClaudeConfig{Model: "b"}) } }()
	wg.Wait()
}
