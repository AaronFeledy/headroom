# Headroom for Stream Deck

Two Stream Deck actions for Windows and macOS:

- **Usage Meter** shows Claude, ChatGPT, Cursor, or Grok usage as a ring around
  the provider logo, with a white pace marker and warning colors.
  Press the key to open Headroom for exact percentages and reset times.
- **Open Headroom** opens the desktop app without displaying a meter.

Requires **Stream Deck 7.1+** and an installed Headroom desktop package with the
unified CLI (supporting `headroom --json --once` and `headroom desktop`).
macOS requires 14.4+. Node.js does not need to be installed separately.

## Install and configure

1. Install Headroom with the [current installer](../../README.md#install).
   If Headroom is running during a reinstall, quit it and reopen it afterward.
2. Double-click the built `.streamDeckPlugin` file and accept Stream Deck's
   installation prompt.
3. Drag **Headroom → Usage Meter** onto a key. Choose a provider and meter.
   Add more keys for other providers or meters.
4. Keep Headroom open to follow the connection and account shown in the desktop.
   Press any usage key, or add **Open Headroom**, to bring up the app.

The default CLI locations are:

| Platform | Stable CLI |
| --- | --- |
| Windows | `%LOCALAPPDATA%\Headroom\cli\headroom.exe` |
| macOS | `~/.local/bin/headroom` |

For custom installations, set **Headroom CLI path** on each key to the absolute
path of the stable CLI, without quotes or arguments. On Windows, use
`cli\headroom.exe`; the top-level executable launches the desktop.
Avoid versioned internal executables, which updates replace.

**First available** selects the provider's first usage meter. Choosing a
specific meter never falls back to another meter if it disappears. Available
meters vary by provider and plan. **Custom meter** accepts IDs such as
`weekly_sonnet` or `weekly_grok_bot` returned by Headroom.
The UI displays ChatGPT; its API identifier remains `Codex`.

## Connection and privacy

Visible keys read usage every 15 seconds. Keys with the same CLI path share
requests; hidden keys stop polling. The plugin runs `headroom --json --once`.
Reading usage does not refresh providers, spend reset credits, start a server,
install updates, or change accounts.

The CLI reads the active desktop's private snapshot and follows its selected
Local, SSH, or HTTPS connection. When no desktop is running, the CLI
can try the unauthenticated localhost server. A failed desktop bridge does
not switch transports. To monitor a remote account, configure it in Headroom and
leave the desktop running.

The plugin stores key preferences and an optional CLI path in Stream Deck.
Credentials and remote connections stay in Headroom. It does not log usage or
provider errors. CLI calls use fixed arguments without a shell, limit output
and execution time, and strip credential, transport, and installer overrides
from the inherited environment.

## Reading the key

Unavailable readings show a muted ring and a status: **Offline**, **Sign in**,
**Retrying**, **No data**, **Disabled**, or **No meter**. If the CLI reports an
offline connection, the key hides cached usage.
**Setup** means the CLI could not be found or its path is invalid. A warning
triangle after a press means the desktop launch failed. Open Headroom for
connection and sign-in details.

The ring fills clockwise from the top as usage increases. The white tick marks
expected usage at this point in the window. Past the tick means over pace;
before it means under pace. Choose meters in the settings panel and press the key for percentages and reset
times in Headroom.

A usage increase triggers one soft 900 ms pulse in the ring's current color,
including increases of less than one percentage point. The pace tick stays fixed.
First readings, unchanged usage, decreases, resets, account or meter changes, and
reconnections do not trigger a pulse. It reflects changes between 15-second
readings, so it cannot show each individual request.

Entering **Critical** triggers three strong red pulses over 2.4 seconds. The
background and border glow and the usage arc swells; the logo and pace marker
stay fixed. This replaces the gentle pulse and plays once per transition.
It can trigger again after the meter recovers below Critical. Opening a key that's
already critical, switching accounts or windows, and reconnecting do not trigger
an alert. Recovery below Critical stops the alert. Both animations stop when
the key is hidden, its settings change, or data becomes unavailable.

Pacing follows the desktop's provider-reported windows and its session, weekly,
Cursor billing, and Grok calendar-month estimates. Unknown, expired, or invalid
windows do not get a tick. Status-only on-demand buckets do not become 0% meters.

Colors match the tray: Normal (purple), Watch (yellow), Warning (orange), and
Critical (red). The same thresholds account for near-exhausted limits and prevent
colors flickering around a boundary. Without timing, colors depend on usage
alone and a small **?** marks unavailable pacing. Warning history resets on a new
window, account, connection setting, or unavailable reading.

## Build and verify

Use Node.js 22 and npm. From this directory:

```sh
npm ci
npm run check
npm run build
npm test
npm run validate
npm run pack
```

The default development build is
`dist/headroom-streamdeck-0.0.0+dev.streamDeckPlugin`.
For a versioned build, pass the same stable SemVer used for Headroom:

```sh
npm run build -- --version 2.3.4+example
npm test
npm run validate
npm run pack
```

The numeric manifest version is `2.3.4.0`; the complete SemVer, including
metadata, remains in `headroom-version.json` and the package filename.
Generated output and dependencies are ignored by Git.

Packaging uses the [official Elgato CLI](https://docs.elgato.com/streamdeck/sdk/introduction/distribution/).
The package includes the SDK, settings UI, and third-party runtime licenses.
App icons use Headroom's SVG; provider marks use the shared desktop assets and
include their attribution.

Tests cover malformed data, missing meters, authentication/offline states,
escaping, process boundaries, shared polling, and settings changes while a read
is in flight. A local WebSocket test host runs the built plugin,
including registration, images, settings, and key presses. Unix tests launch a
disposable fake CLI; Windows protocol tests exercise setup and launch failure.
No test connects to a live provider or consumes reset credits.

`.github/workflows/streamdeck.yml` checks the plugin on Windows, macOS, and
Linux and saves an installable package. It does not publish to Marketplace,
create tags, or alter Headroom's native release workflow.

Before distribution, manually check on Windows and macOS with a physical Stream
Deck: install the package, add several keys, choose different meters, switch
profiles, disconnect/reconnect a device, open Headroom when running and stopped,
and check Local/SSH/HTTPS plus offline and signed-out states. The automated tests
do not cover physical hardware or Stream Deck's settings UI.
