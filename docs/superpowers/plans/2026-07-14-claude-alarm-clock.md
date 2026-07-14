# Claude Alarm Clock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A tray-resident Go/Fyne desktop app where the user enters a target time; at `target − offset` it runs Claude Code headlessly with the Haiku model, sends "Hello world", and shows the response.

**Architecture:** Three Fyne-free packages (`schedule`, `runner`, `config`) behind three interfaces (`Clock`, `Runner`, `Store`), orchestrated by `app.Core`, which emits events on a channel. `internal/ui` is the only package that imports Fyne widgets and the only caller of `fyne.Do`. A single always-running 1-second loop drives both the clock display and the alarm check.

**Tech Stack:** Go 1.26, Fyne v2.8.0, `claude` CLI 2.1.207. Linux/KDE Plasma. `CGO_ENABLED=1`.

**Spec:** `docs/superpowers/specs/2026-07-14-claude-alarm-clock-design.md`. Read it before starting.

## Global Constraints

Every task's requirements implicitly include this section.

- **Module name:** `claudealarm`. Imports are `claudealarm/internal/...`.
- **Go 1.26+, Fyne v2.8.0, `CGO_ENABLED=1`.** Fyne needs cgo; `CGO_ENABLED=0` will not build.
- **`internal/schedule`, `internal/runner`, and `internal/config/store.go` MUST NOT import Fyne.** Only `internal/config/prefs.go`, `internal/ui`, and `cmd/` may. This boundary is what makes the project testable headlessly. A task that breaks it is rejected.
- **Never use `time.After`, `time.Timer`, `time.AfterFunc`, or `fyne.App.ScheduleNotification` for the alarm.** They are monotonic and lose suspend time. Wall-clock polling only.
- **Always `.Round(0)` both operands before comparing two `time.Time` values.** Without it, `Before()` uses monotonic readings and silently reintroduces the suspend bug.
- **Never call `w.Close()` in app code.** It bypasses `SetCloseIntercept` and kills the process. Use `w.Hide()`.
- **All widget/tray mutation from a non-Fyne goroutine MUST be wrapped in `fyne.Do`.** Never call `fyne.Do`/`fyne.DoAndWait` from the main goroutine or from inside a Fyne callback.
- **Never branch on the Claude envelope's `subtype`** — it reports `"success"` on a 404. Branch on `is_error`.
- **Never pass `--dangerously-skip-permissions` or `--bare`** to `claude`. `--bare` breaks OAuth auth on this machine.
- **Run every test with `-race`.** Grep test output for `*** Error in Fyne call thread` — any occurrence is a real data race and must be fixed, not ignored.
- **Commit after every task.** Conventional commits (`feat:`, `test:`, `docs:`, `chore:`). No attribution trailer.

## File Structure

| File | Responsibility |
|---|---|
| `cmd/alarmclock/main.go` | Wiring only: flags, `app.NewWithID`, preflight, tray, run. |
| `internal/schedule/clock.go` | `Clock` + `Ticker` interfaces; real and test implementations. |
| `internal/schedule/spec.go` | `Spec`; `NextTarget`; `FireAt`. All calendar/DST math. |
| `internal/schedule/policy.go` | `Decide` and `DetectJump` — pure functions. No goroutines, no clock. |
| `internal/schedule/alarm.go` | The single always-running tick loop. Owns armed state. |
| `internal/runner/runner.go` | `Runner` interface, `Config`, `Result`. |
| `internal/runner/args.go` | `BuildArgs(Config) []string` — pure. |
| `internal/runner/claude.go` | `exec.CommandContext`, envelope parse, timeout mapping. |
| `internal/runner/fake.go` | `Fake` runner for tests. |
| `internal/config/store.go` | `State`, `Store` interface, `MemStore`. Fyne-free. |
| `internal/config/prefs.go` | `fyne.Preferences` implementation of `Store`. |
| `internal/app/core.go` | Orchestration. Consumes the three interfaces, emits `Event`s. |
| `internal/ui/fonts.go` | `go:embed` the TTF → `fyne.StaticResource`. |
| `internal/ui/theme.go` | Custom theme: `Color`/`Font`/`Icon`/`Size`. |
| `internal/ui/card.go` | Theme-aware card widget. |
| `internal/ui/window.go` | Layout: clock, form, arm button, status, result pane. |
| `internal/ui/tray.go` | `desktop.App` assertion, menu, icon. |
| `internal/ui/bridge.go` | Consumes `Core.Events()`; the only caller of `fyne.Do`. |
| `tools/genicon/main.go` | Generates `assets/icon.png`. Run once. |
| `assets/icon.png` | 64×64 full-colour PNG tray icon. |
| `assets/fonts/JetBrainsMono-Bold.ttf` | Clock face font (SIL OFL 1.1). |

---

### Task 1: Spike — project skeleton and tray lifecycle verification

Resolves the open risk in spec §12 (*"Desktop app with system tray hangs on `app.Quit`"*) **before** any app logic exists. If quit-from-tray hangs, we need to know now, not after twelve tasks.

**Files:**
- Create: `go.mod`, `tools/genicon/main.go`, `assets/icon.png`, `cmd/alarmclock/main.go` (temporary spike version — replaced in Task 13)

**Interfaces:**
- Consumes: nothing.
- Produces: `assets/icon.png` (64×64 NRGBA PNG), a working `go.mod` with Fyne v2.8.0.

- [ ] **Step 1: Initialise the module and add Fyne**

```bash
cd /home/Bill/Code_Projects/GO_Projects/Claude-Alarm-Clock
go mod init claudealarm
go get fyne.io/fyne/v2@v2.8.0
```

Expected: `go.mod` created, `fyne.io/fyne/v2 v2.8.0` in `require`.

If `go get` fails to build cgo deps, the missing Arch packages are:
`sudo pacman -S --needed libxcursor libxrandr libxinerama libxi libgl mesa`

- [ ] **Step 2: Write the icon generator**

Create `tools/genicon/main.go`. Alpha is averaged during downsampling but RGB is held constant, which avoids the dark fringing you get from averaging non-premultiplied transparent black.

```go
// Command genicon renders assets/icon.png, the 64x64 system tray icon.
// Run from the repo root: go run ./tools/genicon
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

const (
	size = 64
	ss   = 8 // supersample factor, for antialiasing
)

var accent = color.NRGBA{R: 0x4C, G: 0x8D, B: 0xFF, A: 0xFF}

// onHand reports whether the point (dx,dy), relative to the clock centre, lies
// on a hand pointing at the given angle with the given length and half-width.
func onHand(dx, dy, angle, length, halfW float64) bool {
	ux, uy := math.Cos(angle), math.Sin(angle)
	along := dx*ux + dy*uy               // distance along the hand's axis
	across := math.Abs(-dx*uy + dy*ux)   // perpendicular distance from it
	return along >= 0 && along <= length && across <= halfW
}

func main() {
	const big = size * ss
	centre := float64(big) / 2
	rOuter, rInner := float64(big)*0.46, float64(big)*0.36

	// coverage[y][x] counts how many supersamples landed on the glyph.
	coverage := make([]int, size*size)
	for y := 0; y < big; y++ {
		for x := 0; x < big; x++ {
			dx, dy := float64(x)+0.5-centre, float64(y)+0.5-centre
			d := math.Hypot(dx, dy)
			on := d <= rOuter && d >= rInner // the bezel ring
			if !on {
				on = onHand(dx, dy, -math.Pi/2, rInner*0.62, float64(ss)*2.2) || // hour hand, up
					onHand(dx, dy, 0, rInner*0.86, float64(ss)*1.6) // minute hand, right
			}
			if on {
				coverage[(y/ss)*size+(x/ss)]++
			}
		}
	}

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for i, n := range coverage {
		c := accent
		c.A = uint8(n * 255 / (ss * ss))
		img.SetNRGBA(i%size, i/size, c)
	}

	if err := os.MkdirAll("assets", 0o755); err != nil {
		panic(err)
	}
	f, err := os.Create("assets/icon.png")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}
```

- [ ] **Step 3: Generate the icon and eyeball it**

```bash
go run ./tools/genicon
file assets/icon.png
```

Expected: `assets/icon.png: PNG image data, 64 x 64, 8-bit/color RGBA, non-interlaced`

Open it. It should be a blue clock ring with two hands, on transparency.

- [ ] **Step 4: Write the spike main**

This is throwaway — Task 13 replaces it. Its only job is to answer three questions about Fyne's real behaviour on Plasma.

Create `cmd/alarmclock/main.go`:

```go
package main

import (
	"fmt"
	"os"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

func main() {
	a := app.NewWithID("gr.polaris.claudealarm")
	w := a.NewWindow("Claude Alarm Clock — spike")
	w.SetContent(widget.NewLabel("Close this window. The process must stay alive.\nThen quit from the tray icon."))
	w.Resize(fyne.NewSize(420, 120))

	iconBytes, err := os.ReadFile("assets/icon.png")
	if err != nil {
		panic(err)
	}
	icon := fyne.NewStaticResource("icon.png", iconBytes)

	if desk, ok := a.(desktop.App); ok {
		quit := fyne.NewMenuItem("Quit", func() {
			fmt.Println("TEARDOWN RAN")
			a.Quit()
		})
		quit.IsQuit = true // else Fyne appends its own Quit, skipping our teardown
		desk.SetSystemTrayMenu(fyne.NewMenu("Claude Alarm",
			fyne.NewMenuItem("Show", func() { w.Show(); w.RequestFocus() }),
			fyne.NewMenuItemSeparator(),
			quit,
		))
		desk.SetSystemTrayIcon(icon)
		desk.SetSystemTrayWindow(w)
	} else {
		fmt.Println("WARNING: not a desktop.App — no tray")
	}

	// The single line keeping the process alive when the user clicks X.
	w.SetCloseIntercept(func() {
		fmt.Println("close intercepted -> hiding")
		w.Hide()
	})

	w.ShowAndRun()
	fmt.Println("ShowAndRun returned cleanly")
}
```

- [ ] **Step 5: Run the spike and verify all three behaviours**

```bash
go run ./cmd/alarmclock
```

Verify, in order:

1. **The tray icon appears** in the Plasma system tray. (A missing SNI host fails *silently* — you would just see no icon.)
2. **Click the window's X.** The window disappears; the terminal prints `close intercepted -> hiding`; **the process is still running.** Confirm from another terminal: `pgrep -af alarmclock`.
3. **Click the tray icon** → the window comes back.
4. **Tray → Quit.** The terminal must print `TEARDOWN RAN`, then `ShowAndRun returned cleanly`, and **the process must exit within a second or two.**

**If step 4 hangs**, spec §12 has reproduced. Record it, and change the tray Quit handler to:

```go
quit := fyne.NewMenuItem("Quit", func() {
    fmt.Println("TEARDOWN RAN")
    a.Quit()
    os.Exit(0) // workaround: Fyne hangs on Quit with a tray registered
})
```

Re-verify, and note the workaround in `MANUAL-TESTS.md` in Task 14.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum tools/ assets/ cmd/
git commit -m "feat: project skeleton, tray icon, and verified tray lifecycle

Spike confirming the three Fyne behaviours the whole design rests on:
tray icon renders on Plasma, SetCloseIntercept keeps the process alive
when the window is closed, and quit-from-tray runs teardown and exits.

Resolves the open risk in spec section 12."
```

---

### Task 2: `schedule` — Clock and Ticker abstractions

**Files:**
- Create: `internal/schedule/clock.go`, `internal/schedule/clock_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Clock interface{ Now() time.Time }`
  - `func NewRealClock() Clock`
  - `type TestClock struct{...}`; `func NewTestClock(time.Time) *TestClock`; `(*TestClock).Now() time.Time`; `(*TestClock).Advance(time.Duration)`; `(*TestClock).Set(time.Time)`
  - `type Ticker interface{ C() <-chan time.Time; Stop() }`
  - `type TickerFunc func(time.Duration) Ticker`
  - `func RealTicker(time.Duration) Ticker`
  - `type ManualTicker struct{...}`; `func NewManualTicker() *ManualTicker`; `(*ManualTicker).Tick()`; `(*ManualTicker).C()`; `(*ManualTicker).Stop()`

- [ ] **Step 1: Write the failing test**

Create `internal/schedule/clock_test.go`:

```go
package schedule

import (
	"testing"
	"time"
)

func TestTestClockAdvances(t *testing.T) {
	start := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	c := NewTestClock(start)

	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want %v", got, start)
	}

	c.Advance(90 * time.Minute)

	want := start.Add(90 * time.Minute)
	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("after Advance, Now() = %v, want %v", got, want)
	}
}

// A TestClock must be safe to advance from one goroutine while the alarm loop
// reads it from another; every test in this package relies on that.
func TestTestClockIsRaceSafe(t *testing.T) {
	c := NewTestClock(time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC))
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			c.Advance(time.Second)
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = c.Now()
	}
	<-done
}

func TestManualTickerDeliversTicks(t *testing.T) {
	mt := NewManualTicker()
	defer mt.Stop()

	go mt.Tick()

	select {
	case <-mt.C():
	case <-time.After(time.Second):
		t.Fatal("no tick delivered within 1s")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test -race ./internal/schedule/
```

Expected: FAIL — `undefined: NewTestClock`, `undefined: NewManualTicker`.

- [ ] **Step 3: Write the implementation**

Create `internal/schedule/clock.go`:

```go
// Package schedule owns all alarm timing. It must never import Fyne.
package schedule

import (
	"sync"
	"time"
)

// Clock is the seam that lets tests simulate a 12-hour suspend in microseconds.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// NewRealClock returns a Clock backed by time.Now.
func NewRealClock() Clock { return realClock{} }

// TestClock is a Clock whose time only moves when a test moves it.
type TestClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewTestClock returns a TestClock reading the given instant.
func NewTestClock(t time.Time) *TestClock { return &TestClock{now: t} }

func (c *TestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d. Use it to simulate suspend.
func (c *TestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set jumps the clock to t. Use it to simulate an NTP step or a TZ change.
func (c *TestClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Ticker is the seam that lets tests drive the alarm loop one tick at a time.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// TickerFunc constructs a Ticker with the given period.
type TickerFunc func(time.Duration) Ticker

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

// RealTicker is the production TickerFunc.
//
// Note this is the one legitimate use of a monotonic timer in this package: it
// only paces the *polling*, it never decides when the alarm fires. The fire
// decision always re-reads the wall clock. A late tick makes the alarm late by
// at most one period; it can never make it fire at the wrong time.
func RealTicker(d time.Duration) Ticker { return realTicker{t: time.NewTicker(d)} }

// ManualTicker is a Ticker that only ticks when a test says so.
type ManualTicker struct {
	ch   chan time.Time
	once sync.Once
}

// NewManualTicker returns a ManualTicker with an unbuffered channel.
func NewManualTicker() *ManualTicker {
	return &ManualTicker{ch: make(chan time.Time)}
}

// Tick delivers one tick, blocking until the loop receives it. The value sent
// is deliberately the zero Time: the alarm loop must read the Clock, never the
// tick payload. A test that depends on the payload is testing the wrong thing.
func (m *ManualTicker) Tick() { m.ch <- time.Time{} }

func (m *ManualTicker) C() <-chan time.Time { return m.ch }

func (m *ManualTicker) Stop() { m.once.Do(func() { close(m.ch) }) }
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test -race ./internal/schedule/ -v
```

Expected: PASS — `TestTestClockAdvances`, `TestTestClockIsRaceSafe`, `TestManualTickerDeliversTicks`.

- [ ] **Step 5: Commit**

```bash
git add internal/schedule/
git commit -m "feat(schedule): add Clock and Ticker seams

Both are interfaces so tests can simulate a 12-hour suspend in
microseconds and drive the alarm loop one tick at a time."
```

---

### Task 3: `schedule` — Spec, NextTarget, FireAt

The calendar and DST math. This is where the offset gets applied, and the tests here are what prove `Add(-offset)` on the instant is correct where naive minute arithmetic is not.

**Files:**
- Create: `internal/schedule/spec.go`, `internal/schedule/spec_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `type Spec struct{ Hour, Minute int; Zone string; Offset, Grace time.Duration }`
  - `(Spec).Validate() error`
  - `(Spec).Location() (*time.Location, error)`
  - `(Spec).NextTarget(from time.Time) (time.Time, error)`
  - `(Spec).FireAt(from time.Time) (fire, target time.Time, err error)`
  - `func ParseHHMM(s string) (hour, minute int, err error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/schedule/spec_test.go`. Read the DST cases carefully — they are the point of this task.

```go
package schedule

import (
	"testing"
	"time"
)

func athens(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Athens")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	return loc
}

func TestNextTargetTodayIfStillFuture(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 7, Minute: 30, Zone: "Europe/Athens"}

	from := time.Date(2026, 7, 14, 6, 0, 0, 0, loc)
	got, err := s.NextTarget(from)
	if err != nil {
		t.Fatal(err)
	}

	want := time.Date(2026, 7, 14, 7, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("NextTarget = %v, want %v", got, want)
	}
}

func TestNextTargetRollsToTomorrowIfPassed(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 7, Minute: 30, Zone: "Europe/Athens"}

	from := time.Date(2026, 7, 14, 9, 0, 0, 0, loc)
	got, err := s.NextTarget(from)
	if err != nil {
		t.Fatal(err)
	}

	want := time.Date(2026, 7, 15, 7, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("NextTarget = %v, want %v", got, want)
	}
}

// A target exactly equal to now has passed. Roll it.
func TestNextTargetExactlyNowRollsToTomorrow(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 7, Minute: 30, Zone: "Europe/Athens"}

	from := time.Date(2026, 7, 14, 7, 30, 0, 0, loc)
	got, err := s.NextTarget(from)
	if err != nil {
		t.Fatal(err)
	}

	if got.Day() != 15 {
		t.Fatalf("NextTarget = %v, want 15 July", got)
	}
}

// Month boundaries must normalise, not overflow.
func TestNextTargetCrossesMonthEnd(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 6, Minute: 0, Zone: "Europe/Athens"}

	from := time.Date(2026, 7, 31, 23, 0, 0, 0, loc)
	got, err := s.NextTarget(from)
	if err != nil {
		t.Fatal(err)
	}

	want := time.Date(2026, 8, 1, 6, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("NextTarget = %v, want %v", got, want)
	}
}

func TestFireAtSubtractsOffset(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 7, Minute: 30, Zone: "Europe/Athens", Offset: 20 * time.Minute}

	from := time.Date(2026, 7, 14, 6, 0, 0, 0, loc)
	fire, target, err := s.FireAt(from)
	if err != nil {
		t.Fatal(err)
	}

	if want := time.Date(2026, 7, 14, 7, 30, 0, 0, loc); !target.Equal(want) {
		t.Fatalf("target = %v, want %v", target, want)
	}
	if want := time.Date(2026, 7, 14, 7, 10, 0, 0, loc); !fire.Equal(want) {
		t.Fatalf("fire = %v, want %v", fire, want)
	}
}

// THE test that justifies Add(-offset) on the instant.
//
// On 2026-03-29 Athens jumps 03:00 EET (+02) -> 04:00 EEST (+03). The hour from
// 03:00 to 04:00 does not exist.
//
// For an 04:30 target with a 1-hour lead-in, naive minute arithmetic gives a
// fire time of 03:30 -- an instant that never happens. Subtracting the offset
// from the *instant* instead gives 02:30 EET: two hours earlier on the wall
// clock, but exactly one hour of real elapsed time before the target, which is
// what the user actually asked for.
func TestFireAtOffsetSpansSpringForwardGap(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 4, Minute: 30, Zone: "Europe/Athens", Offset: time.Hour}

	from := time.Date(2026, 3, 29, 0, 0, 0, 0, loc)
	fire, target, err := s.FireAt(from)
	if err != nil {
		t.Fatal(err)
	}

	// The invariant that matters: exactly one hour of real time elapses.
	if d := target.Sub(fire); d != time.Hour {
		t.Fatalf("target.Sub(fire) = %v, want exactly 1h", d)
	}
	// And it lands two hours earlier on the wall clock, because an hour vanished.
	if got := fire.Format("15:04"); got != "02:30" {
		t.Fatalf("fire wall clock = %s, want 02:30", got)
	}
	if got := target.Format("15:04"); got != "04:30" {
		t.Fatalf("target wall clock = %s, want 04:30", got)
	}
}

// A target inside the spring-forward gap does not exist. Go's time.Date slides
// it forward an hour. We adopt that as our policy; this test pins it so it
// cannot change underneath us silently.
func TestNextTargetInsideSpringForwardGapSlidesForward(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 3, Minute: 30, Zone: "Europe/Athens"} // does not exist on 2026-03-29

	from := time.Date(2026, 3, 29, 0, 0, 0, 0, loc)
	got, err := s.NextTarget(from)
	if err != nil {
		t.Fatal(err)
	}

	if got.Hour() != 4 || got.Minute() != 30 {
		t.Fatalf("NextTarget = %v, want it slid to 04:30", got)
	}
	if !got.After(from) {
		t.Fatalf("NextTarget = %v is not after %v", got, from)
	}
	if got.Day() != 29 {
		t.Fatalf("NextTarget = %v, want it still on the 29th", got)
	}
}

// On 2026-10-25 Athens falls back 04:00 EEST -> 03:00 EET, so 03:30 happens
// twice. Go picks one; the docs do not guarantee which. We do not over-fit to
// that choice -- we assert only the invariants we actually depend on: it is a
// real instant, it is in the future, and its wall clock reads 03:30.
func TestNextTargetInsideFallBackAmbiguityIsStillValid(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 3, Minute: 30, Zone: "Europe/Athens"}

	from := time.Date(2026, 10, 25, 0, 0, 0, 0, loc)
	got, err := s.NextTarget(from)
	if err != nil {
		t.Fatal(err)
	}

	if !got.After(from) {
		t.Fatalf("NextTarget = %v is not after %v", got, from)
	}
	if w := got.Format("15:04"); w != "03:30" {
		t.Fatalf("NextTarget wall clock = %s, want 03:30", w)
	}
	if got.Day() != 25 {
		t.Fatalf("NextTarget = %v, want it on the 25th", got)
	}
}

// An empty Zone means "system local", resolved at call time.
func TestEmptyZoneMeansLocal(t *testing.T) {
	s := Spec{Hour: 7, Minute: 30}
	loc, err := s.Location()
	if err != nil {
		t.Fatal(err)
	}
	if loc != time.Local {
		t.Fatalf("Location() = %v, want time.Local", loc)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		spec    Spec
		wantErr bool
	}{
		{"ok", Spec{Hour: 7, Minute: 30, Offset: time.Minute, Grace: time.Minute}, false},
		{"midnight ok", Spec{Hour: 0, Minute: 0}, false},
		{"last minute ok", Spec{Hour: 23, Minute: 59}, false},
		{"hour too big", Spec{Hour: 24}, true},
		{"hour negative", Spec{Hour: -1}, true},
		{"minute too big", Spec{Minute: 60}, true},
		{"minute negative", Spec{Minute: -1}, true},
		{"negative offset", Spec{Offset: -time.Minute}, true},
		{"negative grace", Spec{Grace: -time.Minute}, true},
		{"offset >= 24h", Spec{Offset: 24 * time.Hour}, true},
		{"bad zone", Spec{Zone: "Mars/Olympus_Mons"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseHHMM(t *testing.T) {
	tests := []struct {
		in         string
		wantH      int
		wantM      int
		wantErr    bool
	}{
		{"07:30", 7, 30, false},
		{"00:00", 0, 0, false},
		{"23:59", 23, 59, false},
		{"7:30", 0, 0, true},   // must be zero-padded
		{"24:00", 0, 0, true},
		{"07:60", 0, 0, true},
		{"", 0, 0, true},
		{"0730", 0, 0, true},
		{"07:30:00", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			h, m, err := ParseHHMM(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseHHMM(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err == nil && (h != tt.wantH || m != tt.wantM) {
				t.Fatalf("ParseHHMM(%q) = %d,%d want %d,%d", tt.in, h, m, tt.wantH, tt.wantM)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test -race ./internal/schedule/ -run 'Spec|NextTarget|FireAt|Validate|ParseHHMM|Zone'
```

Expected: FAIL — `undefined: Spec`, `undefined: ParseHHMM`.

- [ ] **Step 3: Write the implementation**

Create `internal/schedule/spec.go`:

```go
package schedule

import (
	"fmt"
	"time"
)

// Spec is the user's intent, and it is what gets persisted.
//
// We deliberately do NOT persist an absolute time.Time. An instant is
// meaningless across a reboot, a DST change, or a timezone change; the intent
// ("07:30 in Athens, 20 minutes early") survives all three.
type Spec struct {
	Hour   int           // 0-23
	Minute int           // 0-59
	Zone   string        // IANA name, e.g. "Europe/Athens". Empty means system local.
	Offset time.Duration // lead-in: fire this long before the target
	Grace  time.Duration // how stale an unobserved alarm may be and still fire
}

// Validate reports whether the Spec is well-formed.
func (s Spec) Validate() error {
	if s.Hour < 0 || s.Hour > 23 {
		return fmt.Errorf("hour %d out of range 0-23", s.Hour)
	}
	if s.Minute < 0 || s.Minute > 59 {
		return fmt.Errorf("minute %d out of range 0-59", s.Minute)
	}
	if s.Offset < 0 {
		return fmt.Errorf("offset %v is negative", s.Offset)
	}
	if s.Offset >= 24*time.Hour {
		return fmt.Errorf("offset %v must be less than 24h", s.Offset)
	}
	if s.Grace < 0 {
		return fmt.Errorf("grace %v is negative", s.Grace)
	}
	if _, err := s.Location(); err != nil {
		return err
	}
	return nil
}

// Location resolves the Spec's timezone.
//
// We re-resolve on every call rather than caching. time.Local is resolved once
// per process (sync.Once), so a tray app running for days would otherwise never
// notice the user changing their system timezone.
func (s Spec) Location() (*time.Location, error) {
	if s.Zone == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(s.Zone)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q: %w", s.Zone, err)
	}
	return loc, nil
}

// NextTarget returns the next occurrence of Hour:Minute strictly after from --
// today if it is still ahead, otherwise tomorrow.
//
// Day()+1 is safe: time.Date normalises out-of-range fields, so 31 July + 1
// becomes 1 August.
//
// DST policy: for a target inside a spring-forward gap, time.Date slides it
// forward by an hour; for an ambiguous fall-back time it picks one instance.
// We adopt Go's behaviour as our policy, and spec_test.go pins it.
func (s Spec) NextTarget(from time.Time) (time.Time, error) {
	loc, err := s.Location()
	if err != nil {
		return time.Time{}, err
	}
	from = from.In(loc)

	t := time.Date(from.Year(), from.Month(), from.Day(), s.Hour, s.Minute, 0, 0, loc)
	if !t.After(from) {
		t = time.Date(from.Year(), from.Month(), from.Day()+1, s.Hour, s.Minute, 0, 0, loc)
	}
	return t, nil
}

// FireAt returns the instant the alarm should fire, and the target it precedes.
//
// The offset is subtracted from the *instant*, not from the calendar fields.
// That is what keeps the lead-in a true duration when it straddles a DST
// boundary: an offset of one hour always means one hour of real elapsed time,
// even if the wall clock appears to move by two.
func (s Spec) FireAt(from time.Time) (fire, target time.Time, err error) {
	target, err = s.NextTarget(from)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return target.Add(-s.Offset), target, nil
}

// ParseHHMM parses a zero-padded 24-hour "15:04" string.
func ParseHHMM(s string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, fmt.Errorf("time must be HH:MM, e.g. 07:30: %w", err)
	}
	return t.Hour(), t.Minute(), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test -race ./internal/schedule/ -v
```

Expected: PASS, all of them. `TestFireAtOffsetSpansSpringForwardGap` passing is the signal that the offset math is right.

- [ ] **Step 5: Commit**

```bash
git add internal/schedule/
git commit -m "feat(schedule): add Spec with DST-correct target and fire-time math

Persist the user's intent (HH:MM + IANA zone + offset), never an absolute
instant, which is meaningless across reboot, DST, and TZ change.

The offset is applied with Add() on the instant rather than by minute
arithmetic on the calendar fields. Test coverage includes the Athens
2026-03-29 spring-forward gap, where a 04:30 target with a 1h lead-in must
fire at 02:30 EET -- two hours earlier on the wall clock, but exactly one
hour of real elapsed time, because 03:30 does not exist that night."
```

---

### Task 4: `schedule` — Decide and DetectJump (pure policy)

Everything that carries correctness risk, with no goroutines, no channels, and no clock. If these two functions are right, the loop in Task 5 is trivial.

**Files:**
- Create: `internal/schedule/policy.go`, `internal/schedule/policy_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Decision int` with constants `DecideWait`, `DecideFire`, `DecideMissed`
  - `(Decision).String() string`
  - `func Decide(now, fireAt time.Time, grace time.Duration) Decision`
  - `func DetectJump(wallDelta, monoDelta, period time.Duration) (jump time.Duration, jumped bool)`

- [ ] **Step 1: Write the failing tests**

Create `internal/schedule/policy_test.go`:

```go
package schedule

import (
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	fire := time.Date(2026, 7, 14, 7, 10, 0, 0, time.UTC)
	const grace = 5 * time.Minute

	tests := []struct {
		name string
		now  time.Time
		want Decision
	}{
		{"long before", fire.Add(-time.Hour), DecideWait},
		{"one second before", fire.Add(-time.Second), DecideWait},
		{"exactly on time", fire, DecideFire},
		{"one second late", fire.Add(time.Second), DecideFire},
		{"just inside grace", fire.Add(grace - time.Second), DecideFire},
		{"exactly at grace edge", fire.Add(grace), DecideFire},
		{"one second past grace", fire.Add(grace + time.Second), DecideMissed},
		{"hours past grace", fire.Add(3 * time.Hour), DecideMissed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.now, fire, grace); got != tt.want {
				t.Fatalf("Decide(%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}

// A zero grace means the alarm may only fire on the exact tick it is due.
func TestDecideZeroGrace(t *testing.T) {
	fire := time.Date(2026, 7, 14, 7, 10, 0, 0, time.UTC)

	if got := Decide(fire, fire, 0); got != DecideFire {
		t.Fatalf("Decide at exactly fire with zero grace = %v, want DecideFire", got)
	}
	if got := Decide(fire.Add(time.Second), fire, 0); got != DecideMissed {
		t.Fatalf("Decide 1s late with zero grace = %v, want DecideMissed", got)
	}
}

// Decide must compare wall clock to wall clock. A time.Time carrying a
// monotonic reading must not be treated differently from one that has had it
// stripped -- if it is, we have reintroduced the suspend bug inside a function
// that looks like it compares wall clocks.
func TestDecideIgnoresMonotonicReading(t *testing.T) {
	// time.Now() carries a monotonic reading; Round(0) strips it.
	withMono := time.Now()
	stripped := withMono.Round(0)

	fire := stripped.Add(-time.Minute) // one minute overdue, inside a 5m grace

	got1 := Decide(withMono, fire, 5*time.Minute)
	got2 := Decide(stripped, fire, 5*time.Minute)

	if got1 != got2 {
		t.Fatalf("Decide disagrees depending on monotonic reading: %v vs %v", got1, got2)
	}
	if got1 != DecideFire {
		t.Fatalf("Decide = %v, want DecideFire", got1)
	}
}

func TestDetectJump(t *testing.T) {
	const period = time.Second

	tests := []struct {
		name       string
		wallDelta  time.Duration
		monoDelta  time.Duration
		wantJumped bool
		wantJump   time.Duration
	}{
		{
			name:      "normal tick: wall and monotonic agree",
			wallDelta: time.Second, monoDelta: time.Second,
			wantJumped: false,
		},
		{
			name:      "small scheduling jitter is not a jump",
			wallDelta: 1100 * time.Millisecond, monoDelta: 1050 * time.Millisecond,
			wantJumped: false,
		},
		{
			// The machine slept for 12h30m: the wall clock advanced, the
			// monotonic clock did not. This is the suspend signature.
			name:      "suspend: wall advances, monotonic does not",
			wallDelta: 12*time.Hour + 30*time.Minute, monoDelta: time.Second,
			wantJumped: true,
			wantJump:   12*time.Hour + 30*time.Minute - time.Second,
		},
		{
			name:      "NTP steps the clock backwards",
			wallDelta: -30 * time.Second, monoDelta: time.Second,
			wantJumped: true,
			wantJump:   -31 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jump, jumped := DetectJump(tt.wallDelta, tt.monoDelta, period)
			if jumped != tt.wantJumped {
				t.Fatalf("DetectJump jumped = %v, want %v", jumped, tt.wantJumped)
			}
			if jumped && jump != tt.wantJump {
				t.Fatalf("DetectJump jump = %v, want %v", jump, tt.wantJump)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test -race ./internal/schedule/ -run 'Decide|DetectJump'
```

Expected: FAIL — `undefined: Decide`, `undefined: DetectJump`.

- [ ] **Step 3: Write the implementation**

Create `internal/schedule/policy.go`:

```go
package schedule

import "time"

// Decision is what a single tick concludes about an armed alarm.
type Decision int

const (
	// DecideWait: the fire time is still ahead.
	DecideWait Decision = iota
	// DecideFire: the fire time has arrived, or passed within the grace window.
	DecideFire
	// DecideMissed: the fire time passed longer ago than the grace window
	// allows. The machine was almost certainly suspended. Do not run.
	DecideMissed
)

func (d Decision) String() string {
	switch d {
	case DecideWait:
		return "wait"
	case DecideFire:
		return "fire"
	case DecideMissed:
		return "missed"
	default:
		return "unknown"
	}
}

// Decide is the whole firing policy, as a pure function.
//
// Round(0) strips the monotonic reading from BOTH operands. This is not
// cosmetic. When two time.Time values both carry a monotonic reading, Before()
// and Sub() silently use it in preference to the wall clock -- so without these
// two calls we would be comparing monotonic clocks inside a function whose
// entire purpose is to compare wall clocks, and the suspend bug would be back.
//
// The grace window governs UNOBSERVED time only: it answers "the app was not
// watching -- is this alarm now too stale to honour?" It is deliberately not
// consulted when the user arms an alarm explicitly (see Alarm.Arm).
func Decide(now, fireAt time.Time, grace time.Duration) Decision {
	now = now.Round(0)
	fireAt = fireAt.Round(0)

	if now.Before(fireAt) {
		return DecideWait
	}
	if now.Sub(fireAt) > grace {
		return DecideMissed
	}
	return DecideFire
}

// DetectJump reports whether the wall clock moved independently of the
// monotonic clock between two ticks, which means the machine was suspended or
// the clock was stepped (NTP, manual change, timezone change).
//
// The two clocks normally advance together. When they diverge by more than a
// couple of tick periods, something moved the wall clock behind our back, and
// every derived fire time must be recomputed.
func DetectJump(wallDelta, monoDelta, period time.Duration) (time.Duration, bool) {
	jump := wallDelta - monoDelta

	tolerance := 2 * period
	if jump > tolerance || jump < -tolerance {
		return jump, true
	}
	return 0, false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test -race ./internal/schedule/ -v -run 'Decide|DetectJump'
```

Expected: PASS, all cases.

- [ ] **Step 5: Commit**

```bash
git add internal/schedule/
git commit -m "feat(schedule): add pure firing policy and time-jump detection

Decide() is the whole firing policy as a pure function: wait, fire, or
missed. It Round(0)s both operands, because Before() and Sub() silently
prefer the monotonic reading when both times carry one -- which would put
the suspend bug back inside a function whose job is to compare wall clocks.

DetectJump() spots suspend and NTP steps by watching the wall and monotonic
deltas diverge."
```

---

### Task 5: `schedule` — the Alarm loop

One goroutine, one ticker, running for the app's whole life. It always emits a tick (which drives the UI clock face) and, when armed, applies `Decide`.

Because there is only ever **one** loop, and `Arm`/`Disarm` merely mutate state it reads on the next tick, the double-fire race that a spawn-per-alarm design has to guard against with generation counters **cannot occur**. There is nothing to cancel and nothing to respawn.

**Files:**
- Create: `internal/schedule/alarm.go`, `internal/schedule/alarm_test.go`

**Interfaces:**
- Consumes: `Clock`, `TestClock`, `Ticker`, `TickerFunc`, `ManualTicker` (Task 2); `Spec` (Task 3); `Decide`, `DetectJump`, `Decision` (Task 4).
- Produces:
  - `type EventKind int` with constants `EventTick`, `EventFire`, `EventMissed`, `EventJump`
  - `(EventKind).String() string`
  - `type Update struct{ Kind EventKind; Now, FireAt, Target time.Time; Remaining, Late, Jump time.Duration }`
  - `type Alarm struct{...}`
  - `func NewAlarm(clk Clock, period time.Duration, newTicker TickerFunc) *Alarm`
  - `(*Alarm).Run(ctx context.Context)` — blocks; run it in a goroutine
  - `(*Alarm).Updates() <-chan Update`
  - `(*Alarm).Arm(fireAt, target time.Time, grace time.Duration)`
  - `(*Alarm).Disarm()`
  - `(*Alarm).Armed() (armed bool, fireAt, target time.Time)`

- [ ] **Step 1: Write the failing tests**

Create `internal/schedule/alarm_test.go`. The suspend test is the one that matters.

```go
package schedule

import (
	"context"
	"testing"
	"time"
)

// harness wires an Alarm to a TestClock and a ManualTicker, and gives the test
// a single-tick-and-collect primitive.
type harness struct {
	t     *testing.T
	clk   *TestClock
	tick  *ManualTicker
	alarm *Alarm
	stop  context.CancelFunc
}

func newHarness(t *testing.T, start time.Time) *harness {
	t.Helper()

	clk := NewTestClock(start)
	mt := NewManualTicker()
	a := NewAlarm(clk, time.Second, func(time.Duration) Ticker { return mt })

	ctx, cancel := context.WithCancel(context.Background())
	go a.Run(ctx)
	t.Cleanup(cancel)

	return &harness{t: t, clk: clk, tick: mt, alarm: a, stop: cancel}
}

// step advances the clock by d, delivers one tick, and returns every Update the
// loop emitted for it.
func (h *harness) step(d time.Duration) []Update {
	h.t.Helper()
	h.clk.Advance(d)
	h.tick.Tick()

	var got []Update
	for {
		select {
		case u := <-h.alarm.Updates():
			got = append(got, u)
		case <-time.After(200 * time.Millisecond):
			return got
		}
	}
}

func kinds(us []Update) []EventKind {
	ks := make([]EventKind, len(us))
	for i, u := range us {
		ks[i] = u.Kind
	}
	return ks
}

func hasKind(us []Update, k EventKind) bool {
	for _, u := range us {
		if u.Kind == k {
			return true
		}
	}
	return false
}

// A disarmed alarm still ticks -- that is what drives the UI clock face.
func TestAlarmTicksWhenDisarmed(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	got := h.step(time.Second)

	if len(got) != 1 || got[0].Kind != EventTick {
		t.Fatalf("kinds = %v, want exactly [tick]", kinds(got))
	}
	if want := start.Add(time.Second); !got[0].Now.Equal(want) {
		t.Fatalf("Now = %v, want %v", got[0].Now, want)
	}
}

func TestAlarmCountsDownThenFires(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	fire := start.Add(3 * time.Second)
	target := start.Add(1 * time.Hour)
	h.alarm.Arm(fire, target, 5*time.Minute)

	// Two ticks short of the fire time: countdown only.
	for i := 1; i <= 2; i++ {
		got := h.step(time.Second)
		if len(got) != 1 || got[0].Kind != EventTick {
			t.Fatalf("tick %d: kinds = %v, want [tick]", i, kinds(got))
		}
		wantRemaining := time.Duration(3-i) * time.Second
		if got[0].Remaining != wantRemaining {
			t.Fatalf("tick %d: Remaining = %v, want %v", i, got[0].Remaining, wantRemaining)
		}
	}

	// Third tick lands exactly on the fire time.
	got := h.step(time.Second)
	if !hasKind(got, EventFire) {
		t.Fatalf("kinds = %v, want a fire", kinds(got))
	}

	// And it disarms itself, so it cannot fire twice.
	if armed, _, _ := h.alarm.Armed(); armed {
		t.Fatal("alarm still armed after firing")
	}
	if got := h.step(time.Second); hasKind(got, EventFire) {
		t.Fatalf("fired a second time: kinds = %v", kinds(got))
	}
}

// THE test. This is the one that would have caught the monotonic-clock bug.
//
// The machine suspends for 12h30m across the fire time. On resume the alarm is
// hours stale, so it must report MISSED -- not fire, and not fire late.
func TestAlarmSuspendPastGraceReportsMissedAndDoesNotFire(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	fire := start.Add(10 * time.Minute)
	h.alarm.Arm(fire, fire.Add(20*time.Minute), 5*time.Minute)

	// The laptop lid closes. One tick later, 12h30m of wall clock has passed.
	got := h.step(12*time.Hour + 30*time.Minute)

	if !hasKind(got, EventJump) {
		t.Fatalf("kinds = %v, want a jump event", kinds(got))
	}
	if !hasKind(got, EventMissed) {
		t.Fatalf("kinds = %v, want a missed event", kinds(got))
	}
	if hasKind(got, EventFire) {
		t.Fatalf("alarm FIRED after a 12h30m suspend; it must not: kinds = %v", kinds(got))
	}

	for _, u := range got {
		if u.Kind == EventMissed {
			want := 12*time.Hour + 20*time.Minute // 12h30m elapsed, fire was 10m in
			if u.Late != want {
				t.Fatalf("Late = %v, want %v", u.Late, want)
			}
		}
	}

	if armed, _, _ := h.alarm.Armed(); armed {
		t.Fatal("alarm still armed after being missed")
	}
}

// A short suspend that lands inside the grace window must still fire.
func TestAlarmSuspendWithinGraceStillFires(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	fire := start.Add(1 * time.Minute)
	h.alarm.Arm(fire, fire.Add(20*time.Minute), 5*time.Minute)

	// Suspend for 4 minutes: 3 minutes past the fire time, inside a 5m grace.
	got := h.step(4 * time.Minute)

	if !hasKind(got, EventFire) {
		t.Fatalf("kinds = %v, want a fire (3m late is inside a 5m grace)", kinds(got))
	}
	if hasKind(got, EventMissed) {
		t.Fatalf("kinds = %v, want no missed", kinds(got))
	}
}

func TestAlarmDisarmStopsItFiring(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	fire := start.Add(2 * time.Second)
	h.alarm.Arm(fire, fire, time.Minute)
	h.alarm.Disarm()

	got := h.step(10 * time.Second)

	if hasKind(got, EventFire) {
		t.Fatalf("disarmed alarm fired: kinds = %v", kinds(got))
	}
	if !hasKind(got, EventTick) {
		t.Fatalf("kinds = %v, want it still ticking after disarm", kinds(got))
	}
}

// Re-arming replaces the pending alarm. The old fire time must be forgotten --
// this is the race that a spawn-per-alarm design needs a generation counter for,
// and that a single loop makes structurally impossible.
func TestAlarmRearmReplacesPendingFireTime(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	h.alarm.Arm(start.Add(2*time.Second), start.Add(time.Hour), time.Minute)
	h.alarm.Arm(start.Add(9*time.Second), start.Add(2*time.Hour), time.Minute)

	// Past the FIRST fire time. It must not fire: that alarm no longer exists.
	if got := h.step(3 * time.Second); hasKind(got, EventFire) {
		t.Fatalf("fired at the superseded fire time: kinds = %v", kinds(got))
	}

	// Past the second. Exactly one fire.
	got := h.step(7 * time.Second)
	fires := 0
	for _, u := range got {
		if u.Kind == EventFire {
			fires++
		}
	}
	if fires != 1 {
		t.Fatalf("got %d fires, want exactly 1: kinds = %v", fires, kinds(got))
	}
}

func TestAlarmRunStopsOnContextCancel(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	clk := NewTestClock(start)
	mt := NewManualTicker()
	a := NewAlarm(clk, time.Second, func(time.Duration) Ticker { return mt })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of context cancellation")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test -race ./internal/schedule/ -run TestAlarm
```

Expected: FAIL — `undefined: NewAlarm`.

- [ ] **Step 3: Write the implementation**

Create `internal/schedule/alarm.go`:

```go
package schedule

import (
	"context"
	"sync"
	"time"
)

// EventKind is the type of an Update.
type EventKind int

const (
	// EventTick is emitted on every tick, armed or not. It drives the UI clock.
	EventTick EventKind = iota
	// EventFire means: run the job now.
	EventFire
	// EventMissed means the fire time passed while nobody was watching, by more
	// than the grace window. The job is NOT run.
	EventMissed
	// EventJump means the wall clock moved independently of the monotonic clock:
	// the machine suspended, or the clock was stepped. Every derived fire time
	// must be recomputed from the Spec.
	EventJump
)

func (k EventKind) String() string {
	switch k {
	case EventTick:
		return "tick"
	case EventFire:
		return "fire"
	case EventMissed:
		return "missed"
	case EventJump:
		return "jump"
	default:
		return "unknown"
	}
}

// Update is one thing the alarm loop noticed.
type Update struct {
	Kind EventKind
	Now  time.Time // wall clock, monotonic reading already stripped

	FireAt time.Time // zero if disarmed
	Target time.Time // zero if disarmed

	Remaining time.Duration // EventTick, when armed: how long until FireAt
	Late      time.Duration // EventMissed: how far past FireAt we woke up
	Jump      time.Duration // EventJump: how far the wall clock moved
}

// Alarm is a single, always-running poll loop.
//
// It ticks for the entire life of the app, whether armed or not, so the UI has
// a clock. Arm and Disarm mutate state that the loop reads on its next tick.
//
// Because there is only ever ONE loop, there is no spawn, no cancel, and no
// respawn -- and therefore no double-fire race to guard against with a
// generation counter. Re-arming simply overwrites the pending fire time.
type Alarm struct {
	clk       Clock
	period    time.Duration
	newTicker TickerFunc
	out       chan Update

	mu     sync.Mutex
	armed  bool
	fireAt time.Time
	target time.Time
	grace  time.Duration
}

// NewAlarm returns an Alarm. Call Run in a goroutine to start it.
func NewAlarm(clk Clock, period time.Duration, newTicker TickerFunc) *Alarm {
	return &Alarm{
		clk:       clk,
		period:    period,
		newTicker: newTicker,
		out:       make(chan Update),
	}
}

// Updates is the stream of everything the loop notices. It is unbuffered: the
// consumer must keep reading, or the loop blocks.
func (a *Alarm) Updates() <-chan Update { return a.out }

// Arm schedules a fire at fireAt. It replaces any pending alarm.
func (a *Alarm) Arm(fireAt, target time.Time, grace time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.armed = true
	a.fireAt = fireAt.Round(0)
	a.target = target.Round(0)
	a.grace = grace
}

// Disarm cancels any pending alarm. The loop keeps ticking.
func (a *Alarm) Disarm() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.armed = false
	a.fireAt = time.Time{}
	a.target = time.Time{}
}

// Armed reports the current armed state and the pending times.
func (a *Alarm) Armed() (bool, time.Time, time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.armed, a.fireAt, a.target
}

// Run is the loop. It blocks until ctx is cancelled.
func (a *Alarm) Run(ctx context.Context) {
	tk := a.newTicker(a.period)
	defer tk.Stop()

	// Two readings of the same instant: one wall (Round(0) strips the monotonic
	// reading), one still carrying it. Their deltas diverging is what reveals a
	// suspend or a clock step.
	lastWall := a.clk.Now().Round(0)
	lastMono := a.clk.Now()

	for {
		select {
		case <-ctx.Done():
			return

		case _, ok := <-tk.C():
			if !ok {
				return // ticker stopped
			}

			raw := a.clk.Now()
			now := raw.Round(0)

			if jump, jumped := DetectJump(now.Sub(lastWall), raw.Sub(lastMono), a.period); jumped {
				if !a.emit(ctx, Update{Kind: EventJump, Now: now, Jump: jump}) {
					return
				}
			}
			lastWall, lastMono = now, raw

			armed, fireAt, target, grace := a.snapshot()
			if !armed {
				if !a.emit(ctx, Update{Kind: EventTick, Now: now}) {
					return
				}
				continue
			}

			switch Decide(now, fireAt, grace) {
			case DecideWait:
				if !a.emit(ctx, Update{
					Kind: EventTick, Now: now,
					FireAt: fireAt, Target: target,
					Remaining: fireAt.Sub(now),
				}) {
					return
				}

			case DecideFire:
				a.Disarm() // before emitting, so we cannot fire twice
				if !a.emit(ctx, Update{
					Kind: EventFire, Now: now,
					FireAt: fireAt, Target: target,
				}) {
					return
				}

			case DecideMissed:
				a.Disarm()
				if !a.emit(ctx, Update{
					Kind: EventMissed, Now: now,
					FireAt: fireAt, Target: target,
					Late: now.Sub(fireAt),
				}) {
					return
				}
			}
		}
	}
}

func (a *Alarm) snapshot() (bool, time.Time, time.Time, time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.armed, a.fireAt, a.target, a.grace
}

// emit sends u, or reports false if ctx was cancelled while we waited.
func (a *Alarm) emit(ctx context.Context, u Update) bool {
	select {
	case a.out <- u:
		return true
	case <-ctx.Done():
		return false
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test -race ./internal/schedule/ -v -run TestAlarm
```

Expected: PASS, all seven. `TestAlarmSuspendPastGraceReportsMissedAndDoesNotFire` passing means a 12h30m suspend is handled correctly — the bug the whole design exists to avoid.

- [ ] **Step 5: Run the whole package and check coverage**

```bash
go test -race -cover ./internal/schedule/
```

Expected: PASS, coverage ≥ 85%.

- [ ] **Step 6: Commit**

```bash
git add internal/schedule/
git commit -m "feat(schedule): add the alarm loop

A single always-running 1s loop: it ticks for the life of the app (driving
the UI clock face) and, when armed, applies Decide().

Because there is only ever one loop, and Arm/Disarm just mutate state it
reads on the next tick, the double-fire race that a spawn-per-alarm design
needs a generation counter to prevent cannot occur. Re-arming overwrites.

Covered: a simulated 12h30m suspend across the fire time reports MISSED and
does not run the job; a 3-minute suspend inside the 5-minute grace window
still fires."
```

---

### Task 6: `runner` — Config and BuildArgs

Pure. No `exec`, no I/O. Golden-tested, including negative assertions: the dangerous flags must be **absent**.

**Files:**
- Create: `internal/runner/runner.go`, `internal/runner/args.go`, `internal/runner/args_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Config struct{ Bin, WorkDir, Model, Prompt string; BudgetUSD float64; Timeout time.Duration }`
  - `type Result struct{ Text string; CostUSD float64; SessionID string; Duration time.Duration; Raw string }`
  - `type Runner interface{ Run(context.Context, Config) (Result, error) }`
  - `func BuildArgs(Config) []string`
  - `const DefaultModel = "haiku"`, `DefaultPrompt = "Hello world"`, `DefaultBudgetUSD = 0.10`, `DefaultTimeout = 120 * time.Second`

- [ ] **Step 1: Write the failing test**

Create `internal/runner/args_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test -race ./internal/runner/
```

Expected: FAIL — `undefined: BuildArgs`, `undefined: Config`.

- [ ] **Step 3: Write the implementation**

Create `internal/runner/runner.go`:

```go
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
```

Create `internal/runner/args.go`:

```go
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
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test -race ./internal/runner/ -v
```

Expected: PASS, all seven.

- [ ] **Step 5: Commit**

```bash
git add internal/runner/
git commit -m "feat(runner): add Config, Result, Runner, and pure BuildArgs

Golden-tested, including negative assertions that --bare and
--dangerously-skip-permissions never appear: the first breaks OAuth auth,
and the second is unnecessary because --tools \"\" already gives a zero
tool surface.

--safe-mode is asserted present, not optional: without it the CLI inherits
the user's global CLAUDE.md and skills, measured at 16.5s/\$0.0252 against
3.3s/\$0.0044, and it changes the answer."
```

---

### Task 7: `runner` — the exec path, envelope parsing, and the Fake

Tested entirely against a shell script stand-in. No network, no API spend, no real `claude` needed.

**Files:**
- Create: `internal/runner/claude.go`, `internal/runner/fake.go`, `internal/runner/claude_test.go`, `internal/runner/testdata/ok.sh`, `internal/runner/testdata/bad_model.sh`, `internal/runner/testdata/garbage.sh`, `internal/runner/testdata/hang.sh`, `internal/runner/testdata/cwd.sh`

**Interfaces:**
- Consumes: `Config`, `Result`, `Runner` (Task 6).
- Produces:
  - `type CLI struct{}` implementing `Runner`
  - `func NewCLI() *CLI`
  - `func Lookup() (string, error)` — resolves the `claude` binary
  - `type Fake struct{ mu sync.Mutex; Calls []Config; Result Result; Err error; Delay time.Duration }` implementing `Runner`
  - `(*Fake).CallCount() int`, `(*Fake).LastCall() (Config, bool)`

- [ ] **Step 1: Write the test fixtures**

These stand in for the real CLI. Each must be executable.

`internal/runner/testdata/ok.sh` — a real success envelope, shape copied from a verified run:

```sh
#!/bin/sh
# Stands in for a successful `claude -p --output-format json` run.
# Echoes the arguments it was given to stderr so the test can assert on them.
echo "ARGS: $*" >&2
cat <<'JSON'
{"type":"result","subtype":"success","is_error":false,"result":"Hello! How can I help you today?","session_id":"1f0c8f2a-3d4e-4b5a-9c6d-7e8f9a0b1c2d","total_cost_usd":0.0044,"duration_ms":3312,"num_turns":1}
JSON
```

`internal/runner/testdata/bad_model.sh` — **the trap.** Note `subtype` says `"success"` while `is_error` is `true`. Branching on `subtype` here reports a 404 as a success:

```sh
#!/bin/sh
cat <<'JSON'
{"type":"result","subtype":"success","is_error":true,"result":"API Error: 404 {\"type\":\"error\",\"error\":{\"type\":\"not_found_error\",\"message\":\"model: nonexistent-model\"}}","api_error_status":404,"session_id":"deadbeef","total_cost_usd":0,"duration_ms":412}
JSON
exit 1
```

`internal/runner/testdata/garbage.sh` — unparseable stdout plus a diagnostic on stderr:

```sh
#!/bin/sh
echo "this is not json"
echo "something went badly wrong" >&2
exit 2
```

`internal/runner/testdata/hang.sh` — stands in for the network-unreachable case, where the real CLI hangs forever with no output and no exit:

```sh
#!/bin/sh
sleep 300
```

`internal/runner/testdata/cwd.sh` — proves `cmd.Dir` is what sets the working directory:

```sh
#!/bin/sh
printf '{"type":"result","subtype":"success","is_error":false,"result":"%s","session_id":"x","total_cost_usd":0,"duration_ms":1}\n' "$(pwd)"
```

`internal/runner/testdata/echoargs.sh` — returns its own argv as the answer text, so a test can assert that `BuildArgs`'s output actually reaches `exec`:

```sh
#!/bin/sh
# Escape backslashes then double quotes, so the argv survives being embedded in
# a JSON string.
ESCAPED=$(printf '%s' "$*" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')
printf '{"type":"result","subtype":"success","is_error":false,"result":"%s","session_id":"x","total_cost_usd":0,"duration_ms":1}\n' "$ESCAPED"
```

Make them executable:

```bash
chmod +x internal/runner/testdata/*.sh
```

- [ ] **Step 2: Write the failing test**

Create `internal/runner/claude_test.go`:

```go
package runner

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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
```

- [ ] **Step 3: Run the test to verify it fails**

```bash
go test -race ./internal/runner/ -run 'TestCLI|TestFake|TestLookup'
```

Expected: FAIL — `undefined: NewCLI`, `undefined: Fake`, `undefined: Lookup`.

- [ ] **Step 4: Write the implementation**

Create `internal/runner/claude.go`:

```go
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
```

Create `internal/runner/fake.go`:

```go
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

// Calls returns a copy of every Config Run was given.
func (f *Fake) Calls() []Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Config(nil), f.calls...)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test -race -cover ./internal/runner/ -v
```

Expected: PASS, all of them. Coverage ≥ 85%.

If `TestCLIRunSuccess` fails with "permission denied", the fixtures are not executable — re-run `chmod +x internal/runner/testdata/*.sh`.

- [ ] **Step 6: Commit**

```bash
git add internal/runner/
git commit -m "feat(runner): add the CLI exec path, envelope parsing, and a Fake

The envelope struct deliberately has no Subtype field: the CLI reports
\"subtype\":\"success\" on a 404 alongside \"is_error\":true, so not having
the field makes it impossible to branch on it by accident. A fixture pins
that exact envelope.

exec.CommandContext with a deadline is mandatory, not defensive: on network
failure the real CLI hangs forever with no output and no exit, and a
signal-killed process reports exit -1, so the timeout can only be detected
via ctx.Err().

Tested end to end against shell-script stand-ins -- no network, no API
spend, no claude binary required."
```

---

### Task 8: `config` — State, Store, MemStore, PrefsStore

`store.go` is Fyne-free. `prefs.go` is the one non-UI file permitted to import Fyne.

**Files:**
- Create: `internal/config/store.go`, `internal/config/prefs.go`, `internal/config/store_test.go`, `internal/config/prefs_test.go`

**Interfaces:**
- Consumes: `schedule.Spec` (Task 3).
- Produces:
  - `type State struct{ Spec schedule.Spec; WorkDir, Model, Prompt string; Armed bool; FireAt, Target time.Time }`
  - `(State).Validate() error`
  - `func DefaultState(workDir string) State`
  - `type Store interface{ Load() (State, error); Save(State) error }`
  - `type MemStore struct{...}`; `func NewMemStore(State) *MemStore`
  - `type PrefsStore struct{...}`; `func NewPrefsStore(fyne.Preferences) *PrefsStore`

- [ ] **Step 1: Write the failing tests**

Create `internal/config/store_test.go`:

```go
package config

import (
	"testing"
	"time"

	"claudealarm/internal/schedule"
)

func TestDefaultState(t *testing.T) {
	s := DefaultState("/tmp/project")

	if s.WorkDir != "/tmp/project" {
		t.Fatalf("WorkDir = %q", s.WorkDir)
	}
	if s.Model != "haiku" {
		t.Fatalf("Model = %q, want haiku", s.Model)
	}
	if s.Prompt != "Hello world" {
		t.Fatalf("Prompt = %q, want Hello world", s.Prompt)
	}
	if s.Spec.Grace != 5*time.Minute {
		t.Fatalf("Grace = %v, want 5m", s.Spec.Grace)
	}
	if s.Armed {
		t.Fatal("a default state must not be armed")
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("the default state must be valid: %v", err)
	}
}

func TestStateValidate(t *testing.T) {
	valid := DefaultState("/tmp")

	tests := []struct {
		name    string
		mutate  func(*State)
		wantErr bool
	}{
		{"default is valid", func(*State) {}, false},
		{"empty workdir", func(s *State) { s.WorkDir = "" }, true},
		{"empty model", func(s *State) { s.Model = "" }, true},
		{"empty prompt", func(s *State) { s.Prompt = "" }, true},
		{"bad hour propagates from Spec", func(s *State) { s.Spec.Hour = 99 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := valid
			tt.mutate(&s)
			if err := s.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMemStoreRoundTrips(t *testing.T) {
	st := NewMemStore(DefaultState("/tmp"))

	want := State{
		Spec:    schedule.Spec{Hour: 7, Minute: 30, Zone: "Europe/Athens", Offset: 20 * time.Minute, Grace: 5 * time.Minute},
		WorkDir: "/home/Bill/project",
		Model:   "haiku",
		Prompt:  "Hello world",
		Armed:   true,
		FireAt:  time.Date(2026, 7, 15, 7, 10, 0, 0, time.UTC),
		Target:  time.Date(2026, 7, 15, 7, 30, 0, 0, time.UTC),
	}
	if err := st.Save(want); err != nil {
		t.Fatal(err)
	}

	got, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}

	if got.Spec != want.Spec {
		t.Fatalf("Spec = %+v, want %+v", got.Spec, want.Spec)
	}
	if got.Armed != want.Armed {
		t.Fatalf("Armed = %v, want %v", got.Armed, want.Armed)
	}
	if !got.FireAt.Equal(want.FireAt) {
		t.Fatalf("FireAt = %v, want %v", got.FireAt, want.FireAt)
	}
	if !got.Target.Equal(want.Target) {
		t.Fatalf("Target = %v, want %v", got.Target, want.Target)
	}
	if got.WorkDir != want.WorkDir {
		t.Fatalf("WorkDir = %q, want %q", got.WorkDir, want.WorkDir)
	}
}
```

Create `internal/config/prefs_test.go`. This one needs Fyne's test app, which provides a real in-memory `Preferences`:

```go
package config

import (
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"claudealarm/internal/schedule"
)

func TestPrefsStoreRoundTripsThroughFynePreferences(t *testing.T) {
	a := test.NewApp()
	defer test.NewApp() // reset global app state for other tests

	st := NewPrefsStore(a.Preferences())

	want := State{
		Spec: schedule.Spec{
			Hour: 7, Minute: 30,
			Zone:   "Europe/Athens",
			Offset: 20 * time.Minute,
			Grace:  5 * time.Minute,
		},
		WorkDir: "/home/Bill/project",
		Model:   "haiku",
		Prompt:  "Hello world",
		Armed:   true,
		FireAt:  time.Date(2026, 7, 15, 7, 10, 0, 0, time.UTC),
		Target:  time.Date(2026, 7, 15, 7, 30, 0, 0, time.UTC),
	}
	if err := st.Save(want); err != nil {
		t.Fatal(err)
	}

	got, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}

	if got.Spec != want.Spec {
		t.Fatalf("Spec = %+v, want %+v", got.Spec, want.Spec)
	}
	if got.WorkDir != want.WorkDir || got.Model != want.Model || got.Prompt != want.Prompt {
		t.Fatalf("strings did not round-trip: %+v", got)
	}
	if !got.Armed {
		t.Fatal("Armed did not round-trip")
	}
	if !got.FireAt.Equal(want.FireAt) {
		t.Fatalf("FireAt = %v, want %v", got.FireAt, want.FireAt)
	}
	if !got.Target.Equal(want.Target) {
		t.Fatalf("Target = %v, want %v", got.Target, want.Target)
	}
}

// A fresh install has nothing stored. Load must return usable defaults, not an
// error and not a zero State.
func TestPrefsStoreLoadOnFreshInstallReturnsDefaults(t *testing.T) {
	a := test.NewApp()
	defer test.NewApp()

	got, err := NewPrefsStore(a.Preferences()).Load()
	if err != nil {
		t.Fatalf("Load() on a fresh install must not error: %v", err)
	}

	if got.Model != "haiku" {
		t.Fatalf("Model = %q, want the default haiku", got.Model)
	}
	if got.Prompt != "Hello world" {
		t.Fatalf("Prompt = %q, want the default", got.Prompt)
	}
	if got.Spec.Grace != 5*time.Minute {
		t.Fatalf("Grace = %v, want the default 5m", got.Spec.Grace)
	}
	if got.Armed {
		t.Fatal("a fresh install must not be armed")
	}
	if got.WorkDir == "" {
		t.Fatal("WorkDir must default to something, not empty")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test -race ./internal/config/
```

Expected: FAIL — `undefined: DefaultState`, `undefined: NewMemStore`, `undefined: NewPrefsStore`.

- [ ] **Step 3: Write `store.go` (Fyne-free)**

```go
// Package config persists the user's alarm settings.
//
// store.go must never import Fyne. Only prefs.go may.
package config

import (
	"fmt"
	"os"
	"time"

	"claudealarm/internal/schedule"
)

// Defaults.
const (
	DefaultModel  = "haiku"
	DefaultPrompt = "Hello world"
	DefaultHour   = 7
	DefaultMinute = 30
	DefaultOffset = 5 * time.Minute
	DefaultGrace  = 5 * time.Minute
)

// State is everything that survives a restart.
//
// FireAt and Target are persisted alongside the Spec so that a restart can tell
// the difference between "the alarm is still pending" and "the alarm came due
// while we were not running". They are always recomputed from the Spec on load;
// the stored copies exist only to detect a missed alarm.
type State struct {
	Spec schedule.Spec

	WorkDir string
	Model   string
	Prompt  string

	Armed  bool
	FireAt time.Time
	Target time.Time
}

// DefaultState returns a valid, disarmed State for a fresh install.
func DefaultState(workDir string) State {
	return State{
		Spec: schedule.Spec{
			Hour:   DefaultHour,
			Minute: DefaultMinute,
			Zone:   "", // system local
			Offset: DefaultOffset,
			Grace:  DefaultGrace,
		},
		WorkDir: workDir,
		Model:   DefaultModel,
		Prompt:  DefaultPrompt,
		Armed:   false,
	}
}

// Validate reports whether the State is well-formed and armable.
func (s State) Validate() error {
	if err := s.Spec.Validate(); err != nil {
		return err
	}
	if s.WorkDir == "" {
		return fmt.Errorf("working directory is empty")
	}
	if s.Model == "" {
		return fmt.Errorf("model is empty")
	}
	if s.Prompt == "" {
		return fmt.Errorf("prompt is empty")
	}
	return nil
}

// ValidateWorkDir checks the working directory exists and is a directory. It is
// separate from Validate because it touches the filesystem, and we only want to
// pay for that at arm time.
func (s State) ValidateWorkDir() error {
	fi, err := os.Stat(s.WorkDir)
	if err != nil {
		return fmt.Errorf("working directory %q: %w", s.WorkDir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("working directory %q is not a directory", s.WorkDir)
	}
	return nil
}

// Store persists State. It is the seam that lets the app be tested without disk.
type Store interface {
	Load() (State, error)
	Save(State) error
}

// MemStore is an in-memory Store for tests.
type MemStore struct{ state State }

// NewMemStore returns a MemStore seeded with the given State.
func NewMemStore(s State) *MemStore { return &MemStore{state: s} }

func (m *MemStore) Load() (State, error) { return m.state, nil }

func (m *MemStore) Save(s State) error { m.state = s; return nil }
```

- [ ] **Step 4: Write `prefs.go` (the one non-UI file that may import Fyne)**

```go
package config

import (
	"time"

	"fyne.io/fyne/v2"
)

// Preference keys. Namespaced so they cannot collide with Fyne's own.
const (
	keyHour    = "alarm.hour"
	keyMinute  = "alarm.minute"
	keyZone    = "alarm.zone"
	keyOffset  = "alarm.offsetSeconds"
	keyGrace   = "alarm.graceSeconds"
	keyWorkDir = "alarm.workDir"
	keyModel   = "alarm.model"
	keyPrompt  = "alarm.prompt"
	keyArmed   = "alarm.armed"
	keyFireAt  = "alarm.fireAtRFC3339"
	keyTarget  = "alarm.targetRFC3339"
)

// PrefsStore persists State via fyne.Preferences, which writes
// $XDG_CONFIG_HOME/<appID>/preferences.json.
//
// Requires the app to have been created with app.NewWithID -- Preferences()
// does not work otherwise.
//
// Durations are stored as whole seconds and times as RFC3339 strings, because
// fyne.Preferences only handles bool/int/float/string.
type PrefsStore struct{ p fyne.Preferences }

// NewPrefsStore returns a Store backed by Fyne's preferences.
func NewPrefsStore(p fyne.Preferences) *PrefsStore { return &PrefsStore{p: p} }

// Load reads the stored State, falling back to defaults for anything absent.
// A fresh install returns a valid default State, not an error.
func (s *PrefsStore) Load() (State, error) {
	def := DefaultState(defaultWorkDir())

	st := State{
		Spec: schedule.Spec{
			Hour:   s.p.IntWithFallback(keyHour, def.Spec.Hour),
			Minute: s.p.IntWithFallback(keyMinute, def.Spec.Minute),
			Zone:   s.p.StringWithFallback(keyZone, def.Spec.Zone),
			Offset: time.Duration(s.p.IntWithFallback(keyOffset, int(def.Spec.Offset/time.Second))) * time.Second,
			Grace:  time.Duration(s.p.IntWithFallback(keyGrace, int(def.Spec.Grace/time.Second))) * time.Second,
		},
		WorkDir: s.p.StringWithFallback(keyWorkDir, def.WorkDir),
		Model:   s.p.StringWithFallback(keyModel, def.Model),
		Prompt:  s.p.StringWithFallback(keyPrompt, def.Prompt),
		Armed:   s.p.BoolWithFallback(keyArmed, false),
	}

	st.FireAt = parseTime(s.p.StringWithFallback(keyFireAt, ""))
	st.Target = parseTime(s.p.StringWithFallback(keyTarget, ""))

	return st, nil
}

// Save writes the State.
func (s *PrefsStore) Save(st State) error {
	s.p.SetInt(keyHour, st.Spec.Hour)
	s.p.SetInt(keyMinute, st.Spec.Minute)
	s.p.SetString(keyZone, st.Spec.Zone)
	s.p.SetInt(keyOffset, int(st.Spec.Offset/time.Second))
	s.p.SetInt(keyGrace, int(st.Spec.Grace/time.Second))
	s.p.SetString(keyWorkDir, st.WorkDir)
	s.p.SetString(keyModel, st.Model)
	s.p.SetString(keyPrompt, st.Prompt)
	s.p.SetBool(keyArmed, st.Armed)
	s.p.SetString(keyFireAt, formatTime(st.FireAt))
	s.p.SetString(keyTarget, formatTime(st.Target))
	return nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// defaultWorkDir is the process's current directory -- "this current dir", per
// the spec. Falls back to $HOME if that somehow fails.
func defaultWorkDir() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	home, _ := os.UserHomeDir()
	return home
}
```

Add the missing imports to `prefs.go`: `os` and `claudealarm/internal/schedule`.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test -race -cover ./internal/config/ -v
```

Expected: PASS, all four.

- [ ] **Step 6: Commit**

```bash
git add internal/config/
git commit -m "feat(config): add State, Store, MemStore, and a Fyne-backed PrefsStore

store.go stays Fyne-free; prefs.go is the one non-UI file allowed to import
it. Durations persist as whole seconds and times as RFC3339, because
fyne.Preferences only handles bool/int/float/string.

FireAt and Target are stored alongside the Spec purely so a restart can
tell 'still pending' from 'came due while we were not running'."
```

---

### Task 9: `app` — the Core orchestrator

Where the three interfaces meet. **This is where spec §5.3's central distinction is enforced:** `Arm` is an explicit user act and ignores the grace window; `Restore` is unobserved time and honours it.

**Files:**
- Create: `internal/app/core.go`, `internal/app/core_test.go`

**Interfaces:**
- Consumes: `schedule.{Clock, Alarm, Spec, Update, EventKind, Decide, Decision}` (Tasks 2–5); `runner.{Runner, Config, Result}` (Tasks 6–7); `config.{State, Store}` (Task 8).
- Produces:
  - `type Status int` with constants `StatusIdle`, `StatusArmed`, `StatusRunning`, `StatusDone`, `StatusMissed`, `StatusError`
  - `(Status).String() string`
  - `type Event struct{ Status Status; Now, FireAt, Target time.Time; Remaining, Late time.Duration; Result runner.Result; Err error }`
  - `type Core struct{...}`
  - `func New(clk schedule.Clock, al *schedule.Alarm, r runner.Runner, st config.Store) *Core`
  - `(*Core).Run(ctx context.Context)` — blocks; consumes the alarm's updates
  - `(*Core).Events() <-chan Event`
  - `(*Core).State() config.State`
  - `(*Core).Arm(config.State) error`
  - `(*Core).Disarm()`
  - `(*Core).RunNow()`
  - `(*Core).Restore() error`

- [ ] **Step 1: Write the failing tests**

Create `internal/app/core_test.go`:

```go
package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"claudealarm/internal/config"
	"claudealarm/internal/runner"
	"claudealarm/internal/schedule"
)

type rig struct {
	t     *testing.T
	clk   *schedule.TestClock
	tick  *schedule.ManualTicker
	fake  *runner.Fake
	store *config.MemStore
	core  *Core
}

func newRig(t *testing.T, start time.Time) *rig {
	t.Helper()

	clk := schedule.NewTestClock(start)
	mt := schedule.NewManualTicker()
	al := schedule.NewAlarm(clk, time.Second, func(time.Duration) schedule.Ticker { return mt })
	fake := &runner.Fake{Result: runner.Result{Text: "Hello! How can I help you today?", CostUSD: 0.0044}}
	store := config.NewMemStore(config.DefaultState(t.TempDir()))

	c := New(clk, al, fake, store)

	ctx, cancel := context.WithCancel(context.Background())
	go al.Run(ctx)
	go c.Run(ctx)
	t.Cleanup(cancel)

	return &rig{t: t, clk: clk, tick: mt, fake: fake, store: store, core: c}
}

// step advances the clock, ticks once, and drains the events that follow.
func (r *rig) step(d time.Duration) []Event {
	r.t.Helper()
	r.clk.Advance(d)
	r.tick.Tick()

	var got []Event
	for {
		select {
		case e := <-r.core.Events():
			got = append(got, e)
		case <-time.After(300 * time.Millisecond):
			return got
		}
	}
}

func (r *rig) armed(spec schedule.Spec) config.State {
	r.t.Helper()
	st := r.store.MustLoad()
	st.Spec = spec
	if err := r.core.Arm(st); err != nil {
		r.t.Fatalf("Arm() error = %v", err)
	}
	return st
}

func lastStatus(es []Event) (Status, bool) {
	if len(es) == 0 {
		return 0, false
	}
	return es[len(es)-1].Status, true
}

func hasStatus(es []Event, s Status) bool {
	for _, e := range es {
		if e.Status == s {
			return true
		}
	}
	return false
}

func TestCoreArmThenFireRunsClaudeOnce(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	// Target 07:30, offset 20m -> fire at 07:10, ten minutes away.
	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})

	if got := r.step(time.Second); !hasStatus(got, StatusArmed) {
		t.Fatalf("want StatusArmed while counting down, got %v", got)
	}
	if r.fake.CallCount() != 0 {
		t.Fatal("claude ran before the alarm was due")
	}

	got := r.step(10 * time.Minute)

	if !hasStatus(got, StatusDone) {
		t.Fatalf("want StatusDone after firing, got %v", got)
	}
	if r.fake.CallCount() != 1 {
		t.Fatalf("claude ran %d times, want exactly 1", r.fake.CallCount())
	}

	call, _ := r.fake.LastCall()
	if call.Prompt != "Hello world" {
		t.Fatalf("prompt = %q, want %q", call.Prompt, "Hello world")
	}
	if call.Model != "haiku" {
		t.Fatalf("model = %q, want haiku", call.Model)
	}
	if call.WorkDir == "" {
		t.Fatal("WorkDir was not passed to the runner")
	}

	// The result reaches the UI.
	for _, e := range got {
		if e.Status == StatusDone && e.Result.Text != "Hello! How can I help you today?" {
			t.Fatalf("Result.Text = %q", e.Result.Text)
		}
	}
}

// After firing, the core must be idle-ish and disarmed -- never silently
// rescheduled. The spec is explicit: one-shot.
func TestCoreDoesNotRearmItselfAfterFiring(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})
	r.step(10 * time.Minute) // fires

	// A full day later it must not have run again.
	r.step(24 * time.Hour)

	if r.fake.CallCount() != 1 {
		t.Fatalf("claude ran %d times over 24h; a one-shot alarm must run once", r.fake.CallCount())
	}
	if st := r.store.MustLoad(); st.Armed {
		t.Fatal("the persisted state is still armed after firing")
	}
}

// A 12h30m suspend past the grace window: MISSED, and claude must NOT run.
func TestCoreSuspendPastGraceIsMissedAndDoesNotRunClaude(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})

	got := r.step(12*time.Hour + 30*time.Minute)

	if !hasStatus(got, StatusMissed) {
		t.Fatalf("want StatusMissed after a 12h30m suspend, got %v", got)
	}
	if r.fake.CallCount() != 0 {
		t.Fatal("claude ran after a 12h30m suspend; a stale alarm must not fire")
	}
}

// THE distinction from spec 5.3. Arming is an explicit act with the user
// watching, so a fire time already in the past fires IMMEDIATELY -- the grace
// window is not consulted. Grace governs unobserved time only.
//
// Here: the user arms a 07:30 target with a 20-minute lead-in at 07:20. The
// fire time (07:10) is already ten minutes gone -- twice the 5-minute grace --
// but the intent ("do this before 07:30") is still satisfiable, so we run.
func TestCoreArmWithFireTimeAlreadyPastFiresImmediatelyIgnoringGrace(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 20, 0, 0, time.Local)
	r := newRig(t, start)

	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})

	// Give the immediate run a moment to land.
	got := r.step(time.Second)

	if r.fake.CallCount() != 1 {
		t.Fatalf("claude ran %d times, want 1: arming with a past fire time must fire immediately", r.fake.CallCount())
	}
	if !hasStatus(got, StatusDone) && !hasStatus(got, StatusRunning) {
		t.Fatalf("want Running or Done, got %v", got)
	}
}

// Restore is the opposite: it represents time the app was NOT watching (it was
// shut down), so the grace window DOES apply.
func TestCoreRestoreHonoursTheGraceWindow(t *testing.T) {
	// The app was armed to fire at 07:10 and then shut down. It restarts at
	// 09:00, nearly two hours late.
	start := time.Date(2026, 7, 14, 9, 0, 0, 0, time.Local)
	r := newRig(t, start)

	st := config.DefaultState(t.TempDir())
	st.Spec = schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute}
	st.Armed = true
	st.FireAt = time.Date(2026, 7, 14, 7, 10, 0, 0, time.Local)
	st.Target = time.Date(2026, 7, 14, 7, 30, 0, 0, time.Local)
	if err := r.store.Save(st); err != nil {
		t.Fatal(err)
	}

	if err := r.core.Restore(); err != nil {
		t.Fatal(err)
	}

	got := r.step(time.Second)

	if r.fake.CallCount() != 0 {
		t.Fatal("claude ran on restore of an alarm 1h50m stale; grace must apply to unobserved time")
	}
	if !hasStatus(got, StatusMissed) {
		t.Fatalf("want StatusMissed, got %v", got)
	}
}

// A restore inside the grace window does fire.
func TestCoreRestoreWithinGraceFires(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 13, 0, 0, time.Local) // 3m after fire
	r := newRig(t, start)

	st := config.DefaultState(t.TempDir())
	st.Spec = schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute}
	st.Armed = true
	st.FireAt = time.Date(2026, 7, 14, 7, 10, 0, 0, time.Local)
	st.Target = time.Date(2026, 7, 14, 7, 30, 0, 0, time.Local)
	if err := r.store.Save(st); err != nil {
		t.Fatal(err)
	}

	if err := r.core.Restore(); err != nil {
		t.Fatal(err)
	}
	r.step(time.Second)

	if r.fake.CallCount() != 1 {
		t.Fatalf("claude ran %d times, want 1: 3m late is inside a 5m grace", r.fake.CallCount())
	}
}

// A restore of a state that was never armed does nothing.
func TestCoreRestoreOfDisarmedStateDoesNothing(t *testing.T) {
	start := time.Date(2026, 7, 14, 9, 0, 0, 0, time.Local)
	r := newRig(t, start)

	if err := r.core.Restore(); err != nil {
		t.Fatal(err)
	}
	got := r.step(time.Second)

	if r.fake.CallCount() != 0 {
		t.Fatal("claude ran on restore of a disarmed state")
	}
	if s, ok := lastStatus(got); ok && s != StatusIdle {
		t.Fatalf("status = %v, want StatusIdle", s)
	}
}

func TestCoreRunnerFailureBecomesStatusError(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)
	r.fake.Err = errors.New("claude timed out after 2m0s")

	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})

	got := r.step(10 * time.Minute)

	if !hasStatus(got, StatusError) {
		t.Fatalf("want StatusError when the runner fails, got %v", got)
	}
	for _, e := range got {
		if e.Status == StatusError && e.Err == nil {
			t.Fatal("StatusError event carries no error")
		}
	}
}

func TestCoreDisarmStopsIt(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})
	r.core.Disarm()

	r.step(30 * time.Minute)

	if r.fake.CallCount() != 0 {
		t.Fatal("a disarmed alarm fired")
	}
	if st := r.store.MustLoad(); st.Armed {
		t.Fatal("Disarm did not persist")
	}
}

func TestCoreRunNowInvokesClaudeImmediately(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.core.RunNow()
	r.step(time.Second)

	if r.fake.CallCount() != 1 {
		t.Fatalf("RunNow ran claude %d times, want 1", r.fake.CallCount())
	}
}

func TestCoreArmRejectsAnInvalidState(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	bad := config.DefaultState(t.TempDir())
	bad.Spec.Hour = 99

	if err := r.core.Arm(bad); err == nil {
		t.Fatal("Arm accepted an invalid state")
	}
	if st := r.store.MustLoad(); st.Armed {
		t.Fatal("a rejected Arm must not persist as armed")
	}
}

// A time jump must cause the pending fire time to be recomputed from the Spec,
// not left as a stale absolute instant.
func TestCoreRecomputesFireTimeAfterATimeJump(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.armed(schedule.Spec{Hour: 23, Minute: 0, Offset: 0, Grace: 5 * time.Minute})

	// Jump backwards two hours -- an NTP correction. The alarm is still for
	// 23:00 today, so it must still be pending and must not have fired.
	r.clk.Set(start.Add(-2 * time.Hour))
	got := r.step(0)

	if r.fake.CallCount() != 0 {
		t.Fatal("claude ran after a backwards clock step")
	}
	if !hasStatus(got, StatusArmed) {
		t.Fatalf("want it still armed after a clock step, got %v", got)
	}
}
```

`MemStore` needs a test helper. Add it to `internal/config/store.go`:

```go
// MustLoad is Load without the error, for tests.
func (m *MemStore) MustLoad() State { return m.state }
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test -race ./internal/app/
```

Expected: FAIL — `undefined: New`, `undefined: Core`, `undefined: Status`.

- [ ] **Step 3: Write the implementation**

Create `internal/app/core.go`:

```go
// Package app orchestrates the alarm, the runner, and the store.
//
// It must never import Fyne. It emits Events; internal/ui is the only thing
// that renders them.
package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"claudealarm/internal/config"
	"claudealarm/internal/runner"
	"claudealarm/internal/schedule"
)

// Status is what the app is doing.
type Status int

const (
	StatusIdle    Status = iota // disarmed, nothing to report
	StatusArmed                 // counting down
	StatusRunning               // claude is running right now
	StatusDone                  // claude answered
	StatusMissed                // the fire time passed unobserved, beyond grace
	StatusError                 // claude failed
)

func (s Status) String() string {
	switch s {
	case StatusIdle:
		return "idle"
	case StatusArmed:
		return "armed"
	case StatusRunning:
		return "running"
	case StatusDone:
		return "done"
	case StatusMissed:
		return "missed"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// Event is one thing the UI needs to render.
type Event struct {
	Status Status
	Now    time.Time

	FireAt    time.Time
	Target    time.Time
	Remaining time.Duration // StatusArmed: until FireAt
	Late      time.Duration // StatusMissed: how far past FireAt we woke

	Result runner.Result // StatusDone
	Err    error         // StatusError
}

// Core wires the three seams together.
type Core struct {
	clk    schedule.Clock
	alarm  *schedule.Alarm
	runner runner.Runner
	store  config.Store

	out chan Event

	mu     sync.Mutex
	state  config.State
	status Status
}

// New returns a Core. Call Run in a goroutine, and run the Alarm too.
func New(clk schedule.Clock, al *schedule.Alarm, r runner.Runner, st config.Store) *Core {
	loaded, err := st.Load()
	if err != nil {
		loaded = config.DefaultState(".")
	}
	return &Core{
		clk:    clk,
		alarm:  al,
		runner: r,
		store:  st,
		out:    make(chan Event, 8),
		state:  loaded,
		status: StatusIdle,
	}
}

// Events is the stream the UI renders. Buffered, and Core never blocks on it:
// a slow UI drops ticks rather than stalling the alarm.
func (c *Core) Events() <-chan Event { return c.out }

// State returns the current persisted settings.
func (c *Core) State() config.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Arm schedules the alarm. This is an EXPLICIT user act.
//
// Per spec 5.3, the grace window is NOT consulted here. Grace answers "the app
// was not watching -- is this stale?", which is meaningless when the user is
// sitting in front of the app pressing the button. So if the computed fire time
// is already in the past but the target is still ahead (arming an 07:30 target
// with a 20-minute lead-in at 07:20), we fire immediately, however far past the
// fire time we are.
func (c *Core) Arm(st config.State) error {
	if err := st.Validate(); err != nil {
		return err
	}
	if err := st.ValidateWorkDir(); err != nil {
		return err
	}
	// Resolve claude now, at arm time, so a missing binary fails while the user
	// is looking at the app -- not at fire time, when nobody is.
	if _, err := runner.Lookup(); err != nil {
		return err
	}

	now := c.clk.Now()
	fire, target, err := st.Spec.FireAt(now)
	if err != nil {
		return err
	}

	st.Armed = true
	st.FireAt = fire
	st.Target = target

	c.mu.Lock()
	c.state = st
	c.mu.Unlock()

	if err := c.store.Save(st); err != nil {
		return fmt.Errorf("could not save settings: %w", err)
	}

	// The fire time is already gone, but the target is not. Fire now, ignoring
	// grace -- see the doc comment.
	if !now.Round(0).Before(fire.Round(0)) {
		c.alarm.Disarm()
		go c.fire(context.Background(), fire, target)
		return nil
	}

	c.alarm.Arm(fire, target, st.Spec.Grace)
	c.emit(Event{Status: StatusArmed, Now: now, FireAt: fire, Target: target, Remaining: fire.Sub(now)})
	return nil
}

// Restore re-arms a persisted alarm after a restart.
//
// Unlike Arm, this represents time the app was NOT watching, so the grace
// window DOES apply. An alarm that came due while the app was shut down and is
// now hours stale must be reported missed, not fired.
func (c *Core) Restore() error {
	st, err := c.store.Load()
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.state = st
	c.mu.Unlock()

	if !st.Armed || st.FireAt.IsZero() {
		c.setStatus(StatusIdle)
		c.emit(Event{Status: StatusIdle, Now: c.clk.Now()})
		return nil
	}

	now := c.clk.Now()

	switch schedule.Decide(now, st.FireAt, st.Spec.Grace) {
	case schedule.DecideMissed:
		c.disarmAndPersist()
		c.setStatus(StatusMissed)
		c.emit(Event{
			Status: StatusMissed, Now: now,
			FireAt: st.FireAt, Target: st.Target,
			Late: now.Round(0).Sub(st.FireAt.Round(0)),
		})

	case schedule.DecideFire:
		c.disarmAndPersist()
		go c.fire(context.Background(), st.FireAt, st.Target)

	case schedule.DecideWait:
		c.alarm.Arm(st.FireAt, st.Target, st.Spec.Grace)
		c.setStatus(StatusArmed)
		c.emit(Event{
			Status: StatusArmed, Now: now,
			FireAt: st.FireAt, Target: st.Target,
			Remaining: st.FireAt.Sub(now),
		})
	}
	return nil
}

// Disarm cancels a pending alarm.
func (c *Core) Disarm() {
	c.alarm.Disarm()
	c.disarmAndPersist()
	c.setStatus(StatusIdle)
	c.emit(Event{Status: StatusIdle, Now: c.clk.Now()})
}

// RunNow invokes claude immediately, ignoring the schedule entirely. This backs
// the "Run now" button offered in the MISSED state.
func (c *Core) RunNow() {
	go c.fire(context.Background(), time.Time{}, time.Time{})
}

// Run consumes the alarm's updates. It blocks until ctx is cancelled.
func (c *Core) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return

		case u := <-c.alarm.Updates():
			switch u.Kind {
			case schedule.EventJump:
				// The wall clock moved behind our back. Every derived fire time
				// is now suspect -- recompute it from the Spec, which is the
				// only thing that survives a suspend, an NTP step, a DST change,
				// or a timezone change.
				c.recomputeAfterJump(u.Now)

			case schedule.EventTick:
				if armed, _, _ := c.alarm.Armed(); armed {
					c.emit(Event{
						Status: StatusArmed, Now: u.Now,
						FireAt: u.FireAt, Target: u.Target, Remaining: u.Remaining,
					})
				} else if c.currentStatus() == StatusIdle {
					c.emit(Event{Status: StatusIdle, Now: u.Now})
				}

			case schedule.EventFire:
				c.disarmAndPersist()
				go c.fire(ctx, u.FireAt, u.Target)

			case schedule.EventMissed:
				c.disarmAndPersist()
				c.setStatus(StatusMissed)
				c.emit(Event{
					Status: StatusMissed, Now: u.Now,
					FireAt: u.FireAt, Target: u.Target, Late: u.Late,
				})
			}
		}
	}
}

// fire runs claude and reports the outcome.
func (c *Core) fire(ctx context.Context, fireAt, target time.Time) {
	st := c.State()

	bin, err := runner.Lookup()
	if err != nil {
		c.setStatus(StatusError)
		c.emit(Event{Status: StatusError, Now: c.clk.Now(), Err: err})
		return
	}

	c.setStatus(StatusRunning)
	c.emit(Event{Status: StatusRunning, Now: c.clk.Now(), FireAt: fireAt, Target: target})

	res, err := c.runner.Run(ctx, runner.Config{
		Bin:       bin,
		WorkDir:   st.WorkDir,
		Model:     st.Model,
		Prompt:    st.Prompt,
		BudgetUSD: runner.DefaultBudgetUSD,
		Timeout:   runner.DefaultTimeout,
	})
	if err != nil {
		c.setStatus(StatusError)
		c.emit(Event{Status: StatusError, Now: c.clk.Now(), FireAt: fireAt, Target: target, Err: err})
		return
	}

	c.setStatus(StatusDone)
	c.emit(Event{Status: StatusDone, Now: c.clk.Now(), FireAt: fireAt, Target: target, Result: res})
}

// recomputeAfterJump re-derives the fire time from the Spec after the wall clock
// moved. The Spec is the only durable truth; an absolute instant is not.
func (c *Core) recomputeAfterJump(now time.Time) {
	armed, _, _ := c.alarm.Armed()
	if !armed {
		return
	}

	st := c.State()
	fire, target, err := st.Spec.FireAt(now)
	if err != nil {
		return
	}

	st.FireAt, st.Target = fire, target
	c.mu.Lock()
	c.state = st
	c.mu.Unlock()
	_ = c.store.Save(st)

	c.alarm.Arm(fire, target, st.Spec.Grace)
}

func (c *Core) disarmAndPersist() {
	c.mu.Lock()
	c.state.Armed = false
	c.state.FireAt = time.Time{}
	c.state.Target = time.Time{}
	st := c.state
	c.mu.Unlock()
	_ = c.store.Save(st)
}

func (c *Core) setStatus(s Status) {
	c.mu.Lock()
	c.status = s
	c.mu.Unlock()
}

func (c *Core) currentStatus() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// emit never blocks. A slow UI drops a tick; it must never stall the alarm.
func (c *Core) emit(e Event) {
	select {
	case c.out <- e:
	default:
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test -race -cover ./internal/app/ -v
```

Expected: PASS, all twelve. The two that matter most:
- `TestCoreArmWithFireTimeAlreadyPastFiresImmediatelyIgnoringGrace`
- `TestCoreRestoreHonoursTheGraceWindow`

Together they pin spec §5.3: **grace governs unobserved time only.**

- [ ] **Step 5: Run everything so far**

```bash
go test -race -cover ./...
```

Expected: PASS. `schedule`, `runner`, `config`, and `app` all ≥ 80%.

- [ ] **Step 6: Commit**

```bash
git add internal/app/ internal/config/
git commit -m "feat(app): add the Core orchestrator

Enforces the central distinction from spec 5.3: Arm() is an explicit user
act and ignores the grace window (arming an 07:30 target with a 20-minute
lead-in at 07:20 fires immediately, though the fire time is twice the grace
window into the past), while Restore() represents unobserved time and
honours it (an alarm that came due while the app was shut down and is now
two hours stale reports MISSED and does not run).

On a detected time jump the fire time is recomputed from the Spec rather
than trusted as a stored instant, which is what makes the app self-healing
against suspend, NTP steps, DST, and timezone changes.

The whole arm -> fire -> run -> display path is covered with a fake clock,
a manual ticker, and a fake runner: no display, no network, no API spend."
```

---

### Task 10: `ui` — font, theme, and card

**Files:**
- Create: `internal/ui/fonts.go`, `internal/ui/theme.go`, `internal/ui/card.go`, `internal/ui/theme_test.go`
- Create: `assets/fonts/JetBrainsMono-Bold.ttf`, `assets/fonts/OFL.txt`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `var resMonoBold fyne.Resource`
  - `type Theme struct{}` implementing `fyne.Theme`; `func NewTheme() *Theme`
  - Custom size names: `SizeNameClock fyne.ThemeSizeName = "clock"`
  - Custom colour names: `ColorNameCard fyne.ThemeColorName = "card"`
  - `type Card struct{ widget.BaseWidget; Content fyne.CanvasObject }`; `func NewCard(fyne.CanvasObject) *Card`

- [ ] **Step 1: Vendor the font**

JetBrains Mono is SIL OFL 1.1 — free to embed and redistribute, provided the licence ships with it. Its digits are tabular, so the clock will not jitter as the seconds tick.

```bash
mkdir -p assets/fonts
curl -fsSL -o /tmp/jbmono.zip \
  https://github.com/JetBrains/JetBrainsMono/releases/download/v2.304/JetBrainsMono-2.304.zip
unzip -j -o /tmp/jbmono.zip 'fonts/ttf/JetBrainsMono-Bold.ttf' -d assets/fonts/
unzip -j -o /tmp/jbmono.zip 'OFL.txt' -d assets/fonts/
ls -la assets/fonts/
```

Expected: `JetBrainsMono-Bold.ttf` (~270 KB) and `OFL.txt`.

If the download fails, the theme falls back to Fyne's built-in monospace (see Step 3) — the app still works, it just looks less distinctive. Do not block on this.

- [ ] **Step 2: Write the failing test**

Create `internal/ui/theme_test.go`:

```go
package ui

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// A custom theme MUST fall through to the default for every name it does not
// override. A theme that returns zero values for unknown names renders the app
// as invisible text on an invisible background.
func TestThemeFallsThroughForUnhandledNames(t *testing.T) {
	th := NewTheme()

	got := th.Color(theme.ColorNamePrimary, theme.VariantDark)
	want := theme.DefaultTheme().Color(theme.ColorNamePrimary, theme.VariantDark)

	if got != want {
		t.Fatalf("Color(primary) = %v, want the default %v", got, want)
	}
}

func TestThemeOverridesBackgroundAndCard(t *testing.T) {
	th := NewTheme()

	bg := th.Color(theme.ColorNameBackground, theme.VariantDark)
	if bg == theme.DefaultTheme().Color(theme.ColorNameBackground, theme.VariantDark) {
		t.Fatal("background was not overridden")
	}
	if _, ok := bg.(color.NRGBA); !ok {
		t.Fatalf("background = %T, want color.NRGBA", bg)
	}

	card := th.Color(ColorNameCard, theme.VariantDark)
	if card == nil {
		t.Fatal("the card colour is not defined")
	}
	if card == bg {
		t.Fatal("the card must be visually distinct from the background")
	}
}

// The clock face is routed by TextStyle.Monospace. Symbol must be left alone --
// hijacking it breaks icon and emoji rendering.
func TestThemeFontRoutesMonospaceButNotSymbol(t *testing.T) {
	th := NewTheme()

	mono := th.Font(fyne.TextStyle{Monospace: true})
	if mono == nil {
		t.Fatal("Font(monospace) returned nil")
	}

	symbol := th.Font(fyne.TextStyle{Symbol: true})
	want := theme.DefaultTheme().Font(fyne.TextStyle{Symbol: true})
	if symbol != want {
		t.Fatal("Font(symbol) was hijacked; that breaks icon rendering")
	}

	regular := th.Font(fyne.TextStyle{})
	if regular != theme.DefaultTheme().Font(fyne.TextStyle{}) {
		t.Fatal("Font(regular) should fall through to the default")
	}
}

func TestThemeDefinesAClockSize(t *testing.T) {
	th := NewTheme()

	if got := th.Size(SizeNameClock); got < 48 {
		t.Fatalf("Size(clock) = %v, want something large enough to be a clock face", got)
	}
	// And unknown sizes fall through.
	if got, want := th.Size(theme.SizeNamePadding), theme.DefaultTheme().Size(theme.SizeNamePadding); got != want {
		t.Fatalf("Size(padding) = %v, want the default %v", got, want)
	}
}

func TestThemeRoundsCorners(t *testing.T) {
	th := NewTheme()

	if got := th.Size(theme.SizeNameInputRadius); got <= 0 {
		t.Fatalf("Size(inputRadius) = %v, want a positive radius", got)
	}
}
```

- [ ] **Step 3: Write `fonts.go` and `theme.go`**

`internal/ui/fonts.go`:

```go
package ui

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

// JetBrains Mono Bold, SIL OFL 1.1. See assets/fonts/OFL.txt.
//
// Chosen for tabular digits: every glyph is the same width, so the clock face
// does not shift sideways as the seconds tick over.
//
//go:embed ../../assets/fonts/JetBrainsMono-Bold.ttf
var monoBoldTTF []byte

// resMonoBold is the embedded clock font, or nil if it was not vendored -- in
// which case the theme falls back to Fyne's built-in monospace.
var resMonoBold = func() fyne.Resource {
	if len(monoBoldTTF) == 0 {
		return nil
	}
	return fyne.NewStaticResource("JetBrainsMono-Bold.ttf", monoBoldTTF)
}()
```

`internal/ui/theme.go`:

```go
package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Custom theme names. Fyne's name types are just strings, so a theme can define
// its own and look them up with theme.ColorForWidget / theme.SizeForWidget.
const (
	// ColorNameCard is the elevated panel behind the clock.
	ColorNameCard fyne.ThemeColorName = "card"
	// SizeNameClock is the point size of the clock face.
	SizeNameClock fyne.ThemeSizeName = "clock"
)

// The palette. Near-black rather than pure black, and an elevated card a few
// steps lighter -- pure black plus default grey is the look of an unstyled app.
var (
	colBackground = color.NRGBA{R: 0x12, G: 0x14, B: 0x18, A: 0xFF}
	colCard       = color.NRGBA{R: 0x1B, G: 0x1E, B: 0x25, A: 0xFF}
	colAccent     = color.NRGBA{R: 0x4C, G: 0x8D, B: 0xFF, A: 0xFF}
)

// Theme is the app's look.
//
// Every method MUST fall through to theme.DefaultTheme() for names it does not
// handle. A theme that returns a zero value for an unknown name renders the app
// as invisible text on an invisible background.
type Theme struct{}

// NewTheme returns the app theme.
func NewTheme() *Theme { return &Theme{} }

var _ fyne.Theme = (*Theme)(nil)

func (t *Theme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	switch n {
	case theme.ColorNameBackground:
		return colBackground
	case ColorNameCard:
		return colCard
	case theme.ColorNamePrimary:
		return colAccent
	}
	return theme.DefaultTheme().Color(n, v)
}

// Font routes the clock face by TextStyle.Monospace.
//
// Note we never touch TextStyle.Symbol: Fyne uses that to select the icon font,
// and hijacking it breaks icon and emoji rendering everywhere in the app.
func (t *Theme) Font(s fyne.TextStyle) fyne.Resource {
	if s.Monospace && resMonoBold != nil {
		return resMonoBold
	}
	return theme.DefaultTheme().Font(s)
}

func (t *Theme) Icon(n fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(n)
}

func (t *Theme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case SizeNameClock:
		return 88
	case theme.SizeNamePadding:
		return 8
	case theme.SizeNameCardRadius:
		return 12
	case theme.SizeNameInputRadius:
		return 8
	case theme.SizeNameSelectionRadius:
		return 8
	}
	return theme.DefaultTheme().Size(n)
}
```

- [ ] **Step 4: Write `card.go`**

A bare `canvas.Rectangle` samples its colour once and will not re-colour if the theme variant changes. A widget with a renderer re-reads the theme on refresh.

```go
package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Card is an elevated rounded panel.
//
// It is a widget rather than a bare canvas.Rectangle because a Rectangle
// samples its FillColor once, at construction, and will not re-colour when the
// theme variant changes. A renderer re-reads the theme on every Refresh.
type Card struct {
	widget.BaseWidget
	Content fyne.CanvasObject
}

// NewCard wraps content in an elevated panel.
func NewCard(content fyne.CanvasObject) *Card {
	c := &Card{Content: content}
	c.ExtendBaseWidget(c)
	return c
}

func (c *Card) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(theme.ColorForWidget(ColorNameCard, c))
	bg.CornerRadius = theme.SizeForWidget(theme.SizeNameCardRadius, c)

	return &cardRenderer{card: c, bg: bg}
}

type cardRenderer struct {
	card *Card
	bg   *canvas.Rectangle
}

func (r *cardRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)

	pad := theme.SizeForWidget(theme.SizeNamePadding, r.card) * 2
	r.card.Content.Move(fyne.NewPos(pad, pad))
	r.card.Content.Resize(fyne.NewSize(size.Width-2*pad, size.Height-2*pad))
}

func (r *cardRenderer) MinSize() fyne.Size {
	pad := theme.SizeForWidget(theme.SizeNamePadding, r.card) * 2
	m := r.card.Content.MinSize()
	return fyne.NewSize(m.Width+2*pad, m.Height+2*pad)
}

func (r *cardRenderer) Refresh() {
	// Re-read the theme, so a light/dark switch actually re-colours the card.
	r.bg.FillColor = theme.ColorForWidget(ColorNameCard, r.card)
	r.bg.CornerRadius = theme.SizeForWidget(theme.SizeNameCardRadius, r.card)
	r.bg.Refresh()
	canvas.Refresh(r.card)
}

func (r *cardRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.card.Content}
}

func (r *cardRenderer) Destroy() {}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test -race ./internal/ui/ -v
```

Expected: PASS, all five.

If `fonts.go` fails to compile with "pattern ... no matching files", the font was not vendored. Either re-run Step 1, or temporarily replace the `//go:embed` directive with `var monoBoldTTF []byte` and no directive — the theme handles a nil font.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/ assets/fonts/
git commit -m "feat(ui): add theme, embedded clock font, and a theme-aware card

JetBrains Mono Bold (SIL OFL 1.1) for the clock face: tabular digits, so it
does not jitter sideways as the seconds tick. Routed by TextStyle.Monospace;
TextStyle.Symbol is deliberately left alone, because Fyne uses it to select
the icon font and hijacking it breaks icon rendering.

Every theme method falls through to DefaultTheme for names it does not
handle -- a theme that returns zero values for unknown names renders the app
as invisible text on an invisible background.

Card is a widget rather than a canvas.Rectangle so it re-reads the theme on
refresh and actually re-colours on a light/dark switch."
```

---

### Task 11: `ui` — the window

**Files:**
- Create: `internal/ui/window.go`, `internal/ui/window_test.go`

**Interfaces:**
- Consumes: `NewCard`, `SizeNameClock` (Task 10); `app.{Core, Event, Status}` (Task 9); `config.State` (Task 8); `schedule.ParseHHMM` (Task 3).
- Produces:
  - `type Window struct{...}`
  - `func NewWindow(core *app.Core) *Window`
  - `(*Window).Content() fyne.CanvasObject`
  - `(*Window).Apply(app.Event)` — **must be called on the Fyne goroutine only**
  - `(*Window).SetClock(time.Time)`

- [ ] **Step 1: Write the failing test**

Create `internal/ui/window_test.go`:

```go
package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"claudealarm/internal/app"
	"claudealarm/internal/config"
	"claudealarm/internal/runner"
	"claudealarm/internal/schedule"
)

func newTestWindow(t *testing.T) *Window {
	t.Helper()
	test.NewApp()

	clk := schedule.NewTestClock(time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local))
	mt := schedule.NewManualTicker()
	al := schedule.NewAlarm(clk, time.Second, func(time.Duration) schedule.Ticker { return mt })
	core := app.New(clk, al, &runner.Fake{}, config.NewMemStore(config.DefaultState(t.TempDir())))

	return NewWindow(core)
}

func TestWindowRendersAClock(t *testing.T) {
	w := newTestWindow(t)

	w.SetClock(time.Date(2026, 7, 14, 7, 4, 5, 0, time.Local))

	if got := w.clock.Text; got != "07:04:05" {
		t.Fatalf("clock = %q, want 07:04:05", got)
	}
}

func TestWindowArmedStateShowsCountdown(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{
		Status:    app.StatusArmed,
		Now:       time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local),
		FireAt:    time.Date(2026, 7, 14, 7, 10, 0, 0, time.Local),
		Target:    time.Date(2026, 7, 14, 7, 30, 0, 0, time.Local),
		Remaining: 10 * time.Minute,
	})

	if !strings.Contains(w.status.Text, "10m") {
		t.Fatalf("status = %q, want it to show the countdown", w.status.Text)
	}
	if w.armBtn.Text != "Disarm" {
		t.Fatalf("arm button = %q, want Disarm while armed", w.armBtn.Text)
	}
}

func TestWindowDoneStateShowsTheAnswer(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{
		Status: app.StatusDone,
		Now:    time.Date(2026, 7, 14, 7, 10, 4, 0, time.Local),
		Result: runner.Result{
			Text:     "Hello! How can I help you today?",
			CostUSD:  0.0044,
			Duration: 3312 * time.Millisecond,
		},
	})

	if !strings.Contains(w.result.Text, "How can I help you") {
		t.Fatalf("result = %q, want the model's answer", w.result.Text)
	}
	if !strings.Contains(w.status.Text, "$0.0044") {
		t.Fatalf("status = %q, want the cost", w.status.Text)
	}
	if w.armBtn.Text != "Arm" {
		t.Fatalf("arm button = %q, want Arm after firing", w.armBtn.Text)
	}
}

func TestWindowMissedStateShowsHowLateAndOffersRunNow(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{
		Status: app.StatusMissed,
		Now:    time.Date(2026, 7, 14, 11, 20, 0, 0, time.Local),
		FireAt: time.Date(2026, 7, 14, 8, 45, 0, 0, time.Local),
		Late:   2*time.Hour + 35*time.Minute,
	})

	if !strings.Contains(strings.ToUpper(w.status.Text), "MISSED") {
		t.Fatalf("status = %q, want it to say MISSED", w.status.Text)
	}
	if !strings.Contains(w.status.Text, "2h35m") {
		t.Fatalf("status = %q, want it to say how late", w.status.Text)
	}
	if w.runNowBtn.Hidden {
		t.Fatal("the Run now button must be visible in the MISSED state")
	}
}

func TestWindowErrorStateShowsTheError(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{
		Status: app.StatusError,
		Now:    time.Date(2026, 7, 14, 7, 12, 0, 0, time.Local),
		Err:    errors.New("claude timed out after 2m0s"),
	})

	if !strings.Contains(w.status.Text, "timed out") {
		t.Fatalf("status = %q, want the error surfaced", w.status.Text)
	}
}

// Run now is only for the MISSED state; it must not be visible otherwise.
func TestWindowRunNowIsHiddenUnlessMissed(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{Status: app.StatusIdle, Now: time.Now()})

	if !w.runNowBtn.Hidden {
		t.Fatal("the Run now button must be hidden when not missed")
	}
}

func TestWindowTimeEntryValidatesHHMM(t *testing.T) {
	w := newTestWindow(t)

	if err := w.timeEntry.Validate(); err != nil {
		t.Fatalf("the default time must be valid: %v", err)
	}

	w.timeEntry.SetText("25:99")
	if err := w.timeEntry.Validate(); err == nil {
		t.Fatal("25:99 must not validate")
	}

	w.timeEntry.SetText("07:30")
	if err := w.timeEntry.Validate(); err != nil {
		t.Fatalf("07:30 must validate: %v", err)
	}
}

func TestWindowOffsetEntryRejectsNonNumeric(t *testing.T) {
	w := newTestWindow(t)

	w.offsetEntry.SetText("abc")
	if err := w.offsetEntry.Validate(); err == nil {
		t.Fatal("a non-numeric offset must not validate")
	}

	w.offsetEntry.SetText("20")
	if err := w.offsetEntry.Validate(); err != nil {
		t.Fatalf("20 must validate: %v", err)
	}
}

// stateFromForm is what Arm sends to the Core; it must reflect the widgets.
func TestWindowStateFromForm(t *testing.T) {
	w := newTestWindow(t)

	w.timeEntry.SetText("08:45")
	w.offsetEntry.SetText("15")

	st, err := w.stateFromForm()
	if err != nil {
		t.Fatal(err)
	}

	if st.Spec.Hour != 8 || st.Spec.Minute != 45 {
		t.Fatalf("spec = %d:%d, want 8:45", st.Spec.Hour, st.Spec.Minute)
	}
	if st.Spec.Offset != 15*time.Minute {
		t.Fatalf("offset = %v, want 15m", st.Spec.Offset)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test -race ./internal/ui/ -run TestWindow
```

Expected: FAIL — `undefined: NewWindow`.

- [ ] **Step 3: Write the implementation**

Create `internal/ui/window.go`:

```go
package ui

import (
	"fmt"
	"strconv"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/validation"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"claudealarm/internal/app"
	"claudealarm/internal/config"
	"claudealarm/internal/schedule"
)

// Window is the main view.
//
// Every method that touches a widget must run on the Fyne goroutine. Apply is
// called only from bridge.go, inside fyne.Do.
type Window struct {
	core *app.Core
	win  fyne.Window // set by Attach; nil in tests

	clock  *canvas.Text
	status *widget.Label
	result *widget.Label

	timeEntry   *widget.Entry
	offsetEntry *widget.Entry
	workDirEnt  *widget.Entry
	modelEntry  *widget.Entry
	promptEntry *widget.Entry

	armBtn    *widget.Button
	runNowBtn *widget.Button

	content fyne.CanvasObject
}

// NewWindow builds the view. Call Content() for the object to put in a window.
func NewWindow(core *app.Core) *Window {
	w := &Window{core: core}
	st := core.State()

	w.clock = canvas.NewText("--:--:--", theme.Color(theme.ColorNameForeground))
	w.clock.TextSize = theme.Size(SizeNameClock)
	w.clock.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	w.clock.Alignment = fyne.TextAlignCenter

	w.status = widget.NewLabel("Idle")
	w.status.Alignment = fyne.TextAlignCenter
	w.status.Wrapping = fyne.TextWrapWord

	w.result = widget.NewLabel("")
	w.result.Wrapping = fyne.TextWrapWord

	w.timeEntry = widget.NewEntry()
	w.timeEntry.SetPlaceHolder("HH:MM")
	w.timeEntry.SetText(fmt.Sprintf("%02d:%02d", st.Spec.Hour, st.Spec.Minute))
	// Fyne has no time picker, and neither does fyne-x. An alarm is a typing
	// interaction anyway: you type 07:30 and press enter.
	w.timeEntry.Validator = validation.NewTime("15:04")

	w.offsetEntry = widget.NewEntry()
	w.offsetEntry.SetPlaceHolder("minutes")
	w.offsetEntry.SetText(strconv.Itoa(int(st.Spec.Offset / time.Minute)))
	w.offsetEntry.Validator = minutesValidator

	w.workDirEnt = widget.NewEntry()
	w.workDirEnt.SetText(st.WorkDir)

	w.modelEntry = widget.NewEntry()
	w.modelEntry.SetText(st.Model)

	w.promptEntry = widget.NewEntry()
	w.promptEntry.SetText(st.Prompt)

	w.armBtn = widget.NewButton("Arm", w.onArm)
	w.armBtn.Importance = widget.HighImportance

	w.runNowBtn = widget.NewButton("Run now", func() { w.core.RunNow() })
	w.runNowBtn.Hide() // only shown in the MISSED state

	w.content = w.build()
	return w
}

// Attach gives the Window a real fyne.Window, so it can raise dialogs. Not set
// in tests.
func (w *Window) Attach(win fyne.Window) { w.win = win }

// Content is the root object.
func (w *Window) Content() fyne.CanvasObject { return w.content }

func (w *Window) build() fyne.CanvasObject {
	clockCard := NewCard(container.NewVBox(
		layoutCentre(w.clock),
		w.status,
	))

	form := widget.NewForm(
		widget.NewFormItem("Target time", w.timeEntry),
		widget.NewFormItem("Lead-in (min)", w.offsetEntry),
	)

	browse := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), w.onBrowse)
	advanced := widget.NewAccordion(
		widget.NewAccordionItem("Advanced", widget.NewForm(
			widget.NewFormItem("Working dir", container.NewBorder(nil, nil, nil, browse, w.workDirEnt)),
			widget.NewFormItem("Model", w.modelEntry),
			widget.NewFormItem("Prompt", w.promptEntry),
		)),
	)

	buttons := container.NewGridWithColumns(2, w.armBtn, w.runNowBtn)

	resultCard := NewCard(container.NewVScroll(w.result))
	resultCard.Resize(fyne.NewSize(0, 140))

	return container.NewPadded(container.NewVBox(
		clockCard,
		form,
		buttons,
		advanced,
		resultCard,
	))
}

func layoutCentre(o fyne.CanvasObject) fyne.CanvasObject {
	return container.NewCenter(o)
}

// SetClock updates the clock face. Fyne goroutine only.
func (w *Window) SetClock(t time.Time) {
	w.clock.Text = t.Format("15:04:05")
	w.clock.Refresh()
}

// Apply renders one Event. Fyne goroutine only -- bridge.go wraps every call in
// fyne.Do.
func (w *Window) Apply(e app.Event) {
	if !e.Now.IsZero() {
		w.SetClock(e.Now)
	}

	w.runNowBtn.Hide()

	switch e.Status {
	case app.StatusIdle:
		w.status.SetText("Idle")
		w.setArmButton("Arm", widget.HighImportance)

	case app.StatusArmed:
		w.status.SetText(fmt.Sprintf("Armed · fires in %s (at %s, for a %s target)",
			roundDur(e.Remaining), e.FireAt.Format("15:04:05"), e.Target.Format("15:04")))
		w.setArmButton("Disarm", widget.DangerImportance)

	case app.StatusRunning:
		w.status.SetText("Running Claude Code…")
		w.setArmButton("Disarm", widget.DangerImportance)

	case app.StatusDone:
		w.status.SetText(fmt.Sprintf("Done · %s · $%.4f",
			roundDur(e.Result.Duration), e.Result.CostUSD))
		w.result.SetText(e.Result.Text)
		w.setArmButton("Arm", widget.HighImportance)

	case app.StatusMissed:
		w.status.SetText(fmt.Sprintf("MISSED · the alarm was due at %s, %s ago. Claude Code was not run.",
			e.FireAt.Format("15:04:05"), roundDur(e.Late)))
		w.setArmButton("Arm", widget.HighImportance)
		w.runNowBtn.Show()

	case app.StatusError:
		w.status.SetText("Error · " + e.Err.Error())
		w.setArmButton("Arm", widget.HighImportance)
	}
}

func (w *Window) setArmButton(label string, imp widget.Importance) {
	w.armBtn.Text = label
	w.armBtn.Importance = imp
	w.armBtn.Refresh()
}

func (w *Window) onArm() {
	if w.armBtn.Text == "Disarm" {
		w.core.Disarm()
		return
	}

	st, err := w.stateFromForm()
	if err != nil {
		w.fail(err)
		return
	}
	if err := w.core.Arm(st); err != nil {
		w.fail(err)
	}
}

func (w *Window) onBrowse() {
	if w.win == nil {
		return
	}
	dialog.ShowFolderOpen(func(list fyne.ListableURI, err error) {
		if err != nil || list == nil {
			return
		}
		w.workDirEnt.SetText(list.Path())
	}, w.win)
}

// stateFromForm reads the widgets into a config.State.
func (w *Window) stateFromForm() (config.State, error) {
	st := w.core.State()

	hour, minute, err := schedule.ParseHHMM(w.timeEntry.Text)
	if err != nil {
		return config.State{}, err
	}

	mins, err := strconv.Atoi(w.offsetEntry.Text)
	if err != nil {
		return config.State{}, fmt.Errorf("lead-in must be a whole number of minutes")
	}
	if mins < 0 {
		return config.State{}, fmt.Errorf("lead-in cannot be negative")
	}

	st.Spec.Hour = hour
	st.Spec.Minute = minute
	st.Spec.Offset = time.Duration(mins) * time.Minute
	st.WorkDir = w.workDirEnt.Text
	st.Model = w.modelEntry.Text
	st.Prompt = w.promptEntry.Text

	return st, nil
}

func (w *Window) fail(err error) {
	w.status.SetText("Error · " + err.Error())
	if w.win != nil {
		dialog.ShowError(err, w.win)
	}
}

func minutesValidator(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("must be a whole number of minutes")
	}
	if n < 0 {
		return fmt.Errorf("cannot be negative")
	}
	if n >= 24*60 {
		return fmt.Errorf("must be less than 24 hours")
	}
	return nil
}

// roundDur trims a duration to something a human wants to read.
func roundDur(d time.Duration) time.Duration {
	switch {
	case d >= time.Hour:
		return d.Round(time.Minute)
	case d >= time.Minute:
		return d.Round(time.Second)
	default:
		return d.Round(10 * time.Millisecond)
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test -race ./internal/ui/ -v -run TestWindow
```

Expected: PASS, all nine.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/
git commit -m "feat(ui): add the main window

Large monospace clock face on an elevated card, a two-field form (target
time, lead-in), an Arm button that turns Danger-red while armed, and an
Advanced accordion holding the working dir, model, and prompt so the default
view stays clean without anything being hardcoded.

Fyne has no time picker and neither does fyne-x, so the target time is a
widget.Entry with validation.NewTime(\"15:04\") -- which is the right
interaction for an alarm anyway: type 07:30, press enter.

The Run now button appears only in the MISSED state."
```

---

### Task 12: `ui` — the tray and the goroutine bridge

`bridge.go` is the **only** place in the codebase that calls `fyne.Do`. Everything crossing from the Core's goroutine to a widget goes through it.

**Files:**
- Create: `internal/ui/tray.go`, `internal/ui/bridge.go`, `internal/ui/tray_test.go`

**Interfaces:**
- Consumes: `Window` (Task 11); `app.{Core, Event, Status}` (Task 9).
- Produces:
  - `func InstallTray(a fyne.App, w fyne.Window, core *app.Core, icon fyne.Resource) bool` — reports whether a tray was installed
  - `func KeepAliveOnClose(w fyne.Window)`
  - `func Bridge(ctx context.Context, core *app.Core, win *Window)` — blocks; run in a goroutine

- [ ] **Step 1: Write the failing test**

Create `internal/ui/tray_test.go`:

```go
package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

// test.NewApp() does NOT implement desktop.App. That is exactly why InstallTray
// must guard the type assertion -- and this test proves the guard holds, so
// tray.go is safe to compile into a headless test binary.
func TestInstallTrayIsANoOpWithoutADesktopApp(t *testing.T) {
	a := test.NewApp()
	w := test.NewWindow(nil)
	defer w.Close() // a test window; the real one is never Close()d

	installed := InstallTray(a, w, nil, nil)

	if installed {
		t.Fatal("InstallTray reported success on a non-desktop App")
	}
}

// fakeWindow records what KeepAliveOnClose does to a window.
//
// KeepAliveOnClose takes the narrow closableWindow interface rather than
// fyne.Window precisely so this is possible: fyne.Window is a large interface
// with no way to read the close intercept back, which would leave the single
// most load-bearing line in the app untestable.
type fakeWindow struct {
	intercept func()
	hidden    bool
	closed    bool
}

func (f *fakeWindow) SetCloseIntercept(fn func()) { f.intercept = fn }
func (f *fakeWindow) Hide()                       { f.hidden = true }
func (f *fakeWindow) Close()                      { f.closed = true }

// The close intercept is the single thing keeping the process alive when the
// user clicks X: Fyne's destroyWindow() quits the app when the last window
// closes, with NO system-tray exception. This test invokes the registered
// intercept and asserts it hides rather than closes.
func TestKeepAliveOnCloseHidesRatherThanClosing(t *testing.T) {
	f := &fakeWindow{}

	KeepAliveOnClose(f)

	if f.intercept == nil {
		t.Fatal("KeepAliveOnClose registered no close intercept; clicking X would kill the app")
	}

	f.intercept() // the user clicks X

	if !f.hidden {
		t.Fatal("the close intercept must Hide the window")
	}
	if f.closed {
		t.Fatal("the close intercept must NOT Close the window: Close destroys it, and Fyne quits when the last window is destroyed")
	}
}

// fyne.Window must still satisfy the narrow interface, or main.go will not
// compile.
func TestFyneWindowSatisfiesClosableWindow(t *testing.T) {
	test.NewApp()
	w := test.NewWindow(nil)
	defer w.Close()

	var _ closableWindow = w
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test -race ./internal/ui/ -run TestInstallTray
```

Expected: FAIL — `undefined: InstallTray`.

- [ ] **Step 3: Write `tray.go`**

```go
package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"

	"claudealarm/internal/app"
)

// InstallTray registers the system tray icon and menu. It reports whether a
// tray was actually installed.
//
// The desktop.App assertion is not optional: Fyne's test app does not implement
// it, and neither does the mobile driver. Without the guard, this file could not
// be compiled into a test binary.
//
// Linux caveat: fyne.io/systray speaks StatusNotifierItem over D-Bus. If no SNI
// host is running, registration SILENTLY no-ops -- no error, no panic, just an
// invisible icon. Verified present on the target machine (KDE Plasma owns
// org.kde.StatusNotifierWatcher), which is why there is no fallback path here.
// On GNOME this would need the AppIndicator extension.
func InstallTray(a fyne.App, w fyne.Window, core *app.Core, icon fyne.Resource) bool {
	desk, ok := a.(desktop.App)
	if !ok {
		return false
	}

	quit := fyne.NewMenuItem("Quit", func() { a.Quit() })
	// Without IsQuit, Fyne appends its OWN Quit item, which calls App.Quit()
	// directly and skips anything we wanted to do first.
	quit.IsQuit = true

	menu := fyne.NewMenu("Claude Alarm",
		fyne.NewMenuItem("Show", func() {
			w.Show()
			w.RequestFocus()
		}),
		fyne.NewMenuItem("Disarm", func() {
			if core != nil {
				core.Disarm()
			}
		}),
		fyne.NewMenuItemSeparator(),
		quit,
	)

	desk.SetSystemTrayMenu(menu)
	if icon != nil {
		// Must be a plain full-colour PNG. A theme.ThemedResource routes to
		// SetTemplateIcon, which is a macOS-only path and useless on Linux.
		desk.SetSystemTrayIcon(icon)
	}
	desk.SetSystemTrayWindow(w)

	return true
}

// closableWindow is the slice of fyne.Window that KeepAliveOnClose needs.
//
// Narrowed deliberately: fyne.Window is a large interface with no way to read
// the close intercept back, so taking it whole would leave the single most
// load-bearing line in the app impossible to test. fyne.Window satisfies this.
type closableWindow interface {
	SetCloseIntercept(func())
	Hide()
	Close()
}

// KeepAliveOnClose makes the window's close button hide it instead of
// destroying it.
//
// This single line is what keeps the process -- and therefore the alarm --
// alive. Fyne's destroyWindow() ends with `if len(d.windows) == 0 { d.Quit() }`
// and has NO system-tray exception, so without this, clicking X kills the app
// even with a tray icon registered.
//
// Corollary: never call w.Close() anywhere in app code. Close bypasses the
// intercept entirely.
func KeepAliveOnClose(w closableWindow) {
	w.SetCloseIntercept(func() { w.Hide() })
}
```

- [ ] **Step 4: Write `bridge.go`**

```go
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
			e := e // capture for the closure
			fyne.Do(func() { win.Apply(e) })
		}
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test -race ./internal/ui/ -v
```

Expected: PASS, everything in the package.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/
git commit -m "feat(ui): add the system tray and the goroutine bridge

KeepAliveOnClose is the single line keeping the process alive: Fyne's
destroyWindow() quits the app when the last window closes and has no
system-tray exception, so without the intercept, clicking X kills the alarm.

bridge.go is the only caller of fyne.Do in the codebase, and the only
crossing point between the Core's goroutine and any widget. It uses Do, not
DoAndWait -- blocking there would stall the alarm, and DoAndWait from the
main goroutine deadlocks.

InstallTray guards the desktop.App assertion, which is what makes tray.go
safe to compile into a headless test binary: Fyne's test app does not
implement desktop.App."
```

---

### Task 13: `cmd` — wiring and preflight

Replaces the Task 1 spike.

**Files:**
- Modify: `cmd/alarmclock/main.go` (full rewrite)
- Create: `internal/ui/icon.go`

**Interfaces:**
- Consumes: everything.
- Produces: the binary.

- [ ] **Step 1: Embed the icon**

Create `internal/ui/icon.go`:

```go
package ui

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

// The tray and window icon. A plain full-colour PNG: a ThemedResource would
// route to SetTemplateIcon, which is macOS-only and useless on Linux.
//
// Regenerate with: go run ./tools/genicon
//
//go:embed ../../assets/icon.png
var iconPNG []byte

// Icon is the app icon.
var Icon fyne.Resource = fyne.NewStaticResource("icon.png", iconPNG)
```

- [ ] **Step 2: Write main.go**

```go
// Command alarmclock schedules a Claude Code run for a chosen time.
//
// The user enters a target time. The app fires at target minus a configurable
// lead-in, running the Claude Code CLI headlessly in a chosen directory. It
// lives in the system tray and keeps running when the window is closed.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"

	"claudealarm/internal/app"
	"claudealarm/internal/config"
	"claudealarm/internal/runner"
	"claudealarm/internal/schedule"
	"claudealarm/internal/ui"
)

// tickPeriod is how often we compare the wall clock against the fire time.
//
// One second costs about 0.007% of a core (measured) and bounds post-resume
// lateness to a second, while giving the clock face a free tick.
const tickPeriod = time.Second

func main() {
	hidden := flag.Bool("hidden", false, "start minimised to the tray")
	flag.Parse()

	a := fyneapp.NewWithID("gr.polaris.claudealarm") // the ID is required for Preferences()
	a.SetIcon(ui.Icon)
	a.Settings().SetTheme(ui.NewTheme())

	store := config.NewPrefsStore(a.Preferences())
	clk := schedule.NewRealClock()
	alarm := schedule.NewAlarm(clk, tickPeriod, schedule.RealTicker)
	core := app.New(clk, alarm, runner.NewCLI(), store)

	win := ui.NewWindow(core)

	w := a.NewWindow("Claude Alarm Clock")
	w.SetContent(win.Content())
	w.Resize(fyne.NewSize(460, 620))
	win.Attach(w)

	// THE line that keeps the process alive when the user clicks X.
	ui.KeepAliveOnClose(w)

	if !ui.InstallTray(a, w, core, ui.Icon) {
		// No tray means the window is the only way back in, so never start
		// hidden -- the user would have no way to reach the app at all.
		log.Println("warning: no system tray available; the window is the only way to reach the app")
		*hidden = false
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go alarm.Run(ctx)
	go core.Run(ctx)
	go ui.Bridge(ctx, core, win)

	// Preflight: fail loudly now, while someone is looking, rather than at 07:10
	// when nobody is.
	if err := preflight(core); err != nil {
		log.Printf("preflight: %v", err)
	}

	// Re-arm anything that survived a restart. Restore honours the grace window,
	// because a shut-down app is unobserved time.
	if err := core.Restore(); err != nil {
		log.Printf("restore: %v", err)
	}

	a.Lifecycle().SetOnStopped(cancel)

	if *hidden {
		// NewWindow already registered the window, so the app stays alive
		// without ever showing it.
		a.Run()
	} else {
		w.ShowAndRun()
	}
}

// preflight checks, at startup, the things that would otherwise only fail at
// fire time: that claude exists, and that the working directory is real.
func preflight(core *app.Core) error {
	bin, err := runner.Lookup()
	if err != nil {
		return err
	}

	st := core.State()
	if err := st.ValidateWorkDir(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "claude: %s\nworkdir: %s\ndefaults: %s\n",
		bin, st.WorkDir, runner.DefaultsSummary())
	return nil
}
```

- [ ] **Step 3: Build it**

```bash
CGO_ENABLED=1 go build -o alarmclock ./cmd/alarmclock
./alarmclock --help
```

Expected: it builds, and `--help` shows the `-hidden` flag.

- [ ] **Step 4: Run the whole test suite**

```bash
go vet ./...
go test -race -cover ./... 2>&1 | tee /tmp/test.log
grep -c 'Error in Fyne call thread' /tmp/test.log || true
```

Expected: `go vet` clean. All packages PASS, each ≥ 80%. The grep must find **zero** occurrences — any hit is a real data race between the Core's goroutine and a widget.

- [ ] **Step 5: Smoke test it for real**

```bash
./alarmclock
```

Set a target time two minutes from now with a **1-minute lead-in**, so the alarm fires in about a minute. Arm it. Close the window. Confirm from another terminal that it is still alive:

```bash
pgrep -af alarmclock
```

Wait. Claude Code should run and the response should appear (reopen the window from the tray). This is the first end-to-end run and it spends about $0.005.

- [ ] **Step 6: Commit**

```bash
git add cmd/ internal/ui/icon.go
git commit -m "feat(cmd): wire the app together

Preflight resolves the claude binary and stats the working directory at
startup, while someone is looking -- not at fire time, when nobody is.

Restore() re-arms anything that survived a restart, honouring the grace
window because a shut-down app is unobserved time.

If no system tray is available the -hidden flag is forced off: without a
tray the window is the only way to reach the app, so starting hidden would
lock the user out entirely."
```

---

### Task 14: Documentation and manual verification

**Files:**
- Create: `README.md`, `MANUAL-TESTS.md`

- [ ] **Step 1: Write the README**

````markdown
# Claude Alarm Clock

Set a time. At `target − lead-in`, it runs Claude Code headlessly in a directory
you choose, sends a prompt, and shows you the answer.

Lives in the system tray and keeps running when you close the window.

## Build

Needs Go 1.26+, `CGO_ENABLED=1` (Fyne uses OpenGL via cgo), and the `claude` CLI
on your PATH.

On Arch:

```bash
sudo pacman -S --needed go libxcursor libxrandr libxinerama libxi libgl mesa
go build -o alarmclock ./cmd/alarmclock
```

## Run

```bash
./alarmclock            # show the window
./alarmclock -hidden    # start minimised to the tray
```

## What it runs

```
claude -p --model haiku --output-format json --safe-mode --tools "" \
       --permission-mode dontAsk --max-budget-usd 0.10 \
       --no-session-persistence "Hello world"
```

in your chosen working directory. About 3 seconds and $0.005 per run.

`--safe-mode` matters: without it the CLI inherits your global `CLAUDE.md`,
skills, and plugins, which measured 5.7× the cost, 5× the latency, and gave a
different answer.

The model, prompt, and working directory are all editable under **Advanced**.

## Behaviour

**One-shot.** After it fires, it disarms and stays in the tray. It never
silently reschedules itself.

**Missed alarms.** If your machine was asleep or the app was shut down when the
alarm came due, it fires anyway *if* it is less than the grace window late
(default 5 minutes). Beyond that it shows `MISSED` and does **not** run Claude —
an alarm that fires three hours stale is worse than useless. You get a **Run
now** button if you want it anyway.

The grace window governs *unobserved* time only. If you deliberately arm an
07:30 target with a 20-minute lead-in at 07:20, it fires immediately, however
far past the lead-in you are: you asked for it, and you are watching.

## Known limitation

**It cannot wake a sleeping machine.** It polls the wall clock, so if the laptop
is suspended at the fire time, the alarm is evaluated on resume (subject to the
grace window above), not at the fire time. Waking the machine would need
`rtcwake` or a systemd timer with elevated privileges, which is out of scope.

## Why wall-clock polling and not a timer

Go's `time.Timer` is backed by `CLOCK_MONOTONIC`, which does not advance while a
Linux machine is suspended. Measured on the development machine:
`CLOCK_MONOTONIC` 55h12m against `CLOCK_BOOTTIME` 67h42m — **12h30m of suspend
time invisible to timers.** A `time.After(8 * time.Hour)` armed before those
suspends would have fired 12.5 hours late.

So the alarm compares wall clock to wall clock on a 1-second tick. It costs about
0.007% of a core.

## Tray support

The tray uses the StatusNotifierItem D-Bus protocol. It works out of the box on
KDE Plasma, XFCE 4.16+, and waybar (with the `tray` module).

**On GNOME it needs the AppIndicator extension**, and on polybar/i3bar it needs
`snixembed`. Without an SNI host the icon silently does not appear — no error, no
crash. If that happens, do not use `-hidden`; the window is your only way in.

## Tests

```bash
go test -race -cover ./...
```

Everything except the window layout runs headlessly, with no network and no API
spend: the clock, the ticker, and the `claude` invocation are all behind
interfaces. The Claude exec path is tested against shell-script stand-ins in
`internal/runner/testdata/`.

## Licence

The clock face is JetBrains Mono, SIL OFL 1.1 — see `assets/fonts/OFL.txt`.
````

- [ ] **Step 2: Write MANUAL-TESTS.md**

````markdown
# Manual tests

These need a real display, a real desktop environment, or the real `claude` CLI,
so they cannot run in `go test`. Work through them before calling a release done.

Environment this was verified on: **KDE Plasma 6, Wayland, Arch Linux.**

## 1. The tray icon actually appears

```bash
./alarmclock
```

- [ ] A blue clock icon appears in the system tray.

**This failure is silent.** `fyne.io/systray` registers a StatusNotifierItem over
D-Bus; with no SNI host it logs one line and no-ops. Confirm a host exists:

```bash
busctl --user list | grep -i StatusNotifier
```

Expected: both `org.kde.StatusNotifierWatcher` and an
`org.kde.StatusNotifierHost-*`.

## 2. Closing the window does not kill the process

- [ ] Click the window's X. The window disappears.
- [ ] `pgrep -af alarmclock` still shows it running.

If the process dies here, `SetCloseIntercept` is not wired up, and every alarm
will silently die with the window.

## 3. The tray brings the window back

- [ ] Click the tray icon → the window reappears.
- [ ] Tray → Show → the window reappears and takes focus.

## 4. Quit from the tray actually quits

- [ ] Tray → Quit. The process exits within a second or two.

If it **hangs**, this is the fyne issue flagged in spec §12. The workaround is
`os.Exit(0)` after `a.Quit()` in `InstallTray`. Record it here if you hit it.

## 5. A real suspend across the fire time

The test that a unit test cannot do, because it needs a real kernel suspend.

- [ ] Arm an alarm for **10 minutes** from now, with a **1-minute lead-in** and
      the default **5-minute grace**.
- [ ] `systemctl suspend`
- [ ] Wake the machine **more than 20 minutes later.**
- [ ] The app shows `MISSED`, says how late it was, and **Claude Code did not
      run**. A **Run now** button is offered.

Then the inside-grace case:

- [ ] Arm for 2 minutes from now, 1-minute lead-in.
- [ ] `systemctl suspend`, wake after about 4 minutes.
- [ ] The alarm **fires** (it is inside the 5-minute grace) and Claude responds.

## 6. One real Claude invocation

- [ ] Arm for 2 minutes out with a 1-minute lead-in. Let it fire.
- [ ] The answer appears in the window.
- [ ] The status line shows a duration of roughly 3–5s and a cost of roughly
      $0.004–0.006.

If the cost is nearer $0.025 and it took 15+ seconds, `--safe-mode` is not being
passed and the CLI is inheriting your global `CLAUDE.md`.

## 7. Failure paths

- [ ] Set the working directory (Advanced) to a path that does not exist → Arm
      is rejected with a clear message. It does **not** wait until fire time.
- [ ] Set the model to `nonexistent-model` and let it fire → the app shows an
      error mentioning HTTP 404, and does **not** report success.

That second one is the `subtype` trap: the CLI returns `"subtype":"success"`
alongside `"is_error":true` on a bad model. If the app shows a *successful* empty
answer here, something is branching on `subtype`.

## 8. Race check

```bash
go test -race ./... 2>&1 | grep 'Error in Fyne call thread'
```

- [ ] No output. Any hit is a widget being touched from the Core's goroutine
      without `fyne.Do`, which Fyne v2.9 will turn into a crash.
````

- [ ] **Step 3: Work through MANUAL-TESTS.md**

Actually do them. Tick the boxes. Item 5 (a real suspend) is the one most likely
to surface a real bug, and it is the whole reason for the design.

- [ ] **Step 4: Commit**

```bash
git add README.md MANUAL-TESTS.md
git commit -m "docs: add README and the manual test checklist

The manual checklist covers what go test structurally cannot: a real kernel
suspend across the fire time, whether the tray icon actually renders (a
failure that is silent), and one real Claude invocation to confirm the cost
and latency are what --safe-mode predicts."
```

---

## Self-review

**Spec coverage.** Every section maps to a task:

| Spec | Task |
|---|---|
| §2 decisions (headless, one-shot, grace) | 6, 7, 9 |
| §3.1 monotonic/suspend | 4, 5 (`Decide`, `DetectJump`, the suspend test) |
| §3.2 Fyne quits on last window close | 1 (spike), 12 (`KeepAliveOnClose`) |
| §3.3 envelope, `is_error`, timeout, `--safe-mode` | 6, 7 |
| §4 architecture, the three interfaces | 2, 6, 8 |
| §5.1 persist a Spec | 3, 8 |
| §5.2 the poll loop, `Round(0)`, jump detection | 4, 5 |
| §5.3 firing rules + the arm-time exception | 4 (`Decide`), 9 (`Arm` vs `Restore`) |
| §5.4 cancellation | 5 — *simplified*: a single always-running loop makes the double-fire race structurally impossible, so the generation counter is dropped. |
| §5.5 DST policy | 3 (pinned by the Athens tests) |
| §5.6 cannot wake a suspended machine | 14 (README) |
| §6 the runner | 6, 7 |
| §7 tray and lifecycle | 1, 12, 13 |
| §8 UI, theme, time entry | 10, 11 |
| §9 error handling | 7 (runner errors), 11 (surfacing them) |
| §10 testing | every task; 14 for the manual checklist |
| §11 gotchas | enforced in Global Constraints |
| §12 open risk | **Task 1, deliberately first** |

**One deliberate deviation from the spec**, recorded here so it is not mistaken
for an omission: §4.1 lists `internal/schedule/poller.go` holding a
spawn-per-alarm loop with a generation counter (§5.4). Task 5 replaces it with a
single always-running loop, and `policy.go` holds the pure decision function
instead. The race the generation counter existed to prevent cannot occur when
there is only one loop. Fewer moving parts, same behaviour, and the same tests
pass.

**Type consistency.** Checked across tasks: `Spec.FireAt` returns `(fire, target,
err)` everywhere; `schedule.Decide` takes `(now, fireAt, grace)` in Tasks 4, 5,
and 9; `runner.Config` fields match between `BuildArgs` (6), `CLI.Run` (7), and
`Core.fire` (9); `app.Event` fields match between `core.go` (9) and
`window.Apply` (11); `config.State` matches between `store.go` (8),
`core.Arm` (9), and `window.stateFromForm` (11).

**Placeholder scan.** No TBDs. Every code step carries the actual code. Every
test step carries the actual assertions and the expected pass/fail output.
