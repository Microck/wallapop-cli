package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Microck/wallapop-cli/internal/config"
	"github.com/Microck/wallapop-cli/internal/fakewallapop"
	"github.com/Microck/wallapop-cli/internal/galleton"
)

// This fake exercises the daemon API contract, not Wallapop's live renewal
// endpoint. In particular, only Galleton is allowed to hold the imported cookie.
type fakeGalleton struct {
	mu           sync.Mutex
	sessions     map[string]galleton.Metadata
	imports      []galleton.Credentials
	calls        []string
	fail         bool
	deleteStatus int
}

func enableGalleton(t *testing.T) *fakeGalleton {
	t.Helper()
	d := &fakeGalleton{sessions: map[string]galleton.Metadata{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.calls = append(d.calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer daemon-secret" {
			t.Error("daemon did not receive its local API token")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if d.fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "unavailable", "message": "daemon-secret " + fakewallapop.ValidCookie}})
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/sessions/"), "/")
		id := parts[0]
		meta, exists := d.sessions[id]
		if r.Method == http.MethodPut {
			var in galleton.Credentials
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if exists && (!in.Replace || in.ExpectedRevision == nil || *in.ExpectedRevision != meta.Revision) {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "revision_conflict"}})
				return
			}
			d.imports = append(d.imports, in)
			meta = galleton.Metadata{ID: id, Provider: in.Provider, Revision: meta.Revision + 1, Status: "ready"}
			d.sessions[id] = meta
			w.WriteHeader(http.StatusCreated)
		} else if !exists {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found"}})
			return
		} else if r.Method == http.MethodDelete {
			if d.deleteStatus != 0 {
				w.WriteHeader(d.deleteStatus)
				return
			}
			delete(d.sessions, id)
		} else if len(parts) == 2 && parts[1] == "headers" {
			var in map[string]string
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in["url"] != os.Getenv("WALLAPOP_API_BASE_URL") {
				t.Error("wrong target passed to Galleton headers")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"headers": map[string]string{
				"Authorization": "Bearer access-token", "Cookie": "must-not-be-forwarded",
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(meta)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.token"), []byte("daemon-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WALLAPOP_GALLETON_DIR", dir)
	t.Setenv("WALLAPOP_GALLETON_URL", srv.URL)
	t.Setenv("WALLAPOP_GALLETON_TOKEN_FILE", "")
	return d
}

func managedCredentials(t *testing.T, h *harness) config.Credentials {
	t.Helper()
	creds, err := config.LoadCredentials(h.credentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	return creds
}

func TestGalletonLoginStoresOnlyReferenceAndReusesIt(t *testing.T) {
	h := newHarness(t)
	d := enableGalleton(t)
	h.login()
	creds := managedCredentials(t, h)
	s := creds.Profiles["default"]
	if s.GalletonID == "" || s.SessionCookie != "" || !s.SessionExpires.IsZero() {
		t.Fatalf("expected managed metadata without a cookie: %+v", s)
	}
	raw, err := os.ReadFile(h.credentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{fakewallapop.ValidCookie, "access-token", "daemon-secret", "session_cookie"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("credential file contains %q", secret)
		}
	}
	info, err := os.Stat(h.credentialsPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("profile metadata must retain 0600 permissions")
	}
	r := h.must("", "auth", "status", "--check", "--debug")
	if !strings.Contains(r.stdout, `"source": "galleton"`) || !strings.Contains(r.stdout, `"session_valid": true`) || strings.Contains(r.stdout, "session_expires") {
		t.Fatalf("unexpected managed status: %s", r.stdout)
	}
	if strings.Contains(r.stderr, "secret") || strings.Contains(r.stderr, "access-token") {
		t.Fatalf("secret in debug output: %s", r.stderr)
	}
	if len(h.fake.RequestsTo("/api/auth/session")) != 0 {
		t.Fatal("managed profile used the old in-process renewal implementation")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.imports) != 1 || d.imports[0].CookieHeader != "__Secure-next-auth.session-token="+fakewallapop.ValidCookie {
		t.Fatalf("unexpected imports: %+v", d.imports)
	}
}

func TestGalletonMigratesLegacyCookieOnce(t *testing.T) {
	h := newHarness(t)
	h.login()
	d := enableGalleton(t)
	h.must("", "auth", "status", "--check")
	if s := managedCredentials(t, h).Profiles["default"]; s.GalletonID == "" || s.SessionCookie != "" {
		t.Fatal("legacy profile not migrated")
	}
	h.must("", "auth", "status", "--check")
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.imports) != 1 || !strings.Contains(d.imports[0].CookieHeader, fakewallapop.RotatedCookie) || d.imports[0].Replace {
		t.Fatal("migration must import the saved cookie once, without replacement")
	}
	if len(h.fake.RequestsTo("/api/auth/session")) != 1 {
		t.Fatal("CLI minted locally after migration")
	}
}

func TestGalletonRefreshReconnectAndProfileIsolation(t *testing.T) {
	h := newHarness(t)
	d := enableGalleton(t)
	h.login()
	first := managedCredentials(t, h).Profiles["default"].GalletonID
	h.must("", "auth", "refresh")
	h.login() // explicit login replaces using the inspected revision
	h.must("", "auth", "login", "--cookies", h.cookieFile(), "--profile", "work / Ω")
	second := managedCredentials(t, h).Profiles["work / Ω"].GalletonID
	if first == second || len(second) > 64 {
		t.Fatal("profiles must have distinct valid daemon IDs")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.imports) != 3 || !d.imports[1].Replace || d.imports[1].ExpectedRevision == nil {
		t.Fatal("explicit reconnect must use optimistic concurrency")
	}
	refreshes := 0
	for _, call := range d.calls {
		if call == "POST /v1/sessions/"+first+"/refresh" {
			refreshes++
		}
	}
	if refreshes != 1 {
		t.Fatalf("explicit refresh calls = %d, want 1", refreshes)
	}
}

func TestGalletonOutageNeverFallsBackOrDropsMetadata(t *testing.T) {
	h := newHarness(t)
	d := enableGalleton(t)
	h.login()
	d.mu.Lock()
	d.fail = true
	d.mu.Unlock()
	for _, args := range [][]string{{"auth", "refresh"}, {"auth", "logout"}, {"profile", "remove", "default", "--yes"}} {
		r := h.run("", args...)
		if r.code == 0 || strings.Contains(r.stderr, "daemon-secret") || strings.Contains(r.stderr, fakewallapop.ValidCookie) {
			t.Fatalf("unsafe outage handling: %+v", r)
		}
	}
	if managedCredentials(t, h).Profiles["default"].GalletonID == "" {
		t.Fatal("failed daemon deletion discarded local recovery metadata")
	}
	if len(h.fake.RequestsTo("/api/auth/session")) != 0 {
		t.Fatal("outage caused fallback to local renewal")
	}
	// Metadata-only commands remain usable even with the daemon down.
	h.must("", "auth", "status")
	h.must("", "profile", "list")
}

func TestGalletonMissingSessionRequiresLogin(t *testing.T) {
	h := newHarness(t)
	d := enableGalleton(t)
	h.login()
	d.mu.Lock()
	d.sessions = map[string]galleton.Metadata{}
	d.mu.Unlock()
	r := h.run("", "auth", "refresh")
	if r.code != 3 || !strings.Contains(r.stderr, "auth login") {
		t.Fatalf("missing managed session: %+v", r)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.imports) != 1 {
		t.Fatal("missing managed session was silently recreated")
	}
}

func TestGalletonLogoutAndRemovalForgetDaemonSession(t *testing.T) {
	for _, args := range [][]string{{"auth", "logout"}, {"profile", "remove", "default", "--yes"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			h := newHarness(t)
			d := enableGalleton(t)
			h.login()
			h.must("", args...)
			if _, ok := managedCredentials(t, h).Profiles["default"]; ok {
				t.Fatal("local profile was not removed")
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if len(d.sessions) != 0 {
				t.Fatal("daemon retained a logged-out session")
			}
		})
	}
}

func TestGalletonDoesNotPersistEnvironmentOverride(t *testing.T) {
	h := newHarness(t)
	d := enableGalleton(t)
	t.Setenv("WALLAPOP_SESSION_TOKEN", fakewallapop.ValidCookie)
	h.must("", "auth", "refresh")
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.calls) != 0 {
		t.Fatal("ephemeral environment override reached persistent daemon storage")
	}
	if _, err := os.Stat(h.credentialsPath()); !os.IsNotExist(err) {
		t.Fatal("environment override was persisted locally")
	}
}
