// Package runner invokes the Claude Code CLI. It must never import Fyne.
package runner

import (
	"context"
	"fmt"
	"time"
)

// Defaults. The model and prompt are what the spec asks for; the budget is a
// stop-loss with ~20x headroom over a measured run (~$0.0044).
const (
	DefaultModel     = "haiku"
	DefaultPrompt    = "Hello world"
	DefaultBudgetUSD = 0.10
	DefaultTimeout   = 120 * time.Second
)

// Config is one invocation of the CLI.
type Config struct {
	// Bin is the resolved path to the claude executable. Resolve it with
	// exec.LookPath at arm time, while the user is watching -- not at fire
	// time, when nobody is.
	Bin string

	// WorkDir is the directory claude runs in. There is no --cwd flag: the
	// process working directory is the only mechanism. (--add-dir is a tool
	// allowlist, not a working directory.)
	WorkDir string

	Model     string
	Prompt    string
	BudgetUSD float64
	Timeout   time.Duration
}

// Result is a successful invocation.
type Result struct {
	Text      string        // the envelope's .result -- the model's answer
	CostUSD   float64       // .total_cost_usd
	SessionID string        // .session_id
	Duration  time.Duration // measured by us, wall clock
	Raw       string        // full stdout, for the details pane
}

// Runner is the seam that lets the whole fire path be tested without spending
// money, touching the network, or having claude installed.
type Runner interface {
	Run(ctx context.Context, c Config) (Result, error)
}

// DefaultsSummary is a one-line human-readable description of the defaults,
// for the UI and the README.
func DefaultsSummary() string {
	return fmt.Sprintf("%s · %q · budget $%.2f · timeout %s",
		DefaultModel, DefaultPrompt, DefaultBudgetUSD, DefaultTimeout)
}
