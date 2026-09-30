# WSL service deployment

For a managed installation that supports `headroom update`, use the **per-user**
[`headroom.service`](headroom.service) and the
[CLI service setup and Windows/WSL pairing guide](../../../docs/cli.md).
It uses `~/.config/headroom/` and participates in verified restart and
rollback. Migrate an existing system-wide service once before opting in.

The legacy sample units below remain available for independently managed
`usage-server` deployments. They are not automatically controlled by the updater.

These sample systemd units run the Headroom Go usage server directly in a WSL distribution
with systemd enabled. Replace `USER` in the service files with your Linux account
and adjust the paths before installing them into `/etc/systemd/system`.

## Configuration

Install `usage-server` into `~/.local/bin` and create the private directory
`~/.config/headroom` (mode 0700). Its `config.yaml` supplies provider settings and
the listen address, using the server configuration documented in
[the server README](../../README.md). Bind to the WSL network adapter that clients
can reach. An off-loopback bind requires authentication.

The server automatically renames the legacy config directory when the new one
is absent, leaving a compatibility link so existing units and timers keep
working. Start the updated server with existing unit paths before switching
their `EnvironmentFile` paths to the new directory.

Create an owner-readable environment file `server.env` (mode 0600) containing
`USAGE_AUTH_TOKEN`, `USAGE_API_URL`, and `CURSOR_AUTH_PATH`. Use your server's
address and existing Cursor CLI auth-file path. Never commit this file or
provider credentials. Install `sync_cursor_auth.py` into
`~/.local/lib/claude-usage-widget`.

Off-loopback servers disable automatic Cursor credential discovery. The timer
checks Cursor each minute and seeds the server's memory-only credential from an
existing CLI login when necessary, including after server restarts. It does not
refresh the CLI login itself; sign into Cursor again if that login expires.
The helper logs only generic failures and bypasses proxies for this connection.

## Startup and cutover

After adjusting and copying the units, run as an administrator in WSL:

```bash
systemctl daemon-reload
systemctl enable --now usage-server.service usage-server-cursor-auth.timer
systemctl status usage-server.service usage-server-cursor-auth.timer
```

The units start when the WSL distribution starts. They do not themselves launch
WSL at Windows boot. The server retries when its adapter is unavailable.

Back up the Headroom settings file privately, select Remote mode, then set a
nonempty server URL and its bearer token. Remote mode disables bundled server
startup. Stop the old Windows server and disable any independent Windows service
or scheduled task that launches it. Verify authenticated access and a WSL
service restart before relying on the new service. Remote plain HTTP disables
browser credential forwarding from Headroom; use WSL credential files and this
sync helper, or configure the encrypted [SSH connection](../../../docs/ssh.md)
with SSH access enabled under the backend's OS account. The retained Qt setting names differ from the legacy
WinForms JSON keys, but the server API and bearer-token behavior are unchanged.

To roll back, repoint clients to the prior server and restore the appropriate
private settings backup before disabling these units:

```bash
systemctl disable --now usage-server-cursor-auth.timer usage-server.service
```
