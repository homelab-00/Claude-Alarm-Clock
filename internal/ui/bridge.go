package ui

import (
	"context"

	"fyne.io/fyne/v2"

	"claudealarm/internal/app"
)

// Bridge pumps the Core's events onto the Fyne goroutine.
//
// This is the ONLY place in the codebase that calls fyne.Do, and it is the only
// crossing point between the Core's goroutine and any widget. Since Fyne v2.6
// all callbacks run on a single goroutine, and touching a widget from another
// one is a data race. v2.8 has a temporary safety net that logs
//
//	*** Error in Fyne call thread, this should have been called in fyne.Do[AndWait]
//
// and silently repairs the call. v2.9 removes it. Any occurrence of that string
// in the logs is a real latent race.
//
// We use fyne.Do (queue and return), never fyne.DoAndWait (queue and block):
// blocking here would stall the Core, and calling DoAndWait from the main
// goroutine is a genuine deadlock.
func Bridge(ctx context.Context, core *app.Core, win *Window) {
	for {
		select {
		case <-ctx.Done():
			return

		case e, ok := <-core.Events():
			if !ok {
				return
			}
			// e is a fresh binding on every execution of this case (each
			// select iteration declares it anew), so the closure below
			// captures this iteration's value safely without a manual
			// shadow copy.
			fyne.Do(func() { win.Apply(e) })
		}
	}
}
