# Headroom agent guidelines

## Project overview

Headroom is a shared Qt Quick tray app for Windows, macOS, and Linux plus a Go usage API
server. It monitors Claude, ChatGPT, Cursor, and Grok usage. The compatible API
and configuration name for ChatGPT remains `Codex`.

The canonical repository is `https://github.com/AaronFeledy/headroom`, with the
default checkout name `headroom`. The former repository URL redirects there.
For Go compatibility, module declarations and imports intentionally remain under
`github.com/AaronFeledy/claude-usage-widget`; do not rename them as incidental
branding cleanup.

## Layout

- `clients/desktop/` — primary Qt 6.6+ Windows/macOS/Linux UI, local-server manager,
  Windows credential-helper client, startup, diagnostics, and package updater.
- `packaging/headroom-manager/` — Go archive validator, stable launcher,
  installer, unified CLI, terminal dashboard, native Windows/WSL pairing,
  staged acquisition, transactional apply, rollback, and recovery.
- `packaging/` — per-platform assembly, public installers, pinned attribution
  sources, package contract, and fixtures.
- `clients/windows/` — retained .NET 8 WinForms reference/rollback client and the
  Windows credential-helper sources used by Headroom packages.
- `server/` — Go 1.25 usage API, provider integrations, configuration, and
  Dockerfile. `server/app` is the shared runtime used by `headroom serve` and
  the retained `usage-server` shim.
- `tests/windows/` — Linux-runnable C# lifecycle and browser fixtures.
- `.github/workflows/headroom-packages.yml` — reusable five-desktop/six-CLI package and
  release gate used by PR and release entry workflows.
- `docs/` — migration plan, desktop parity audit, and Home Assistant REST sensor
  guidance.

## Architecture and compatibility

All desktop platforms default to Local mode. Headroom probes `127.0.0.1:7823`, attaches
to a compatible server, or starts the adjacent `usage-server` executable. A saved
remote address overrides this default. Local discovery and attached HTTP requests
never send saved bearer tokens or browser credentials. The bundled child uses
`--desktop-session`: a private stdin session token and stdout certificate identity
establish per-launch TLS trust for health, usage, version, and credential requests.
Do not replace certificate verification with PID checks or ignored TLS errors.
Headroom owns, stops, or hands off only a server process it started. Remote mode
accepts a normalized HTTP(S) base URL; nonempty bearer tokens are sent as
`Authorization: Bearer <token>`.

The public `headroom` command displays usage, `headroom serve` runs the server,
and `headroom update` updates the managed installation. Bare CLI output refreshes
in a terminal and prints once when piped; it does not start a server or invent
data. With no explicit transport it can read the active desktop's private
snapshot, then fall back to localhost only when no desktop is running.
Desktop and CLI native updates share the same transaction manager. Explicit
Windows/WSL pairing stages both targets before either applies; never infer it
from hostnames, URLs, or a server's claim to be local. Cross-kernel partial
completion must remain visible and recoverable, never reported as atomic.
Managed `serve` receipts bind the executable, kernel process identity, owner,
arguments, and approved server environment. Readiness is acknowledged only after
server configuration and listener binding succeed. Linux/WSL supervision supports
only the current user's `headroom.service`; never infer an arbitrary unit name,
control system-wide units, or invoke sudo during an update.

Local remains the fresh-install default; SSH is the recommended remote option
in the UI, with direct HTTP(S) available separately. Preserve saved choices and
never fall back from SSH to HTTP. SSH mode uses system OpenSSH and a separate
saved SSH address. It requires
trusted host keys and key/agent authentication; never weaken host-key checking
or expose cookies in arguments. Linux/WSL standalone servers opt in with
`--ssh-access`, `ssh_access`, or `USAGE_SSH_ACCESS`. The fixed
`usage-server --ssh-stdio` receiver connects to the existing server through an
owner-only per-account Unix socket and validates the peer UID. It never creates
another provider poller or falls back to TCP. Native Windows receivers are not
supported; Windows, macOS, and Linux desktop clients can use Linux/WSL receivers.
Keep usage, version, and credential requests on the same selected transport.

An explicit desktop **Update server** action uses the selected SSH connection
to run only `headroom update --this-install-only`. The remote managed CLI owns
validation, apply, service restart, and rollback; a successful command exit is
followed by SSH health checks for the exact target version. Never retry the update
command automatically, pass arbitrary commands/paths from the UI, weaken a
usage-only forced key, or expose this action over HTTP. Source, capture,
explicit-config, and system-managed desktop sessions cannot invoke it.

The server defaults to `127.0.0.1:7823`. Off-loopback binds require `auth_token`,
`USAGE_AUTH_TOKEN`, or `--auth-token` before listen/provider construction.

Keep these compatibility contracts unless the task explicitly changes them: Go
modules/imports, `usage-server` binary, legacy server config/service paths, API
provider key `Codex`, and snake_case API fields. Use the canonical Headroom
repository URL for public acquisition and user-facing links while accepting the
former repository's exact release URLs where updater compatibility requires it.
Headroom displays `Codex` as ChatGPT. On first normal Windows launch it may import
`%APPDATA%\ClaudeUsageWidget\settings.json`; the retained WinForms project is not
the default packaged UI.

The API contract is frozen in `server/internal/usage`: optional strings and
reset timestamps are explicit `null`; `is_success` is derived from
`error == null`; `buckets` is always present and empty on error. Providers exit
through `usage.FromBuckets` / `WithBuckets`, which normalize order, remove
duplicates, and cap at 12. Shared IDs include `session`, `plan`, `auto`, `api`,
`credits`, `weekly`, `weekly_<slug>`, `extra`, and `on_demand`. Cursor Grok Bot
is `weekly_grok_bot`. Credit meters appear only when enabled or nonzero.

Official packages use strict schema 1 manifests and immutable generations under
the current-user Headroom root. Public acquisition is available only to a
trusted native per-user install. Capture, explicit-config, source, and
system-managed sessions make no public update requests. Restart
apply uses a two-way acknowledgement/commit, exact process identity, readiness,
rollback, and durable recovery. Do not add a second archive validator outside
the Go manager. macOS framework links are the only archive symlinks allowed:
they must be manifest-listed and resolve within the same packaged framework.
Mac payloads use Headroom.app/Contents/MacOS; the stable Finder launcher is
~/Applications/Headroom.app and managed generations live under
~/Library/Application Support/Headroom. Login startup writes only the owned
LaunchAgent plist for the next login; do not bootstrap or bootout the current
app when changing that preference. Preserve Apple framework structure for code
signing. Ad-hoc signatures are not Developer ID signing or notarization.

## Release and version rules

One stable SemVer without a leading `v` flows through the desktop, launcher,
manager, server, helper, package filenames, manifests, and release tag. Build
metadata is preserved. Numeric components are separately derived for PE and
.NET assembly versions and may not exceed 65534.

The reusable package workflow builds Windows x64, Windows ARM64, Linux x86_64,
and macOS x86_64/ARM64 (deployment target 12.0) with Qt 6.8.3. It verifies the exact packages on native runners, runs the
legacy harness and server Go/race/vet/build/Docker gates, and assembles five
desktop packages, six CLI packages, legacy/full/CLI release manifests, six standalone servers, both installers,
and `SHA256SUMS`. PR workflows have read-only contents permission and never
publish. Merges to `main` do not release automatically: the `Release` workflow
must be deliberately dispatched on `main`. After dispatch, version resolution,
native package validation, tag creation, and publication are automated. The
release resolver is read-only; only the final gated job may create or verify the
tag at the initiating commit and publish. Historical v1.x release records and
tags remain, but their binary assets were retired after v2.0.0. Never publish a
tag or release during local validation.

The published v2.0.0 updater predates the repository rename and rejects the old
GitHub API endpoint's redirect. The first post-rename release must tell v2.0.0
users to rerun a current external installer once; builds after v2.0.0 use the
canonical release endpoint.

Qt notices come from the hash-pinned official 6.8.3 source archives recorded in
`packaging/qt-sources-6.8.3.json`. Preserve referenced notices and license texts,
label the module-source inventory as a conservative superset rather than an
exact binary SBOM, and record optional installed-kit SPDX SBOMs when present.

## Verification

From the repository root:

```bash
cmake -S clients/desktop -B clients/desktop/build -G Ninja -DCMAKE_BUILD_TYPE=Release
cmake --build clients/desktop/build --parallel
ctest --test-dir clients/desktop/build --output-on-failure -E headroom-reset-prohibited-tests

python3 -m unittest discover -s packaging/tests -p 'test_*.py'
bash packaging/tests/test-install-download.sh

cd packaging/headroom-manager
go test -skip 'CodexResetEndpoint|ConsumeResetCredit|Reset' ./...
go vet ./...
go build ./...
go test -race -shuffle=on -count=1 -skip 'CodexResetEndpoint|ConsumeResetCredit|Reset' ./...

cd ../../server
go test -skip 'CodexResetEndpoint|ConsumeResetCredit' ./...
go vet ./...
go build ./...
go test -race -shuffle=on -count=1 -skip 'CodexResetEndpoint|ConsumeResetCredit' ./...

cd ..
dotnet build clients/windows/ClaudeUsageWidget.csproj -c Release -r win-x64
dotnet run --project tests/windows/ServerProcessManagerTests.csproj -c Release
```

Cross-check the manager on both Windows targets:

```bash
cd packaging/headroom-manager
GOOS=windows GOARCH=amd64 go test -c ./contract
GOOS=windows GOARCH=arm64 go test -c ./contract
```

Native UI, installer, package and transaction behavior requires the matching CI
runner. Docker validation is cache-only in CI; do not claim local Docker runtime
validation unless it actually ran.

## Documentation and data safety

Derive flags, environment variables, YAML keys, provider defaults, paths,
release names, and API fields from source. Do not claim a Home Assistant add-on
exists; only `docs/home-assistant.md` REST sensor configuration exists. State
that an external reinstall requires quitting and reopening an already-running
Qt app until that lifecycle is changed. Keep bearer tokens, provider credentials,
browser cookies, URLs containing secrets, and live account output redacted.
Never stage `.omo/**`; it contains task-local caches and evidence only.

The optional `packaging/tests/ssh_server_smoke.py` requires Linux/OpenSSH and
uses disposable test keys and a provider-disabled backend. It reserves the
current account's fixed SSH socket and refuses to run if that socket exists.
Never run it against an account already serving SSH access or reuse live keys.

The optional `packaging/tests/cli_systemd_smoke.py` requires Linux with a working
current-user systemd bus and refuses any existing `headroom.service` unit. It
uses a disposable provider-disabled installation and a temporary runtime unit
link, runs a real repair/restart, then stops the unit and removes that link. Never
run it against an account already using the fixed unit. Linux CLI CI runs it on
an isolated runner account.
