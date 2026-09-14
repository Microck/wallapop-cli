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
default `auth login` path; email/password is offered behind `--password` for accounts where it
works, and it produces the same stored Session shape by logging in through the web flow.

## Consequences

- Login needs a cookie export from a logged-in browser (an extension such as Cookie-Editor, or
  devtools), because the cookie is HttpOnly and cannot be copied from `document.cookie`.
- The CLI depends on the web app's NextAuth route staying put. A change there breaks every
  authenticated command at once, which is visible and reported as exit 7 with an issue link.
- Sessions expire after 30 days of the cookie's lifetime; the CLI re-persists the rotated cookie
  after each mint so an actively used Profile keeps sliding forward.
- No password ever touches the CLI's storage.
