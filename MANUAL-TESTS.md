# Manual tests

`go test` covers the alarm math, the suspend/jump detection, the Claude
envelope parsing, and the widget code, all headlessly. It structurally
cannot cover a real system tray, a real kernel suspend, or a real billed
Claude invocation. This checklist is what's left. Work through it before
calling a release done.

Verify on a real desktop session — these do not work over SSH without a
display, and several need a live D-Bus session.

Both form fields are typed as zero-padded `HH:MM`. Where a step below asks
for a "1-minute lead-in", type `00:01` into **Run this early** — a bare `1`
is rejected, because `1` alone cannot say whether you mean an hour or a
minute.

## 1. The tray icon actually appears

```bash
./alarmclock
```

- [ ] A clock icon appears in the system tray.

**This failure is silent.** `fyne.io/systray` registers a
StatusNotifierItem over D-Bus; if no SNI host is running, registration
no-ops — no error printed, no crash, just no icon. If step 1 fails, confirm
a host actually exists before assuming the app is broken:

```bash
busctl --user list | grep -i StatusNotifier
```

Expect to see both `org.kde.StatusNotifierWatcher` (or another
implementation's watcher) and an `org.kde.StatusNotifierHost-*` entry. If
neither appears, the desktop environment has no SNI host (see README —
GNOME needs the AppIndicator extension, polybar needs `snixembed`), and the
missing icon is expected, not a bug in this app.

## 2. Closing the window does not kill the process

- [ ] Click the window's close (X) button. The window disappears.
- [ ] `pgrep -af alarmclock` still shows the process running.

If the process dies here, `SetCloseIntercept` (`internal/ui/tray.go`,
`KeepAliveOnClose`) is not wired up, and every armed alarm dies with the
window — silently, with no warning to the user that it happened.

## 3. The tray restores the window

- [ ] With the window hidden (from step 2), click the tray icon. The window
      reappears.
- [ ] Tray menu → **Show**. The window reappears and takes focus.

## 4. Quit from the tray actually quits

- [ ] Tray menu → **Quit**. The process exits within a second or two.
      Confirm with `pgrep -af alarmclock` — nothing should be listed.

**This is a known open risk, not yet verified by a human**: there is a
reported upstream `fyne-io/fyne` issue describing a tray app hanging on
`app.Quit()`. If it hangs here, the workaround is to call `os.Exit(0)` after
teardown rather than relying on `a.Quit()` alone. Record the outcome
(hung / did not hang) in this file when you run this check.

## 5. A real kernel suspend across the fire time

The single most important item on this list, and the whole reason this
app polls the wall clock instead of using a timer (see README). No unit
test can fabricate a genuine `CLOCK_MONOTONIC`/wall-clock divergence — it
takes a real suspend.

**Case A — beyond the grace window (must report MISSED, must not run
Claude):**

- [ ] Arm an alarm for **10 minutes** from now, with a **1-minute** lead-in
      and the default **5-minute** grace (so it's due to fire in about 9
      minutes).
- [ ] `systemctl suspend`
- [ ] Wake the machine **more than 20 minutes later** (well past fire time +
      grace).
- [ ] The app shows `MISSED`, states how late it was, and Claude Code did
      **not** run (no cost, no answer in the result pane). A **Run now**
      button is offered.

**Case B — inside the grace window (must fire):**

- [ ] Arm for **2 minutes** from now, **1-minute** lead-in (fires in about 1
      minute).
- [ ] `systemctl suspend`
- [ ] Wake after about **4 minutes** — past the fire time, but within the
      5-minute grace.
- [ ] The alarm **fires** on wake and Claude responds normally.

## 6. One real Claude invocation

- [ ] Arm for 2 minutes out with a 1-minute lead-in. Let it fire without any
      suspend involved.
- [ ] The answer appears in the result pane.
- [ ] The status line shows a duration of roughly **3–5 seconds** and a cost
      of roughly **$0.004–0.006**.

If it instead takes 15+ seconds and costs nearer $0.025, `--safe-mode` is
not being passed on this run and the CLI is inheriting your global
`CLAUDE.md` and skills — see the README's measured comparison.

## 7. Failure paths

- [ ] Open **Advanced**, set the working directory to a path that does not
      exist, then try to arm. Arm is **rejected immediately**, with a clear
      message — not accepted and left to fail silently at fire time.
- [ ] Set the model (Advanced) to `nonexistent-model`, arm for 2 minutes out,
      and let it fire. The app shows an error mentioning **HTTP 404**, and
      does **not** report success or show an empty answer as if it worked.

That second case is the `subtype` trap: on a bad-model error the CLI's JSON
envelope reports `"subtype":"success"` *alongside* `"is_error":true`. If the
app ever shows a successful-looking empty answer here, something has
regressed to branching on `subtype` instead of `is_error`
(`internal/runner/claude.go`).

## 8. Race check

```bash
go test -race ./... 2>&1 | grep 'Error in Fyne call thread'
```

- [ ] No output.

Any hit means a widget was mutated from a goroutine other than Fyne's own
without going through `fyne.Do` — Fyne v2.8 currently logs and silently
repairs this, but v2.9 turns it into a crash.
