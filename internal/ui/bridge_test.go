package ui

import (
	"context"
	"sync"
	"testing"
	"time"

	"claudealarm/internal/app"
)

// fakeDispatcher stands in for fyne.Do. It records how many times it was
// invoked and, critically, whether a call is in flight right now -- that flag
// is how recordingApply below proves an event never reaches the widget layer
// except from inside a dispatch, which is the property that keeps every
// widget write on the Fyne goroutine.
//
// Crucially, do must NOT run fn synchronously. The real fyne.Do queues fn
// and returns immediately; a synchronous fake runs the closure before pump's
// select loop can advance to its next iteration, which means a shared
// loop-variable capture bug (var e app.Event declared outside the loop,
// case e, ok = <-events instead of :=) is invisible: the closure reads e
// before anything has a chance to overwrite it. Queuing the closures and
// running them later, once every event has already been read off the
// channel, is what makes that bug observable: a shared e would have been
// overwritten to its final value by the time flush runs, so every queued
// closure would report the same (wrong) event.
type fakeDispatcher struct {
	mu          sync.Mutex
	calls       int
	dispatching bool
	queue       []func()
}

// do queues fn for later execution instead of running it inline, mirroring
// fyne.Do's queue-and-return semantics.
func (d *fakeDispatcher) do(fn func()) {
	d.mu.Lock()
	d.calls++
	d.queue = append(d.queue, fn)
	d.mu.Unlock()
}

// flush runs every closure do() has queued so far, in the order they were
// queued, marking dispatching true for the duration of each -- so
// recordingApply's offCross check still catches an apply call that happens
// outside of a do closure even though the run is deferred.
func (d *fakeDispatcher) flush() {
	d.mu.Lock()
	queue := d.queue
	d.queue = nil
	d.mu.Unlock()

	for _, fn := range queue {
		d.mu.Lock()
		d.dispatching = true
		d.mu.Unlock()

		fn()

		d.mu.Lock()
		d.dispatching = false
		d.mu.Unlock()
	}
}

func (d *fakeDispatcher) isDispatching() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dispatching
}

// recordingApply stands in for Window.Apply. It records every event it is
// handed, in order, and flags offCross if it was ever called while the
// dispatcher was NOT the caller -- i.e. an event that reached "the widget
// layer" without crossing onto the Fyne goroutine, which in the real app is a
// live data race.
type recordingApply struct {
	disp *fakeDispatcher

	mu       sync.Mutex
	events   []app.Event
	offCross bool
}

func (r *recordingApply) apply(e app.Event) {
	crossed := r.disp.isDispatching()

	r.mu.Lock()
	defer r.mu.Unlock()
	if !crossed {
		r.offCross = true
	}
	r.events = append(r.events, e)
}

func (r *recordingApply) snapshot() (events []app.Event, offCross bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]app.Event(nil), r.events...), r.offCross
}

// TestPumpDeliversEveryEventInOrder is property 1: every event sent on the
// channel must reach apply, in the order it was sent.
func TestPumpDeliversEveryEventInOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	want := []app.Event{
		{Status: app.StatusArmed},
		{Status: app.StatusRunning},
		{Status: app.StatusDone},
		{Status: app.StatusMissed},
	}

	events := make(chan app.Event, len(want))
	for _, e := range want {
		events <- e
	}
	close(events)

	disp := &fakeDispatcher{}
	rec := &recordingApply{disp: disp}

	pump(ctx, events, rec.apply, disp.do)
	// pump has now read every event off the channel and queued one closure
	// per event without running any of them -- exactly the moment a shared
	// loop variable would already hold its final value. Only after that do
	// we run the queued closures.
	disp.flush()

	got, offCross := rec.snapshot()
	if offCross {
		t.Fatal("apply ran outside disp.do: an event reached the widget layer without crossing onto the Fyne goroutine")
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Status != want[i].Status {
			t.Fatalf("event %d: got status %v, want %v (events must arrive in send order)", i, got[i].Status, want[i].Status)
		}
	}
}

// TestPumpAppliesEveryEventThroughTheDispatcher is property 2: apply must
// never be called except from inside a do closure. Without that boundary,
// Core's goroutine touches widgets directly -- the exact data race Bridge
// exists to prevent. dispatcher.calls tracking one dispatch per event (rather
// than, say, pump batching events into a single do call, or calling apply
// directly and do never firing) is what makes this assertion meaningful.
func TestPumpAppliesEveryEventThroughTheDispatcher(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan app.Event, 2)
	events <- app.Event{Status: app.StatusIdle}
	events <- app.Event{Status: app.StatusError}
	close(events)

	disp := &fakeDispatcher{}
	rec := &recordingApply{disp: disp}

	pump(ctx, events, rec.apply, disp.do)
	disp.flush()

	got, offCross := rec.snapshot()
	if offCross {
		t.Fatal("apply ran outside disp.do at least once: this is the property that keeps the app race-free, and it was violated")
	}
	if disp.calls != len(got) {
		t.Fatalf("dispatcher.calls = %d, want %d (exactly one crossing per applied event)", disp.calls, len(got))
	}
}

// TestPumpReturnsPromptlyWhenContextIsCancelled is property 3: cancelling ctx
// must make pump return, even with no events ever sent and nothing to drain.
func TestPumpReturnsPromptlyWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan app.Event) // never sent to, never closed

	disp := &fakeDispatcher{}
	rec := &recordingApply{disp: disp}

	done := make(chan struct{})
	go func() {
		pump(ctx, events, rec.apply, disp.do)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not return promptly after its context was cancelled")
	}
}

// TestPumpReturnsPromptlyWhenEventsChannelCloses is property 4: closing the
// events channel must make pump return -- no spin, no panic on the zero value
// read from a closed channel.
func TestPumpReturnsPromptlyWhenEventsChannelCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan app.Event)

	disp := &fakeDispatcher{}
	rec := &recordingApply{disp: disp}

	done := make(chan struct{})
	go func() {
		pump(ctx, events, rec.apply, disp.do)
		close(done)
	}()

	close(events)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not return promptly after the events channel closed")
	}
}
