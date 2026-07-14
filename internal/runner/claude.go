package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// envelope is the `--output-format json` result object.
//
// There is deliberately no Subtype field. The CLI reports "subtype":"success"
// on a 404 bad-model error, alongside "is_error":true. Not having the field
// makes it impossible to branch on it by accident.
type envelope struct {
	IsError        bool    `json:"is_error"`
	Result         string  `json:"result"`
	APIErrorStatus *int    `json:"api_error_status"`
	SessionID      string  `json:"session_id"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
}

// CLI runs the real claude binary.
type CLI struct{}

// NewCLI returns a Runner backed by the real CLI.
func NewCLI() *CLI { return &CLI{} }

// Lookup resolves the claude binary on PATH.
//
// Call this at arm time, while the user is looking at the app -- not at fire
// time, when nobody is watching and there is nowhere useful to put the error.
func Lookup() (string, error) {
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude not found on PATH: %w", err)
	}
	return path, nil
}

// Run invokes the CLI and waits for the response.
func (CLI) Run(ctx context.Context, c Config) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.Bin, BuildArgs(c)...)

	// The ONLY way to set claude's working directory. There is no --cwd flag.
	cmd.Dir = c.WorkDir

	// nil means Go connects /dev/null. An open-but-empty stdin pipe makes the
	// CLI wait 3 seconds for input that never arrives.
	cmd.Stdin = nil

	// Killing the direct child on cancellation is not enough: if that child is
	// itself a wrapper (a shell script, say) that forked a grandchild before
	// dying, the grandchild can be orphaned still holding the stdout/stderr
	// pipes open. Without a bound, cmd.Wait would then block until that
	// orphan exits on its own -- observed empirically via the hang.sh
	// fixture, where killing the wrapping `sh` left `sleep 300` running
	// and cmd.Run() blocked for the full 5 minutes instead of returning at
	// the deadline. WaitDelay bounds that: once it elapses after the
	// context is done, Go force-closes the pipes so Wait returns promptly
	// regardless of what any orphaned descendant is doing.
	cmd.WaitDelay = 1 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	// A timeout is NOT visible in the exit code -- a signal-killed process
	// reports -1. ctx.Err() is the only reliable signal.
	//
	// This path is not theoretical: on network failure the real CLI hangs
	// forever, printing nothing and never exiting.
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return Result{}, fmt.Errorf("claude timed out after %s: %w", c.Timeout, err)
		}
		return Result{}, fmt.Errorf("claude cancelled: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		return Result{}, fmt.Errorf(
			"claude produced unparseable output (exit: %v, stderr: %q): %w",
			exitDesc(runErr), stderr.String(), err)
	}

	// Branch on is_error, never on subtype.
	if env.IsError {
		if env.APIErrorStatus != nil {
			return Result{}, fmt.Errorf("claude failed (HTTP %d): %s", *env.APIErrorStatus, env.Result)
		}
		return Result{}, fmt.Errorf("claude failed: %s", env.Result)
	}

	if runErr != nil {
		return Result{}, fmt.Errorf("claude exited badly (%v, stderr: %q) but reported no error in its output",
			exitDesc(runErr), stderr.String())
	}

	return Result{
		Text:      env.Result,
		CostUSD:   env.TotalCostUSD,
		SessionID: env.SessionID,
		Duration:  elapsed,
		Raw:       stdout.String(),
	}, nil
}

func exitDesc(err error) string {
	if err == nil {
		return "0"
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("exit %d", ee.ExitCode())
	}
	return err.Error()
}
