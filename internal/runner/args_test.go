package runner

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Bin:       "/usr/bin/claude",
		WorkDir:   "/home/Bill/Code_Projects/GO_Projects/Claude-Alarm-Clock",
		Model:     "haiku",
		Prompt:    "Hello world",
		BudgetUSD: 0.10,
		Timeout:   120 * time.Second,
	}
}

func TestBuildArgsGolden(t *testing.T) {
	want := []string{
		"-p",
		"--model", "haiku",
		"--output-format", "json",
		"--safe-mode",
		"--tools", "",
		"--permission-mode", "dontAsk",
		"--max-budget-usd", "0.10",
		"--no-session-persistence",
		"Hello world",
	}

	got := BuildArgs(testConfig())

	if !slices.Equal(got, want) {
		t.Fatalf("BuildArgs mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

// The prompt must always be last, and must never be treated as a flag even if
// it starts with a dash.
func TestBuildArgsPromptIsLast(t *testing.T) {
	c := testConfig()
	c.Prompt = "--not-a-flag"

	got := BuildArgs(c)

	if got[len(got)-1] != "--not-a-flag" {
		t.Fatalf("last arg = %q, want the prompt", got[len(got)-1])
	}
}

// Negative assertions. These flags must NEVER appear. --bare breaks OAuth auth
// on this machine, and --dangerously-skip-permissions is unnecessary because
// --tools "" already gives a zero tool surface.
func TestBuildArgsNeverContainsDangerousFlags(t *testing.T) {
	banned := []string{
		"--dangerously-skip-permissions",
		"--bare",
		"--allowedTools",
		"--add-dir",
	}

	got := BuildArgs(testConfig())

	for _, b := range banned {
		if slices.Contains(got, b) {
			t.Fatalf("BuildArgs contains banned flag %q: %#v", b, got)
		}
	}
}

// --safe-mode is load-bearing, not optional. Without it the CLI inherits the
// user's global CLAUDE.md and skills: measured at 16.5s and $0.0252 instead of
// 3.3s and $0.0044, and it changes the answer.
func TestBuildArgsAlwaysSafeMode(t *testing.T) {
	got := BuildArgs(testConfig())

	if !slices.Contains(got, "--safe-mode") {
		t.Fatalf("BuildArgs is missing --safe-mode: %#v", got)
	}
}

func TestBuildArgsFormatsBudgetToTwoDecimals(t *testing.T) {
	c := testConfig()
	c.BudgetUSD = 0.5

	got := BuildArgs(c)

	i := slices.Index(got, "--max-budget-usd")
	if i < 0 || i+1 >= len(got) {
		t.Fatalf("no --max-budget-usd value: %#v", got)
	}
	if got[i+1] != "0.50" {
		t.Fatalf("budget = %q, want %q", got[i+1], "0.50")
	}
}

func TestBuildArgsUsesConfiguredModel(t *testing.T) {
	c := testConfig()
	c.Model = "sonnet"

	got := BuildArgs(c)

	i := slices.Index(got, "--model")
	if i < 0 || got[i+1] != "sonnet" {
		t.Fatalf("model not honoured: %#v", got)
	}
}

func TestConfigDefaults(t *testing.T) {
	if DefaultModel != "haiku" {
		t.Fatalf("DefaultModel = %q, want haiku", DefaultModel)
	}
	if DefaultPrompt != "Hello world" {
		t.Fatalf("DefaultPrompt = %q, want Hello world", DefaultPrompt)
	}
	if DefaultTimeout != 120*time.Second {
		t.Fatalf("DefaultTimeout = %v, want 2m", DefaultTimeout)
	}
	if !strings.Contains(DefaultsSummary(), "haiku") {
		t.Fatalf("DefaultsSummary should mention the model: %q", DefaultsSummary())
	}
}
