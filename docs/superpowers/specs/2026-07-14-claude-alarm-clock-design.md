# Claude Alarm Clock — Design

**Date:** 2026-07-14
**Status:** Approved
**Target:** Go 1.26 + Fyne v2.8.0, Linux (KDE Plasma 6 / Wayland)

## 1. Purpose

A small desktop app that fires a Claude Code run at a scheduled time.

The user enters a **target time**. The app computes an **alarm time** of
`target − offset`, where the offset is configurable. The app lives in the system
tray and keeps running when the main window is closed. At the alarm time it runs
the Claude Code CLI in a configured working directory, using the Haiku model,
sends the prompt `Hello world`, waits for the response, and lets the CLI exit.
The response is displayed in the app window and raised as a desktop
notification.

## 2. Decisions

These were settled during brainstorming and are not open questions.

| Decision | Choice |
|---|---|
| How Claude Code is invoked | Headless (`claude -p`), output captured and rendered in-app. No terminal window, no pty. |
| After the alarm fires | App stays in the tray and disarms. One-shot; it never silently reschedules. |
| Alarm missed while suspended | Grace window (default 5 min). Within it, fire immediately. Beyond it, do **not** run; show `MISSED` and disarm. |
| Language | Go. Confirmed a good fit: single binary, negligible idle cost, native `os/exec` and D-Bus tray. |
| UI | Dark single-column, large monospace clock. Author's discretion (user delegated). |

### 2.1 Deliberate exclusions (YAGNI)

- **No repeating/daily alarm.** One-shot only.
- **No single-instance socket or `--no-tray` fallback.** These would guard
  against desktops with no StatusNotifierItem host (GNOME, polybar). Verified
  unnecessary: `org.kde.StatusNotifierWatcher` and a live
  `org.kde.StatusNotifierHost` are both present on the target Plasma session.
  Revisit only if the app must run on GNOME.
- **No `fyne.io/x/fyne` dependency.** It was only wanted for `NumericalEntry`;
  a `widget.Entry` with a validator is equivalent and drops a module.
- **No visible terminal / pty driving of the Claude TUI.** Rejected: it
  reintroduces failure modes that `-p` structurally eliminates.

## 3. Verified environment facts

Established empirically on the target machine, not from memory. These are load-bearing.

| Fact | Value |
|---|---|
| Fyne stable | v2.8.0 (2026-07-08). Tray backend `fyne.io/systray` v1.12.2, pure-Go D-Bus. |
| Build requirement | `CGO_ENABLED=1` (GLFW/OpenGL). X11 by default; runs under XWayland on Plasma. |
| Claude CLI | `/usr/bin/claude`, version 2.1.207. |
| Desktop | KDE Plasma 6, Wayland. SNI tray host present. `org.freedesktop.Notifications` present. |
| Go | 1.26.5. |

### 3.1 Go timers lose suspend time

Go's timers are backed by `CLOCK_MONOTONIC`, which does not advance while the
machine is suspended. `CLOCK_BOOTTIME` appears nowhere in the Go 1.26 Linux
runtime.

Measured on this machine: `CLOCK_MONOTONIC` 55h12m vs `CLOCK_BOOTTIME` 67h42m —
**12h30m of suspend time invisible to timers.** A `time.After(8 * time.Hour)`
armed before those suspends would have fired 12.5 hours late.

Consequences:
- Never use `time.After` / `time.Timer` / `time.AfterFunc` as the alarm mechanism.
- Never use `fyne.App.ScheduleNotification` either — it is `time.AfterFunc`
  underneath on Linux.
- The alarm **must** poll the wall clock. See §5.

### 3.2 Fyne quits when the last window is destroyed

`internal/driver/glfw/loop.go` ends `destroyWindow()` with
`if len(d.windows) == 0 { d.Quit() }`. **There is no system-tray exception.**

Consequently `w.SetCloseIntercept(func() { w.Hide() })` is the single line that
keeps the process — and therefore the alarm — alive when the user clicks the
window's close button. It is not a convenience.

Related: `w.Close()` called from our own code **bypasses** the intercept. We
never call it; we call `w.Hide()` internally, always.

### 3.3 The Claude CLI JSON envelope

On a bad-model 404 the envelope contains `"subtype": "success"` *together with*
`"is_error": true`. **Branch on `is_error`. Never on `subtype`.**

On network failure the CLI **hangs forever** — no output, no exit, retrying
indefinitely. `exec.CommandContext` with a deadline is mandatory. On timeout kill,
`ExitError.ExitCode()` is `-1`, so a timeout must be detected via `ctx.Err()`,
not the exit code.

`--safe-mode` matters a lot. Measured, same prompt:

| Mode | Latency | Input tokens | Cost | Answer |
|---|---|---|---|---|
| Default | 16.5 s | ~32k | $0.0252 | Polluted — discussed the user's global skills |
| `--safe-mode --tools ""` | 3.3 s | 3,359 | $0.0044 | Clean |

## 4. Architecture

The organising rule:

> **Nothing under `internal/schedule`, `internal/runner`, or `internal/config`
> may import Fyne.**

That boundary is what makes the alarm math, the suspend handling, and the Claude
invocation testable with plain `go test` on a headless machine. `internal/ui` is
the only package that touches widgets and the only package that calls `fyne.Do`.

Three interfaces are the seams:

```go
type Clock  interface{ Now() time.Time }
type Runner interface{ Run(context.Context, runner.Config) (runner.Result, error) }
type Store  interface{ Load() (schedule.Spec, error); Save(schedule.Spec) error }
```

`internal/app` orchestrates them and emits events on a channel. `internal/ui`
consumes that channel and is the sole owner of widget mutation.

### 4.1 Module layout

Files are kept small and single-purpose (target 200–400 lines, hard cap 800).

```
cmd/alarmclock/main.go        Wiring: flags, app.NewWithID, preflight, tray, Run.
internal/schedule/spec.go     Spec{Hour,Minute,Zone,Offset,Grace}; NextTarget; FireAt.
internal/schedule/poller.go   1s wall-clock poll loop; fire + time-jump detection.
internal/schedule/alarm.go    Controller: Set/Stop, generation counter, grace policy.
internal/schedule/clock.go    Clock interface; realClock; testClock.
internal/runner/runner.go     Runner interface, Config, Result.
internal/runner/args.go       BuildArgs(Config) []string — pure, golden-tested.
internal/runner/claude.go     exec.CommandContext, envelope parse, timeout mapping.
internal/runner/fake.go       FakeRunner for tests.
internal/config/store.go      Store interface.
internal/config/prefs.go      fyne.Preferences impl. The one non-ui file touching fyne.
internal/app/core.go          Orchestration. No widgets.
internal/ui/theme.go          Custom theme: Color/Font/Icon/Size.
internal/ui/fonts.go          go:embed JetBrains Mono -> fyne.NewStaticResource.
internal/ui/card.go           Theme-aware card widget.
internal/ui/window.go         Layout: clock, form, arm button, status, result pane.
internal/ui/tray.go           desktop.App assertion, menu, icon.
internal/ui/bridge.go         Consumes core events; the only caller of fyne.Do.
assets/icon.png               64x64 full-colour PNG.
```

## 5. Scheduling

### 5.1 Persist a Spec, never an instant

An absolute `time.Time` is meaningless across reboot, DST change, and timezone
change. We store the intent:

```go
type Spec struct {
    Hour, Minute int
    Zone         string        // IANA name. time.Local is cached for the process
                               // lifetime (sync.Once), so a long-lived tray app would
                               // never observe a system TZ change. Re-resolve via
                               // time.LoadLocation on every recompute.
    Offset       time.Duration // the configurable lead-in
    Grace        time.Duration // default 5m
}
```

`Zone` is not exposed in the UI. It defaults to the system's local zone name,
resolved once at arm time and stored. Storing the *name* (rather than relying on
`time.Local`) is what lets the app notice a system timezone change on the next
recompute.

`NextTarget(now)` returns the next occurrence of `Hour:Minute` — today if still
in the future, otherwise tomorrow.

`FireAt = NextTarget(now).Add(-Offset)`. The offset is applied with `Add` on the
**instant**, not by minute arithmetic on the calendar fields. This is what keeps
it correct when the offset straddles a DST boundary.

### 5.2 The poll loop

A 1-second `time.Ticker` re-reads `time.Now()` and compares wall clock to wall
clock. Measured cost: 0.007% of one core.

Both operands get `.Round(0)` to strip the monotonic reading before comparison.
This is subtle and essential: without it, `Before()` uses the monotonic readings
when both operands carry one, silently reintroducing the suspend bug inside a
function that looks like it compares wall clocks.

Suspend and NTP steps are detected in the same loop: when the wall-clock delta
and the monotonic delta between two ticks diverge by more than a couple of tick
periods, the machine slept or the clock was stepped.

`FireAt` is recomputed on **app start** (see `Restore`, §5.3) and after every
fire. It is deliberately **not** recomputed on a detected time jump.

> **Correction (2026-07-14, during implementation).** This section originally
> said `FireAt` was also recomputed on every detected time jump, "making the
> design self-healing". That was wrong, and it directly contradicted §5.3.
>
> After a laptop sleeps past the fire time, recomputing from the `Spec` yields
> *tomorrow's* occurrence — so the alarm silently re-arms and the user is never
> told they missed it. That violates §2 ("one-shot; it never silently
> reschedules") and defeats the grace window entirely.
>
> Recompute-on-jump also buys nothing:
> - **Suspend** does not invalidate `FireAt`. It is an absolute instant, and it
>   is still the correct one; `Decide` handles it (fire, or missed).
> - **DST** is already resolved at computation time — `time.Date` applies the
>   UTC offset in effect *at the target date*, so an alarm armed before a
>   changeover for a date after it is correct when computed.
> - **A timezone change produces no clock jump at all** (the instant does not
>   move), so `DetectJump` never observes it. Recompute-on-jump could not have
>   fixed the TZ case even in principle. A TZ change while armed keeps the
>   original absolute instant; it is re-resolved from the `Spec` on the next
>   arm or restart. This is a documented limitation, not a bug.
>
> It only helped a rare NTP-step edge case, at the cost of the behaviour the
> user explicitly chose. Removed.

`EventJump` is therefore **informational**: it tells the UI the wall clock moved,
and it is what the poll loop uses to explain a large gap between ticks. It does
not mutate the alarm. Because `Core` no longer touches the alarm on a jump, there
is nothing for the alarm's own tick evaluation to race against.

### 5.3 Firing rules

Let `now` be the wall clock at a tick, `fire` the computed alarm instant.

- `now < fire` → tick; update countdown.
- `fire <= now <= fire + Grace` → **fire**. Run Claude. Then disarm.
- `now > fire + Grace` → **do not run.** Enter `MISSED` state, show how late it
  was, disarm. Offer a "Run now" button; the Arm button (now reading "Arm"
  again, since the alarm disarmed itself) doubles as the way to re-arm for
  the next occurrence -- there is no separate "Re-arm" button.

**The grace window governs unobserved time only.** It answers exactly one
question: "the app was not watching (suspended, or not running) — is this alarm
now too stale to honour?" It does **not** apply at arm time.

Arming edge case: if the user arms a target whose `fire` instant is already in
the past but whose `target` is still in the future (e.g. arms an 07:30 target
with a 20-minute offset at 07:20), **fire immediately, regardless of how far past
`fire` we are.** Arming is an explicit act performed with the user looking at the
screen, so staleness is not a meaningful concept: the intent ("do this before
07:30") is still satisfiable. A 5-minute grace window must not block this.

If the user enters a target time that has already passed today (arms 07:30 at
09:00), `NextTarget` rolls to tomorrow. This is normal alarm-clock behaviour and
is not an error.

### 5.4 Cancellation

`context.Context` plus a generation counter. A reset cancels the old poller and
spawns a new one; a poller is never reused. Updates carrying a superseded
generation are dropped. This closes the "user re-armed while the old poller was
mid-fire" double-fire race.

### 5.5 DST policy

For a target landing in a spring-forward gap, Go's `time.Date` silently slides it
an hour later; for an ambiguous fall-back time it picks the second instance, and
the docs do not guarantee that. We accept Go's behaviour as our policy and **pin
it with tests** so it cannot change underneath us silently.

### 5.6 Known limitation

Polling cannot wake a suspended machine. If the laptop is asleep at the alarm
time, the alarm fires on wake (subject to the grace window), not at the alarm
time. This is documented in the README.

## 6. The Claude Code runner

### 6.1 Command

```
claude -p \
  --model haiku \
  --output-format json \
  --safe-mode \
  --tools "" \
  --permission-mode dontAsk \
  --max-budget-usd 0.10 \
  --no-session-persistence \
  "Hello world"
```

- `cmd.Dir = workdir`. There is **no `--cwd` flag**; the process working
  directory is the only mechanism. (`--add-dir` is a tool allowlist, not a cwd.)
- `cmd.Stdin = nil`, so Go connects `/dev/null`. An open-but-empty stdin pipe
  costs a 3-second stall plus a stderr warning.
- `exec.CommandContext` with a 120s timeout.
- `--tools ""` gives a zero tool surface, which makes permissions moot. We
  therefore never use `--dangerously-skip-permissions`.
- We do **not** use `--bare` — it refuses OAuth/keychain auth, which is what this
  machine uses, and hard-fails with "Not logged in".

### 6.2 Result handling

Parse the JSON envelope. Take the answer from `.result`. Branch on `is_error`
(never `subtype`). Surface `total_cost_usd` and duration in the UI.

Failure paths, all distinct and all surfaced:
- Timeout → `ctx.Err() == context.DeadlineExceeded`. Not detectable from the exit code.
- `is_error: true` → show `.result` (the error text) and `api_error_status`.
- Unparseable stdout → show exit code and stderr.
- Non-zero exit with parseable output → show both.

### 6.3 Preflight

`exec.LookPath("claude")`, `claude --version`, and `os.Stat(workdir)` + `IsDir()`
run **at arm time**, while the user is looking at the app — not at fire time,
when nobody is watching.

## 7. Tray and window lifecycle

```go
a := app.NewWithID("gr.polaris.claudealarm") // ID required for Preferences
w := a.NewWindow("Claude Alarm Clock")

if desk, ok := a.(desktop.App); ok {   // false under fyne's test app — always guard
    quit := fyne.NewMenuItem("Quit", func() { core.Shutdown(); a.Quit() })
    quit.IsQuit = true                 // else Fyne appends its own, skipping teardown
    desk.SetSystemTrayMenu(fyne.NewMenu("Claude Alarm",
        fyne.NewMenuItem("Show", func() { w.Show(); w.RequestFocus() }),
        fyne.NewMenuItem("Disarm", func() { core.Disarm() }),
        fyne.NewMenuItemSeparator(),
        quit,
    ))
    desk.SetSystemTrayIcon(resIconPng)  // 64x64 full-colour PNG, not a ThemedResource
    desk.SetSystemTrayWindow(w)
}

w.SetCloseIntercept(func() { w.Hide() }) // the only thing keeping the process alive
w.ShowAndRun()
```

The tray icon must be a plain full-colour PNG. A `theme.ThemedResource` routes to
`SetTemplateIcon`, which is a macOS-only path and useless on Linux. Icon errors
are logged, never returned.

All widget and tray-menu mutation originating from the poller goroutine must be
wrapped in `fyne.Do`. Fyne v2.8 has a temporary safety net that logs
`*** Error in Fyne call thread` and repairs the call; **v2.9 removes it**. Any
occurrence of that string in test output is a real latent race and must be fixed.

## 8. UI

Dark, single-column, one focal point.

- **Clock**: a large `canvas.Text` (size ~88) in **JetBrains Mono Bold**
  (SIL OFL 1.1), embedded via `go:embed` and served from the custom theme's
  `Font()` method, routed by `TextStyle.Monospace`. Tabular digits, so ticking
  seconds do not jitter the layout. `TextStyle.Symbol` is never hijacked — that
  breaks icon rendering.
- **Palette**: near-black ground `#121418`, elevated card `#1B1E25`. Never pure
  black, never default grey. The custom `Color()` and `Size()` methods always
  fall through to `theme.DefaultTheme()` for names they do not override. The
  deprecated `theme.DarkTheme()` / `LightTheme()` are not used.
- **Card**: a small custom widget (not a bare `canvas.Rectangle`, which samples
  its colour once and will not re-colour on a light/dark switch). Uses
  `theme.ColorForWidget` in its renderer.
- **Root**: `container.NewPadded`. Omitting root padding is the clearest visual
  tell of an unstyled Fyne app.

### 8.1 Time entry

Fyne has **no time picker**, and neither does `fyne.io/x/fyne` (its complete
widget list is `AnimatedGif`, `Calendar` (date-only), `CompletionEntry`,
`FileTree`, `GridWrap`, `HexWidget`, `Map`, `NumericalEntry`,
`TwoStateToolbarAction`). There is no third-party one worth adopting.

So: a `widget.Entry` with `Validator = validation.NewTime("15:04")`, which gives
an error border and message for free. Setting an alarm is a *typing* interaction
— type `07:30`, press enter. A 60-item minute dropdown would be worse.

The offset uses a `widget.Entry` with a numeric validator (rather than pulling in
`fyne.io/x/fyne` for `NumericalEntry`).

Both live in a `widget.Form`, which auto-disables its submit button while any
validator fails — that gates the Arm button for free.

### 8.2 Layout

Main view: clock readout, target time, offset, Arm button, status line, result
pane. The Arm button is `widget.HighImportance`, switching to
`widget.DangerImportance` when armed.

In the `MISSED` state (§5.3) the status area additionally shows how late the
alarm was, plus a **Run now** button (invoke Claude immediately). There is no
separate "Re-arm" button: MISSED already disarmed the alarm, so the Arm button
has reverted to reading "Arm", and pressing it re-arms for the next
occurrence.

Working directory, model, and prompt live in a collapsed `widget.Accordion`
labelled "Advanced", so the default view stays clean but nothing is hardcoded.
The working directory row pairs an entry with a `dialog.ShowFolderOpen` button.

## 9. Error handling

Every failure is surfaced in the UI status line and, for alarm-time failures,
also as a desktop notification. Nothing is silently swallowed.

| Failure | Surfaced as |
|---|---|
| `claude` not on PATH | Blocked at arm time, with the message. |
| Working dir missing / not a dir | Blocked at arm time. |
| Invalid time / offset | Inline validator error; Arm stays disabled. |
| Claude timeout (120s) | `TIMED OUT` + notification. Disarm. |
| Claude `is_error: true` | Error text + `api_error_status` + notification. |
| Unparseable output | Exit code + stderr shown. |
| Alarm missed beyond grace | `MISSED`, with how late, + notification. Disarm. |
| No SNI tray host | Out of scope — verified present. Would show an invisible icon. |

## 10. Testing

Target 80% coverage. Achievable because `ui/` is the only package needing a driver.

### 10.1 No display, no network, no `claude` binary

| Unit | Approach |
|---|---|
| `Spec.NextTarget` / `FireAt` | Table tests. Must include Europe/Athens 2026-03-29 (03:30 spring-forward gap) and 2026-10-25 (03:30 ambiguous). Pins the DST policy of §5.5. |
| Offset math | `target=07:30, offset=20m → fire=07:10`. `now=07:20` → fire-immediately. An offset spanning a DST boundary, proving `Add(-offset)` on the instant is right and minute arithmetic is wrong. |
| `poller.Run` | Inject `Clock` and a manually-driven tick channel. **Simulate suspend by jumping the fake clock 12h between two ticks; assert exactly one fire event and one time-jump event.** This is the test that would have caught the monotonic bug. |
| Grace window | Jump the clock to `fire + 4m` → fires. To `fire + 6m` → `MISSED`, runner never called. |
| Re-arm race | Set, then Set again; assert the first generation's late update is dropped and only one fire reaches the channel. Run under `-race`. |
| `runner.BuildArgs` | Golden slice compare. **Assert `--dangerously-skip-permissions` is absent** and `--bare` is absent. |
| Envelope parsing | Fixtures: success; the bad-model envelope with `"subtype":"success"` + `"is_error":true` (guards the trap in §3.3); truncated garbage. |
| `runner.CLI` exec path | Make `Config.Bin` injectable, point it at `testdata/fake-claude.sh` — echoes a canned envelope, or `sleep 999` to exercise the `DeadlineExceeded` path, or `exit 1`. Hermetic, fast, no API spend. |
| `app.Core` | `testClock` + manual ticks + `FakeRunner`. Assert `FakeRunner.Calls[0].WorkDir` and `.Prompt == "Hello world"`, called exactly once. |

### 10.2 Needs `fyne.io/fyne/v2/test` (software driver, still headless)

`test.NewApp()` does **not** implement `desktop.App` — which is precisely why the
`if desk, ok := a.(desktop.App); ok` guard in `ui/tray.go` makes it safe to
compile into tests. Use `test.Tap`, `test.Type`, `test.AssertRendersToMarkup`.

Run with `-race` and grep the log for `*** Error in Fyne call thread`.

### 10.3 Needs a real display / DE / CLI — manual checklist

Documented in `MANUAL-TESTS.md`:

1. Tray icon actually appears on Plasma (a missing SNI host fails *silently*).
2. Quit-from-tray does not hang.
3. Clicking the window's X hides it and does **not** kill the process (`ps` after).
4. Real suspend: arm for 10 minutes, `systemctl suspend`, wake after 20 → asserts
   the grace-window MISSED path against a real kernel suspend.
5. One real `claude` invocation, confirming `--model haiku` still resolves and
   the cost/latency are as measured.

## 11. Gotchas checklist

**Timing**
1. Never `time.After` / `time.Timer` / `time.AfterFunc` for the alarm.
2. Never `fyne.App.ScheduleNotification` — `time.AfterFunc` underneath.
3. `.Round(0)` **both** operands before comparing.
4. `time.Local` is cached for the process lifetime. Store an IANA zone name.
5. Recompute `FireAt` on start and after every fire — **never on a time jump.**
   Recomputing on a jump silently re-arms a missed alarm for tomorrow instead of
   reporting it MISSED, which is the opposite of what §5.3 requires. See §5.2.
6. `EventJump` is informational. It must not mutate the alarm. A jump handler
   that re-arms is racing the alarm's own evaluation of the same tick.
7. Fire exactly once. Guard the fire/missed path with a compare-and-clear
   (`disarmIfStill`), not an unconditional disarm: the user may re-arm in the
   window between the loop deciding and the loop clearing.

**Fyne**
7. `SetCloseIntercept` + `w.Hide()` is the only thing keeping a tray app alive.
8. Never call `w.Close()` in our own code — it bypasses the intercept.
9. Wrap every widget/tray mutation from the poller goroutine in `fyne.Do`. Never
   call `fyne.Do`/`DoAndWait` from the main goroutine or inside a Fyne callback —
   `DoAndWait` from main is a real deadlock.
10. `app.NewWithID`, or `Preferences()` will not work.
11. Preference change listeners fire synchronously on the calling goroutine.
12. Tray icon: 64×64 full-colour PNG, not a `ThemedResource`.
13. Tray Quit item needs `IsQuit = true` or teardown is skipped.
14. Custom `Color()`/`Size()` must fall through to `theme.DefaultTheme()`.
15. `CGO_ENABLED=1`.

**Claude CLI**
16. `exec.CommandContext` with a timeout is mandatory — network failure hangs forever.
17. Detect timeout via `ctx.Err()`, not the exit code (`-1` on signal-kill).
18. Never branch on `subtype`; branch on `is_error`.
19. `cmd.Stdin = nil`.
20. `--safe-mode` — otherwise we inherit the user's global CLAUDE.md and skills.
21. Never `--bare` (breaks OAuth auth); never `--dangerously-skip-permissions`.
22. `cmd.Dir` is the only way to set the working directory.
23. Re-verify against `claude --version` on upgrade; this CLI has no stable contract.

## 12. Open risk

`fyne-io/fyne` has a reported issue, "Desktop app with system tray hangs on
`app.Quit`", which the research could not confirm or refute (GitHub was
unreachable from the research sandbox). This is exactly the failure mode a tray
app hits. **Mitigation: verify quit-from-tray on Plasma as the very first
implementation step, before writing app logic.** If it reproduces, the fallback
is `os.Exit` after an explicit teardown.
