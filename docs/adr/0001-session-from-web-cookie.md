# 0001. Sessions come from the web session cookie, not from email/password

Date: 2026-09-14
Status: accepted

## Context

Wallapop has no public API for buyers. The web app authenticates through Keycloak behind
NextAuth: the browser holds a 30-day HttpOnly cookie `__Secure-next-auth.session-token`, and
`GET es.wallapop.com/api/auth/session` exchanges it for a Keycloak access token that lives
five minutes. Every `api.wallapop.com` call carries that access token as a Bearer.

Alternatives considered:

1. Email + password against `POST /api/v3/access/login` plus refresh tokens. Works for some
   accounts, but suspicious-login MFA sends an approve link by email, Google/Apple/Facebook
   accounts cannot use it at all, and the CLI would hold the account password long enough to
   type it.
2. Driving a headless browser through the real login page. Wallapop's CDN blocks headless
   Chrome, and it would drag a browser runtime into a single-binary CLI.
3. Importing the session cookie the user already has in their browser and minting access
   tokens from it exactly as the web app does. Verified live: the mint works with that one
   cookie alone, the cookie rotates on each mint, and older copies keep working.

## Decision

The stored Session is the web session cookie plus the browser's `device_id`. Access tokens are
minted on demand, cached in memory only, and never written to disk. Cookie import is the
only `auth login` path (see addendum: the `--password` idea from issue #7 did not verify).

## Consequences

- Login needs a cookie export from a logged-in browser (an extension such as Cookie-Editor, or
  devtools), because the cookie is HttpOnly and cannot be copied from `document.cookie`.
- The CLI depends on the web app's NextAuth route staying put. An unexpected shape or status
  there surfaces as exit 7 with an issue link; a rejected or expired Session on the unchanged
  route stays exit 3.
- Sessions expire after 30 days of the cookie's lifetime; the CLI re-persists the rotated cookie
  after each mint so an actively used Profile keeps sliding forward.
- No password ever touches the CLI's storage.

## Addendum 2026-09-15: email/password spike (issue #7) — not offered

Spike question: does `POST /api/v3/access/login` yield the web session cookie, or only a
mobile access/refresh token pair?

Findings (probed live without an account, except the MFA bullet which follows from the
OAuth architecture and the ADR context above):
- `GET https://es.wallapop.com/api/auth/providers` lists exactly one NextAuth provider,
  `keycloak` (OAuth): no credentials provider is advertised. The observed NextAuth session
  path is the Keycloak browser redirect, and the separate password endpoint was not verified
  to bridge into it.
- `POST https://api.wallapop.com/api/v3/access/login` is served by a separate auth service
  (`x-wallapop-service: auth`). Probes without valid credentials return an empty 400, so the
  request schema is unknown, and neither the BFF route table nor unofficial-client auth flows
  show any endpoint exchanging those tokens for the NextAuth cookie.
- The "approve this login" MFA case completes in the browser against Keycloak (architectural
  inference, not directly probed); afterwards only the browser holds the HttpOnly session cookie.
- Full verification with valid credentials was not possible on the spike machine (no test
  account — same blocker as issue #3). Per the ticket's own rule, an unverified bridge
  means the flag stays out.

Verdict: password login is not offered. `auth login` keeps cookie import as its single path,
`--password` stays out of the command tree, and no password ever touches the CLI. If a future
run with a test account demonstrates a password-to-session-cookie exchange, reopen issue #7
with the captured (redacted) request/response shapes.
