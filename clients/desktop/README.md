# Headroom desktop client

A shared Windows, macOS, and Linux Qt Quick client for the Headroom usage API. It has a
frameless tray popup, system-tray meter, official provider icons, reset
countdowns, pacing warnings, notifications, local-server lifecycle management,
diagnostics, and verified package updates. Windows packages can read supported
Cursor and Grok browser cookies through a separate current-user helper.

## Build and run

Requires CMake 3.21.1+, a C++17 compiler, and Qt 6.6+ with Quick, Quick Controls 2,
Widgets, Network, SVG image support, and Test. On Arch / EndeavourOS these come
from `base-devel cmake ninja qt6-base qt6-declarative qt6-svg` (plus
`qt6-wayland` for a Wayland session). KDE tray anchoring uses the optional
`kstatusnotifieritem` and `layer-shell-qt` (6.6+) libraries detected by CMake.
Building with those optional packages enables native KDE Wayland tray attachment.
Without LayerShellQt, the client prefers XWayland when available so tray clicks
can position the popup beside the icon. Pure Wayland sessions without that
integration leave window placement to the compositor. Explicit Qt platform
overrides are preserved.

macOS builds require macOS 12 or newer and Qt 6.8.3 or newer. CMake creates a
menu-bar-only application bundle with the `io.headroom.Headroom` identifier.

```bash
cmake -S clients/desktop -B clients/desktop/build -G Ninja -DCMAKE_BUILD_TYPE=Release
cmake --build clients/desktop/build
clients/desktop/build/headroom
```

On Windows, run the same configure/build/test flow from a Visual Studio
developer shell whose target architecture matches the installed Qt kit:

```powershell
cmake -S clients/desktop -B clients/desktop/build -G Ninja -DCMAKE_BUILD_TYPE=Release -DHEADROOM_WITH_KDE_TRAY=OFF -DHEADROOM_WITH_LAYER_SHELL=OFF
cmake --build clients/desktop/build --parallel
ctest --test-dir clients/desktop/build --output-on-failure
clients/desktop/build/headroom.exe
```

Use an x64 Qt kit with an x64 developer shell, or the Qt MSVC ARM64 kit with an
ARM64 shell. Set `CMAKE_PREFIX_PATH` or `Qt6_DIR` to that kit if Qt is not
already discoverable in the shell, and add the kit's `bin` directory to `PATH`
when running the source-built app and tests. The exact Qt 6.8.3 native package recipe is in
[headroom-packages.yml](../../.github/workflows/headroom-packages.yml).

On macOS 12 or newer, configure with the Qt 6.8.3 kit for the host architecture.
Release packages build separate Apple silicon and Intel bundles:

```bash
cmake -S clients/desktop -B clients/desktop/build -G Ninja \
  -DCMAKE_BUILD_TYPE=Release -DCMAKE_PREFIX_PATH="$HOME/Qt/6.8.3/macos"
cmake --build clients/desktop/build --parallel
ctest --test-dir clients/desktop/build --output-on-failure
open clients/desktop/build/headroom.app
```

With the official Qt 6.8.3 kit and Xcode/Command Line Tools 26, select an
installed macOS 15 SDK when configuring. The macOS 26 SDK omits the AGL
framework referenced by that Qt kit. For example, if this SDK is installed:

```bash
cmake -S clients/desktop -B clients/desktop/build \
  -DCMAKE_OSX_SYSROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX15.4.sdk
```

Use the SDK path available in your Xcode or Command Line Tools installation;
this does not change Headroom's macOS 12 deployment target.

Mac test builds generate fresh synthetic TLS certificates with the system
`/usr/bin/openssl`. On macOS 15, local TLS fixture runs need a Qt kit built with
the macOS 15 SDK or newer. Native CI tests the official Qt 6.8.3 kit in a
disposable keychain because that kit's older SDK cannot request memory-only
private-key import on macOS 15. The application uses the Go server's in-memory
TLS identity and Apple's native client verification.

On Linux, install the executable, application-menu entry, and icon for your
user:

```bash
cmake --install clients/desktop/build --prefix "$HOME/.local"
```

Ensure `$HOME/.local/bin` is on your desktop session's PATH. Open **Headroom**
from the application menu. For a custom installation prefix, add its `bin`
directory to PATH as well.

This CMake install is a source installation. Official release packages use the
verified per-user installer and stable launcher described in the repository
[installation guide](../../README.md#install).

## Connect

Windows, macOS, and Linux start in **Local** mode at `http://127.0.0.1:7823`, attaching to
an existing server or starting an adjacent packaged server. Saved connection
settings take precedence. To connect directly, choose **HTTP(S)**, enter the
server's base HTTP(S) address and bearer token, then choose **Save & connect**.
Reverse-proxy path prefixes are supported.

**SSH is recommended for remote connections.** Choose it to reach a Linux or WSL
server using an address such as
`ssh://usageuser@server.example:2222` or an existing SSH alias. SSH mode uses
OpenSSH key/agent authentication and trusted host keys, keeps its settings
separate from the HTTP address/token, and carries usage, version, and credential
requests to the same running backend. The backend must enable SSH access under
the SSH login account. Follow the [SSH setup guide](../../docs/ssh.md). Local stays
the default for a fresh install, saved choices are retained, and SSH never falls
back to HTTP.

Local discovery and attached plain HTTP servers receive no saved bearer tokens
or browser cookies. A bundled server uses a fresh TLS certificate and session
token exchanged through private process pipes. Health, usage, version, and
credential requests all verify that server identity. The session is discarded
when the child exits or connection settings change. To use authentication with
an independently managed server, configure it explicitly in HTTP(S) or SSH
settings; direct browser credential forwarding requires HTTPS.

The client polls `GET /api/v1/usage`; Refresh reads the server's current cache,
not a forced provider refresh. Polling defaults to 60 seconds and can be adjusted
in settings. Requests time out, refuse redirects, and retain last
readings on failure with a visible offline banner. Empty and malformed responses,
expired provider authentication, and rejected bearer tokens have separate states.
Failures back off exponentially up to five minutes (or the configured interval
if longer), with normal polling restored after success. Manual Refresh bypasses
the wait.

Configuration is saved atomically at `~/.config/Headroom/Headroom/settings.json`
on Linux, honoring `XDG_CONFIG_HOME`, `~/Library/Application Support/Headroom/Headroom/settings.json`
on macOS, and `%APPDATA%\Headroom\Headroom\settings.json` on Windows. Linux and macOS settings and migration backups use owner-only (`0600`)
permissions. Windows uses the current user's roaming application-data directory;
POSIX mode bits are not used as a claim about Windows ACLs. These files contain
the bearer token in plaintext. Tokens never appear in the exposed UI state or error messages. Leave the token
field blank to keep the saved token for the same address. Changing the address
clears the old token unless a new one is entered. Select **Remove saved token** to clear it.

The remote server must accept connections from your machine. The API server's
default bind is loopback; consult [server configuration](../../server/README.md)
for its listen address and mandatory authentication on non-loopback binds.

## Provider ordering and tray

Provider meters are the first content in the popup. A compact sticky footer
contains provider filtering, Refresh, connection status, and Settings, keeping
controls available while the meters scroll. Providers occupy full-width rows. Meters share the available width and wrap
within their row on smaller windows. In the stacked layout, the first meter
always spans the full width, with remaining meters below it; the final meter
fills any remaining columns. All providers use the same meter colors, with
warning colors reserved for high usage and over-pace indicators. Hovering a
provider highlights its border while preserving contrast with the meter tracks.
Drag a provider panel onto another panel. Drop in the upper half to insert
before it, or the lower half to insert after it. A line marks the insertion point.
Rows read top to bottom. The first provider always supplies the tray meter; the full order
is saved across restarts and polls. Right-click a provider icon or title to move a provider
directly to the top. Orders are local to each client. A provider without usable
data produces an unknown tray meter instead of silently switching providers.

On managed Windows installations, the stable launcher owns the tray icon with a
persistent GUID, so versioned desktop updates keep the same Windows tray identity.
The desktop sends rendered icons and notifications over inherited private pipes;
the helper forwards clicks and physical icon geometry. It exits with its parent
or when its pipe closes, and the desktop stops it before update shutdown. Qt's
tray remains the fallback if the validated helper cannot start. Source and
isolated sessions continue using Qt directly. Users may need to show the new
stable icon once when upgrading from an older build; the app never rewrites
Windows tray preferences.

The tray shows the first provider's first usage bucket as a ring around a
centered provider logo, without a numeric label. A white tick marks expected pace;
a secondary dot shows the highest warning among that provider's other meters.
The tooltip has two lines: provider/primary usage, then reset time and warning
level when needed. Connection errors replace those details with a short status.
When the server cannot be reached, a red X replaces the tray's provider logo.
Offline dashboard cards, the empty connection panel, and the footer divider turn
red while retaining the last readings. Their normal appearance returns on recovery.

When the ring or secondary dot enters Critical, the tray briefly catches fire
for four seconds, then flashes slowly for three minutes. Opening or focusing
Headroom, clicking or scrolling the tray, or opening its menu acknowledges the
warning and stops the animation. Hovering also stops it when the desktop exposes
tray hover or icon geometry (Windows and supported X11 trays). KDE/Wayland trays
use the other engagement actions because their protocol does not report hover.
The static warning color remains until usage recovers. Polls and reconnects do
not restart an acknowledged warning; a new transition into Critical can alert
again. A focused dashboard suppresses the animation.

Click the tray icon to open or hide the frameless dashboard beside it. The popup
stays above ordinary windows and dismisses when focus moves outside the app;
settings and transient menus keep it open. KDE/Wayland uses native activation
coordinates and LayerShellQt placement, accommodating any panel edge and
clamping the popup to the selected monitor. Without a known icon position,
opening from the app menu uses the lower-right of the active screen. There is
no native title bar, minimize/maximize controls, or taskbar entry in tray mode.
The window has softly rounded, transparent corners, including its footer and dialog overlays.
Drag any edge or corner to resize the window. Headroom remembers the selected
dimensions in its existing settings file and reuses them when the popup is
reopened or the app is launched again. Restored sizes are constrained to
460 × 420 logical pixels where the screen permits, an absolute 1600 × 1200
ceiling, and the current screen's available area with a 24-pixel margin on every
side. This keeps every edge reachable after display, resolution, or scale changes.
Use **Quit Headroom** from the tray menu or Ctrl+Q to exit. A second launch in the
same user and configuration scope opens the existing popup. An explicit `--config`
path uses its own instance scope and never imports or changes the normal profile.
`--background` starts hidden when a system tray is available.
On desktops without a tray, the frameless app opens as a regular window and
closing it exits normally.

Notifications fire on upward transitions into Warning or Critical, once per
transition. They can be disabled in settings. The client
preserves every API bucket (up to the contract's 12 per provider), including
model-specific and billable meters. Status-only on-demand buckets are labeled
without an invented percentage.

Keyboard shortcuts: **Ctrl+R** refreshes, **Ctrl+,** opens settings, **Ctrl+Q** quits,
and **Escape** closes an open overlay, or hides the window to the tray when one is available.
On macOS, Qt maps those Control shortcuts to the standard Command key.

## Desktop settings and diagnostics

**Start Headroom when I sign in** enables an XDG autostart entry on Linux or the
current user's `Headroom` Run entry on Windows, launching the quoted executable
with `--background`. On macOS it atomically manages the owner-only
`~/Library/LaunchAgents/io.headroom.Headroom.plist` entry for the next login.
This toggle is disabled in capture
and isolated-config modes. On the first normal Windows launch, Headroom imports
schemas 0–3 from `%APPDATA%\ClaudeUsageWidget\settings.json` only when the new
settings file is absent. The legacy file remains untouched and a create-once
backup is kept beside the new settings. Imported empty API addresses retain local
mode. Local mode probes and attaches to a compatible server on `127.0.0.1:7823`,
or starts an adjacent bundled `usage-server` executable with bounded readiness and
restart handling. Headroom stops only a server process that it started.

Settings shows the app version and checks the configured server's authenticated
health/version endpoint. Official per-user packages perform one delayed startup
check and automatically download, verify, and stage a newer matching Headroom
bundle without sending the backend bearer token. The settings panel also supports
manual checks, staging, and exact-version repair when a trusted install is
missing its server or Windows credential helper. **Restart to apply** revalidates
the stage, switches to a new immutable generation, accepts readiness only from
the expected process, and rolls back if startup fails. An interrupted switch is
recovered on the next launch. Source builds use the installed source update
guide, while system-managed builds defer to their package manager and never
write into a per-user package installation. Capture and explicit-config sessions
cannot check or download public releases.

The legacy WinForms updater cannot install a Headroom package. Existing users
run the Headroom installer once, then the settings import and startup migration
take place on the first normal launch. See the
[upgrade guide](../../docs/upgrading-to-headroom.md) for the complete transition
and rollback steps.

Rerunning an external installer replaces the verified on-disk generation. If a
Headroom window is already open, quit and reopen it afterward; a new launcher
invocation may activate the existing primary process until that process exits.

**Open diagnostics** shows the last 500 events in this session, with UTC times,
categories, Copy log, and Clear. It records controlled connection/settings/tier
summaries, never raw requests, response bodies, tokens, URLs, or account details.
Nothing is written to a log file by this console.

## Verification

`--config PATH` selects an alternate settings file. Screenshot capture does not
start or poll a backend, so an empty isolated configuration renders the
disconnected setup interface without exposing live readings.

```bash
ctest --test-dir clients/desktop/build --output-on-failure
QT_QPA_PLATFORM=offscreen QT_QUICK_BACKEND=software \
  clients/desktop/build/headroom --config /tmp/headroom-settings.json \
  --screenshot /tmp/headroom.png
```

Tests cover API parsing, legacy bucket fallback, URL handling, authenticated
requests, secret-free localhost discovery, offline data retention, settings
permissions, order persistence, and actual pointer-driven drag and drop in the
QML window. Tests bind a loopback socket and need permission to do so. UI tests
render the desktop, settings, and
compact views with Qt's software renderer. Desktop tray integration still
requires a real desktop session.

Provider assets and their sources are documented in
[the shared icon directory](../shared/provider-icons/README.md).

## Pacing

Every measured usage bar includes a pale marker at its estimated expected usage:
`100 × elapsed time / window length`. The text compares actual usage with that
baseline in percentage points (pp). “10 pp over pace” means usage is ten points
higher than the fraction of the window elapsed; “under pace” means capacity is
being consumed more slowly. Hover the bar or comparison for exact percentages.
The marker advances on the client's 30-second clock, including between polls.

The current API exposes reset times but not period starts. Following the Windows
client's pacing conventions, Headroom estimates five hours for Claude/ChatGPT
sessions, seven days for weekly buckets (including model-specific and Grok Bot
buckets), 30 days for Cursor billing pools, and the previous calendar month for
Grok credits. These are pacing estimates, not provider-guaranteed rate forecasts.
A Claude weekly fallback labeled “Weekly” uses seven days even in a session slot.
Meters with no reset, unknown duration, a reset outside the expected window, or
an expired window show “Pace unavailable.” Status-only billing rows have no
percentage or pacing marker. The existing Windows pacing indicators are retained.

## Windows feature comparison

Headroom uses the server's provider names, subtitles, bucket labels, variable
meter counts, status text, and authentication errors. Live accounts may return
different bucket layouts. Both clients retain pacing and provider ordering.
When ChatGPT reports one or more banked usage resets, Headroom shows the available
count at the right of its weekly meter's bottom text row. The label turns red only
while ChatGPT's weekly meter is in the shared Critical warning state. The count is read-only, is not a usage
meter, and stays hidden at zero, when unknown, or when that provider is unavailable.
Activate the label to open ChatGPT's usage and reset controls in a browser.
At 95% weekly usage or higher, a **Use reset…** button appears when a banked reset
is available. It requires explicit confirmation and uses the selected backend
transport. Each confirmed request is submitted once, without retries. The button
stays disabled after submission, including after a connection failure or app
restart, until fresh usage for the same account falls below 95%. Headroom schedules
read-only usage refreshes after submission to pick up the reset's effect. The
button can appear again when weekly usage subsequently reaches 95%. Both the
desktop and server must support resets.

Headroom supports both remote connections and an owned local usage server on
Windows, macOS, and Linux. Windows can forward supported Cursor and Grok browser cookies
from Chrome, Edge, Brave, and Firefox through the bundled helper, but only to
the verified bundled server session, a configured HTTPS server, or the SSH
receiver. The helper
uses the current Windows user's browser encryption context; it cannot read other
users' profiles or bypass unsupported newer encrypted values, and macOS and Linux have no
browser helper. Official per-user packages
share the verified update flow described above. The WSL service remains a
separately managed deployment documented in
[the server deployment notes](../../server/deploy/wsl/README.md).

## Appearance and provider names

The interface uses the [Dracula palette](https://draculatheme.com/contribute),
with purple usage bars shared by every provider, white pacing markers, cyan under-pace text,
yellow, orange, and red concern levels based on the remaining allowance and time. Backgrounds, controls, settings, app icon, and tray
use the same palette. Muted text and surface shades are adapted for readability.

The server's `Codex` provider is displayed as **ChatGPT** throughout Headroom.
Its API identifier and saved ordering key remain `Codex`, preserving existing
connections, pacing calculations, and provider preferences.

## Period divisions and parity

Meter notches represent hours in five-hour windows, days in weekly windows,
and seven-day boundaries in monthly windows. Monthly meters leave a shorter
final segment after day 28 when needed. Hover a notch to see its boundary.
The notches and pacing marker use the same period calculation. Cursor uses a
30-day billing estimate; Grok monthly meters use a calendar-month estimate.
Unknown periods have no time notches.

See the [desktop parity audit](../../docs/desktop-parity.md) for restored features,
remaining omissions, and intentional differences.

## Pacing-aware warning colors

The original Windows UI switches between two fixed utilization threshold sets
depending on whether usage is above or below the expected pace. Headroom instead
measures how much of the allowance for the **remaining** window has already been
spent ahead of schedule:

```text
pressure = max(0, (used_percent - elapsed_percent) / (100 - elapsed_percent))
```

Pressure below 10% stays neutral purple; 10% is yellow, 25% orange, and 50% red.
These are presentation thresholds, not limits imposed by a provider. A tiny
positive pacing difference keeps neutral text and does not add a provider to
the attention count. Tooltips show the remaining allowance, time, and calculation.

| Used | Window elapsed | Remaining allowance spent early | Color |
| --- | --- | --- | --- |
| 12% | 10% | 2.2% | Purple |
| 82% | 80% | 10% | Yellow |
| 95% | 90% | 50% | Red |
| 90% | 90% | 0% | Purple |

A separate low-capacity floor applies even on/under pace: 95% used is at least
yellow, 99% at least orange, and 100% red. Without a usable reset window, the
original percentage fallback applies: yellow at 50%, orange at 75%, red at 90%.
Expired or unrecognized windows do not produce an invented pacing estimate.

The dashboard, bar fills, percentages, pacing text, attention count, and primary
tray meter use the same state machine and update as time passes. This
compares against even spending across a window; it does not infer a recent burn
rate or predict future activity.

### Shared state machine

`warning.cpp` defines one tier table: thresholds, colors, labels, and whether an
upward transition alerts. The controller owns one state per provider/bucket and
reset window. QML and the tray read that state; they do not decide severity
independently. The meter's normal 30-second clock also permits recovery as time
passes without additional spending.

| Tier | Enter at pacing pressure | Recover below | Color | Pop-up on upward entry |
| --- | --- | --- | --- | --- |
| Normal | Below Watch | — | Purple | No |
| Watch | 10% | 8% | Yellow | No |
| Warning | 25% | 20% | Orange | Yes |
| Critical | 50% | 40% | Red | Yes |

Low-capacity guards also have hysteresis: Watch enters at 95% usage and releases
below 94%; Warning at 99% / below 98%; Critical at 100% / below 99.5%. With no
pacing, fallback tiers enter at 50/75/90% and release below 45/70/85%. A tier
recovers only when neither its pacing condition nor its capacity guard holds.
Escalation can jump directly to any tier; recovery can skip tiers as well.

Initial connection and each new reset window establish a baseline without
notification bursts. Later upward transitions into Warning or Critical notify
when enabled. Unchanged polls, downward transitions, and toggling notifications
back on do not notify. A genuine recovery followed by escalation
can alert again. Provider order changes do not reset state or re-arm alerts.

Connection failures freeze state. Expired known windows retain their state until
a replacement window arrives, avoiding false percentage-only alerts from old
readings at reset time. Provider failures retain prior bucket state until valid
data returns; removed buckets/providers are discarded. States are session-local;
restarting the app establishes a new baseline.
