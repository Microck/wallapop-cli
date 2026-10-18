// Package galleton integrates the official Go SDK with wallapop-cli's lifecycle.
// Provider renewal and storage are exclusively implemented by the pinned daemon.
package galleton

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	sdk "github.com/Microck/galleton/client"
)

const DefaultURL = "http://127.0.0.1:8766"

// Configured selects an externally managed daemon. The default needs no settings.
func Configured() bool {
	return os.Getenv("WALLAPOP_GALLETON_DIR") != "" || os.Getenv("WALLAPOP_GALLETON_URL") != "" || os.Getenv("WALLAPOP_GALLETON_TOKEN_FILE") != ""
}

type Client struct {
	sdk       *sdk.Client
	closeOnce sync.Once
	release   func()
}

// Error retains classification but never includes an upstream message or body.
// RetryAt is the daemon's own scheduling hint (pause end, backoff deadline).
type Error struct {
	Status  int
	Code    string
	RetryAt time.Time
}

func (e *Error) Error() string { return fmt.Sprintf("galleton returned HTTP %d", e.Status) }
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

type Metadata = sdk.Metadata
type Credentials = sdk.Credentials
type Headers = sdk.Headers
type Provider = sdk.Provider

// ProviderName is the bundled adapter's provider.
const ProviderName = "wallapop"

// FromEnv is the advanced external-daemon path. It never installs or starts it.
func FromEnv() (*Client, error) {
	file := os.Getenv("WALLAPOP_GALLETON_TOKEN_FILE")
	if file == "" {
		dir := os.Getenv("WALLAPOP_GALLETON_DIR")
		if dir == "" {
			base, err := os.UserConfigDir()
			if err != nil {
				return nil, errors.New("cannot locate the external Galleton token")
			}
			dir = filepath.Join(base, "galleton")
		}
		file = filepath.Join(dir, "api.token")
	}
	token, err := readRegular(file, 4096)
	if err != nil {
		return nil, errors.New("cannot read the external Galleton api.token")
	}
	return New(os.Getenv("WALLAPOP_GALLETON_URL"), string(token))
}

func New(base, token string) (*Client, error) {
	if base == "" {
		base = DefaultURL
	}
	u, err := url.Parse(base)
	if err != nil || u.ForceQuery {
		return nil, errors.New("galleton URL must be a plain loopback HTTP origin")
	}
	client, err := sdk.New(base, token)
	if err != nil {
		return nil, errors.New("galleton requires a literal loopback HTTP origin and a nonempty single-line API token")
	}
	return &Client{sdk: client}, nil
}

func safeError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var e *sdk.Error
	if errors.As(err, &e) {
		out := &Error{Status: e.Status, Code: e.Code}
		if t, perr := time.Parse(time.RFC3339, e.RetryAt); perr == nil {
			out.RetryAt = t
		}
		return out
	}
	return errors.New("galleton could not complete the operation; it may already have completed. Run `wallapop auth service status` before retrying")
}
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		if c.release != nil {
			c.release()
		}
	})
}
func (c *Client) Connect(ctx context.Context, id string, in Credentials) (Metadata, error) {
	out, err := c.sdk.Connect(ctx, id, in)
	return out, safeError(ctx, err)
}
func (c *Client) Status(ctx context.Context, id string) (Metadata, error) {
	out, err := c.sdk.Status(ctx, id)
	return out, safeError(ctx, err)
}
func (c *Client) Headers(ctx context.Context, id, target string) (Headers, error) {
	out, err := c.sdk.Headers(ctx, id, target)
	return out, safeError(ctx, err)
}
func (c *Client) Refresh(ctx context.Context, id string) (Metadata, error) {
	out, err := c.sdk.Refresh(ctx, id)
	return out, safeError(ctx, err)
}
func (c *Client) Forget(ctx context.Context, id string) error {
	err := safeError(ctx, c.sdk.Forget(ctx, id))
	if IsStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}
func (c *Client) Shutdown(ctx context.Context) error { return safeError(ctx, c.sdk.Shutdown(ctx)) }

// Ping is a read-only liveness check; it does not acquire or refresh credentials.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.sdk.List(ctx)
	return safeError(ctx, err)
}

// provider describes the wallapop provider as the daemon is configured: never
// the proxy URL, only whether there is one, and any pause.
func (c *Client) provider(ctx context.Context) (Provider, error) {
	list, err := c.sdk.Providers(ctx)
	if err != nil {
		return Provider{}, safeError(ctx, err)
	}
	for _, p := range list {
		if p.Name == ProviderName {
			return p, nil
		}
	}
	return Provider{}, &Error{Status: http.StatusNotFound, Code: "not_found"}
}

// Pause stops the daemon's renewals for the provider for seconds (1-86400). It
// only extends an existing pause, and the daemon keeps it across restarts.
func (c *Client) Pause(ctx context.Context, seconds int) (time.Time, error) {
	p, err := c.sdk.Pause(ctx, ProviderName, seconds)
	if err != nil {
		return time.Time{}, safeError(ctx, err)
	}
	if p.PausedUntil == nil {
		return time.Time{}, nil
	}
	return *p.PausedUntil, nil
}
