# Connect through SSH

SSH mode connects the Windows, macOS, or Linux Headroom app to a usage server on Linux
or WSL. It encrypts usage requests, server version checks, and supported browser
credential recovery. Local mode and direct HTTP(S) connections remain available.

The client uses the system OpenSSH executable and your existing SSH configuration,
keys, and authentication agent. Headroom does not store an SSH password. The
server must already have SSH access configured; Headroom does not create accounts
or install an SSH daemon.

## Prepare the server

Install a matching `usage-server` executable and enable SSH access in its existing
configuration:

```yaml
ssh_access: true
```

Alternatively, pass `--ssh-access` when starting the standalone server or set
`USAGE_SSH_ACCESS=true`. Restart the service after changing its configuration.
The server's ordinary HTTP address, bearer authentication, and provider settings
continue to work as before.

The SSH login must use the **same OS account** as the running usage server. The
receiver connects to that account's protected socket at
`~/.local/share/headroom/ssh/control.sock`. Both processes must see the same home
directory and filesystem namespace. A service running as a dedicated account
requires SSH access for that account; logging into a different account does not
grant access to its server.

Make `usage-server` available on the SSH session's command search path. Headroom
executes the fixed command `usage-server --ssh-stdio`; this receiver connects to
the existing service and does not start another provider poller. One server per
account can enable the fixed SSH socket.

Native Windows and macOS servers do not support this receiver. Windows and
macOS **clients** can connect to Linux or WSL receivers. Use HTTPS for a remote
native Windows or macOS backend.
SSH access cannot be combined with the bundled local `--desktop-session` mode.

## Prepare the client

Install the OpenSSH client if it is not already available. Configure key or agent
authentication for the backend. For example, an existing SSH configuration can
contain:

```sshconfig
Host usage-backend
    HostName server.example
    User usageuser
    Port 22
    IdentityFile ~/.ssh/id_ed25519
```

Connect with your SSH client first and verify the server's host-key fingerprint
through a trusted channel before accepting it. Headroom requires a trusted host
key and refuses unknown or changed keys. It does not disable host-key checking
or display password/passphrase prompts. Load encrypted keys into your SSH agent
before connecting. See the [OpenSSH host-key settings](https://man.openbsd.org/ssh_config#StrictHostKeyChecking).

Local remains the default on a fresh install. For a remote backend, select
**SSH · Recommended** in Headroom's connection settings and enter an address such as:

```text
ssh://usage-backend
ssh://usageuser@server.example:2222
```

Omitted users and ports come from your SSH configuration. Passwords, query
parameters, fragments, and custom command paths are not accepted in this address.
SSH mode keeps its address separate from the saved HTTP(S) address and bearer
token, and never sends that bearer token through SSH. Headroom remembers the
selected mode. If SSH fails, it reports the failure and retries SSH; it never
switches to HTTP automatically. Choose **HTTP(S)** explicitly for a direct
connection, including HTTPS to a native Windows backend.

## Credential recovery and server identity

On Windows, Headroom's existing browser helper can recover supported Cursor and
Grok credentials and send them through SSH. Linux still uses server-side
credential files or the documented [WSL credential sync](../server/deploy/wsl/README.md);
SSH does not add a Linux browser-cookie reader.

Credentials travel through private process streams. On the remote host, the
receiver verifies the socket's permissions, ownership, and peer UID before
sending a request. The service shares its existing API handler and in-memory
providers with that socket, so a successful credential update affects the server
already supplying your usage data. It does not write a credential exchange file
or pass cookies in command arguments.

The receiver accepts only usage and health reads and Cursor/Grok credential
updates. It has bounded requests and responses, deadlines, and no TCP fallback.
The SSH client disables agent forwarding, PTY allocation, local commands, and
connection multiplex reuse. It does not trust an unrelated localhost HTTP
listener as proof of an SSH connection.

For a dedicated key, an administrator can restrict the server-side authorized
key to the receiver command, using the executable's actual absolute path:

```text
restrict,command="/usr/local/bin/usage-server --ssh-stdio" ssh-ed25519 <public-key> headroom
```

Verify host trust before restricting the key. This key then supports Headroom's
receiver protocol rather than an interactive shell.

## Update the remote server

In an official desktop installation, **About & Updates → Update server** uses
the selected SSH account to execute `headroom update --this-install-only`.
Install the [managed CLI](cli.md#install-without-a-desktop) on that host first;
both `headroom` and `usage-server` must be in its noninteractive SSH PATH.
The CLI updates only its own managed installation and registered server. Migrate
legacy standalone services to the documented per-user `headroom.service` if you
want them restarted by the updater.

This action needs permission to execute that command. The usage-only forced key
above intentionally cannot do this; retain that restriction and update manually,
or deliberately select an SSH account/key with update access. Headroom never
changes authorized keys, falls back to HTTP, or requests administrator access.
The desktop records progress in diagnostics and confirms the server's version
after the update's restart. A lost connection leaves the outcome unconfirmed;
it does not automatically repeat the update command.

## Troubleshooting

- **Unknown or changed host key:** verify the host's identity with your SSH client.
  Do not disable host-key checking to get past the error.
- **Key authentication fails:** confirm your SSH alias, username, port, and loaded
  agent keys. Interactive password and passphrase prompts are unavailable.
- **Receiver unavailable:** confirm the new server is running with SSH access
  enabled, the receiver executable is available, and both use the same OS account
  and home directory.
- **Unsafe socket directory:** correct its ownership and permissions. The final
  `ssh` directory must be private to its owner, and the socket must have mode
  `0600`. Headroom rejects symlinks and unsafe directories.
- **Another server already owns the socket:** only one server per account can
  enable SSH access. Shut down that server normally before starting its replacement.
- **Invalid response:** remove shell startup output that is written to stdout
  for noninteractive SSH commands. The receiver's stdout is reserved for its
  protocol; diagnostic output belongs on stderr.

## Verify an implementation build

The isolated Linux smoke test needs OpenSSH client/server tools and a usable
current-user SSH/PAM account. It creates disposable keys, an isolated backend
configuration with all providers disabled, and a localhost-only SSH daemon. It
temporarily uses the account's fixed SSH socket and refuses to run if that socket
already exists. It does not edit the user's SSH settings or provider credentials:

```bash
python3 packaging/tests/ssh_server_smoke.py --server /path/to/usage-server
```

The test exercises the actual SSH receiver and running backend, then verifies
that a changed host key prevents the receiver from being invoked. Qt transport,
settings, cancellation, and browser-recovery fixtures run with the desktop tests.
