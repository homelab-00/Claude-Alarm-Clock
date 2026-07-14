package app

import (
	"testing"

	"claudealarm/internal/config"
	"claudealarm/internal/runner"
)

// config.DefaultModel/DefaultPrompt and runner.DefaultModel/DefaultPrompt are
// deliberately duplicated: config must not import runner (it would pull the
// runner's dependencies into the persistence layer just to read two
// constants), and runner must not import config either. Neither set of
// constants should be deleted in favor of the other.
//
// That duplication has no compiler-enforced link between the two copies, so
// nothing stops them silently diverging -- e.g. if runner.DefaultModel were
// bumped to a newer model on a deprecation, config.DefaultState would keep
// pre-filling the old one with no error and no test failure, and the app
// would offer one model in the UI while running another.
//
// internal/app already imports both packages, so it is the natural place to
// assert the two copies stay equal. If this test fails, update whichever
// constant lagged behind -- do not delete either copy.
func TestDefaultConstantsMatchBetweenConfigAndRunner(t *testing.T) {
	if config.DefaultModel != runner.DefaultModel {
		t.Fatalf("config.DefaultModel = %q, runner.DefaultModel = %q: the two copies have diverged", config.DefaultModel, runner.DefaultModel)
	}
	if config.DefaultPrompt != runner.DefaultPrompt {
		t.Fatalf("config.DefaultPrompt = %q, runner.DefaultPrompt = %q: the two copies have diverged", config.DefaultPrompt, runner.DefaultPrompt)
	}
}
