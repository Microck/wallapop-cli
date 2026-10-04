// Package galleton implements the small subset of Galleton's HTTP/JSON API
// needed by the CLI. Credential renewal and persistence belong to the daemon,
// not this client. See https://github.com/Microck/galleton/blob/main/docs/openapi.json.
package galleton

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
	"path/filepath"
	"strings"
	"time"
)

const DefaultURL = "http://127.0.0.1:8766"

// Configured opts new logins and legacy profiles into managed sessions. A
// profile already migrated to Galleton must never fall back to local renewal.
func Configured() bool {
	return os.Getenv("WALLAPOP_GALLETON_DIR") != "" || os.Getenv("WALLAPOP_GALLETON_URL") != "" || os.Getenv("WALLAPOP_GALLETON_TOKEN_FILE") != ""
}

type Client struct {
	base  string
	token string
	http  *http.Client
}

// Error deliberately excludes the daemon's message/body: a misconfigured
// provider or local service could echo credentials in either of them.
type Error struct {
	Status int
	Code   string
}

func (e *Error) Error() string { return fmt.Sprintf("galleton returned HTTP %d", e.Status) }

func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

type Metadata struct {
	ID              string    `json:"id"`
	Provider        string    `json:"provider"`
	Revision        uint64    `json:"revision"`
	Status          string    `json:"status"`
	LastRefresh     time.Time `json:"last_refresh"`
	AccessExpiresAt time.Time `json:"access_expires_at"`
}

type Credentials struct {
	Provider         string  `json:"provider"`
	CookieOrigin     string  `json:"cookie_origin"`
	CookieHeader     string  `json:"cookie_header"`
	Replace          bool    `json:"replace,omitempty"`
	ExpectedRevision *uint64 `json:"expected_revision,omitempty"`
}

type Headers struct {
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// FromEnv only reads a local token file; it makes no network request. Default
// paths match Galleton's os.UserConfigDir()/galleton state directory.
func FromEnv() (*Client, error) {
	file := os.Getenv("WALLAPOP_GALLETON_TOKEN_FILE")
	if file == "" {
		dir := os.Getenv("WALLAPOP_GALLETON_DIR")
		if dir == "" {
			base, err := os.UserConfigDir()
			if err != nil {
				return nil, errors.New("set WALLAPOP_GALLETON_DIR to the daemon state directory")
			}
			dir = filepath.Join(base, "galleton")
		}
		file = filepath.Join(dir, "api.token")
	}
	token, err := os.ReadFile(file)
	if err != nil {
		return nil, errors.New("cannot read Galleton api.token; initialize the daemon and set WALLAPOP_GALLETON_DIR or WALLAPOP_GALLETON_TOKEN_FILE")
	}
	return New(os.Getenv("WALLAPOP_GALLETON_URL"), string(token))
}

func New(base, token string) (*Client, error) {
	if base == "" {
		base = DefaultURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("WALLAPOP_GALLETON_URL must be a plain loopback HTTP origin")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("WALLAPOP_GALLETON_URL must use a literal loopback IP address")
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("Galleton requires a nonempty single-line local API token")
	}
	return &Client{
		base: strings.TrimRight(base, "/"), token: token,
		http: &http.Client{
			Timeout: 5 * time.Minute,
			// Never send the administrator token through an environment proxy
			// or follow a redirect, even to another loopback service.
			Transport:     &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func sessionPath(id string) (string, error) {
	if len(id) < 1 || len(id) > 64 {
		return "", errors.New("invalid Galleton session ID")
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", errors.New("invalid Galleton session ID")
		}
	}
	return "/v1/sessions/" + id, nil
}

func (c *Client) call(ctx context.Context, method, id, suffix string, in, out any) error {
	path, err := sessionPath(id)
	if err != nil {
		return err
	}
	var body []byte
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path+suffix, bytes.NewReader(body))
	if err != nil {
		return errors.New("could not construct the Galleton request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Galleton is unavailable or the request timed out; start the daemon and check its configuration")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("invalid or interrupted Galleton response; the operation may already have completed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &envelope)
		return &Error{Status: resp.StatusCode, Code: envelope.Error.Code}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return errors.New("Galleton returned an invalid JSON response")
		}
	}
	return nil
}

func (c *Client) Connect(ctx context.Context, id string, in Credentials) (Metadata, error) {
	var out Metadata
	err := c.call(ctx, http.MethodPut, id, "", in, &out)
	return out, err
}

func (c *Client) Status(ctx context.Context, id string) (Metadata, error) {
	var out Metadata
	err := c.call(ctx, http.MethodGet, id, "", nil, &out)
	return out, err
}

func (c *Client) Headers(ctx context.Context, id, target string) (Headers, error) {
	var out Headers
	err := c.call(ctx, http.MethodPost, id, "/headers", map[string]string{"url": target}, &out)
	return out, err
}

func (c *Client) Refresh(ctx context.Context, id string) (Metadata, error) {
	var out Metadata
	err := c.call(ctx, http.MethodPost, id, "/refresh", struct{}{}, &out)
	return out, err
}

func (c *Client) Forget(ctx context.Context, id string) error {
	err := c.call(ctx, http.MethodDelete, id, "", nil, nil)
	if IsStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}
