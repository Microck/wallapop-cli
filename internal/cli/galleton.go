package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Microck/wallapop-cli/internal/galleton"
	"github.com/Microck/wallapop-cli/internal/wallapop"
)

// managedSession is a TokenSource, not a second renewal implementation. Each
// token is obtained from Galleton; its daemon serializes renewal across CLI,
// watch, and MCP processes and durably saves rotated provider credentials.
type managedSession struct {
	mu          sync.Mutex
	id          string
	origin      string
	target      string
	cookie      string // import-only; discarded once Galleton accepts it
	replace     bool   // only an explicit auth login may replace a daemon session
	initialized bool
	client      *galleton.Client
	redact      func(string)
	lastToken   string
	onReady     func() error
}

func (s *managedSession) initialize(ctx context.Context) error {
	if s.initialized {
		return nil
	}
	if s.client == nil {
		c, err := galleton.FromEnv()
		if err != nil {
			return err
		}
		s.client = c
	}
	meta, err := s.client.Status(ctx, s.id)
	exists := err == nil
	if err != nil && !galleton.IsStatus(err, http.StatusNotFound) {
		return err
	}
	if exists && (meta.ID != s.id || meta.Provider != "wallapop") {
		return errors.New("Galleton session does not belong to the wallapop provider")
	}
	if !exists || s.replace {
		if s.cookie == "" {
			return &galleton.Error{Status: http.StatusNotFound, Code: "not_found"}
		}
		in := galleton.Credentials{
			Provider: "wallapop", CookieOrigin: s.origin,
			CookieHeader: wallapop.SessionCookieName + "=" + s.cookie,
		}
		if exists {
			in.Replace, in.ExpectedRevision = true, &meta.Revision
		}
		meta, err = s.client.Connect(ctx, s.id, in)
		// Two processes can attempt the same first migration. Adopt the
		// winner; never overwrite its possibly already-rotated credential.
		var apiErr *galleton.Error
		if !s.replace && errors.As(err, &apiErr) && apiErr.Code == "already_exists" {
			meta, err = s.client.Status(ctx, s.id)
		}
		if err != nil {
			return err
		}
		if meta.ID != s.id || meta.Provider != "wallapop" {
			return errors.New("Galleton returned unexpected session metadata")
		}
	}
	s.cookie = ""
	s.initialized = true
	return nil
}

func (s *managedSession) AccessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accessToken(ctx)
}

func (s *managedSession) accessToken(ctx context.Context) (string, error) {
	if err := s.initialize(ctx); err != nil {
		return "", managedError(err)
	}
	h, err := s.client.Headers(ctx, s.id, s.target)
	if err != nil {
		return "", managedError(err)
	}
	// Do not forward Cookie or arbitrary daemon-supplied headers. Wallapop's
	// API transport needs only the short-lived bearer token for this origin.
	authorization := ""
	for key, value := range h.Headers {
		if strings.EqualFold(key, "Authorization") {
			if authorization != "" {
				return "", managedError(errors.New("Galleton returned duplicate authorization headers"))
			}
			authorization = value
		}
	}
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.ContainsAny(authorization, "\r\n") {
		return "", managedError(errors.New("Galleton did not return a bearer token; check the wallapop adapter"))
	}
	if s.redact != nil && parts[1] != s.lastToken {
		s.redact(parts[1])
		s.lastToken = parts[1]
	}
	if s.onReady != nil {
		if err := s.onReady(); err != nil {
			return "", fmt.Errorf("Galleton holds the session, but local migration could not be saved (retry the command): %w", err)
		}
		s.onReady = nil
	}
	return parts[1], nil
}

func (s *managedSession) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.initialize(ctx); err != nil {
		return managedError(err)
	}
	if _, err := s.client.Refresh(ctx, s.id); err != nil {
		return managedError(err)
	}
	_, err := s.accessToken(ctx)
	return err
}

func managedError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	e := &wallapop.Error{Kind: wallapop.KindNetwork, Msg: err.Error()}
	var apiErr *galleton.Error
	if !errors.As(err, &apiErr) {
		return e
	}
	e.Status = apiErr.Status
	switch {
	case apiErr.Code == "reauth_required" || apiErr.Code == "renewal_uncertain" || apiErr.Status == http.StatusNotFound:
		e.Kind = wallapop.KindAuth
		e.Msg = "Galleton needs this profile to be reconnected. Run `wallapop auth login` with a fresh cookie export"
	case apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden:
		e.Kind = wallapop.KindAuth
		e.Msg = "Galleton rejected local access; check api.token and the provider's allowed origins"
	case apiErr.Status == http.StatusTooManyRequests:
		e.Kind = wallapop.KindBlocked
		e.Msg = "Galleton reports a rate limit; wait before retrying"
	case apiErr.Status == http.StatusConflict:
		e.Kind = wallapop.KindAuth
		e.Msg = "Galleton paused this session or its revision changed; inspect it with `galleton status` before reconnecting"
	case apiErr.Status < 500:
		e.Kind = wallapop.KindGeneric
		e.Msg = "Galleton rejected the request; check the wallapop adapter and daemon configuration"
	default:
		e.Msg = "Galleton could not complete the operation; inspect the daemon before retrying"
	}
	// Never automatically replay a possibly completed import/renewal.
	return e
}

// Namespace by credentials file as well as profile. Arbitrary profile names
// remain supported without colliding with other CLI homes sharing one daemon.
func (a *App) managedProfileID(profile string) string {
	path, err := filepath.Abs(a.Paths.CredentialsFile)
	if err != nil {
		path = filepath.Clean(a.Paths.CredentialsFile)
	}
	sum := sha256.Sum256([]byte(path + "\x00" + profile))
	return fmt.Sprintf("wallapop-%x", sum[:27])
}

func (a *App) bindManagedSession(id, cookie, deviceID string, replace bool, onReady func() error) {
	a.Client.Redact(cookie)
	a.managed = &managedSession{
		id: id, cookie: cookie, origin: a.Client.WebBase, target: a.Client.APIBase,
		replace: replace, onReady: onReady, redact: a.Client.Redact,
	}
	a.Session = wallapop.NewSession(a.Client, "", deviceID)
	a.Session.Delegate = a.managed
	a.Client.Tokens = a.Session
}

func (a *App) attachManagedSession() bool {
	// Preserve the existing ephemeral environment override. Importing it
	// silently would turn a non-persistent secret into persistent daemon state.
	if os.Getenv("WALLAPOP_SESSION_TOKEN") != "" {
		return false
	}
	s, ok := a.Creds.Profiles[a.Profile]
	if !ok || (s.GalletonID == "" && (!galleton.Configured() || s.SessionCookie == "")) {
		return false
	}
	id := s.GalletonID
	if id == "" {
		id = a.managedProfileID(a.Profile)
	}
	var persist func() error
	if s.GalletonID == "" || s.SessionCookie != "" {
		persist = func() error {
			old := a.Creds.Profiles[a.Profile]
			next := old
			next.GalletonID, next.SessionCookie = id, ""
			next.SessionExpires = time.Time{} // daemon metadata does not expose cookie expiry
			next.UpdatedAt = time.Now().UTC()
			a.Creds.Profiles[a.Profile] = next
			if err := a.saveCreds(); err != nil {
				a.Creds.Profiles[a.Profile] = old
				return err
			}
			return nil
		}
	}
	cookie := s.SessionCookie
	if s.GalletonID != "" {
		// A saved reference is authoritative, even if an older process left
		// a stale cookie behind. Only explicit login may recreate it.
		cookie = ""
	}
	a.bindManagedSession(id, cookie, s.DeviceID, false, persist)
	return true
}

func (a *App) managedLogin(cookie, deviceID string) bool {
	s := a.Creds.Profiles[a.Profile]
	if !galleton.Configured() && s.GalletonID == "" {
		return false
	}
	id := s.GalletonID
	if id == "" {
		id = a.managedProfileID(a.Profile)
	}
	a.bindManagedSession(id, cookie, deviceID, true, nil)
	return true
}

func (a *App) managedID() string {
	if a.managed != nil {
		return a.managed.id
	}
	return ""
}

func (a *App) refreshSession(ctx context.Context) error {
	if a.managed != nil {
		return a.managed.Refresh(ctx)
	}
	_, err := a.Session.AccessToken(ctx)
	return err
}

func (a *App) forgetManagedProfile(ctx context.Context, profile string) error {
	s, ok := a.Creds.Profiles[profile]
	if !ok || (s.GalletonID == "" && !galleton.Configured()) {
		return nil
	}
	id := s.GalletonID
	if id == "" {
		// Also clean up a daemon import whose local migration write failed.
		id = a.managedProfileID(profile)
	}
	c, err := galleton.FromEnv()
	if err != nil {
		return managedError(err)
	}
	if err := c.Forget(ctx, id); err != nil {
		return managedError(err)
	}
	return nil
}
