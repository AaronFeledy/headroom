# Headroom usage server

The Go server polls enabled providers and serves cached usage to Headroom, Home
Assistant REST sensors, and compatible API clients. The `usage-server` binary,
configuration keys, environment variables, and API wire names remain compatible
with Claude Usage Widget deployments; legacy default config locations migrate
automatically.

## Managed installation

The managed distribution provides `headroom serve` for this server and
`headroom update` for verified self-updates. Install the CLI-only package with
`install.sh --cli` or `install.ps1 -CLI`; it contains no Qt dependencies. A
shared native desktop installation updates its desktop, CLI, and bundled server
together. Windows desktops can explicitly pair with a WSL CLI installation for
coordinated updates. See the [CLI and service guide](../docs/cli.md).

Existing standalone `usage-server` deployments remain supported. Their YAML,
service configuration paths, flags, and API do not change. Unmanaged binaries
and source checkouts use their original deployment method to update.

## Build

From `server/`:

```bash
go test -skip 'CodexResetEndpoint|ConsumeResetCredit' ./...
go vet ./...
go build ./...
go build -trimpath -ldflags='-s -w -buildid=' -o usage-server ./cmd/usage-server
```

Cross-build the Windows local server from `server/`:

```bash
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o usage-server-win-x64.exe ./cmd/usage-server
GOOS=windows GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o usage-server-win-arm64.exe ./cmd/usage-server
```

## Run

Local-only default:

```bash
./usage-server
```

The default bind is `127.0.0.1:7823`. Off-loopback binds require bearer auth:

```bash
USAGE_AUTH_TOKEN='replace-with-a-long-random-token' \
  ./usage-server --listen-addr 0.0.0.0:7823
```

Call an authenticated remote server with:

```bash
curl -H 'Authorization: Bearer replace-with-a-long-random-token' \
  http://server.example:7823/api/v1/usage
```

Linux and WSL servers can also enable Headroom's SSH transport with
`--ssh-access` or `ssh_access: true`. The SSH login must use the same OS account
as the service. This adds a protected Unix socket to the existing server while
preserving its HTTP configuration. See the [SSH setup guide](../docs/ssh.md).

## Endpoints

- `GET /api/v1/usage` - array of cached provider usage entries.
- `GET /api/v1/usage/{provider}` - one cached provider entry, for example `Claude` or `codex`.
- `GET /api/v1/health` - server status, version, and provider health.
- `PUT /api/v1/providers/cursor/credentials` - memory-only Cursor credential push with exactly one of `cookie` or `access_token`. Cookies may include `source_name` (trimmed, 1–40 characters, no controls); access tokens may not.
- `GET /api/v1/tls/proof?nonce=<64 hex>` - unauthenticated TLS identity proof; available only over HTTPS when a bearer token is configured.
- `PUT /api/v1/providers/grok/credentials` - memory-only Grok browser credential push with exactly one JSON field: `cookie`.
- `POST /api/v1/providers/codex/reset` - explicitly confirmed banked reset for ChatGPT. Requires JSON fields `request_id` (UUID), `confirmed` (`true`), and the 64-character lowercase `account_fingerprint` from the latest successful usage response. New requests require fresh weekly usage of at least 95% and an available banked reset. Browser-origin requests are rejected.

ChatGPT (`Codex`) meters follow the windows returned by OpenAI. A weekly-only
allowance produces one `weekly` bucket; an absent five-hour window does not
produce a `session` bucket. A reported window with zero usage remains visible.

Successful ChatGPT (`Codex`) usage entries include read-only
`rate_limit_reset_credits` metadata when the upstream response reports a valid
banked-reset count. It contains `available_count` and a nullable, opaque
`account_fingerprint` that binds a confirmed action to the observed account
without exposing the provider account ID. The field is `null` when the count is
unknown or on provider errors.

The reset response contains `outcome`: `reset`, `already_redeemed`,
`nothing_to_reset`, or `no_credit`. Reset submission is a single attempt: no
automatic or manual retry, upload replay, redirect, or transport fallback. The
desktop saves its request ID before sending through the selected local, HTTP(S),
or SSH transport and keeps the button disabled across restarts. Only a successful
weekly reading below 95% releases that account's lock; the button becomes eligible
again when weekly usage later reaches 95% and a banked reset is available.

Reset requests reject browser-origin headers and require JSON, the configured
bearer authentication (or the trusted SSH peer), a fresh weekly reading of at
least 95%, a positive credit count, and the same account fingerprint for
eligibility and consumption. The fingerprint is checked against current
credentials immediately before every provider request. Repeated request IDs return
the recorded result or an error without another OpenAI reset request. The server
remembers attempted IDs for its process lifetime; the saved desktop receipt and
upstream UUID retain their protection across server restarts. Every submitted
attempt blocks further new requests in that process until the same account's
usage below 95% has been observed, including while a successful reset propagates.

After submission, the server waits three seconds and performs one read-only OpenAI
usage fetch. The desktop checks the usage cache after 5, 12, and 25 seconds while
waiting, then continues normal polling. These reads never resubmit a reset.

Each usage entry also includes an always-present `auth` object: `state`
(`signed_in`, `signed_out`, or `expired`), nullable `{kind, name}` `source`,
nullable `sign_in_command` and `sign_in_url`, `accepts_browser_credentials`, and
an array `checked` (up to 12 `{kind, name, status}` entries). Only Cursor accepts
browser credentials and populates `checked`. A signed-in credential can coexist
with a network or upstream error. Existing fields remain unchanged; Cursor's
`needs_reauth` is true when signed out or expired, and `reauth_command` mirrors
its sign-in command.

When `auth_token` or `USAGE_AUTH_TOKEN` is set, public HTTP endpoints other than
the TLS proof endpoint require
`Authorization: Bearer <token>`. The protected SSH socket authenticates the local
OS account and supplies that token internally; an SSH client does not need it.

## Configuration

Config is applied in this order: defaults, YAML, environment variables, then CLI flags.

Default config path:

- Windows: `%APPDATA%\Headroom\config.yaml`
- Unix (including Linux/macOS): `${XDG_CONFIG_HOME:-$HOME/.config}/headroom/config.yaml`

When the new Unix directory is absent, the server renames the real legacy
`claude-usage-widget` directory to `headroom`, preserving all files and modes,
then leaves a relative compatibility symlink at the legacy path. Existing units,
environment-file references, and older server generations keep working. If both
paths are real directories (for example, the desktop already created `headroom`
for `settings.json`), the server merges legacy entries in name order without
replacing existing entries, preserving file modes. An empty legacy directory is
replaced by the same relative symlink. Conflicts leave the legacy directory and
conflicting entries intact with one warning listing their names; the new config
still takes precedence. An entry rename failure stops the merge with a warning;
a link failure leaves the moved files in the new folder and logs a warning. This
also applies to `--config` or `USAGE_CONFIG` paths directly in either directory;
unrelated explicit paths and user-managed legacy symlinks are left untouched.
Windows default-path startup copies the legacy
`%APPDATA%\ClaudeUsageWidget\config.yaml` without overwriting an existing new
config and uses the new file from then on, leaving the legacy file and desktop
settings untouched; explicit Windows paths do not migrate. Migration failures
are logged and the loader falls back to the legacy config when available.

CLI flags:

- `--config <path>` - config YAML path.
- `--listen-addr <host:port>` - HTTP listen address.
- `--auth-token <token>` - bearer token.
- `--tls <auto|on|off>` - default `auto` enables TLS for non-loopback binds.
- `--tls-cert-file <path>` and `--tls-key-file <path>` - optional paired PEM files.
- `--poll-interval <duration>` - Go duration such as `30s`, `1m`, or `5m`.
- `--ssh-access` - opt in to the private per-account SSH socket on Linux/WSL;
  disabled by default and incompatible with `--desktop-session`.
- `--ssh-stdio` - run the restricted one-request SSH receiver. Must be used
  alone; it connects to the running server without loading provider configuration
  or creating a second poller.
- `--desktop-session` - reserved for the bundled Headroom child process; requires
  a numeric loopback bind and a bounded private stdin handshake. It uses a fresh
  in-memory TLS identity and session bearer token instead of the configured
  token, and publishes only the public certificate and listener identity on
  stdout. It has no YAML or environment equivalent. Standalone services should
  omit this flag and retain their normal HTTP/reverse-proxy configuration.

Environment variables:

- `USAGE_CONFIG` - config YAML path.
- `USAGE_LISTEN_ADDR` - HTTP listen address.
- `USAGE_AUTH_TOKEN` - bearer token.
- `USAGE_TLS`, `USAGE_TLS_CERT_FILE`, `USAGE_TLS_KEY_FILE` - TLS settings.
- `USAGE_POLL_INTERVAL` - Go duration.
- `USAGE_SSH_ACCESS` - boolean enabling the Linux/WSL SSH socket.
- `USAGE_PROVIDER_<NAME>_ENABLED` - boolean provider toggle, for example `USAGE_PROVIDER_CODEX_ENABLED=true`.
- `USAGE_PROVIDER_<NAME>_CREDENTIALS_PATH` - provider credential path, for example `USAGE_PROVIDER_GROK_CREDENTIALS_PATH=/var/lib/usage-server/grok-auth.json`.
- `USAGE_PROVIDER_CURSOR_BROWSER_CREDENTIALS` - boolean; defaults to true.

YAML keys:

```yaml
listen_addr: 127.0.0.1:7823
auth_token: ""
tls: auto
tls_cert_file: ""
tls_key_file: ""
poll_interval: 60s
ssh_access: false
providers:
  claude:
    enabled: true
    credentials_path: ~/.claude/.credentials.json
  codex:
    enabled: true
    credentials_path: ~/.codex/auth.json
  cursor:
    enabled: true
    credentials_path: ""
    browser_credentials: true
  grok:
    enabled: true
    credentials_path: ~/.grok/auth.json
```

All four providers are enabled by default; Claude defaults to
`~/.claude/.credentials.json`, and the others discover credentials automatically
when no path is set. Disable unwanted providers with `enabled: false` or
`USAGE_PROVIDER_<NAME>_ENABLED=false`. The API name `Codex` is intentionally
retained while Headroom displays it as ChatGPT. Codex can also discover
`CODEX_HOME/auth.json`, `~/.codex/auth.json`, Windows WSL auth, and OpenCode
auth. Cursor discovery works on every bind, including off-loopback. Its priority
is cursor-agent `auth.json`, Cursor app `state.vscdb`, server-side Firefox, then
memory-only desktop cookies or API access tokens. An explicit Cursor credential
path replaces only CLI-file discovery. On WSL it also checks Windows profiles
(preferring `$USER`, respecting the automount root in `/etc/wsl.conf`); native
Windows checks WSL homes without running Windows executables. Cross-kernel
source names include `(Windows)` or `(WSL)`.

Firefox profiles are searched default-first. Chrome, Edge, Brave, and Chromium
are detected but their cookies are never decrypted; `checked` can report
`encrypted`, `locked`, `unreadable`, `signed_out`, or `expired`. Set
`providers.cursor.browser_credentials: false` to skip server-side browsers.
Working credentials remain active until rejected or a higher-priority file
changes. Rejected credentials are remembered in memory and fallbacks are tried
within the same fetch; JWT expiration is checked before HTTP requests. Browser
databases are read through private, disposable SQLite snapshots including WAL
files. No pushed credential is persisted.

Headroom never sends browser credentials to remote plain HTTP. Grok weekly usage
primarily uses the authenticated CLI billing endpoint with `~/.grok/auth.json`
or Windows WSL auth; the memory-only browser `sso` cookie is an optional fallback
when CLI weekly data is unavailable.

## HTTPS identity and verification

TLS-enabled standalone servers accept both HTTP and HTTPS on the same port;
existing plain-HTTP clients keep working. Explicit cert/key files enable TLS
and must be supplied together; combining them with `tls: off` is invalid.
Without files, a self-signed RSA identity is saved in the migrated config base's
`headroom/tls/{cert.pem,key.pem}` (`Headroom\\tls` under `%APPDATA%` on Windows).
The directory is mode 0700 and the key is 0600. Certificates last 397 days and
are regenerated at startup if invalid or fewer than 30 days remain. In `auto`
mode an identity failure logs a warning and leaves HTTP available; `on` and
explicit files fail startup instead. The listening log records the leaf's
SHA-256 fingerprint. Desktop-session TLS and SSH sockets are unchanged.

The CLI first uses system certificate trust. With a token, an untrusted HTTPS
certificate can be verified using a fresh nonce and the token-keyed HMAC proof,
without sending the token. The proof binds the exact peer leaf certificate;
authenticated requests then use an in-memory pin with certificate verification
enabled. Rotation requires a new successful proof. An explicit HTTP URL with a
token attempts HTTPS on the same host and port first; older HTTP-only servers
remain supported. Never disable verification on authenticated clients merely
to accept a self-signed server.

## Authentication Safety

The server refuses to start when `listen_addr` is not loopback and the auth token is empty. Safe examples:

```bash
./usage-server --listen-addr 127.0.0.1:7823
USAGE_AUTH_TOKEN='replace-with-a-long-random-token' ./usage-server --listen-addr 0.0.0.0:7823
```

Do not place real provider credentials or bearer tokens in committed files. Prefer systemd environment files, Docker secrets, or Home Assistant `secrets.yaml` for tokens.

## Raspberry Pi Systemd Sample

This compatible sample keeps its existing paths: the binary is installed at
`/opt/claude-usage-widget/usage-server`, config lives at
`/etc/claude-usage-widget/config.yaml`, provider credential copies are owned by
the dedicated `usagewidget` user, and secrets are in
`/etc/claude-usage-widget/usage-server.env`.

`/etc/claude-usage-widget/config.yaml`:

```yaml
listen_addr: 0.0.0.0:7823
poll_interval: 60s
providers:
  claude:
    enabled: true
    credentials_path: /var/lib/claude-usage-widget/.claude/.credentials.json
  codex:
    enabled: true
    credentials_path: /var/lib/claude-usage-widget/.codex/auth.json
  cursor:
    enabled: false
  grok:
    enabled: true
    credentials_path: /var/lib/claude-usage-widget/.grok/auth.json
```

`/etc/claude-usage-widget/usage-server.env`:

```text
USAGE_AUTH_TOKEN=replace-with-a-long-random-token
```

`/etc/systemd/system/claude-usage-widget-server.service`:

```ini
[Unit]
Description=Headroom usage API server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=usagewidget
Group=usagewidget
EnvironmentFile=/etc/claude-usage-widget/usage-server.env
ExecStart=/opt/claude-usage-widget/usage-server --config /etc/claude-usage-widget/config.yaml
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/claude-usage-widget

[Install]
WantedBy=multi-user.target
```

Setup sketch:

```bash
sudo useradd --system --home /var/lib/claude-usage-widget --create-home --shell /usr/sbin/nologin usagewidget
sudo install -d -o usagewidget -g usagewidget -m 700 /var/lib/claude-usage-widget
sudo install -d -o root -g root -m 755 /opt/claude-usage-widget /etc/claude-usage-widget
sudo install -o root -g root -m 755 usage-server /opt/claude-usage-widget/usage-server
sudo chmod 600 /etc/claude-usage-widget/usage-server.env
sudo systemctl daemon-reload
sudo systemctl enable --now claude-usage-widget-server
```

Copy provider credential files with owner `usagewidget` and mode `600`.

## Docker

The image default still binds to loopback inside the container, which is not useful for published ports. Override the bind and provide auth:

```bash
docker build -t headroom-usage-server ./server
docker run --rm -p 7823:7823 \
  -e USAGE_AUTH_TOKEN='replace-with-a-long-random-token' \
  -v "$HOME/.claude/.credentials.json:/home/nonroot/.claude/.credentials.json:ro" \
  headroom-usage-server --listen-addr 0.0.0.0:7823
```

Add read-only credential mounts as needed. All providers, including Grok, are enabled by default, so Grok only needs its credential file made available—for example, mount `~/.grok/auth.json` to the path configured for the container user (override it with `-e USAGE_PROVIDER_GROK_CREDENTIALS_PATH=...` when the mount path differs).
