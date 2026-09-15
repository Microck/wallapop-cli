# wallapop-cli usage

`wallapop` is a terminal client for Wallapop. Output is JSON on stdout by default; `--format pretty` for humans, `--format jsonl` to stream, `--format toon` for compact agent context. Errors go to stderr; `--error-format json` gives a machine-readable envelope.

## Exit codes

| code | meaning |
|---|---|
| 0 | success (including "nothing changed") |
| 1 | generic failure |
| 2 | usage: bad flags, unknown filter key, missing location, cancelled prompt |
| 3 | auth: no session or session rejected (`wallapop auth login`) |
| 4 | not found |
| 5 | blocked or rate limited by Wallapop; wait and retry |
| 6 | network or timeout |
| 7 | Wallapop changed its API; stderr has a prefilled issue link |
| 130 | interrupted |

## Login

```
wallapop auth login --cookies cookies.txt        # Netscape export from a logged-in browser
wallapop auth status --check
```

The only secret kept is the `__Secure-next-auth.session-token` cookie. Profiles: `--profile NAME` or `WALLAPOP_PROFILE`.

The session slides: every authenticated command renews it for 30 days, and 30 days without one ends it. `wallapop watch check` renews it as a side effect, so a scheduled watch keeps the session alive. Without watches, schedule `wallapop auth refresh` (prints the new expiry); `auth status` shows the current one.

## Search

```
wallapop search "thinkpad x1" --max-price 400 --sort newest --limit 20
wallapop search --category 100 --filter brand=Toyota --filter max_km=120000
wallapop search filters --category 100          # keys and values for --filter
wallapop category list
```

Output: `{"items":[{hash,title,price,currency,reserved,sold,shippable,location,url,created_at,modified_at,...}],"next_page":"..."}`. Pass `next_page` back with `--next` for more.

## Items, sellers, account

```
wallapop item show HASH_OR_URL
wallapop item favorite HASH | unfavorite HASH
wallapop item reserve HASH [--off] | sold HASH --yes | delete HASH --yes   # own items
wallapop user show USER | items USER | reviews USER        # hash, profile URL or slug
wallapop me show | items [--sold] | favorites
```

## Chat

```
wallapop chat list [--unread] [--archived]
wallapop chat show CONV [--limit N] [--no-mark-read]
wallapop chat send CONV "text"          # or: echo text | wallapop chat send CONV -
wallapop chat start ITEM "text"         # opens the conversation with the seller if needed
wallapop chat open CONV                 # line-mode live chat; --format jsonl streams incoming
wallapop chat archive CONV [--undo]
```

CONV is a conversation hash, a unique prefix, or an item hash you already talk about. Messages are oldest first.

## Watches

```
wallapop watch add search "thinkpad" --max-price 400 --name x1 --notify phone   # add --emit-initial to report what exists now
wallapop watch add item HASH_OR_URL --name bike
wallapop watch add seller USER --name good-seller
wallapop watch check [NAME...] [--all]                      # one shot, prints events
wallapop watch run [--interval 2m]                          # foreground loop
wallapop watch events [NAME] --since 24h
wallapop watch service install|uninstall|status            # systemd/launchd timer
```

Event shape: `{"type":"item.new|item.price_changed|item.reserved|item.unreserved|item.sold|item.removed|item.edited|seller.new_item","watch":"x1","profile":"default","at":"...","item":{...},"change":{"from":300,"to":250}}`.

Sinks are config entries: `[sinks.phone] type="ntfy" url="https://ntfy.sh" topic="deals"`, `type="webhook" url=... template="discord"`, `type="exec" command=["/path/script"]` (event JSON on stdin). `wallapop sink test phone` sends a synthetic event.

## Config

`$XDG_CONFIG_HOME/wallapop-cli/config.toml`; `wallapop config set watch.interval 10m`; `wallapop config path`. Credentials live in `credentials.toml` (0600), state in `$XDG_DATA_HOME/wallapop-cli/state.db`.
