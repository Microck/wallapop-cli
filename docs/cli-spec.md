# wallapop-cli spec

Status: draft agreed in the design interview on 2026-09-14. Vocabulary in `../CONTEXT.md`.
Endpoint facts come from live read-only probes on that date; the API is unofficial and may drift.

## 1. Name

Binary `wallapop`. Module `github.com/Microck/wallapop-cli`. Go 1.27, single static binary.

## 2. One-liner

Search, track and chat on Wallapop from the terminal, with your own account.

## 3. Usage

```
wallapop [global flags] <noun> <verb> [args] [flags]
```

Noun-verb throughout. No catch-all subcommand, no abbreviations.

## 4. Command tree

```
auth       login | status | logout
profile    list | use | remove
search     [keywords...] | filters
category   list
item       show | open | favorite | unfavorite | reserve | sold | delete
user       show | items | reviews
me         show | items | favorites
chat       list | show | send | start | open | archive
watch      add (search|item|seller) | list | remove | check | run | events | service (install|uninstall|status)
sink       list | test
config     path | get | set | list
doctor
skills     list | get
completion <shell>
```

### auth

- `auth login [--profile NAME] [--cookies FILE | --cookies-stdin]`
  Default is the cookie import prompt: it asks for a Netscape cookie export (Cookie-Editor or
  similar) or the pasted value of `__Secure-next-auth.session-token`, reads that cookie plus
  `device_id`, mints an Access token once to validate, stores the Session, and seeds the
  Profile's default Location from `GET /api/v3/users/me`. Every login method (password,
  Google, Apple, Facebook) ends in the same browser cookie, so this is the only path.
  Email+password login (`POST /api/v3/access/login`) is not implemented: the flow was not
  verified and cookie import covers every account. Implicitly creates the Profile; the
  first Profile becomes default.
- `auth status` shows profile, account name, the stored cookie's expiry, where the session
  came from; `--check` mints once to prove the Session still works.
- `auth refresh` mints once and prints the same status as `auth status`, now carrying the
  rotated cookie's expiry. Exit 3 when the Session is rejected. For people who schedule with
  cron instead of `watch service`; `watch check` already does this on its own.
- `auth logout [--yes]` deletes the Profile's Session. Not a destructive Wallapop action, so no
  prompt; `--yes` exists for symmetry only.

Session mechanics (verified): `GET https://es.wallapop.com/api/auth/session` with the
session cookie returns `{token, idToken, expires}`; `token` is a Keycloak JWT valid 5 minutes.
The response rotates the session cookie via `Set-Cookie`; the CLI persists the newest one
(old ones keep working, so a lost rotation is not fatal). Access tokens are cached in memory
and re-minted 30 s before expiry. All `api.wallapop.com` calls send
`Authorization: Bearer <token>` and `X-DeviceOS: 0`.

Sliding window (verified 2026-09-14): every mint re-issues the cookie with a fresh 30-day
`Expires`, so running any authenticated command at least once every 30 days keeps the Session
alive indefinitely, and 30 days of silence ends it. Watches read public data and would never
mint, so `watch check` and each `watch run` tick mint once when the Profile has a stored
Session; a rejected Session there is a stderr warning, not a failure, because the Watches
still ran. The stored expiry is `session_expires` in `credentials.toml`. A Session from
`WALLAPOP_SESSION_TOKEN` is never persisted, so nothing slides it; re-export it before it
expires.

### profile

- `profile list` (name, account, default marker, session expiry)
- `profile use NAME` sets the default
- `profile remove NAME [--yes]` removes session and its Watches

Selection order: `--profile` > `WALLAPOP_PROFILE` > default in config.

### search

- `search [keywords...] [--lat F --lng F] [--distance KM] [--min-price N --max-price N]
  [--condition new|as_good_as_new|good|fair|has_given_it_all]... [--category ID]
  [--shipping] [--since today|week|month] [--sort relevance|newest|price_asc|price_desc]
  [--filter key=value]... [--limit N] [--pages N] [--next TOKEN]`
  Hits `GET /api/v3/search`. Location defaults to the Profile's, then config `location`.
  Without a Profile and without `--lat/--lng`, exit 2 with a hint. `--filter` is validated
  against `GET /api/v3/search/filters/regular-filters` for the resolved category; unknown keys
  exit 2 listing the valid ones. JSON output carries `next_page` for continuation.
- `search filters [--category ID] [keywords...]` prints the filters Wallapop offers for that
  category (id, type, allowed values). Source of truth for `--filter`.

### category

- `category list [--tree]` from `GET /api/v3/categories`.

### item

Item argument accepts a 12-char hash or an `es.wallapop.com/item/...` URL. Bare numeric ids
are rejected with a hint: the item page needs the slug, and Wallapop has no numeric-id route.
URLs resolve to the hash through the item page's `__NEXT_DATA__`; hashes combine the API
detail with the page flags.

- `item show ITEM` merges `GET /api/v3/items/{hash}` with the page flags (reserved, sold,
  expired, modified date, views, favorites).
- `item open ITEM` opens the web page in the browser.
- `item favorite ITEM` / `item unfavorite ITEM` via `PUT /api/v3/items/{hash}/favorite`.
- `item reserve ITEM [--off]`, `item sold ITEM [--yes]`, `item delete ITEM [--yes]` act on the
  account's own Items. `sold` and `delete` prompt in a TTY and require `--yes` otherwise.

### user / me

- `user show USER`, `user items USER`, `user reviews USER` (public endpoints; USER is id or slug).
- `me show`, `me items [--sold]`, `me favorites` (authenticated).

### chat

Conversation argument: conversation hash, unique prefix of it, or an Item hash the account
already has a Conversation about. Every endpoint below, including the writes and the PubNub
publish, was exercised live against the owner's test account on 2026-09-14.

- `chat list [--unread] [--archived] [--limit N]` from `GET /bff/messaging/inbox`
  (`page_size`, `max_messages`, `from=<next_from>`) or `GET /bff/messaging/archived`.
- `chat show CONV [--limit N]` from `GET /bff/messaging/conversation/{hash}` (last 30
  Messages embedded), older pages via
  `GET /api/v3/instant-messaging/archive/conversation/{hash}/messages?max_messages=30&from=`.
  Oldest first. Marks the Conversation read with a PubNub message action `seen` on the last
  Message, unless `--no-mark-read`.
- `chat send CONV TEXT` (`-` reads TEXT from stdin). PubNub publish on the Conversation's
  `channel` with `message={id,payload:{text}}` and
  `meta={type:"text",sender:{platform:{app_version,os_version}},to_user_hash,from_user_hash,conversation_hash}`,
  auth token from `GET /api/v3/instant-messaging/token` (PAM v3, grants `inbox.<me>`,
  `signals.<me>`, `chat.<me>.*.*`, `chat.*.*.<me>`). The channel string is opaque; never build it.
- `chat start ITEM TEXT`: `POST /api/v3/instant-messaging/conversation {item_hash_id}`
  (bundle) returns `{conversation_id, channel}`; error code 100 means Wallapop's cap on new
  conversations. Then `chat send`. If a Conversation for that Item already exists in the inbox,
  reuse it instead of posting.
- `chat open CONV` line-mode REPL: prints history, then long-polls PubNub subscribe on
  `inbox.<me>` and prints Messages whose `conversation_hash` matches, while reading lines from
  stdin to send. `/quit` or Ctrl-D exits. No TUI.
- `chat archive CONV [--undo]`: `PUT /api/v3/instant-messaging/conversations/archive`
  `{conversation_ids:[hash]}` (bundle), `unarchive` for `--undo`. 409 counts as success.

Not exposed: block/unblock, phone sharing, translation.

### watch

Watches, Checks, Events and Sinks are defined in `CONTEXT.md`.

- `watch add search [keywords...] [search flags] --name NAME [--notify SINK]... [--interval DUR] [--pages N] [--emit-initial]`
- `watch add item ITEM --name NAME [--notify]... [--emit-initial]`
- `watch add seller USER --name NAME [--notify]... [--emit-initial]`
  `add` runs the first Check immediately as a silent baseline; with `--emit-initial` that
  baseline is reported (and delivered to Sinks) as `item.new` / `seller.new_item` Events.
- `watch list`, `watch remove NAME [--yes]`.
- `watch check [NAME...] [--all]` runs one Check per named Watch (default: all due). Prints
  Events (JSON array, or one object per line with `--format jsonl`), delivers them to the
  Watch's Sinks, updates state. Idempotent; exit 0 even when nothing changed. Mints once when
  the Profile has a stored Session so the scheduled timer keeps it alive (see auth).
- `watch run [--interval DUR]` foreground loop: `check --all` every interval until Ctrl-C.
  Interval default 5m, floor 30s, plus up to 10% jitter.
- `watch events [NAME] [--since DUR] [--limit N]` reads stored Event history.
- `watch service install [--interval DUR]` writes a systemd user service+timer (Linux) or
  a launchd agent (macOS) that runs `wallapop watch check --all --profile ...`. On Windows
  it prints the `schtasks` command to run by hand. `uninstall` removes it, `status` shows the
  unit state and last run. Logs go to journald / a file under the state dir.

Event types: `item.new`, `item.price_changed`, `item.reserved`, `item.unreserved`,
`item.sold`, `item.removed`, `item.edited`, `seller.new_item`. Event shape:

```json
{"type":"item.price_changed","watch":"iphone","profile":"default","at":"2026-09-14T10:40:00Z",
 "item":{"hash":"...","title":"...","price":{"amount":250,"currency":"EUR"},"url":"..."},
 "change":{"from":300,"to":250}}
```

Search Watches diff the first `--pages` pages (default 1): new hashes and price changes on
already-seen hashes. Item Watches diff the item detail. Seller Watches diff the user's item list.

### sink

Sinks live in config, not in the database:

```toml
[sinks.phone]
type = "ntfy"
url = "https://ntfy.sh"
topic = "wallapop"
token_file = "~/.config/wallapop-cli/ntfy-token"   # optional

[sinks.discord]
type = "webhook"
url = "https://discord.com/api/webhooks/..."
# body: the Event JSON; set template = "discord" for a {content: ...} wrapper

[sinks.script]
type = "exec"
command = ["~/bin/on-wallapop-event"]
# receives one Event JSON on stdin per Event
```

- `sink list`, `sink test NAME` (sends a synthetic Event).

### config / doctor / skills / completion

- `config path | list | get KEY | set KEY VALUE` on `config.toml`.
- `doctor` checks: config parses, Session mints, `api.wallapop.com` reachable, PubNub token
  obtainable, service unit state. Exit 1 if any check fails.
- `skills list | get NAME` prints the embedded agent-facing docs (usage, output shapes).
- `completion bash|zsh|fish|powershell` (cobra).

## 5. Global flags

| flag | default | meaning |
|---|---|---|
| `--profile NAME` | config default | which Profile acts |
| `--format json\|pretty\|jsonl\|toon` | `json` | stdout rendering. `pretty` is for humans; `jsonl` streams one object per line (watch, chat open) |
| `--no-color` | off | also honours `NO_COLOR`, `TERM=dumb`, non-TTY |
| `--error-format text\|json` | `text` | stderr error rendering |
| `--no-input` | off | never prompt; missing input is an error |
| `--debug` | off | HTTP request/response log on stderr, secrets redacted |
| `-h, --help`, `--version` | | |

`-q/--quiet` is not offered; JSON output is already minimal and `--format` covers the rest.

## 6. I/O contract

- stdout: data only, one JSON document per command (or JSON lines). `pretty` output never
  goes to stdout when stdout is not a TTY unless asked for explicitly.
- stderr: errors, prompts, progress, debug.
- Prompts only when stdin, stdout and stderr are all TTYs and `--no-input` is absent.
- Secrets never appear in argv: cookies come from a file, stdin or the wizard.

## 7. Exit codes

| code | meaning |
|---|---|
| 0 | success (including "nothing changed") |
| 1 | generic failure |
| 2 | usage: bad flags, unknown filter key, missing location |
| 3 | auth: no Session, Session rejected, MFA pending |
| 4 | not found: item/user/conversation/watch |
| 5 | blocked or rate limited (CloudFront 403, 429) |
| 6 | network / timeout |
| 7 | API changed: known endpoint returned an unexpected shape or status |
| 130 | interrupted |

## 8. Errors

Text errors are one sentence, lowercase-led, consequence plus remedy. JSON envelope on stderr
with `--error-format json`: `{code, category, retryable, message, http_status, endpoint,
suggested_commands, issue_url}`.

Exit 7 always prints an `issue_url` pointing at
`https://github.com/Microck/wallapop-cli/issues/new?template=api-change.yml&...` with the
form fields prefilled: CLI version, OS/arch, command path (no argument values), endpoint path,
HTTP status, first 300 characters of the response body with tokens redacted. Plus a hint to
rerun with `--debug`.

User agent is `wallapop-cli/<version> (+https://github.com/Microck/wallapop-cli)` (verified
accepted on both hosts). If CloudFront answers 403 to it, the request is retried once with a
browser user agent and a one-time stderr note says so.

## 9. Config, state, env

Paths via XDG (`adrg/xdg`):

| what | path | mode |
|---|---|---|
| config | `$XDG_CONFIG_HOME/wallapop-cli/config.toml` | 0644 |
| sessions | `$XDG_CONFIG_HOME/wallapop-cli/credentials.toml` | 0600, atomic writes |
| state | `$XDG_DATA_HOME/wallapop-cli/state.db` (SQLite, `modernc.org/sqlite`) | 0600 |
| logs | `$XDG_STATE_HOME/wallapop-cli/` | |

`credentials.toml` holds one table per Profile: session cookie, device id, account hash, name.
It is never relocatable by config, only by XDG variables.

Config (`pelletier/go-toml/v2`, unknown keys are errors):

```toml
default_profile = "default"

[profiles.default]
location = { lat = 40.4168, lng = -3.7038, radius_km = 20 }

[watch]
interval = "5m"

[sinks.phone]
type = "ntfy"
url = "https://ntfy.sh"
topic = "wallapop"
```

Env: `WALLAPOP_PROFILE`, `WALLAPOP_CONFIG` (config file path), `WALLAPOP_SESSION_TOKEN`
(session cookie value for CI, wins over the credentials file), `WALLAPOP_ERROR_FORMAT`,
`WALLAPOP_API_BASE_URL` / `WALLAPOP_WEB_BASE_URL` / `WALLAPOP_PUBNUB_BASE_URL` (tests),
`NO_COLOR`, `HTTPS_PROXY`. Precedence: flags > env > config.

## 10. Safety

- Destructive on Wallapop's side, so prompt or `--yes`: `item sold`, `item delete`.
- `--yes` accepted but never required elsewhere.
- `watch check` and `watch run` are idempotent and crash-only: state is written after Events
  are delivered, so a crash re-emits rather than loses.
- Ctrl-C exits within one in-flight request; a second Ctrl-C exits immediately.
- Never phones home. No analytics.

## 11. Examples

```sh
wallapop auth login                                   # wizard, cookie export
wallapop auth login --cookies ~/Downloads/wallapop.txt --profile work
wallapop search "thinkpad x1" --max-price 400 --distance 50 --sort newest --format pretty
wallapop search --category 100 --filter brand=Toyota --filter max_km=120000 --limit 20
wallapop search filters --category 100 --format pretty
wallapop item show https://es.wallapop.com/item/thinkpad-x1-carbon-1092837465
wallapop item favorite k2j3h4g5f6d7
wallapop watch add search "thinkpad x1" --max-price 400 --name x1 --notify phone
wallapop watch add item k2j3h4g5f6d7 --name that-bike
wallapop watch check --all --format jsonl | jq -r 'select(.type=="item.new") | .item.url'
wallapop watch service install --interval 10m
wallapop chat list --unread --format pretty
wallapop chat send 8f1c2 "Sigue disponible?"
wallapop chat start k2j3h4g5f6d7 "Hola, lo recogería hoy"
echo "Te lo dejo en 200" | wallapop chat send 8f1c2 -
wallapop chat open 8f1c2
```

## 12. Open items carried into implementation

- PubNub subscribe (live receive in `chat open`) is tested against the fake server only; a
  second account is needed to exercise it end to end.
- Authenticated writes work with `Authorization` + `X-DeviceOS: 0` alone; no extra device
  headers were needed.
- Legacy XMPP transport exists behind a feature flag on the web; the CLI implements PubNub only.

## 13. Not in v1

Email+password login, creating or editing Items (image upload), Wallapop server-side saved searches (v2, read-only),
offers and anything touching wallet, shipping or payments (never), settings edits (never),
MCP server (v2), AUR/npm/Mintlify docs (after first stable release), Windows service install.

## 14. Stack

cobra + pflag, `net/http` (no HTTP framework, no PubNub SDK), `modernc.org/sqlite`,
`pelletier/go-toml/v2`, `adrg/xdg`, goreleaser 2.x (homebrew cask, scoop, checksums with
GitHub attestations), `curl | sh` installer. Tests: stdlib `testing`, `httptest.Server` as a
fake Wallapop with redacted recorded fixtures, binary-level tests with an isolated XDG home.
No mocks.

## 15. Layout

```
cmd/wallapop/main.go entrypoint; lives under cmd/ so `go install` names the binary wallapop
internal/cli/        one file per noun (auth.go, search.go, item.go, chat.go, watch.go, ...)
internal/wallapop/   API client: session.go, search.go, items.go, users.go, chat.go, pubnub.go
internal/store/      sqlite schema, watches, events, seen-state
internal/watch/      check logic per target type, diffing, event construction
internal/sink/       ntfy, webhook, exec
internal/output/     json/jsonl/pretty/toon renderers, error envelope, exit codes
internal/config/     config.toml, credentials.toml, xdg paths
internal/cli/skills/ embedded agent docs (go:embed needs them beside the package)
docs/                this spec, ADRs
```
