# Galleton-managed sessions

[Galleton](https://github.com/Microck/galleton) can replace the CLI's in-process
cookie renewal and plaintext cookie persistence with a local, encrypted session
vault. The CLI uses Galleton's HTTP/JSON API; no Node.js runtime or additional Go
module dependency is needed. Install and run the Galleton daemon separately.

## Start the daemon

Build or install Galleton following its README. From a wallapop-cli checkout, use
the supplied provider configuration (replace the example absolute state path):

```sh
galleton init --dir /absolute/private/galleton-state
galleton serve --dir /absolute/private/galleton-state --config ./examples/galleton.wallapop.json
```

Keep the daemon running. In the shell running wallapop-cli:

```sh
export WALLAPOP_GALLETON_DIR=/absolute/private/galleton-state
wallapop auth login --cookies ~/Downloads/cookies.txt
wallapop auth status --check
```

Only import a browser session belonging to an account you are authorized to use.
Never put cookie values or the daemon API token in command arguments, committed
files, or logs. The normal cookie file/stdin import options remain available.

The adapter is a mapping of the CLI's existing Wallapop session endpoint, not a
new login method. It permits only `https://es.wallapop.com` and
`https://api.wallapop.com`, extracts `/token`, and requires a replacement cookie.
It requests renewal at most four minutes after a successful renewal, following
the legacy client's conservative interval. Wallapop's session `expires` field is
not treated as the access token's expiry. Ambiguous renewals are not automatically
retried (`retry_safe: false`). Provider changes, revocation, rate limits, or
reauthentication requirements can still stop renewal. This does not create a
permanent session or bypass provider restrictions.

## Connection settings

| Environment variable | Meaning |
| --- | --- |
| `WALLAPOP_GALLETON_DIR` | Private daemon state directory containing `api.token`. |
| `WALLAPOP_GALLETON_URL` | Local daemon origin; default `http://127.0.0.1:8766`. Only literal loopback HTTP addresses are accepted. |
| `WALLAPOP_GALLETON_TOKEN_FILE` | Explicit API token file, taking precedence over the directory setting. |

Setting any of these opts new logins and existing cookie profiles into Galleton.
With no directory/token-file override, the token is read from
`os.UserConfigDir()/galleton/api.token`, matching Galleton's default directory.
For a custom daemon directory or port, supply the same settings to every CLI,
watch service, and MCP process that uses the profile. The CLI neither starts the
daemon nor installs its startup service; see Galleton's deployment documentation.

The API token administers every session in that daemon. Protect its directory
and token file, and use separate OS users/daemon instances for separate trust
domains. Requests to the daemon never use environment proxies or follow
redirects. The token is never sent to Wallapop.

## Migrate an existing profile

After setting the connection environment and starting the daemon, run:

```sh
wallapop auth status --check --profile work
# Or force a renewal:
wallapop auth refresh --profile work
```

The first token request imports the saved cookie into a stable, namespaced daemon
session. After Galleton supplies a usable bearer token, the CLI atomically replaces
the local cookie with a `galleton_id` and account metadata. `credentials.toml`
remains mode `0600`, but no longer contains the managed profile's cookie or any
access token. Other profiles are not migrated until they are used.

A failed daemon call does not discard the local cookie. If the daemon accepted
an import but the local metadata write fails, retrying adopts that daemon
session instead of overwriting its newer credentials with the old cookie.
Concurrent first imports likewise adopt the winning import. Explicit
`auth login` is the only operation that replaces an existing daemon session, and
it uses the daemon's inspected revision to reject concurrent replacements.

Once a profile has a `galleton_id`, it **never falls back to in-process renewal**,
even if the connection environment is removed or the daemon is unavailable. A
missing daemon session requires a new explicit login. A failed login can leave an
imported session in the daemon; inspect it with `galleton list` / `galleton status`
before deciding whether to reconnect or forget it.

Unconfigured, unmigrated profiles retain the old behavior for compatibility.
`WALLAPOP_SESSION_TOKEN` remains an explicit, non-persistent environment override:
it uses the legacy in-process path and is not silently imported into Galleton.
Remove that override to use the profile's managed session.

## Status, refresh, and removal

`wallapop auth status` and `profile list` read local metadata without contacting
the daemon. Managed status reports `source: "galleton"` and `galleton_id`.
`auth status --check` asks the daemon for a usable API token; `auth refresh`
explicitly requests a renewal, even when the daemon has a cached token.

The daemon API does not expose individual cookie expiry, so managed profiles
omit `session_expires` rather than confusing it with access-token expiry.
`updated_at` is the local metadata update, not the latest daemon refresh. For
renewal timing or paused-session details, use the returned ID:

```sh
galleton status --dir /absolute/private/galleton-state SESSION_ID
```

`auth logout` and `profile remove` forget the corresponding daemon session before
deleting local metadata. If the daemon cannot confirm deletion, the command fails
and preserves the local reference so removal can be retried. This forgets CLI
credentials; it does not revoke the original browser login at Wallapop.

The CLI requests only the short-lived bearer token for the API origin. It does
not forward daemon cookies or arbitrary returned headers. Uploads and chat keep
their existing direct transports instead of inheriting Galleton managed-request
body limits. Cookie renewal and persistence belong solely to the daemon for
managed profiles.

## Verification

The tests exercise the HTTP contract and CLI flows against local fake services:
login, migration, profile isolation, revision-checked reconnect, refresh, logout,
errors, and secret handling. They do not contact a real Wallapop account or prove
that the live provider endpoint remains unchanged. The adapter should be verified
with an authorized account before relying on unattended renewal.
