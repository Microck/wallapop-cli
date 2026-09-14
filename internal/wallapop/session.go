package wallapop

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SessionCookieName is the NextAuth cookie that carries the 30-day web session.
// It is HttpOnly, so users export it with a cookie extension, not from the console.
const SessionCookieName = "__Secure-next-auth.session-token"

// Session mints five-minute access tokens from the web session cookie exactly
// as the browser does (GET /api/auth/session). The cookie rotates on every
// mint; OnRotate lets the caller persist the newest copy.
type Session struct {
	Cookie   string
	DeviceID string
	OnRotate func(newCookie string)

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
	for _, ck := range resp.Cookies() {
		if ck.Name == SessionCookieName && ck.Value != "" && ck.Value != s.Cookie {
			s.Cookie = ck.Value
			c.Redact(ck.Value)
			if s.OnRotate != nil {
				s.OnRotate(ck.Value)
			}
		}
	}
	exp := jwtExpiry(out.Token)
	if exp.IsZero() {
		exp = time.Now().Add(4 * time.Minute)
	}
	return out.Token, exp, nil
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

// ParseCookieExport reads a Netscape cookie file (Cookie-Editor, curl, browser
// exporters) and returns the session cookie and device id. It also accepts a
// bare cookie value, a `name=value` pair, or a `Cookie:` header line, so users
// can paste whatever they copied.
func ParseCookieExport(r io.Reader) (sessionCookie, deviceID string, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// Netscape format: domain, flag, path, secure, expiry, name, value.
		// HttpOnly cookies are prefixed "#HttpOnly_" but still tab-separated.
		if fields := strings.Split(line, "\t"); len(fields) >= 7 {
			switch fields[5] {
			case SessionCookieName:
				sessionCookie = fields[6]
			case "device_id":
				deviceID = fields[6]
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		// Header or pasted pairs: "Cookie: a=b; c=d" or "a=b; c=d".
		line = strings.TrimPrefix(line, "Cookie:")
		for _, pair := range strings.Split(line, ";") {
			pair = strings.TrimSpace(pair)
			name, value, found := strings.Cut(pair, "=")
			if !found {
				// A bare value: the session token is a long JWE with dots.
				if strings.Count(pair, ".") >= 2 && len(pair) > 100 {
					sessionCookie = pair
				}
				continue
			}
			switch strings.TrimSpace(name) {
			case SessionCookieName:
				sessionCookie = strings.TrimSpace(value)
			case "device_id":
				deviceID = strings.TrimSpace(value)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", err
	}
	if sessionCookie == "" {
		return "", "", errors.New("no " + SessionCookieName + " cookie found in the input")
	}
	return sessionCookie, deviceID, nil
}

// NewDeviceID makes a fresh device id for imports whose export lacked the
// device_id cookie. Wallapop only needs it to be a stable UUID per device.
func NewDeviceID() string { return newUUID() }
