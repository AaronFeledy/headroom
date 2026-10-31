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
go test -skip 'CodexResetEndpoint|ClaudeResetEndpoint|ConsumeResetCredit' ./...
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
- `POST /api/v1/usage/refresh` - asynchronously request an enabled-provider refresh
  with an empty body and no query. Returns `202` with `status: "accepted"` and
  `retry_after_seconds: 15`, or `status: "coalesced"` and a positive retry delay
  while a round is outstanding. Admissions have a process-wide 15-second cooldown:
  once the outstanding round completes, early requests return `429`,
  `error: "refresh rate limited"`, a positive `retry_after_seconds`, and
  `Retry-After`. Unavailable/stopping pollers or no enabled providers return `503`.
  Browser-origin headers are rejected; normal bearer/SSH authorization applies.
- `GET /api/v1/usage/{provider}` - one cached provider entry, for example `Claude` or `codex`.
- `GET /api/v1/health` - server status, version, and provider health.
- `PUT /api/v1/providers/cursor/credentials` - memory-only Cursor credential push with exactly one of `cookie` or `access_token`. Cookies may include `source_name` (trimmed, 1–40 characters, no controls); access tokens may not.
- `GET /api/v1/tls/proof?nonce=<64 hex>` - unauthenticated TLS identity proof; available only over HTTPS when a bearer token is configured.
- `PUT /api/v1/providers/grok/credentials` - memory-only Grok browser credential push with exactly one JSON field: `cookie`.
- `POST /api/v1/providers/codex/reset` - explicitly confirmed banked reset for ChatGPT. Requires JSON fields `request_id` (UUID), `confirmed` (`true`), and the 64-character lowercase `account_fingerprint` from the latest successful usage response. New requests require fresh weekly usage of at least 90% and an available banked reset. Browser-origin requests are rejected.
- `POST /api/v1/providers/claude/reset` - explicitly confirmed banked reset for Claude. Uses the same three JSON fields and browser-origin rejection as the Codex route. New requests require fresh successful Claude usage, a positive available count, `usable_now: true`, and a matching account fingerprint.

ChatGPT (`Codex`) meters follow the windows returned by OpenAI. A weekly-only
allowance produces one `weekly` bucket; an absent five-hour window does not
produce a `session` bucket. A reported window with zero usage remains visible.

Successful ChatGPT (`Codex`) usage entries include read-only
`rate_limit_reset_credits` metadata when the upstream response reports a valid
banked-reset count. It contains `available_count` and a nullable, opaque
`account_fingerprint` that binds a confirmed action to the observed account
without exposing the provider account ID. The field is `null` when the count is
unknown or on provider errors. The additive `usable_now` field is `null` for
Codex; its weekly eligibility floor is 90%. This is Headroom's conservative
near-the-limit policy, not the OpenAI API minimum. [OpenAI's published rules](https://help.openai.com/en/articles/20001498-how-resets-work-in-codex)
allow manual use when a covered allowance has less than 80% remaining; a reset
counts only when it restores a qualifying allowance. OpenAI can still refuse a
submitted request; the server does not retry.

The reset response contains `outcome`: `reset`, `already_redeemed`,
`nothing_to_reset`, or `no_credit`. Reset submission is a single attempt: no
automatic or manual retry, upload replay, redirect, or transport fallback. The
desktop saves its request ID before sending through the selected local, HTTP(S),
or SSH transport and keeps spending blocked across restarts. Only a successful
weekly reading below 90% releases that account's lock; readings at 90–94.99% do
not. The options remain available for information while spending is blocked.
Another explicit reset can become eligible when weekly usage later reaches 90%
and a banked reset is available.

Reset requests reject browser-origin headers and require JSON, the configured
bearer authentication (or the trusted SSH peer), a fresh weekly reading of at
least 90%, a positive credit count, and the same account fingerprint for
eligibility and consumption. The fingerprint is checked against current
credentials immediately before every provider request. Repeated request IDs return
the recorded result or an error without another OpenAI reset request. The server
remembers attempted IDs for its process lifetime; the saved desktop receipt and
upstream UUID retain their protection across server restarts. Every submitted
attempt blocks further new requests in that process until the same account's
usage below 90% has been observed, including while a successful reset propagates.

After submission, the server waits three seconds and performs one read-only OpenAI
usage fetch. The desktop checks the usage cache after 5, 12, and 25 seconds while
waiting, then continues normal polling. These reads never resubmit a reset.

### Claude banked resets

Anthropic's banked-reset status, profile, and consumption endpoints are
undocumented. Successful Claude usage may include the same
`rate_limit_reset_credits` object, with `available_count`, nullable
`account_fingerprint`, and boolean `usable_now`. Available counts sum unpaused
grants with remaining resets. `usable_now` additionally requires eligibility,
no active cooldown, and a usable next grant. Valid status with no available grants
reports zero available resets and `usable_now: false`, retaining the verified
account fingerprint so the desktop can reconcile a pending receipt. Absent or
malformed status and provider usage errors report null metadata. A malformed,
duplicate, or unsupported grant, or an inconsistent next-grant reference, makes
the entire status unknown; a partial count or inferred refusal cannot release a
pending reset. Profile lookup failure leaves the count visible but the fingerprint
null, which prevents consumption and account-bound receipt release. Account and
organization UUIDs never appear in API JSON.

The ordinary usage GET remains unchanged so extra-usage data is preserved. A
separate read-only GET appends exactly `?cedar_ember=1&skip_spend=1`. Status reads
are normally at least five minutes apart, shortened to one minute when the
ordinary session or weekly reading reaches 95%. Failed status reads, including
429s, do not fail ordinary usage; last-good metadata is retained for at most
15 minutes. Status is not read after a failed ordinary usage fetch. Profiles
are fetched for valid banked status, including zero available counts, and cached
by access token. This preserves account binding after a server restart without
reusing another token's identity.

The Claude reset route applies the same JSON size/shape, confirmation, UUID,
fingerprint, browser-header, authentication, shared in-flight lock, and deadline
guards as Codex. Ineligible new requests return `409` with
`fresh eligible Claude usage is required`. Immediately before submission, the
provider checks fresh credentials, the token-bound profile identity, and a fresh
status GET. Claude outcomes are `reset`, `already_redeemed`, `nothing_to_reset`,
or `unavailable`; Codex retains its existing outcomes including `no_credit`.
Successful outcomes return `200`; uncertain or unavailable submissions return
`502` without retry. Invalid JSON/confirmation/identity fields return `400`,
browser headers `403`, and non-JSON content `415`, as on the Codex route.

Claude attempts are saved in an owner-only journal under
`<default config directory>/claude-reset-state/attempts.json`, alongside the
default TLS directory. The directory is independent of `--config`, address,
bearer token, and installation generation. It retains every UUID and only an
account fingerprint, the final pre-submission available count, phase, and any
known terminal outcome; provider account IDs and credentials are never stored.
There are at most 1024 attempts without eviction; a full journal blocks new IDs.
A repeated successful ID returns `already_redeemed`; uncertain IDs are never
resent, including after a server restart.

One cross-process lock spans the final fresh status GET, a synced pending
journal commit, the single upstream attempt, and its terminal journal commit.
New IDs remain blocked until a fresh successful status GET for the same verified
account, made under that lock after the pending attempt, shows fewer available
resets than the **final server baseline** or `usable_now: false`. All outcomes,
including definitive non-consuming results, retain this protection until that
read. Cached bank metadata and ordinary usage `fetch_status.fetched_at` never
reconcile an attempt. One delayed read-only usage refresh is scheduled after an
attempt; its status read bypasses the pre-submission cache. Both HTTP and SSH
uploads are single-shot, non-rewindable, and never redirected or retried.

Successful usage can include optional
`rate_limit_reset_credits.reconciled_request_ids`, containing the current
account's durably resolved UUIDs (up to 1024): reconciled attempts and validated
requests proved not submitted. A rejection is recorded as `not_submitted` with
the request's fingerprint, an explicit zero baseline, and outcome `unavailable`.
It is inserted only when the UUID is absent under the same provider and
cross-process locks; an existing uncertain attempt is never replaced. Replaying
that UUID remains terminal and cannot submit a reset. Ambiguous responses never
create this evidence. The journal is revalidated on
every usage serialization, including cached snapshots. A desktop retires its
Claude receipt only when its provider, account fingerprint, and exact request
UUID match this evidence. This also lets a lagging desktop reconcile an older
request after later requests. Older servers or receipts without matching
evidence keep the desktop latch closed.

The journal directory is created exclusively once and serves as the durable
initialization marker. A missing/corrupt ledger in an existing directory, a
lock failure, or a write/sync failure disables new attempts and reconciliation
evidence. Each write first syncs a `write-in-progress` marker and leaves it in
place on failure; no process automatically repairs or discards uncertain state.
Deleting or rolling back **all** reset state is outside the server guarantee;
the desktop's separately saved pending receipt still prevents its own retry.
Direct provider construction with an empty `Options.ResetStateDir` disables
consumption rather than writing to the real user's home directory.

Initialization makes every ancestor directory durable before the journal can
permit submission. Unix syncs the entire ancestor chain on each initialization,
including retries after an earlier sync failure. Windows publishes each new
directory with a same-volume, non-replacing `MoveFileEx WRITE_THROUGH` rename;
sibling initialization locks and pending markers preserve an uncertain outcome
across processes and restarts. These markers are never automatically repaired.
This relies on the operating system's persistence primitives; tests use isolated
storage fixtures and do not simulate actual power loss or execute resets.

Never exercise either provider's consume method, reset HTTP route, or SSH reset
forwarding during tests or manual validation, even with a fake upstream. Their
skip-only safety tests are intentional, not TODOs.

Each usage entry also includes an always-present `auth` object: `state`
(`signed_in`, `signed_out`, or `expired`), nullable `{kind, name}` `source`,
nullable `sign_in_command` and `sign_in_url`, `accepts_browser_credentials`, and
an array `checked` (up to 12 `{kind, name, status}` entries). Only Cursor accepts
browser credentials and populates `checked`. A signed-in credential can coexist
with a network or upstream error. Existing fields remain unchanged; Cursor's
`needs_reauth` is true when signed out or expired, and `reauth_command` mirrors
its sign-in command.

Usage reads remain cache-only. Providers run independently, with at most one
fetch per provider and a 30-second service deadline for background attempts.
Read-only usage network failures and HTTP 408/5xx retry after 10, 20, and 40
seconds (capped by the configured polling interval), then resume normal cadence.
A successful attempt resets that recovery episode; requesting refresh does not.
HTTP 429 deadlines apply to scheduled, explicit, and synchronous fetches. Valid
`Retry-After` seconds/dates are never shortened; otherwise backoff starts at the
larger of the configured interval or 60 seconds and doubles up to the larger of
that interval or five minutes. OAuth refresh, credential, parsing, and trust
failures do not receive accelerated retries.

Polled entries optionally include `fetch_status`: `fetched_at` is the actual
attempt-completion UTC RFC3339Nano timestamp; `failure_kind` is `null` on success
or `transient`, `rate_limited`, `auth`, or `other` on failure. `credential_epoch`
is a nullable, opaque process-secret HMAC identity for the credential/account/source
used by that attempt, changing on credential rotation or server restart. Unknown
or mixed credential contexts use `null`; clients must not associate older meters
with them. Failure responses still have `is_success: false`, empty `buckets`, and
null `rate_limit_reset_credits`.

When `auth_token` or `USAGE_AUTH_TOKEN` is set, public HTTP endpoints other than
the TLS proof endpoint require
`Authorization: Bearer <token>`. The protected SSH socket authenticates the local
OS account and supplies that token internally; an SSH client does not need it.

## Read-only usage details

The canonical API contract is in `internal/usage/details.go` and `types.go`.
Each usage entry includes nullable, additive `usage_details`, independent of
percent meters and reset actions. Its sections are:

- `plan`: reported plan label, unlimited status, and scope.
- `quotas`: metadata linked to real meters by `bucket_id`, including reported
  availability and plan context.
- `finances`: separate monetary ledger items with their own reported periods,
  included usage, prepaid balance, extra-spend cap, and enabled/top-up status.
- `credit_grants`: individual grants with reported amounts, product restrictions,
  expiry, and scope. Restricted grants are never combined with prepaid balance.
- `reset_inventory`: reported count, nullable currently valid count, and validity/
  expiry windows. This is inventory, not permission to redeem a reset.
- `product_usage`: provider-reported percentage breakdown, optionally linked by
  `parent_bucket_id`. These are not independent quotas and must not be summed
  into a new usage meter.

Sections (each quota row for `quotas`) carry `state`: `ok`, `stale`, `unknown`,
or `unsupported`, plus nullable `observed_at`. Stale data retains its original
observation time; unknown and unsupported do not mean zero. Unknown sections,
lists, and optional values are `null`; a known empty list is `[]`, and an
explicit zero remains zero. Monetary `*_minor` fields are exact integer minor
units with a reported currency (`USD` in these adapters), not provider credits
or a conversion from credit percentages. Individual grant expiry and unknown
scope remain separate rather than being inferred or collapsed into a total.

Scope describes only its containing row. `kind` is `unknown`, `personal`, or
`team`; nullable `shared_pool` records only explicit provider-reported pool
context. Matching percentages, labels, or `isUnifiedBillingUser` do not prove
that Cursor Grok Bot and standalone Grok share an account or allowance. Without
that proof they remain unknown or separate, never joined across providers.

Buckets also include nullable `measurement`. `percent` explicitly identifies a
percent meter; `status` identifies a status-only row. Missing/null preserves
older clients' bucket heuristics. Clients must suppress percentage bars and
pace for `status` or an unrecognized explicit measurement, rather than infer a
percentage from numeric `utilization`. That numeric field remains for legacy
compatibility. Provider keys, stable bucket IDs (including `weekly_grok_bot`),
`current`/`weekly` headers, authentication, and error contracts are unchanged:
`is_success` follows `error == null`, errors have empty `buckets`, and failed
ordinary usage has `usage_details: null`.

### Cursor and Grok selection

Cursor uses authoritative current weekly usage when its credential context is
known to use weekly allowances. Source timestamps accept checked numeric epoch
milliseconds (including numeric strings) or RFC3339 timestamps; API times are
normalized to UTC. Older monthly and request-based mappings remain available for
legacy accounts and payloads. Once weekly usage is known, a failed or missing
current-period reading must not fall back to a stale monthly zero; it reports
unavailable usage instead. Clients may retain a labelled stale reading only
under their existing credential-bound stale-data policy.

Grok's credit-based primary allowance is weekly. Optional plain monetary billing
is a separate ledger with its own reported period; historical spend is never
guessed into the active week. Optional enrichment uses bounded requests and
credential/context-scoped caches, normally refreshed five minutes apart with
last-good observations retained for at most 15 minutes. Failed enrichment can
leave details stale or unknown without failing ordinary usage or changing auth.
This does not make Cursor's authoritative weekly reading optional once that
context is known.

Grok reset inventory is strictly read-only: count, validity, and expiry only.
It never populates `rate_limit_reset_credits`, exposes a redemption action, or
stores or logs redemption handle IDs. Read-only inventory decoder/status tests
are allowed. Consume methods, HTTP reset routes, and SSH reset forwarding remain
forbidden to execute even against fakes, and live provider test flags must never
be enabled for validation. These details introduce no banking or billing
mutations and no authentication-flow changes.

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
