package wallapop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Microck/galleton/cookiefile"
)

// SessionCookieName is the NextAuth cookie that carries the 30-day web session.
// It is HttpOnly, so users export it with a cookie extension, not from the console.
const SessionCookieName = "__Secure-next-auth.session-token"

// Session mints five-minute access tokens from the web session cookie exactly
// as the browser does (GET /api/auth/session). Every mint rotates the cookie
// and re-issues it with a fresh 30-day expiry, so any authenticated call keeps
// the Session alive. OnRotate lets the caller persist the newest copy and its
// expiry; CookieExpires is zero until the first rotation is seen.
type Session struct {
	Cookie        string
	CookieExpires time.Time
	DeviceID      string
	OnRotate      func(newCookie string, expires time.Time)
	// Delegate replaces local minting/caching for daemon-managed sessions.
	Delegate TokenSource

	client *Client
	mu     sync.Mutex
	token  string
	exp    time.Time
}

func NewSession(c *Client, cookie, deviceID string) *Session {
	c.Redact(cookie)
	return &Session{Cookie: cookie, DeviceID: deviceID, client: c}
}

// AccessToken returns a cached token or mints a new one 30 s before expiry.
func (s *Session) AccessToken(ctx context.Context) (string, error) {
	if s.Delegate != nil {
		return s.Delegate.AccessToken(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Until(s.exp) > 30*time.Second {
		return s.token, nil
	}
	tok, exp, err := s.mint(ctx)
	if err != nil {
		return "", err
	}
	s.token, s.exp = tok, exp
	return tok, nil
}

type sessionResponse struct {
	Token   string `json:"token"`
	Expires string `json:"expires"`
}

func (s *Session) mint(ctx context.Context) (string, time.Time, error) {
	c := s.client
	var out sessionResponse
	resp, err := c.do(ctx, request{
		method:     http.MethodGet,
		base:       c.WebBase,
		path:       "/api/auth/session",
		rawHeaders: map[string]string{"Cookie": SessionCookieName + "=" + s.Cookie},
	}, &out)
	if err != nil {
		return "", time.Time{}, err
	}
	// NextAuth answers 200 with an empty object for an invalid session.
	if out.Token == "" {
		return "", time.Time{}, &Error{Kind: KindAuth, Status: resp.StatusCode, Endpoint: "GET /api/auth/session", Msg: "the stored session is no longer valid. Run `wallapop auth login` again"}
	}
	c.Redact(out.Token)
	// Every re-issued cookie counts, even with an unchanged value: the expiry
	// it carries is what slides the Session window.
	for _, ck := range resp.Cookies() {
		if ck.Name == SessionCookieName && ck.Value != "" {
			if ck.Value != s.Cookie {
				c.Redact(ck.Value)
			}
			s.Cookie = ck.Value
			s.CookieExpires = cookieExpiry(ck)
			if s.OnRotate != nil {
				s.OnRotate(ck.Value, s.CookieExpires)
			}
		}
	}
	exp := jwtExpiry(out.Token)
	if exp.IsZero() {
		exp = time.Now().Add(4 * time.Minute)
	}
	return out.Token, exp, nil
}

// cookieExpiry follows RFC 6265: Max-Age wins over Expires when both are
// present. Zero means the server sent a cookie with no lifetime.
func cookieExpiry(ck *http.Cookie) time.Time {
	if ck.MaxAge > 0 {
		return time.Now().Add(time.Duration(ck.MaxAge) * time.Second)
	}
	return ck.Expires
}

// jwtExpiry reads exp from an unverified JWT. Verification is not needed: the
// token is only used to schedule the next mint.
func jwtExpiry(tok string) time.Time {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

// CookieFormats lists the export formats ParseCookieExport accepts.
var CookieFormats = []string{cookiefile.Auto, cookiefile.Netscape, cookiefile.JSON, cookiefile.Header}

// ParseCookieExport reads a browser cookie export with Galleton's cookiefile
// parser and returns the session cookie and device id. format is auto (or
// empty), netscape (cookies.txt), json (Cookie-Editor or EditThisCookie, as an
// array or a {"url", "cookies"} wrapper, and DevTools/Playwright lists) or
// header (`Cookie: a=b; c=d` or `a=b; c=d`). Only cookies for hosts are read,
// so a whole-browser export works and another site's NextAuth session cookie
// is never picked up. A bare session token, as copied from the browser, is
// accepted too.
func ParseCookieExport(r io.Reader, format string, hosts ...string) (sessionCookie, deviceID string, err error) {
	data, err := io.ReadAll(io.LimitReader(r, cookiefile.MaxInput+1))
	if err != nil {
		return "", "", err
	}
	if format == "" {
		format = cookiefile.Auto
	}
	if token := strings.TrimSpace(string(data)); format == cookiefile.Auto && isBareSessionToken(token) {
		return token, "", nil
	}
	cookies, _, err := cookiefile.Parse(data, format, hosts...)
	if err != nil {
		return "", "", err
	}
	for _, c := range cookies {
		switch c.Name {
		case SessionCookieName:
			sessionCookie = c.Value
		case "device_id":
			deviceID = c.Value
		}
	}
	if sessionCookie == "" {
		return "", "", errors.New("no " + SessionCookieName + " cookie found in the input")
	}
	return sessionCookie, deviceID, nil
}

// isBareSessionToken recognizes the session cookie's value on its own: a long
// JWE with dots and none of the separators the export formats use.
func isBareSessionToken(s string) bool {
	return len(s) > 100 && strings.Count(s, ".") >= 2 && !strings.ContainsAny(s, "=;:\t\n\r {}[]\"")
}

// NewDeviceID makes a fresh device id for imports whose export lacked the
// device_id cookie. Wallapop only needs it to be a stable UUID per device.
func NewDeviceID() string { return newUUID() }
