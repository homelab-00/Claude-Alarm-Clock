package runner

import "strconv"

// BuildArgs assembles the CLI arguments. Pure, so it can be golden-tested.
//
// Every flag here is load-bearing:
//
//	-p                        headless print mode: send one prompt, print the
//	                          answer, exit. Also skips the workspace-trust dialog.
//	--output-format json      gives us .result, .is_error, .total_cost_usd.
//	--safe-mode               do NOT inherit the user's global CLAUDE.md, skills,
//	                          or plugins. Measured: without it, 16.5s/$0.0252 and
//	                          a polluted answer; with it, 3.3s/$0.0044 and a clean
//	                          one.
//	--tools ""                zero tool surface. This is what makes permissions
//	                          moot, so we never need --dangerously-skip-permissions.
//	--permission-mode dontAsk belt and braces: in -p mode a tool call needing
//	                          approval is silently denied, never prompted, so
//	                          there is no interactive hang to work around.
//	--max-budget-usd          stop-loss.
//	--no-session-persistence  this is a fire-and-forget job; do not litter the
//	                          user's session history.
//
// The prompt is always last, so it is never mistaken for a flag.
//
// Deliberately absent: --bare (refuses OAuth/keychain auth, which is what this
// machine uses, and hard-fails "Not logged in") and
// --dangerously-skip-permissions (unnecessary, see --tools above).
func BuildArgs(c Config) []string {
	return []string{
		"-p",
		"--model", c.Model,
		"--output-format", "json",
		"--safe-mode",
		"--tools", "",
		"--permission-mode", "dontAsk",
		"--max-budget-usd", strconv.FormatFloat(c.BudgetUSD, 'f', 2, 64),
		"--no-session-persistence",
		c.Prompt,
	}
}
