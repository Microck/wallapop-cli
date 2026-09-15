# wallapop-cli v1

Vocabulary: `CONTEXT.md`. Decisions already recorded: `docs/adr/0001-session-from-web-cookie.md`. Interface contract: `docs/cli-spec.md`.

## Problem Statement

Wallapop is a browser-and-app-only marketplace. Someone hunting for a specific second-hand item has to reopen the site, re-enter the same Search, scroll past what they already saw, and remember which prices changed. Talking to a Seller means switching to the web chat. Nothing can be scripted, scheduled, piped into other tools, or driven by an agent, and there is no public API.

## Solution

A single-binary Go CLI, `wallapop`, that logs in with the user's own account by importing the browser Session, and then exposes the web's buyer-side features as noun-verb commands with JSON output by default: Search with every Filter Wallapop offers, Item and Seller inspection, Favorites, own listings, Conversations and Messages, and local Watches that turn the marketplace into a stream of Events delivered to stdout or Sinks. Background Checks run from the OS scheduler. When Wallapop changes its API, the CLI fails loudly with a prefilled GitHub issue link instead of degrading.

## User Stories

### Authentication and profiles

1. As a buyer, I want to log in by exporting cookies from my browser, so that Google, Apple, Facebook and password accounts all work the same way.
2. As a buyer, I want the CLI to keep my Session alive by re-persisting the rotated cookie, so that I log in once a month, not once a day.
3. As a buyer, I want to paste a cookie export path or a bare cookie value into a prompt, so that I do not have to remember flags.
4. As a script author, I want to pass the cookie export with a flag or on stdin, so that login works without a terminal.
5. As a buyer, I want the CLI to validate the Session at login and tell me who I am logged in as, so that a bad export fails immediately.
6. As a buyer, I want my account location saved as the default search Location at login, so that my first search already works.
7. As a buyer with two accounts, I want named Profiles, so that each account has its own Session, Location and Watches.
8. As a buyer with two accounts, I want to switch the default Profile, so that I do not type `--profile` every time.
9. As a buyer, I want to see the state of my Session, so that I know whether it is still valid before running a batch.
10. As a buyer, I want to log out of one Profile without losing its config and Watches, so that re-login is cheap.
11. As a buyer, I want to remove a Profile entirely including its Watches, so that an old account leaves no residue.
12. As a CI user, I want to supply the Session cookie by environment variable, so that a job can run without a credentials file.
13. As a privacy-conscious user, I want the credentials file kept separate from config at mode 0600, so that sharing my config never leaks my Session.

### Search

14. As a buyer, I want to search by keywords around my Location with a radius, so that results are things I can actually go and collect.
15. As a buyer, I want price, condition, category, shipping and listing-age filters as flags, so that the common web filters are one keystroke away.
16. As a buyer, I want to sort by newest, price or relevance, so that I can spot fresh or cheap listings first.
17. As a car buyer, I want brand, model, mileage, year, gearbox and engine filters, so that vertical filters match the web.
18. As a buyer, I want vertical filters validated against what Wallapop currently offers, so that a typo fails with the list of valid keys instead of silently returning everything.
19. As a buyer, I want to list the Filters available for a category, so that I can discover what `--filter` accepts.
20. As a buyer, I want to browse the category tree with ids, so that I can pick the right `--category`.
21. As a buyer, I want to fetch several result pages or continue from a page token, so that deep searches are possible.
22. As a buyer, I want a compact human table with hash, price, flags, title, city and URL, so that I can scan results in the terminal.
23. As a buyer, I want to override the Location per invocation, so that I can search somewhere I am travelling to.

### Items, sellers, account

24. As a buyer, I want the full detail of an Item including reserved, sold and expired flags, so that I know whether it is still worth messaging about.
25. As a buyer, I want to pass an Item URL copied from the browser as well as a hash, so that I never have to extract the hash by hand.
26. As a buyer, I want to open an Item in my browser from the terminal, so that I can see the photos.
27. As a buyer, I want to add and remove Favorites, so that my web favorites list stays in sync with what I found in the terminal.
28. As a buyer, I want to see a Seller's profile, rating, sales count and reviews, so that I can judge whether to buy from them.
29. As a buyer, I want to list a Seller's other Items, so that I can bundle purchases.
30. As a buyer, I want to look up a Seller by profile URL or slug, so that I do not need their hash.
31. As a seller, I want to list my own active and sold Items, so that I can see my inventory.
32. As a seller, I want to mark my Item reserved or unreserved, so that buyers see its state while I negotiate.
33. As a seller, I want to mark my Item sold or delete it, with a confirmation prompt or `--yes`, so that an irreversible action never happens by accident.
34. As a buyer, I want to list my Favorites, so that I can review them in the terminal.

### Chat

35. As a buyer, I want to list my Conversations with unread counts and last Message, so that I see what needs a reply.
36. As a buyer, I want to filter to unread Conversations only, so that I can triage quickly.
37. As a buyer, I want to read a Conversation oldest-first with older pages loaded on demand, so that context is in reading order.
38. As a buyer, I want reading a Conversation to mark it read as the web does, unless I opt out, so that the web badge matches.
39. As a buyer, I want to send a Message into an existing Conversation, with the text from an argument or stdin, so that templated replies can be scripted.
40. As a buyer, I want to message a Seller about an Item I have no Conversation with yet, so that first contact does not need the browser.
41. As a buyer, I want the CLI to reuse an existing Conversation about that Item rather than open a duplicate, so that the Seller is not spammed.
42. As a buyer, I want to refer to a Conversation by a hash prefix or by the Item's hash, so that I do not type 12 characters.
43. As a buyer, I want a line-mode chat that streams incoming Messages while I type replies, so that a live negotiation works in one terminal.
44. As an agent author, I want incoming Messages as JSON lines, so that a program can react to them.
45. As a buyer, I want to archive and unarchive Conversations, so that my inbox stays clean.
46. As a buyer, I want a clear error when Wallapop refuses to open more new Conversations, so that I know it is their cap and not my mistake.

### Watches, Checks, Events, Sinks

47. As a buyer, I want to save a Search as a Watch, so that new listings find me instead of the other way round.
48. As a buyer, I want to watch a single Item for price changes, reservation, sale or removal, so that I can act at the right moment.
49. As a buyer, I want to watch a Seller for new listings, so that I catch what a good seller posts next.
50. As a buyer, I want the first Check to record a baseline silently, so that adding a Watch does not flood me with everything that already exists.
51. As a buyer, I want the option to emit everything on the first Check, so that I can seed a feed intentionally.
52. As a buyer, I want a one-shot Check that prints only the Events since the last run, so that cron or a systemd timer can drive it.
53. As a buyer, I want a foreground loop with an interval, so that I can leave a terminal open while I wait for a deal.
54. As a buyer, I want the interval floored and jittered, so that many Watches do not hammer Wallapop in lockstep.
55. As a buyer, I want Events as JSON objects with type, watch, item and change, so that I can filter them with jq.
56. As a buyer, I want Events delivered to ntfy, so that my phone buzzes.
57. As a buyer, I want Events posted to a webhook with Discord and Slack text templates, so that they land in a channel.
58. As a buyer, I want Events piped to my own executable, so that anything not built in is one script away.
59. As a buyer, I want Sinks declared in config and attached to Watches by name, so that one phone target serves many Watches.
60. As a buyer, I want to test a Sink with a synthetic Event, so that I know the plumbing works before the real deal appears.
61. As a buyer, I want a failed Sink to be reported but not to block state being saved, so that one dead webhook does not re-fire every Event forever.
62. As a buyer, I want the Event history stored and queryable by Watch and time, so that I can see what happened while I was away.
63. As a buyer, I want to install a background schedule with one command on Linux and macOS, so that Checks survive reboots without a daemon.
64. As a Windows user, I want the exact scheduler command printed for me, so that I can set it up by hand.
65. As a buyer, I want to see and remove the background schedule, so that uninstalling is clean.
66. As a buyer with two Profiles, I want each Watch bound to the Profile that created it, so that Checks use the right account and Location.

### Output, errors, configuration

67. As a script author, I want JSON on stdout by default and everything else on stderr, so that pipelines never break on decoration.
68. As a human, I want `--format pretty` tables with colour that respect `NO_COLOR`, `TERM=dumb` and non-TTY, so that it looks right everywhere.
69. As an agent author, I want `--format toon` and `--format jsonl`, so that output is token-efficient or streamable.
70. As a script author, I want distinct exit codes for usage, auth, not-found, blocked, network and API-changed failures, so that retries and alerts can be decided without parsing text.
71. As a script author, I want a JSON error envelope on stderr with category, retryability and suggested commands, so that a wrapper can recover automatically.
72. As a user, I want API-changed errors to print a GitHub issue link with version, OS, command path, endpoint, status and a redacted body excerpt prefilled, and never my search terms or coordinates, so that reporting takes one click and leaks nothing.
73. As a user, I want the CLI to identify itself honestly by user agent and fall back to a browser user agent only if the CDN rejects it, telling me once, so that I know what it is doing on my behalf.
74. As a user, I want a debug flag that logs every request with secrets redacted, so that I can diagnose without exposing my Session.
75. As a user, I want config in XDG paths with a TOML file that rejects unknown keys, so that typos are caught.
76. As a user, I want to read and set config keys from the CLI, so that I do not have to know the file location.
77. As a user, I want a doctor command that checks config, Session, API reachability, chat token and schedule state, so that setup problems are one command to find.
78. As an agent author, I want embedded usage docs retrievable from the CLI, so that an agent can learn the tool without the web.
79. As a user, I want shell completion for bash, zsh, fish and PowerShell, so that flags and enum values are discoverable.
80. As a user, I want `--no-input` to disable every prompt and fail with an actionable message, so that automation never hangs.
81. As a user, I want Ctrl-C to exit promptly with code 130, so that a stuck long-poll does not trap me.

### Distribution

82. As a user, I want `go install`, a `curl | sh` installer, Homebrew and Scoop, so that installation matches my platform.
83. As a security-minded user, I want release checksums with GitHub attestations, so that I can verify the binary.
84. As a reader, I want a README with a disclaimer that this is unofficial and against Wallapop's terms, so that I choose knowingly.

## Implementation Decisions

- Language and stack: Go, cobra with pflag, `net/http` only (no PubNub SDK), `modernc.org/sqlite`, `pelletier/go-toml/v2`, `adrg/xdg`, `toon-go` for the toon format, goreleaser for releases. Single static binary.
- Module layout: one API package that owns every Wallapop host, path, header and raw shape and normalizes them into a stable output contract (Item, User, Conversation, Message, Filter, Category). Separate packages for config and credentials, the state store, Check logic, Sinks, output rendering, and the cobra command tree with one file per noun. Upstream drift is fixed in one package.
- Session model (ADR 0001): the stored secret is the NextAuth web session cookie plus device id. Access tokens are minted from the web session route, cached in memory, refreshed 30 s before their 5-minute expiry, and never persisted. The rotated cookie returned by each mint is re-persisted. An empty mint response is an auth failure, not an API-changed failure. Password login is not implemented in v1; the endpoint exists but the flow was not verified, and cookie import covers every login method.
- Cookie import accepts a Netscape export file, a `name=value` list, a `Cookie:` header line, or the bare cookie value. Only the session cookie and `device_id` are kept.
- Profiles: `--profile` > `WALLAPOP_PROFILE` > config default > `default`. Login creates the Profile implicitly; the first one becomes default. Credentials live in a separate 0600 TOML file keyed by Profile, never relocatable by config.
- Search: `category_id` is the query key the backend honours. Common filters are typed flags mapped to Wallapop's keys (`min_sale_price`, `distance_in_km`, `time_filter`, `order_by`, `condition`, `is_shippable`). Vertical filters go through `--filter key=value`, validated against the regular-filters endpoint for the resolved category; unknown keys and list values are usage errors (exit 2) listing the valid set. Pagination is the opaque `next_page` JWT passed back verbatim.
- Item references: 12-character hash or full item URL. Bare numeric ids are rejected with a hint because the item page requires the slug. Hash lookups combine the API detail (slug, description, condition, counters) with the item page's `__NEXT_DATA__` (reserved, sold, expired, on-hold, modified date). URL lookups need the page only. A page 404 after an API hit marks the Item expired.
- User references: hash, profile URL, or slug; slugs resolve through the profile page's `__NEXT_DATA__`.
- Chat: inbox and single-conversation reads come from the messaging BFF; older pages from the instant-messaging archive endpoint; the BFF's newest-first order is reversed to oldest-first. Sending publishes over PubNub REST on the server-supplied channel with the web's message and meta shape (`type: text`, sender platform, from/to user hashes, conversation hash). Receiving long-polls PubNub subscribe on `inbox.<user hash>` and filters by conversation hash. Read receipts are PubNub message actions of type `seen` on the last inbound Message. New Conversations are created with the instant-messaging conversation endpoint and API code 100 is surfaced as "blocked". Archive and unarchive treat 409 as success. Channel strings are opaque and never constructed.
- Conversation references: full hash tried directly; otherwise prefix or Item hash matched across the first three inbox pages; multiple matches are a usage error listing them.
- Watches: stored in SQLite with kind (search, item, seller), a kind-specific JSON target, Sink names, interval, pages, Profile, and a baselined flag. Per-Watch seen state holds hash, price, reserved, sold, title, modified date and a JSON snapshot. Events are appended with type, time, item JSON and change JSON.
- Check semantics: search Watches diff the configured number of pages and report `item.new`, `item.price_changed`, `item.reserved`, `item.unreserved`; departures are not reported because page one churns. Item Watches additionally report `item.sold`, `item.removed` (page 404 or expired flag) and `item.edited` (modified date advanced with no other change). Seller Watches report `seller.new_item`, price and reservation changes, and `item.removed` when a hash leaves the complete list. The first Check baselines silently unless `--emit-initial`.
- Delivery order: Events are delivered to Sinks first, then the store commits seen state, removals, events and the check timestamp in one transaction. A crash re-emits rather than loses. Sink failures are written to stderr and do not abort the commit.
- Scheduling: `watch check` runs the named Watches, or all due Watches (last check plus interval elapsed). `watch run` loops in the foreground. Interval default 5 minutes from config, floor 30 seconds, plus up to 10% deterministic jitter. `watch service install` writes a systemd user service and timer on Linux or a launchd agent on macOS invoking `watch check --all --profile <p>` with logs under the XDG state directory; on Windows it prints the `schtasks` command. No daemon, no PID file.
- Sinks: declared in config as `[sinks.<name>]` with type ntfy (URL, topic, optional token file), webhook (URL, optional `discord` or `slack` template) or exec (command receiving the Event JSON on stdin plus type and watch name in the environment). Each Watch stores the Sink names it uses.
- Output: one JSON document per command by default; `jsonl` writes one object per line for slices; `pretty` uses tab-aligned tables and per-type renderers with no box drawing; `toon` for agents. Colour is off under `NO_COLOR`, `TERM=dumb`, `--no-color` or a non-TTY stdout. Prompts require stdin, stdout and stderr to all be TTYs and `--no-input` absent.
- Errors: a single typed error in the API package carries kind, HTTP status, Wallapop API code, endpoint, redacted 300-character body excerpt and retryability. Kinds map to exit codes 1 through 7 as in the interface contract; usage errors from the CLI layer are exit 2; interrupts are 130. `--error-format json` or `WALLAPOP_ERROR_FORMAT=json` emits an envelope on stderr with code, category, retryable, message, HTTP status, endpoint, suggested commands and issue URL. API-changed errors always carry an issue URL that prefills a GitHub issue form with version, OS/arch, command path, endpoint, status and body excerpt, never argument values.
- HTTP behaviour: honest user agent `wallapop-cli/<version> (+repo)`, `X-DeviceOS: 0` on every API call, 20-second timeout except the 5-minute PubNub long-poll. A CloudFront 403 triggers exactly one retry with a browser user agent for the rest of the process and one stderr notice. Base URLs for the API host, web host and PubNub are overridable by environment for tests. Secrets registered with the client are redacted from debug lines and error bodies.
- Confirmations: only `item sold`, `item delete` and `profile remove` prompt; non-interactive invocations need `--yes` or fail with exit 2. Everything else is reversible and runs without asking.
- Config commands operate on dotted keys over the TOML document and re-validate by decoding strictly after each set.
- Doctor runs a fixed checklist and exits 1 if any check fails: config parses, credentials parse, Session mints, categories endpoint reachable, chat token obtainable, schedule unit state.
- Skills docs are embedded in the binary and served by `skills list` and `skills get`.
- Distribution: goreleaser producing archives, checksums, Homebrew cask and Scoop manifest; GitHub attestations on the checksums in the release workflow; a `curl | sh` installer; `go install` works from the module path. README follows the author's lowercase template with a disclaimer section. MIT licence.

## Testing Decisions

A good test drives the CLI the way a user or script does and asserts only on observable behaviour: exit code, stdout bytes, stderr messages, files left on disk, and requests received by the fake server. No test reaches into package internals, and no mocks of Go interfaces are used; the substitute for Wallapop is a real HTTP server serving redacted recordings.

- Seam: the in-process CLI entrypoint that takes version, args, stdin, stdout and stderr and returns the exit code. Every behaviour is tested through it. The environment supplies isolated XDG config, data and state directories, and the three base-URL overrides pointing at the fake server.
- Fake Wallapop: one test-support HTTP server that serves the recorded response shapes captured on 2026-09-14 (search page with `next_page`, filters for general and cars categories, categories, item detail, item page HTML with `__NEXT_DATA__`, user, user items, stats, reviews, session mint with cookie rotation, inbox, conversation, older messages, chat token, PubNub publish, subscribe and message-action) with small mutable state so writes are observable: favorites toggled, conversations created, messages published, archive flags, and a request log the tests read back. It also plays the failure modes: CloudFront 403 HTML, 429, empty mint response, malformed JSON, 404 pages.
- Sinks are tested against the same fake server for ntfy and webhook, and against a small shell script for exec.
- Behaviours to cover: cookie import from each accepted input form and the resulting credentials file mode; login seeding the default Location; mint caching and rotated-cookie persistence; search flag to query-key mapping including `category_id`; `--filter` validation errors; pagination with `--pages` and `--next`; item lookup by hash and by URL, expired-on-404; conversation resolution by hash, prefix and item hash including ambiguity; `chat send` publishing the exact PubNub payload and meta; `chat start` reusing versus creating; `chat show` marking read and the opt-out; each Watch kind's Event set across two Checks including the silent baseline and `--emit-initial`; Sink delivery before commit and continued commit on Sink failure; due-Watch selection and interval floor; every exit code and the JSON error envelope; the issue URL contents and the absence of argument values in it; user-agent fallback on CloudFront 403 with a single notice; `--no-input` failing prompts; `--format` variants; `NO_COLOR` handling; confirmation gating on sold, delete and profile remove.
- Prior art: the author's other CLIs test by driving the compiled binary with the full environment scrubbed and an HTTP mock server behind base-URL overrides. This project keeps that shape but runs in-process through the entrypoint seam for speed, plus one process-level test that builds the binary and checks `--version`, `--help` and exit code propagation.
- Focused tests only: no smoke tests per command, no snapshot of every pretty table. Each test names the behaviour it protects.

## Out of Scope

- Email and password login and the MFA email-approve flow.
- Creating or editing Items, including image upload.
- Creating or deleting Wallapop's server-side saved searches (reading them and importing one as a Watch arrived with #6), offers, wallet, shipping, payments, reviews writing, settings edits, blocking users, phone sharing, translation.
- Reading browser cookie stores directly.
- A TUI, an MCP server, an `agent` command.
- Windows scheduled-task installation, Windows ACL hardening.
- XMPP transport.
- AUR, npm shim, Mintlify docs.
- Geocoding place names to coordinates.
- Any anti-bot evasion beyond the single browser user-agent fallback.

## Further Notes

- The two chat writes (create conversation, archive) and the PubNub publish, subscribe and message-action calls are recovered from the web bundle and have not yet been exercised live. The first live send will be made from the owner's test account against an Item of the implementer's choosing, as authorised.
- Wallapop's terms prohibit bots and data extraction. The README disclaimer states this and that account suspension is the stated remedy.
- Implementation status at the time of writing: the full command tree, fake-server test suite, README, goreleaser and CI config are in the repository; every read and write path except live PubNub receive was exercised against Wallapop on 2026-09-14 with the owner's test account.
