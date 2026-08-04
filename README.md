# Claude Alarm Clock

A desktop alarm clock for Linux. You set a target time; at `target − lead-in`
it runs Claude Code headlessly in a working directory you choose, sends it a
prompt, and shows you the answer. It lives in the system tray and keeps
running when you close the window, so the alarm survives even if you never
look at it again until it fires.

## Requirements

**The `claude` CLI must be on your `PATH`.** This app is a scheduler around
Claude Code, not a replacement for it — at fire time it execs `claude` and
shows you the answer. Without it the app starts and reports the problem at
startup rather than at 07:10 when nobody is watching.

Built for **glibc 2.35 and newer**: Ubuntu 22.04 and newer, Debian 12 and
newer, current Fedora, Arch and every rolling distribution. Older systems —
RHEL/Rocky/Alma 9, Ubuntu 20.04, Debian 11 — are not supported.

(Do not claim the app "will not start" on those. As measured on 2026-08-04 the
binary's actual symbol floor is `GLIBC_2.34`, below the container's 2.35, so
some of them may work by accident. Promising 2.35 is what the build
environment guarantees; promising less would be a claim CI does not enforce,
and the floor can rise at any time.)

x86_64 only.

## Install

### AppImage

```bash
chmod +x claude-alarm-clock-1.0.0-x86_64.AppImage
./claude-alarm-clock-1.0.0-x86_64.AppImage
```

`libfuse2` is **not** required. The bundled AppImage runtime is statically
linked against libfuse3, so nothing needs installing alongside it — only the
kernel FUSE module and the standard setuid `fusermount3` helper, both present
by default on mainstream desktops. Where FUSE is genuinely unavailable
(containers, hardened kernels), run it without mounting:

```bash
./claude-alarm-clock-1.0.0-x86_64.AppImage --appimage-extract-and-run
```

The AppImage deliberately does **not** bundle glibc or the graphics stack
(libGL, libEGL, libX11, libxcb, libdrm, Mesa). Those always come from your
system, so it works on Mesa and NVIDIA alike.

### tar.xz

Installs a desktop entry and icon, so the app appears in your application menu
and can be autostarted with `-hidden`.

```bash
tar -xJf claude-alarm-clock-1.0.0-linux-amd64.tar.xz
cd alarmclock
sudo make install      # /usr/local/{bin,share/applications,share/pixmaps}
```

Or without root:

```bash
make user-install      # ~/.local/{bin,share/applications,share/icons}
```

`make uninstall` and `make user-uninstall` reverse either. The Makefile honours
`PREFIX=` and `DESTDIR=` for packagers.

### Verifying a download

```bash
sha256sum -c SHA256SUMS
```

## Run

```bash
./alarmclock            # show the window
./alarmclock -hidden    # start minimised to the tray
./alarmclock -version   # print version information and exit
```

`-hidden` is refused (silently downgraded to a visible window) if no system
tray is available — see "Tray support" below. Starting hidden with no way
back to the window would strand you with no way to reach the app at all.

`-version` prints the build identity and exits before any Fyne
initialisation, so it works with no display — which is exactly why the
release workflow's packaging smoke test runs it, to prove the linker actually
stamped the right tag into the binary. A release binary reports that tag; a
plain `go build` from an untagged checkout like this one instead falls back
to the toolchain's pseudo-version and git revision:

```
$ ./alarmclock -version
Claude Alarm Clock v0.0.0-20260804232740-2ebd31237f51 (2ebd312)
```

## What it runs

At fire time the app execs:

```
claude -p --model haiku --output-format json --safe-mode --tools "" \
       --permission-mode dontAsk --max-budget-usd 0.10 \
       --no-session-persistence "Hello world"
```

with the process's working directory (`cmd.Dir`, the only mechanism — there
is no `--cwd` flag) set to whatever directory you configured. Model, prompt,
and working directory are all editable under **Advanced** in the window; the
values above are just the defaults.

`--safe-mode` is not optional. Without it, the CLI inherits your global
`CLAUDE.md`, skills, and plugins. Measured on the development machine, same
prompt:

| Mode | Latency | Cost | Answer |
|---|---|---|---|
| Default (no `--safe-mode`) | 16.5 s | $0.0252 | Polluted — the model discussed the user's global skills instead of answering |
| `--safe-mode --tools ""` | 3.3 s | $0.0044 | Clean |

`--tools ""` gives the run a zero tool surface, which is also what makes
`--dangerously-skip-permissions` unnecessary — there's nothing for it to skip.
`--max-budget-usd 0.10` is a stop-loss with roughly 20x headroom over the
measured cost. `--no-session-persistence` keeps this fire-and-forget run out
of your Claude session history.

## Behaviour

**One-shot.** After the alarm fires, it disarms and the app sits idle in the
tray. It never silently reschedules itself for the next day — if you want it
again tomorrow, you arm it again.

**Missed alarms and the grace window.** If your machine was suspended, or the
app wasn't running, when the alarm came due, it still fires *if* it's
noticed within the grace window (default 5 minutes) of the fire time.
Beyond that it reports `MISSED` — how late it was — and does **not** run
Claude: an alarm that fires three hours stale is worse than useless. A
**Run now** button lets you invoke Claude anyway if you still want the
answer.

The grace window governs *unobserved* time only. It answers one question:
"the app wasn't watching — is this now too stale to honour?" It does not
apply when you're the one arming it. If you arm an 07:30 target with a
20-minute lead-in at 07:20, the computed fire time (07:10) is already ten
minutes in the past — but it fires immediately anyway, however far past the
fire time you are. Arming is an explicit act performed with you looking at
the screen, so staleness isn't a meaningful concept there.

## Build from source

You need Go 1.26 or later (developed and tested against 1.26.5),
`CGO_ENABLED=1` (Fyne's GLFW/OpenGL backend requires cgo), and the `claude`
CLI on your `PATH`.

On Arch:

```bash
sudo pacman -S --needed go libxcursor libxrandr libxinerama libxi libgl mesa
go build -o alarmclock ./cmd/alarmclock
```

`CGO_ENABLED=1` is Go's default when a C toolchain is present, so you
shouldn't need to set it explicitly unless your environment overrides it.

Packaging locally with `scripts/package-linux.sh` needs two more things:
the `fyne` CLI on your `PATH`, at the same pinned version both CI workflows
install —

```bash
go install fyne.io/tools/cmd/fyne@v1.7.2
```

— without which the script fails immediately with `fyne: command not found`;
and network access, since the script downloads
`linuxdeploy-x86_64.AppImage` from GitHub on first run (cached in the working
directory afterwards).

`scripts/package-linux.sh <version>` builds both release artifacts locally into
`dist/`. Note that `fyne package` **rewrites `FyneApp.toml` in place**, stripping
every comment, reordering keys and incrementing `Build`. The script takes a copy
first and restores it on exit, so running the script is safe even with
uncommitted edits in that file. If you invoke `fyne package` by hand, restore the
file yourself — and be aware that `git checkout -- FyneApp.toml` will also throw
away any uncommitted edits you had, since it restores the index, not the state
the file was in a moment earlier.

## Known limitation

**It cannot wake a sleeping machine.** The app only polls the wall clock (see
below); it has no mechanism to interrupt a suspend. If the laptop is asleep
at the fire time, the alarm is evaluated on resume, subject to the grace
window above — not at the fire time itself. Actually waking the machine
would need `rtcwake` or a systemd timer with elevated privileges, which is
out of scope for this app.

## Why wall-clock polling and not a timer

Go's `time.Timer` (and `time.After`, and `fyne.App.ScheduleNotification`,
which is `time.AfterFunc` underneath on Linux) is driven by
`CLOCK_MONOTONIC`, which does not advance while the machine is suspended.

Measured on the development machine: `CLOCK_MONOTONIC` read 55h12m of uptime
against `CLOCK_BOOTTIME`'s 67h42m — **12h30m of suspend time invisible to
any timer.** A `time.After(8 * time.Hour)` armed before those suspends would
have fired 12.5 hours late, with no way to detect that it had.

So the alarm never arms a timer. A 1-second `time.Ticker` re-reads
`time.Now()` on every tick and compares that wall-clock reading against the
computed fire instant. This costs about 0.007% of one core. The same loop
diffs the wall-clock delta against a monotonic delta between ticks to detect a
suspend or an NTP step — see `internal/schedule/policy.go` and
`internal/schedule/alarm.go` for the detail, including why both times are
passed through `.Round(0)` before comparison.

## Tray support

The tray icon uses the StatusNotifierItem D-Bus protocol
(`fyne.io/systray`). This was verified working on **KDE Plasma 6**
(Wayland), the development environment. XFCE (4.16+) and waybar (with its
`tray` module) implement SNI natively and should work the same way, though
they were not verified here.

**GNOME does not implement SNI out of the box** — you need the
[AppIndicator/KStatusNotifierItem](https://extensions.gnome.org/extension/615/appindicator-support/)
extension. **polybar's tray does not speak SNI either**; it needs
[`snixembed`](https://git.sr.ht/~steef/snixembed) running as a bridge.

If no SNI host is present, the tray icon **silently fails to appear** — no
error, no crash, nothing in the UI. Do not start with `-hidden` unless you've
confirmed a tray icon actually shows up first; without one, the window is
your only way to reach the app.

## Tests

```bash
go test -race -cover ./...
```

Per-package coverage as measured on this branch:

| Package | Coverage |
|---|---|
| `internal/schedule` | 88.4% |
| `internal/runner` | 87.3% |
| `internal/app` | 83.7% |
| `internal/ui` | 87.8% |
| `internal/config` | 74.5% |
| `internal/buildinfo` | 100.0% |
| `cmd/alarmclock` | 0.0% (wiring only, exercised manually) |

The alarm math, the suspend/NTP-jump detection, the Claude invocation, and
the app orchestration run headlessly with no display, no network, and no API
spend, and mostly sit behind interfaces (`Clock`, `Runner`, `Store`) — except
that `internal/app/core.go`'s `Arm` and `fire` both resolve the `claude`
binary via the package-level `runner.Lookup()`, a hardcoded
`exec.LookPath("claude")` that bypasses the injected `Runner`. It is only
ever resolved there, never executed, but `go test ./...` still needs
*something* named `claude` on your `PATH` to satisfy it — a clone without
Claude Code installed will fail here with `claude not found on PATH`. The
Claude exec path itself is tested against shell-script stand-ins under
`internal/runner/testdata/` rather than the real CLI. `internal/ui` uses
Fyne's software test driver (`fyne.io/fyne/v2/test`), which is still
headless but does exercise real widget code.

What this cannot cover — a real tray icon rendering, a real kernel suspend,
one real billed Claude invocation — is in `MANUAL-TESTS.md`.

`go test -race` output should never contain the string
`Error in Fyne call thread`; that string means a widget was touched from a
goroutine other than the Fyne one without going through `fyne.Do`, which
Fyne v2.8 currently repairs and logs but v2.9 will turn into a crash instead.

## Licence

The clock face is set in JetBrains Mono Bold, embedded via `go:embed` and
licensed under the SIL Open Font License 1.1 — see `assets/fonts/OFL.txt`.
