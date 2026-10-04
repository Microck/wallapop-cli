# CLI-managed Galleton sessions

The normal workflow is unchanged:

```sh
wallapop auth login --cookies ~/Downloads/cookies.txt
wallapop search "thinkpad"
```

Release binaries contain a pinned Galleton executable for their OS and architecture.
The CLI extracts it into private storage, verifies its hash, starts it as needed,
and connects through the official Go SDK. No separate Galleton installation,
configuration file, port selection, or environment variables are needed.

## Installation and source builds

The same self-contained release binary is used by the shell installer, Homebrew,
Scoop, npm, and AUR. `make build` and `make install` also include the engine.
GoReleaser builds the pinned engine for Linux, macOS, and Windows on amd64 and arm64
before embedding the matching executable in each release.

A plain `go install .../cmd/wallapop@VERSION` cannot run the bundle generator.
On its first authenticated use, that source build uses the already-required Go
toolchain to build and cache the exact same upstream version. Both module hashes
are checked first. An uncached source install therefore needs module-download
access once. Release binaries and `make install` need neither Go nor a daemon
download at runtime. No executable is fetched from `latest`, PATH, or an arbitrary
URL. The source pin and checksums live in `internal/galleton/bundle/version.go`.

## Lifecycle

Commands load config and local metadata without starting the engine. The first
operation requiring a managed session acquires a process lease and starts or reuses
one engine per private state directory. The daemon binds an OS-selected literal
loopback port and reports the address after binding; startup verifies its local
API token and health response before importing credentials.

Cross-process locks serialize startup and local profile metadata updates. The
Galleton vault provides another exclusive lock around credential rotation.
Separate CLI invocations and concurrent watch/MCP operations share the engine;
one client exiting cannot stop it while another holds a lease. Crashed-client
leases are reclaimed using OS locks rather than stale PID guesses.

After the last client exits, the host allows a ten-second idle grace period,
then requests authenticated shutdown and lets Galleton drain/checkpoint pending
operations. Shutdown is serialized against new startup. A later command restarts
the engine and reopens the same vault. It never replays an ambiguous renewal.

## Optional unattended renewal

Nothing installs an OS service automatically. To keep renewal active between CLI
invocations and restart it at OS login, explicitly run:

```sh
wallapop auth service enable
wallapop auth service status
wallapop auth service disable
```

This registers a user systemd unit on Linux, a LaunchAgent on macOS, or an
interactive logon task on Windows. It uses absolute executable/state paths and
does not copy provider credentials into the service definition. Linux does not
automatically enable user lingering; these are login services, not a promise of
renewal while the computer is off or the user session is stopped.

For containers or another supervisor, use `wallapop auth service run` in the
foreground. `auth service stop` stops an idle engine; it refuses while clients
remain active. Disable an installed service before stopping its engine.

## Migration and storage

An existing cookie profile is migrated on its first authenticated use, including:

```sh
wallapop auth status --check --profile work
```

The CLI imports the legacy cookie under a stable, namespaced session ID. Only
after Galleton supplies a usable bearer token does it replace the local cookie
with `galleton_id`. Failed imports or metadata writes preserve recovery
information. Retrying an interrupted migration adopts the existing daemon session
rather than overwriting a newer credential with a stale cookie. Concurrent writes
preserve unrelated profiles and reject stale reconnect/logout changes.

The default vault is `galleton/` alongside the CLI's `credentials.toml`, under
`$XDG_CONFIG_HOME/wallapop-cli`. The directory is private, files use mode 0600 on
Unix, and extracted executables use 0700. On Windows, filesystem ACLs also need
to protect the owning user's profile. The local API token and encryption key
administer the entire vault; encryption does not protect against other processes
running as the same OS user. Do not share the state directory or include it in
public backups. Managed cookies/access tokens are not stored in credentials.toml.

`auth status` and `profile list` remain local/offline. Managed status reports
`source: "galleton"` and a session ID. `auth status --check` verifies that a usable
token is available; `auth refresh` explicitly requests renewal and fails nonzero
when renewal fails. Cookie expiry is omitted because Galleton does not expose it;
`updated_at` describes CLI metadata, not the last engine renewal.

Logout and profile removal forget the daemon session before removing its local
reference. A failed daemon deletion leaves the reference for retry. This does not
revoke the original browser login. A saved managed reference never silently falls
back to a stale local cookie or creates a replacement session; use explicit login
with a fresh browser export when reauthentication is required.

## Advanced external daemon and ephemeral override

`WALLAPOP_GALLETON_DIR`, `WALLAPOP_GALLETON_URL`, or
`WALLAPOP_GALLETON_TOKEN_FILE` explicitly select an externally managed daemon for
advanced deployments. The CLI neither starts nor stops that service. The supplied
`examples/galleton.wallapop.json` is for that mode only. Its settings must reach
all relevant CLI/watch/MCP processes. Keep using the same external daemon for
existing external profiles; switching storage is not an automatic cross-vault
migration. Normal users should leave these variables unset.

`WALLAPOP_SESSION_TOKEN` remains an explicit, non-persistent cookie override for
CI. It uses the in-process token path and is never silently imported into the
persistent vault. Remove it to use a saved managed profile. Explicit `auth login`
always imports the supplied cookie into managed storage.

## Provider limits and verification

The bundled adapter only trusts `https://es.wallapop.com` and
`https://api.wallapop.com`, maps the existing `/api/auth/session` response's
`/token`, requires a retained replacement cookie, and schedules renewal within
four minutes. It does not confuse the session `expires` field with bearer-token
expiry, follow provider redirects, bypass revocation, or retry ambiguous renewal
failures. Provider behavior may change and still require reauthentication.

Only the short-lived API bearer token reaches the existing Wallapop HTTP client.
Uploads and chat retain their direct transports; the CLI does not inherit the
daemon's managed-request body limits or forward arbitrary returned headers.

Tests cover the official SDK contract, fake-provider login/migration, process
leases, real bundled daemon startup/shutdown, TLS-provider cookie rotation and
restart persistence, secret handling, and release compilation. These tests do
not verify a live Wallapop account. No live account credentials are used in CI.
