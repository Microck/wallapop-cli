# wallapop-cli

Terminal client for the Wallapop marketplace: search and inspect listings, track them over time, and talk to sellers, all against Wallapop's unofficial web API.

## Language

### Marketplace

**Item**:
A single listing on Wallapop, identified by its 12-character hash. The numeric id in web URLs is an alias that resolves to the hash.
_Avoid_: Listing, product, ad, post

**Seller**:
The Wallapop user who published an Item. The same person is a **User** when viewed on their own, outside an Item.
_Avoid_: Owner, vendor

**Search**:
A keyword query plus a set of Filters, always centred on a Location.
_Avoid_: Query (that is only the keyword part)

**Filter**:
One constraint on a Search: price range, distance, condition, category, or a category-specific attribute such as a car's brand.

**Location**:
A latitude/longitude pair with a radius. Every Search has one; the default comes from the active Profile.

**Category**:
Wallapop's own taxonomy node for Items. Category-specific Filters exist only under some Categories.

### Tracking

**Watch**:
A saved instruction to observe one target (a Search, an Item, or a Seller) and report changes. Lives locally, never on Wallapop's servers.
_Avoid_: Alert, tracker, monitor, subscription

**Check**:
One run of a Watch: fetch the current state, compare it with the last stored state, emit Events, store the new state. Idempotent.
_Avoid_: Poll, tick, scan

**Event**:
One observed change produced by a Check: new Item, price changed, reserved, sold, removed, Seller listed something.
_Avoid_: Notification (that is the delivery, not the change), alert

**Sink**:
A destination an Event is delivered to besides stdout: ntfy, webhook, or an executable.
_Avoid_: Notifier, channel, target

**Saved search**:
Wallapop's server-side search alert, owned by the account. Distinct from a Watch.

### Account

**Profile**:
One named Wallapop account known to the CLI, with its Session and default Location. One Profile is the default.
_Avoid_: Account (ambiguous with the Wallapop account itself), user

**Session**:
The long-lived web session cookie and device id that let the CLI act as a Profile's account. Short-lived access tokens are minted from it and are not part of the Session.
_Avoid_: Credentials, cookies, login, token

**Access token**:
A five-minute bearer token minted from a Session, sent on every authenticated API call. Disposable; never stored as the secret.
_Avoid_: Session token, JWT

**Favorite**:
An Item the account has marked on Wallapop. Server-side, unlike a Watch.

### Chat

**Conversation**:
A Wallapop chat thread between the account and one other User about one Item.
_Avoid_: Chat, thread, room, channel (the PubNub channel is an implementation detail)

**Message**:
One text entry inside a Conversation, sent by either side.
