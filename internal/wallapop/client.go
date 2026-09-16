// Package wallapop is the only place that knows Wallapop's unofficial API:
// hosts, paths, headers, response shapes. Everything above it works with the
// normalized types in types.go, so an upstream change is fixed here and only here.
package wallapop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	DefaultAPIBase    = "https://api.wallapop.com"
	DefaultWebBase    = "https://es.wallapop.com"
	DefaultPubNubBase = "https://ps.pndsn.com"

	// The web client's PubNub keys; they are public in the JS bundle.
	pubNubPublishKey   = "pub-c-255dc549-86f5-4abd-8b9e-921d5a02fde7"
	pubNubSubscribeKey = "sub-c-89405e27-d4df-4d87-aca1-d6e9118f0a0d"

	// browserUA is only used after CloudFront rejects the honest user agent.
	browserUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"
)

// TokenSource hands out a valid access token; Session implements it.
type TokenSource interface {
	AccessToken(ctx context.Context) (string, error)
}

type Client struct {
	HTTP       *http.Client
	APIBase    string
	WebBase    string
	PubNubBase string
	UserAgent  string
	// Debug, when non-nil, receives one line per request and response with
	// secrets redacted.
	Debug io.Writer
	// Notice receives one-off human hints (currently only the browser UA fallback).
	Notice func(string)
	Tokens TokenSource

	uaMu          sync.Mutex
	useBrowserUA  bool
	redactStrings []string
}

// New builds a client with base URLs from WALLAPOP_*_BASE_URL (tests point
// these at a fake server) and a 20 s per-request timeout.
func New(version string) *Client {
	c := &Client{
		HTTP:       &http.Client{Timeout: 20 * time.Second},
		APIBase:    envOr("WALLAPOP_API_BASE_URL", DefaultAPIBase),
		WebBase:    envOr("WALLAPOP_WEB_BASE_URL", DefaultWebBase),
		PubNubBase: envOr("WALLAPOP_PUBNUB_BASE_URL", DefaultPubNubBase),
		UserAgent:  "wallapop-cli/" + version + " (+https://github.com/Microck/wallapop-cli)",
	}
	// The web session route sets cookies we must read ourselves, so the client
	// must not follow redirects into HTML login pages silently.
	c.HTTP.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return c
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return strings.TrimRight(v, "/")
	}
	return def
}

// Redact registers a secret that must never appear in debug output or error bodies.
func (c *Client) Redact(secret string) {
	if secret != "" {
		c.redactStrings = append(c.redactStrings, secret)
	}
}

func (c *Client) redact(s string) string {
	for _, r := range c.redactStrings {
		s = strings.ReplaceAll(s, r, "[REDACTED]")
	}
	return s
}

type request struct {
	method string
	base   string
	path   string
	query  url.Values
	body   any
	auth   bool
	// rawHeaders are added verbatim (Cookie for the session mint).
	rawHeaders map[string]string
	// acceptStatus lists non-2xx codes the caller treats as success (409 on archive).
	acceptStatus []int
	// httpClient overrides the shared client for this request. The PubNub
	// long poll needs a timeout no other call wants, and `chat open` publishes
	// on the shared client while a poll is in flight, so it cannot be swapped.
	httpClient *http.Client
}

// do performs one API call, decodes JSON into out (may be nil) and classifies
// failures into *Error so the CLI can map them to exit codes. It retries once
// with a browser user agent when CloudFront rejects the honest one.
func (c *Client) do(ctx context.Context, r request, out any) (*http.Response, error) {
	u := r.base + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	var bodyBytes []byte
	if r.body != nil {
		var err error
		bodyBytes, err = json.Marshal(r.body)
		if err != nil {
			return nil, err
		}
	}
	endpoint := r.method + " " + r.path

	build := func() (*http.Request, error) {
		var rd io.Reader
		if bodyBytes != nil {
			rd = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, r.method, u, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.currentUA())
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-DeviceOS", "0")
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range r.rawHeaders {
			req.Header.Set(k, v)
		}
		if r.auth {
			if c.Tokens == nil {
				return nil, &Error{Kind: KindAuth, Endpoint: endpoint, Msg: "this command needs a logged-in profile. Run `wallapop auth login`"}
			}
			tok, err := c.Tokens.AccessToken(ctx)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		return req, nil
	}

	req, err := build()
	if err != nil {
		return nil, err
	}
	c.debugf("> %s %s", r.method, c.redact(u))
	httpClient := c.HTTP
	if r.httpClient != nil {
		httpClient = r.httpClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, c.netError(endpoint, err)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	if err != nil {
		return nil, c.netError(endpoint, err)
	}
	c.debugf("< %d %s (%d bytes)", resp.StatusCode, endpoint, len(raw))

	// CloudFront answers 403 with an HTML page when it dislikes the request.
	// Once per process, retry with a browser user agent; if that works, keep it.
	if resp.StatusCode == http.StatusForbidden && isCloudFrontBlock(raw) && !c.browserUAActive() {
		c.setBrowserUA()
		if c.Notice != nil {
			c.Notice("wallapop rejected the wallapop-cli user agent; retrying with a browser user agent for this run")
		}
		req, err = build()
		if err != nil {
			return nil, err
		}
		resp, err = httpClient.Do(req)
		if err != nil {
			return nil, c.netError(endpoint, err)
		}
		raw, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return nil, c.netError(endpoint, err)
		}
		c.debugf("< %d %s (%d bytes, browser UA)", resp.StatusCode, endpoint, len(raw))
	}

	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	for _, s := range r.acceptStatus {
		if resp.StatusCode == s {
			ok = true
		}
	}
	if !ok {
		return resp, c.statusError(endpoint, resp.StatusCode, raw)
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp, &Error{Kind: KindAPIChanged, Status: resp.StatusCode, Endpoint: endpoint, Body: "decode: " + err.Error() + "; body: " + c.snippet(raw), Msg: "wallapop returned a response the cli does not understand"}
		}
	}
	return resp, nil
}

func (c *Client) currentUA() string {
	c.uaMu.Lock()
	defer c.uaMu.Unlock()
	if c.useBrowserUA {
		return browserUA
	}
	return c.UserAgent
}

func (c *Client) browserUAActive() bool {
	c.uaMu.Lock()
	defer c.uaMu.Unlock()
	return c.useBrowserUA
}

func (c *Client) setBrowserUA() {
	c.uaMu.Lock()
	c.useBrowserUA = true
	c.uaMu.Unlock()
}

func isCloudFrontBlock(body []byte) bool {
	return bytes.Contains(body, []byte("The request could not be satisfied")) || bytes.Contains(body, []byte("cloudfront"))
}

func (c *Client) debugf(format string, args ...any) {
	if c.Debug != nil {
		fmt.Fprintf(c.Debug, format+"\n", args...)
	}
}

func (c *Client) snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return c.redact(s)
}

func (c *Client) netError(endpoint string, err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &Error{Kind: KindNetwork, Endpoint: endpoint, Retryable: true, Msg: "request to wallapop timed out. Try again or check your network"}
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &Error{Kind: KindNetwork, Endpoint: endpoint, Retryable: true, Msg: "could not reach wallapop: " + c.redact(err.Error())}
}

func (c *Client) statusError(endpoint string, status int, raw []byte) error {
	e := &Error{Status: status, Endpoint: endpoint, Body: c.snippet(raw)}
	// Wallapop's own error envelope, when present, has a human message.
	var env struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &env)
	e.APICode = env.Code
	switch {
	case status == http.StatusUnauthorized:
		e.Kind = KindAuth
		e.Msg = "wallapop rejected the session. Run `wallapop auth login` again"
	case status == http.StatusForbidden || status == http.StatusTooManyRequests:
		e.Kind = KindBlocked
		e.Retryable = true
		e.Msg = "wallapop blocked or rate-limited the request. Wait a while before retrying"
	case status == http.StatusNotFound || status == http.StatusGone:
		e.Kind = KindNotFound
		e.Msg = "wallapop has nothing at " + endpoint
		if env.Message != "" {
			e.Msg = env.Message
		}
	case status >= 500:
		e.Kind = KindGeneric
		e.Retryable = true
		e.Msg = fmt.Sprintf("wallapop answered HTTP %d. Try again later", status)
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity || status == http.StatusConflict:
		e.Kind = KindGeneric
		e.Msg = fmt.Sprintf("wallapop refused the request (HTTP %d)", status)
		if env.Message != "" {
			e.Msg = env.Message
		}
	default:
		e.Kind = KindAPIChanged
		e.Msg = fmt.Sprintf("unexpected HTTP %d from wallapop", status)
	}
	return e
}

// getJSON is the common read path against the API host.
func (c *Client) getJSON(ctx context.Context, path string, q url.Values, auth bool, out any) error {
	_, err := c.do(ctx, request{method: http.MethodGet, base: c.APIBase, path: path, query: q, auth: auth}, out)
	return err
}
