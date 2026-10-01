# Upgrade from Claude Usage Widget to Headroom

Existing Windows users can switch to Headroom with a one-time installation.
Headroom imports supported settings and startup preferences, and keeps the old
settings intact. Current downloadable Headroom packages are available from
[v2.0.0](https://github.com/AaronFeledy/headroom/releases/tag/v2.0.0) onward.
Historical v1.x release records remain, but their binary assets have been
retired.

The old app's **Check for updates** does not perform this transition. Its updater
looks for `ClaudeUsageWidget-win-x64.exe` or `ClaudeUsageWidget-win-arm64.exe`;
Headroom ships a complete Qt package with a different installation layout.
An automatic upgrade bridge for those already-installed legacy executables has
not been shipped. After the one-time installation, Headroom's own updater handles
future Headroom releases.

## Windows upgrade steps

1. Quit Claude Usage Widget from its tray menu. Keep its settings and backend
   configuration in place. The installer also stops the old executable at
   `%LOCALAPPDATA%\ClaudeUsageWidget\ClaudeUsageWidget.exe`, but does not stop
   portable copies or copies installed elsewhere.
2. Run the installer in PowerShell 5.1 or newer under the same Windows account
   that ran the old app:

   ```powershell
   irm https://raw.githubusercontent.com/AaronFeledy/headroom/main/install.ps1 | iex
   ```

   The installer chooses Windows x64 or ARM64 automatically, validates the
   complete package, installs under `%LOCALAPPDATA%\Headroom`, and creates a
   **Headroom** Start menu shortcut. It removes the old Start menu shortcut only
   if that shortcut points at the standard legacy installation.
3. Let the installer launch Headroom normally. If it was already open, quit and
   reopen it to use the newly installed version. Automatic import runs only when
   neither the new nor previous Headroom settings file exists; screenshot and
   explicit `--config` sessions do not copy or import settings automatically.
4. Check Connection settings, provider order, and **Start Headroom when I sign
   in**. Existing HTTP(S) connections stay selected. A fresh installation or an
   empty legacy API address uses Local mode. Choosing SSH is a separate setup
   step, described in the [SSH guide](ssh.md); it is never selected in place of
   an existing connection automatically.

## What is carried over

The importer reads schema versions 0–3 from
`%APPDATA%\ClaudeUsageWidget\settings.json` and writes
`%APPDATA%\Headroom\settings.json`.

Existing Qt settings are copied once from
`%APPDATA%\Headroom\Headroom\settings.json` when the new file is absent. Linux
copies from
`${XDG_CONFIG_HOME:-$HOME/.config}/Headroom/Headroom/settings.json`, and macOS from
`~/Library/Application Support/Headroom/Headroom/settings.json`, to
`${XDG_CONFIG_HOME:-$HOME/.config}/headroom/settings.json`. Sources are retained;
later saves use the new file. A failed copy keeps using the previous file for
that session and records a notice in diagnostics. Single-instance identity stays
on the previous path, including for the CLI bridge.

| Existing preference | Headroom behavior |
| --- | --- |
| `ApiUrl` | Keeps the configured HTTP(S) address; an empty address uses Local mode |
| `ApiToken` | Preserved for direct HTTP(S) connections; never sent during local HTTP discovery or through SSH |
| `RefreshIntervalSeconds` | Preserved within the supported 15–900 second range |
| `NotificationsEnabled` | Preserved |
| `ProviderOrder`, `PrimaryProvider` | Preserves supported provider order, removes duplicates, appends missing providers; the first provider drives the tray meter |
| `StartWithWindows` | When enabled, registers Headroom at sign-in, then removes the old app's Run entry after the replacement succeeds |
| `DebugMode` | Not imported; use Headroom's built-in diagnostics |

The original file is left untouched. A create-once copy is stored beside the new
settings as `settings.json.legacy.bak`, including fields the importer does not
use. Existing Headroom settings always take precedence; reinstalling does not
overwrite them or re-import changes subsequently made in the old app.

If the legacy settings are malformed, use an unsupported schema, or cannot be
backed up, Headroom reports the problem and leaves the source intact. Startup
migration failures are shown in settings and remain retryable on a later launch.

For existing Headroom installs, normal launch also repairs an enabled startup
entry that points directly at an older installed version: it switches that exact
Headroom command to the stable launcher for the same installation. This ensures
future sign-ins follow updates. Disabled entries, capture sessions, and custom or
unrelated startup commands are left alone.

## Existing servers and credentials

Existing Headroom desktop users should rerun the updated official installer once
to add the unified `headroom` CLI entry and PATH integration. On Linux this moves
the desktop entry inside the managed root so bare `headroom` opens the terminal
dashboard; application-menu and login entries continue opening the desktop.
Quit and reopen an already-running Qt app after this external reinstall.

Standalone servers can keep their current deployment or install the CLI-only
package and run `headroom serve` with the same configuration. Stop the old server
before switching its launch command. The managed Linux CLI also provides the
`usage-server` compatibility entry for SSH stdio receivers. A pre-existing
unmanaged executable is not silently adopted as an updater-owned process.
For managed self-updates, migrate the old system-wide service to the fixed
per-user `headroom.service`. See the [CLI guide](cli.md) for that one-time cutover
and coordinated Windows/WSL updates.

Changing the frontend does not require moving or renaming an independently
managed backend. The `usage-server` executable name, HTTP API, provider keys,
and legacy server configuration paths remain compatible. New defaults are:

- Windows server config: `%APPDATA%\Headroom\config.yaml`
- Unix server config (including Linux/WSL/macOS): `${XDG_CONFIG_HOME:-$HOME/.config}/headroom/config.yaml`
- Custom `--config` paths and existing service definitions can stay in place.

Unix startup automatically renames the legacy config directory when the new one
is absent and leaves a compatibility symlink, preserving `config.yaml`,
`server.env`, and other entries. Existing units referencing the legacy path keep
working and may be updated to the new path. If the desktop has already created
the new folder, Unix startup merges nonconflicting legacy entries into it and
leaves the link when the old folder is empty. Conflicting names stay in the old
folder with a warning; the new config wins. Windows default-path startup copies
the legacy config without removing it or the legacy desktop settings.

Provider credential files stay on the machine running the backend. They are not
copied by the frontend settings importer. Headroom displays the API's `Codex`
provider as **ChatGPT**, so existing server settings and Home Assistant sensors
do not need their provider keys renamed.

Browser credential recovery now requires the verified bundled local session,
HTTPS, or an SSH-capable client and backend. An existing plain HTTP connection
can still supply usage, but browser cookies will not be forwarded to it. If an
independently managed local server requires a bearer token, configure its address
and token explicitly under **HTTP(S)** (called **Remote** in v1.8.0). See the
[connection guide](../clients/desktop/README.md).

SSH is an optional server upgrade: install a version with SSH support, enable
`ssh_access: true`, and follow the same-account and host-key setup in the
[SSH guide](ssh.md). Installing the frontend does not enable SSH on a remote
machine or update its service automatically. The original v1.8.0 Headroom release
predates SSH support.

## Rollback

Quit Headroom before launching the old executable. The installer retains the
legacy installation and its settings; it does not copy subsequent Headroom
setting changes back into the old file. Disable Headroom's sign-in option and
re-enable the old app's startup option if you want to keep using the old client.

Headroom-to-Headroom updates use verified packages and transactional rollback if
the newly selected generation fails to start. That mechanism applies after the
initial migration; it is separate from the manual return to the legacy app.
