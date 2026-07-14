package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func script(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func cfg(t *testing.T, name string) Config {
	t.Helper()
	return Config{
		Bin:       script(t, name),
		WorkDir:   t.TempDir(),
		Model:     DefaultModel,
		Prompt:    DefaultPrompt,
		BudgetUSD: DefaultBudgetUSD,
		Timeout:   5 * time.Second,
	}
}

func TestCLIRunSuccess(t *testing.T) {
	got, err := NewCLI().Run(context.Background(), cfg(t, "ok.sh"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if want := "Hello! How can I help you today?"; got.Text != want {
		t.Fatalf("Text = %q, want %q", got.Text, want)
	}
	if got.CostUSD != 0.0044 {
		t.Fatalf("CostUSD = %v, want 0.0044", got.CostUSD)
	}
	if got.SessionID != "1f0c8f2a-3d4e-4b5a-9c6d-7e8f9a0b1c2d" {
		t.Fatalf("SessionID = %q", got.SessionID)
	}
	if got.Duration <= 0 {
		t.Fatalf("Duration = %v, want > 0", got.Duration)
	}
	if !strings.Contains(got.Raw, "total_cost_usd") {
		t.Fatalf("Raw does not look like the envelope: %q", got.Raw)
	}
}

// THE trap test. The bad-model envelope reports "subtype":"success" alongside
// "is_error":true. Any implementation that branches on subtype passes a 404
// through as a successful answer. This test fails if we ever do that.
func TestCLIRunBadModelIsAnErrorDespiteSubtypeSayingSuccess(t *testing.T) {
	_, err := NewCLI().Run(context.Background(), cfg(t, "bad_model.sh"))
	if err == nil {
		t.Fatal("Run() error = nil; the envelope had is_error:true and must not be reported as success")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should surface the API status: %v", err)
	}
}

// Node processes (which is what the real `claude` binary is) routinely log
// diagnostics to stderr. When the CLI reports is_error:true, that stderr
// output is exactly what the user needs to see -- the alarm just failed
// unattended. Both is_error error messages (with and without
// api_error_status) must surface it, along with the exit code.
//
// claude.go has two separate is_error branches -- one for
// env.APIErrorStatus != nil, one for == nil -- each formatting its own
// fmt.Errorf call with stderrSuffix/exitDesc. Exercising only the
// api_error_status fixture, as this test previously did despite its doc
// comment's claim, left the other branch free to drop stderr and the exit
// code with the whole suite still green. Both fixtures here carry
// is_error:true, stderr, and a non-zero exit; they differ only in whether
// api_error_status is present, which is what selects the branch.
func TestCLIRunIsErrorSurfacesStderrAndExitCode(t *testing.T) {
	t.Run("with api_error_status", func(t *testing.T) {
		_, err := NewCLI().Run(context.Background(), cfg(t, "bad_model_stderr.sh"))
		if err == nil {
			t.Fatal("Run() error = nil; is_error:true must not be reported as success")
		}
		if !strings.Contains(err.Error(), "404") {
			t.Fatalf("error should surface the API status: %v", err)
		}
		if !strings.Contains(err.Error(), "node:internal warning") {
			t.Fatalf("error should surface stderr: %v", err)
		}
		if !strings.Contains(err.Error(), "exit 1") {
			t.Fatalf("error should surface the exit code: %v", err)
		}
	})

	t.Run("without api_error_status", func(t *testing.T) {
		_, err := NewCLI().Run(context.Background(), cfg(t, "bad_result_stderr.sh"))
		if err == nil {
			t.Fatal("Run() error = nil; is_error:true must not be reported as success")
		}
		if !strings.Contains(err.Error(), "node:internal warning") {
			t.Fatalf("error should surface stderr: %v", err)
		}
		if !strings.Contains(err.Error(), "exit 1") {
			t.Fatalf("error should surface the exit code: %v", err)
		}
	})
}

// A network failure makes the real CLI hang forever, with no output and no
// exit. The context deadline is the only thing that saves us -- and the exit
// code is -1 on a signal kill, so it cannot be used to detect this.
func TestCLIRunTimesOut(t *testing.T) {
	c := cfg(t, "hang.sh")
	c.Timeout = 300 * time.Millisecond

	start := time.Now()
	_, err := NewCLI().Run(context.Background(), c)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Run() error = nil, want a timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error should say it timed out: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("took %v to time out; the deadline is not being enforced", elapsed)
	}
}

// On a real timeout the real `claude` CLI -- a Node process that forks
// helpers -- hangs forever retrying a failed network request. If the
// timeout path only killed the direct child (the shape exec.CommandContext
// gives you for free), that retry loop would be orphaned and keep running
// in the background: one leaked process per failed alarm. This proves the
// whole process group is killed, not just the immediate child.
//
// orphan.sh backgrounds a long sleep (the stand-in for the orphan-prone
// grandchild), records its pid in cmd.Dir, then itself sleeps. After Run()
// times out, the recorded pid must no longer be alive.
func TestCLIRunKillsOrphanedGrandchildOnTimeout(t *testing.T) {
	c := cfg(t, "orphan.sh")
	c.Timeout = 300 * time.Millisecond

	_, err := NewCLI().Run(context.Background(), c)
	if err == nil {
		t.Fatal("Run() error = nil, want a timeout")
	}

	pidFile := filepath.Join(c.WorkDir, "orphan.pid")
	pidBytes, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("orphan.sh did not write its grandchild's pid: %v", readErr)
	}
	pidStr := strings.TrimSpace(string(pidBytes))
	pid, convErr := strconv.Atoi(pidStr)
	if convErr != nil {
		t.Fatalf("orphan.pid contained %q, not a pid: %v", pidStr, convErr)
	}

	// Poll instead of a fixed sleep: this must be deterministic, not a race
	// against an arbitrary delay.
	deadline := time.Now().Add(1 * time.Second)
	for {
		killErr := syscall.Kill(pid, 0)
		if errors.Is(killErr, syscall.ESRCH) {
			return // gone -- the grandchild was reaped along with its parent
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild pid %d is still alive 1s after Run() returned; the timeout orphaned it", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Cancelling the caller's context must also kill the child.
func TestCLIRunHonoursCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := NewCLI().Run(ctx, cfg(t, "hang.sh"))

	if err == nil {
		t.Fatal("Run() error = nil, want cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want it to wrap context.Canceled", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("cancellation was not honoured")
	}
}

func TestCLIRunUnparseableOutputSurfacesStderrAndExitCode(t *testing.T) {
	_, err := NewCLI().Run(context.Background(), cfg(t, "garbage.sh"))
	if err == nil {
		t.Fatal("Run() error = nil, want a parse failure")
	}
	if !strings.Contains(err.Error(), "something went badly wrong") {
		t.Fatalf("error should surface stderr: %v", err)
	}
	if !strings.Contains(err.Error(), "exit 2") {
		t.Fatalf("error should surface the exit code: %v", err)
	}
}

// cmd.Dir is the only way to set the working directory: there is no --cwd flag.
func TestCLIRunSetsWorkingDirectory(t *testing.T) {
	c := cfg(t, "cwd.sh")
	dir := t.TempDir()
	c.WorkDir = dir

	got, err := NewCLI().Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}

	// macOS symlinks /var -> /private/var; resolve before comparing.
	wantDir, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(strings.TrimSpace(got.Text))
	if gotDir != wantDir {
		t.Fatalf("claude ran in %q, want %q", gotDir, wantDir)
	}
}

// BuildArgs is unit-tested in args_test.go, but that proves nothing about
// whether those args actually reach exec. echoargs.sh returns its own argv as
// the answer text, so this asserts the whole wiring end to end.
func TestCLIRunActuallyPassesTheBuiltArgsToTheProcess(t *testing.T) {
	got, err := NewCLI().Run(context.Background(), cfg(t, "echoargs.sh"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	for _, want := range []string{"-p", "--model haiku", "--output-format json", "--safe-mode", "Hello world"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("the process did not receive %q; it got: %s", want, got.Text)
		}
	}
	for _, banned := range []string{"--dangerously-skip-permissions", "--bare"} {
		if strings.Contains(got.Text, banned) {
			t.Fatalf("the process received the banned flag %q: %s", banned, got.Text)
		}
	}
}

func TestLookupFindsClaudeOrSaysWhyNot(t *testing.T) {
	path, err := Lookup()
	if err != nil {
		if !strings.Contains(err.Error(), "claude") {
			t.Fatalf("error should name the binary: %v", err)
		}
		t.Skip("claude not installed on this machine; the error path is what we assert")
	}
	if path == "" {
		t.Fatal("Lookup returned an empty path and no error")
	}
}

func TestFakeRecordsCalls(t *testing.T) {
	f := &Fake{Result: Result{Text: "faked"}}

	c := Config{WorkDir: "/tmp", Model: "haiku", Prompt: "Hello world"}
	got, err := f.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}

	if got.Text != "faked" {
		t.Fatalf("Text = %q, want faked", got.Text)
	}
	if f.CallCount() != 1 {
		t.Fatalf("CallCount = %d, want 1", f.CallCount())
	}
	last, ok := f.LastCall()
	if !ok || last.Prompt != "Hello world" {
		t.Fatalf("LastCall = %+v", last)
	}
}

func TestFakeReturnsConfiguredError(t *testing.T) {
	want := errors.New("boom")
	f := &Fake{Err: want}

	_, err := f.Run(context.Background(), Config{})

	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if f.CallCount() != 1 {
		t.Fatalf("a failing call must still be recorded")
	}
}
