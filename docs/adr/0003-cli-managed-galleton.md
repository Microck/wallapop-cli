# 0003: CLI-managed Galleton lifecycle

## Decision

Use the official Go SDK and a pinned Galleton daemon for persistent profiles. Keep the
engine a separate process: it owns the vault lock and coordinates concurrent CLI, watch,
and MCP operations. Do not duplicate its renewal/storage engine inside each command.

Distribution is a single wallapop executable containing the matching engine bytes. Runtime
extraction is private and hash-checked. A same-executable host supervises the engine and
coordinates process leases; it does not inherit provider credentials or application secrets.
A short idle grace period avoids a daemon per command without installing perpetual startup.
Unattended renewal and OS-login startup require explicit `auth service enable` or `run`.

Normal login/search/chat commands need no new environment variables or separately installed
Galleton. Legacy profiles migrate automatically, but do not lose their local cookie until
a usable token has been received and metadata can be saved. Provider operations are never
silently replayed on ambiguous errors. Metadata updates use cross-process compare-and-swap.

## Tradeoffs

Bundled releases are larger. Native engine executables must be built/tested for every
supported release target. Plain Go installs cannot execute code generation and need a
one-time pinned engine build with Go; `make install` avoids runtime preparation.

The default engine stops after its clients exit. It cannot renew offline, after shutdown,
or bypass provider expiration. User-login service installation is separate from ordinary
use and does not silently enable system-wide privileges or Linux user lingering.

External-daemon environment overrides remain for advanced deployments. The explicit
non-persistent session environment override remains an exception to managed storage.

This supersedes ADR 0001's local persistence/cache implementation, not its choice to import
an authorized browser session. Wallapop transport details stay in internal/wallapop.
