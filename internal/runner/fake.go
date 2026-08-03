package runner

import (
	"context"
	"sync"
	"time"
)

// Fake is a Runner that records its calls and returns canned results. It is
// what lets the whole arm -> fire -> run -> display path be tested with no
// network, no API spend, and no claude binary.
type Fake struct {
	// Result is returned on success.
	Result Result
	// Err, if set, is returned instead.
	Err error
	// Delay, if set, blocks for this long -- use it to test the running state.
	Delay time.Duration

	mu    sync.Mutex
	calls []Config
}

// Run records the call and returns the canned Result or Err.
func (f *Fake) Run(ctx context.Context, c Config) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()

	if f.Delay > 0 {
		select {
		case <-time.After(f.Delay):
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}

	if f.Err != nil {
		return Result{}, f.Err
	}
	return f.Result, nil
}

// CallCount reports how many times Run was called.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// LastCall returns the most recent Config Run was given.
func (f *Fake) LastCall() (Config, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return Config{}, false
	}
	return f.calls[len(f.calls)-1], true
}
