# Headroom desktop parity audit

Audited against the retained Windows WinForms source on 2026-09-09. Headroom is
the shared Qt desktop for Windows and Linux. The older client remains a reference
and rollback implementation; differences below are intentional unless stated.

## Shared product behavior

| Area | Headroom behavior |
| --- | --- |
| Provider data | Preserves all API buckets, labels, subtitles, status text, reset times, provider errors, and authentication guidance; `Codex` is displayed as ChatGPT and `OpenCodeGo` as OpenCode Go |
| Provider order | Saves the complete order; drag and drop or move a provider to the top; the first provider controls the tray meter |
| Pacing | Uses five-hour, weekly, Cursor 30-day, and Grok and OpenCode Go calendar-month estimates when reset data permits; dashboard, tray, attention count, and notifications share one warning state |
| Warning transitions | Applies pace pressure, capacity guards, hysteresis, and session-local baselines; alerts only on upward Warning or Critical transitions |
| Polling | Retains last good data on failure, distinguishes setup/auth/network/provider states, backs off to five minutes or the configured interval, and lets manual Refresh bypass the delay |
| Local mode | Probes `127.0.0.1:7823` without secrets; attaches without saved auth, or starts the adjacent bundle with a private, verified TLS session; owns and stops only its child |
| Remote mode | Accepts normalized HTTP(S) base URLs and bearer auth; the server still requires authentication for non-loopback binds |
| SSH mode | Windows/Linux clients use existing OpenSSH keys and trusted host keys to reach an opted-in Linux/WSL backend through its protected socket; saved HTTP settings remain separate |
| Startup | Registers the stable package launcher, or the current executable for a source/system build, with `--background` |
| Diagnostics | Keeps 500 controlled session events with UTC timestamps; copy and clear are available; tokens, URLs, raw bodies, credentials, and account output are omitted |
| Updates | Official packages make one delayed check, automatically download and verify newer native packages, and apply them transactionally on restart; exact-version repair is available for a trusted incomplete auxiliary component |

Both platforms default to Local mode; saved remote settings override it. Windows
can use the packaged current-user helper to forward supported Cursor and Grok
browser cookies to its verified bundled server session, configured HTTPS, or SSH
receiver. The [SSH setup guide](ssh.md) covers the same-account requirement and
server opt-in.
Unlike the retained Windows implementation, Headroom does not send browser
cookies or saved tokens to an unverified attached localhost HTTP listener.
Linux uses server-side credential files or the WSL sync helper.

## Tray and window behavior

Headroom keeps the first provider selected even when its data fails and renders
an unknown tray meter. The legacy client could fall back to another successful
provider. Headroom's tray uses the selected provider's first measurable API
bucket; status-only buckets remain visible in the popup but do not drive the
tray. The legacy tray used the compatibility `current` field.

The centered provider badge is surrounded by the primary usage ring. A white tick
marks expected spend and a secondary dot carries the highest warning among the
provider's other meters. The compact tooltip shows primary usage, reset timing,
and a warning or connection state. Full detail remains in the popup.

Headroom anchors its frameless popup near the tray and dismisses it when focus
moves outside the app. KDE/Wayland uses StatusNotifierItem activation coordinates
and LayerShellQt when those optional source-build dependencies are present.
Official generic packages use Qt tray activation and available window placement;
package smoke tests do not prove interactive placement for every desktop shell.
On a desktop without a tray, Headroom opens as a normal frameless window and
closing it exits.

## Meter layout and appearance

Headroom preserves the legacy client's variable API bucket counts while using a
responsive layout. Rows span the popup width and wrap meters for narrow windows. Period
notches mark hours in a five-hour estimate, days in a weekly estimate, and
seven-day boundaries in a monthly estimate. Unknown periods have no invented
notches or pace marker.

The Dracula-based palette and provider icons are shared across Windows and Linux.
The tray intentionally omits a numeric label. Pressure below 10% remains purple;
Watch begins at 10%, Warning at 25%, and Critical at 50%, with lower recovery
thresholds. Low-capacity and no-period fallbacks are documented in the
[desktop guide](../clients/desktop/README.md).

## Installation differences and limits

Official per-user packages use the same verified manifest and immutable
installation model on both systems. Windows packages include the browser helper,
MSVC runtime, and Start menu shortcut. Linux x86_64 packages target the Ubuntu
22.04 desktop ABI, include X11/Wayland Qt plugins, and add an XDG application
entry. Source and system-managed builds do not use public self-update traffic.

Rerunning an external installer repairs or replaces the on-disk generation. It
launches the stable entry and accepts startup only after the selected generation
reports a matching nonce, process ID, executable path, and compiled version. If
Headroom is already open, that launch may activate the old primary process; the
installer then reports that a restart is required instead of claiming the new
generation started. Quit and reopen Headroom to complete that transition. The
in-app restart path stops and replaces the expected running generation.

## Source references

- [Desktop controller](../clients/desktop/controller.cpp)
- [Desktop warning policy](../clients/desktop/warning.cpp)
- [Desktop tray rendering](../clients/desktop/trayvisual.cpp)
- [Desktop local-server manager](../clients/desktop/managedserver.cpp)
- [Desktop SSH transport](../clients/desktop/sshnetwork.cpp)
- [Desktop updater](../clients/desktop/updateservice.cpp)
- [Desktop startup service](../clients/desktop/startup.cpp)
- [Retained Windows tray behavior](../clients/windows/TrayIcon/TrayApplicationContext.Helpers.cs)
- [Retained Windows updater](../clients/windows/Services/UpdateService.cs)
