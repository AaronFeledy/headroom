# Headroom in a terminal

The public `headroom` command and desktop app use the same installed package.
Run `headroom` for a live terminal dashboard, or `headroom desktop` to open the
tray app. The terminal shows provider meters, used and remaining allowance,
reset countdowns, estimated pace, period markers, and warning tiers.

```sh
headroom                         # live dashboard in a terminal
headroom --once                  # one snapshot
headroom --plain                 # one snapshot without color or cursor controls
headroom --json                  # API JSON, once even in a terminal
headroom --watch --json          # newline-delimited snapshots until interrupted
headroom --weekly-only           # weekly meters only
headroom --ssh usageuser@server.example
headroom --url https://usage.example
headroom serve --config /path/to/config.yaml
headroom update
headroom version
```

Redirected output prints once unless `--watch` is supplied. `NO_COLOR` disables
color; `TERM=dumb` disables automatic live refresh and cursor controls. Live
mode retries after an outage and keeps the last successful readings with an
offline label. Before the first successful response it shows an unavailable
state. It never invents usage.

With no explicit address, the CLI first asks the running desktop for its
read-only snapshot, preserving the desktop's selected connection and provider
order. If no desktop is running, it reads `http://127.0.0.1:7823`. It does not
start a server just to display usage. `headroom serve` starts the server
explicitly. Explicit `--ssh` or `--url` options override discovery.

The terminal uses the desktop warning policy: pressure relative to the remaining
window, capacity guards near the limit, and lower exit thresholds to avoid
flickering between tiers. Period lengths remain estimates when the API provides
only a reset timestamp. Positive banked-reset counts are read-only metadata.

## Connections

SSH uses system OpenSSH, trusted host keys, and key/agent authentication. The
server needs SSH access enabled as described in [SSH setup](ssh.md). The fixed
receiver remains `usage-server --ssh-stdio`; the managed CLI install supplies
that compatibility entry on Linux and macOS. The CLI never falls back from SSH
to HTTP and never requests a password interactively.

Direct remote HTTP(S) authentication reads `HEADROOM_AUTH_TOKEN` from the
environment, or `HEADROOM_AUTH_TOKEN_FILE` on Linux/macOS. A token file must be a
regular file readable only by its owner and contain one nonempty line. Set only
one of those variables. Windows currently supports the environment variable.
Tokens are not command-line arguments. Localhost discovery never sends either
token, and HTTP redirects are rejected. Use HTTPS for a direct remote connection.

## Install without a desktop

CLI-only packages contain the usage client, server, and package manager without
Qt. They support Windows x64/ARM64, Linux x86_64/ARM64, and macOS Intel/Apple Silicon.

```sh
curl -fsSL https://raw.githubusercontent.com/AaronFeledy/headroom/main/install.sh | sh -s -- --cli
```

On Windows, download the official `install.ps1` and run it with `-CLI`.
Installation does not automatically start a server. Run `headroom serve` with
your existing server configuration. The existing YAML keys, environment
variables, default bind address, and API remain compatible; see the
[server guide](../server/README.md).

The default command location is `~/.local/bin/headroom` on Linux/macOS and
`%LOCALAPPDATA%\Headroom\cli\headroom.exe` on Windows. Add `~/.local/bin` to your
shell's PATH if necessary. The Windows installer adds its CLI directory to the
current user's PATH; open a new terminal after installation.

Existing desktop installations can continue updating through the desktop.
Rerun the updated official installer once to install the public CLI entry and
its PATH integration. Settings and provider configuration are retained. Quit
and reopen an already-running Qt app after an external reinstall.

## Updates

`headroom update` uses the same verified packages and transactional updater as
the desktop. On a shared native installation, either entry point updates the
desktop, CLI, and bundled server together. A running desktop coordinates its
own restart; a closed desktop stays closed after a CLI update. CLI-only installs
download CLI-only packages.

Source checkouts, unmanaged binaries, and system-managed installations do not
use public self-update acquisition. Update those with their original deployment
method. A server outside the managed installation is not adopted merely because
it responds on localhost.

With an SSH connection, **About & Updates → Update server** runs the fixed
`headroom update --this-install-only` command on the selected SSH host. Install
the official CLI there and make `headroom` available in that account's SSH PATH.
The remote installation performs its own package verification, restart, and
rollback; the desktop waits for the connected server to report the expected
version before reporting success. Only that remote installation is updated;
Windows/WSL pairing is a separate action described below.

The action requires a normal installed desktop session and SSH key/agent
authentication that permits the update command. A key restricted to the usage
receiver cannot update the server. Headroom does not broaden the key's permissions,
send a bearer token, invoke sudo, or add an update HTTP endpoint. Source, capture,
explicit-config, and system-managed desktop sessions do not run this action.
Progress and authored error summaries appear in desktop diagnostics. If SSH drops
or verification times out, the result is uncertain and the update may still
finish; check the server before retrying. Only health reads are retried
automatically, never the update command.

Direct HTTP(S) connections still need `headroom update` on the server's machine,
or an explicitly paired Windows/WSL installation. An older remote server is
called out in the footer and About/settings view. Version checks use the selected
connection and refresh after reconnecting.

## Linux and WSL automatic server startup

Managed `headroom serve` processes register their exact executable and process
identity with the local updater. An update stops that registered server after
commit and starts the new version before reporting success. The same rule applies
to the fixed **per-user** systemd unit `headroom.service`; arbitrary service names
and system-wide units are not controlled by the updater.

For an official CLI installation at the default path, install the supplied
[headroom.service](../server/deploy/wsl/headroom.service):

```sh
mkdir -p ~/.config/systemd/user
cp server/deploy/wsl/headroom.service ~/.config/systemd/user/headroom.service
systemctl --user daemon-reload
systemctl --user enable --now headroom.service
systemctl --user status headroom.service
```

Run these commands from the checkout containing that sample file, or download
the sample first. Keep your existing private `config.yaml` and optional
`server.env` in `~/.config/claude-usage-widget/`. Adjust `ExecStart` for a custom
CLI path or config location. Do not rename the unit. The update command uses
only the current user's fixed unit and never invokes sudo.

For startup before login, enable lingering for the service account with
`loginctl enable-linger "$USER"` (your system may require administrator approval).
WSL also needs [systemd enabled](https://learn.microsoft.com/en-us/windows/wsl/systemd).
This unit starts when the distribution starts; it does not launch WSL at Windows
boot or keep the distribution alive on its own. Retain your existing Windows
mechanism for starting WSL.

To migrate the old system-wide `usage-server.service`, stop and disable that
specific unit before enabling the per-user one. Keep any credential-sync timer
that is still needed. If `~/.local/bin/usage-server` is an unmanaged old binary,
move it to a private backup after stopping its service, then rerun the CLI
installer. The installer refuses to overwrite an unrelated compatibility entry.
Existing system-wide deployments can continue using their original deployment
method instead of opting in to managed updates.

## Windows desktop with a WSL server

Install the desktop package on Windows and the CLI-only package in WSL. Associate
them explicitly; a matching hostname or localhost URL does not establish update
ownership. From either installed CLI, specify both public entries and the exact
WSL distribution and user:

```text
headroom pair windows-wsl --windows-entry "C:\Users\Example\AppData\Local\Headroom\cli\headroom.exe" --wsl-distribution Ubuntu --wsl-user usageuser --wsl-entry /home/usageuser/.local/bin/headroom
```

Pairing uses native WSL/Windows process invocation, with no SSH or update HTTP
endpoint. It stores reciprocal private records and transfers only installation
and update metadata. It does not change the desktop's usage connection settings.

After pairing, `headroom update` from either CLI, or **Restart to apply** in the
Windows desktop, verifies the same release for both installations before applying
either. The paired installation applies first; the initiating installation
applies after the other reports the exact target version. Each native updater
retains its own readiness checks and rollback.

There is no atomic rollback across Windows and WSL. If one side updates and the
other fails, a private progress record preserves the exact unfinished target.
Run `headroom update` again to resume. If the paired side's outcome cannot be
verified, use the exact version printed by the error, for example
`headroom update --this-install-only --version 2.1.0` there, then retry the
original command. This stages that release instead of accidentally advancing to
latest. `--this-install-only` explicitly bypasses pairing for recovery; omitting
`--version` still selects the latest compatible release for that installation.

If either side has already advanced past the unfinished version, run
`headroom update --reconcile` on the original initiating installation. This
explicitly chooses the latest common release after verifying both current
installations. It stages both sides before replacing the unfinished progress
record or applying either side, never downgrades, and keeps the old progress if
verification or staging fails. The normal `headroom update` command continues
to preserve the exact unfinished target.

An interrupted pairing handshake is not an active pair. Complete the original
pairing command before using paired updates, or explicitly select
`--this-install-only` to update locally. The desktop retains its downloaded
package when pairing needs attention.

## Build from source

```sh
cd packaging/headroom-manager
go build -o headroom ./cmd/headroom
./headroom --once
./headroom serve --help
```

This builds the public runtime against the shared `server/app` package. Source
builds keep `headroom update` disabled; use Git and rebuild them. The standalone
`server/cmd/usage-server` build remains available for existing deployments.
