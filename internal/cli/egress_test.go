package cli_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Microck/wallapop-cli/internal/fakewallapop"
)

// forwardProxy is a plain HTTP forward proxy that counts what it relays.
func forwardProxy(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var relayed atomic.Int64
	direct := &http.Transport{Proxy: nil}
	t.Cleanup(direct.CloseIdleConnections)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			http.Error(w, "not a proxy request", http.StatusBadRequest)
			return
		}
		relayed.Add(1)
		out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out.Header = r.Header.Clone()
		out.Header.Del("Proxy-Authorization")
		resp, err := direct.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	return srv, &relayed
}

// closedAddr is a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestProxyCarriesAllWallapopTraffic(t *testing.T) {
	h := newHarness(t)
	h.daemon.proxied = true
	proxy, relayed := forwardProxy(t)
	hostPort := strings.TrimPrefix(proxy.URL, "http://")
	t.Setenv("WALLAPOP_PROXY", "http://alice:pr0xy-secret@"+hostPort)
	h.fake.AddItem(bike("hashproxy001", 10))

	h.login()
	h.must("", "search", "bici", "--lat", "40", "--lng", "-3")
	// The fake daemon renews on its own, directly; the real one gets the
	// proxy through its adapter (covered in internal/galleton).
	cli := len(h.fake.RequestsUnder("/")) - len(h.fake.RequestsTo("/api/auth/session"))
	if got, want := relayed.Load(), int64(cli); got == 0 || got != want {
		t.Fatalf("proxy relayed %d of %d requests", got, want)
	}

	r := h.run("", "doctor")
	if !strings.Contains(r.stdout, "http://"+hostPort) || strings.Contains(r.stdout+r.stderr, "pr0xy-secret") || strings.Contains(r.stdout, "alice") {
		t.Fatalf("doctor must show the proxy without credentials:\n%s\n%s", r.stdout, r.stderr)
	}
}

func TestUnreachableProxyFailsClosed(t *testing.T) {
	for _, scheme := range []string{"http", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			h := newHarness(t)
			t.Setenv("WALLAPOP_PROXY", scheme+"://bob:hunter22@"+closedAddr(t))
			r := h.run("", "search", "bici", "--lat", "40", "--lng", "-3")
			if r.code != 6 || !strings.Contains(r.stderr, "proxy") || strings.Contains(r.stderr, "hunter22") {
				t.Fatalf("exit %d\n%s", r.code, r.stderr)
			}
			if n := len(h.fake.RequestsUnder("/")); n != 0 {
				t.Fatalf("%d requests went direct", n)
			}
		})
	}
}

func TestInvalidProxyIsAUsageErrorAndConfigCanFixIt(t *testing.T) {
	h := newHarness(t)
	t.Setenv("WALLAPOP_PROXY", "ftp://127.0.0.1:21")
	if r := h.run("", "search", "bici", "--lat", "40", "--lng", "-3"); r.code != 2 || len(h.fake.RequestsUnder("/")) != 0 {
		t.Fatalf("invalid proxy: exit %d, %d requests\n%s", r.code, len(h.fake.RequestsUnder("/")), r.stderr)
	}
	h.must("", "config", "path")
	t.Setenv("WALLAPOP_PROXY", "")
	for _, bad := range []string{"socks5h://user:pw@127.0.0.1:1088", "gopher://x:70", "socks5h://127.0.0.1"} {
		if r := h.run("", "config", "set", "proxy", bad); r.code != 2 {
			t.Fatalf("config set proxy %s: exit %d", bad, r.code)
		}
	}
	h.must("", "config", "set", "proxy", "socks5h://"+closedAddr(t))
	if r := h.run("", "search", "bici", "--lat", "40", "--lng", "-3"); r.code != 6 || len(h.fake.RequestsUnder("/")) != 0 {
		t.Fatalf("config proxy unused: exit %d, %d direct requests\n%s", r.code, len(h.fake.RequestsUnder("/")), r.stderr)
	}
}

// A hand-edited config.toml with proxy credentials is refused like
// `config set` refuses it, and nothing is sent until it is fixed.
func TestConfigProxyWithCredentialsIsRefused(t *testing.T) {
	h := newHarness(t)
	t.Setenv("WALLAPOP_PROXY", "")
	path := filepath.Join(h.home, "config", "wallapop-cli", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("proxy = \"http://bob:hunter22@"+closedAddr(t)+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := h.run("", "search", "bici", "--lat", "40", "--lng", "-3")
	if r.code != 2 || !strings.Contains(r.stderr, "WALLAPOP_PROXY") || strings.Contains(r.stderr, "hunter22") || len(h.fake.RequestsUnder("/")) != 0 {
		t.Fatalf("exit %d, %d requests\n%s", r.code, len(h.fake.RequestsUnder("/")), r.stderr)
	}
	h.must("", "config", "set", "proxy", "socks5h://127.0.0.1:1080")
}

// An external daemon that renews directly must not be used while the CLI is
// told to send everything through a proxy.
func TestProxyRefusesAnUnproxiedExternalDaemon(t *testing.T) {
	h := newHarness(t)
	proxy, _ := forwardProxy(t)
	t.Setenv("WALLAPOP_PROXY", proxy.URL)
	r := h.run("", "auth", "login", "--cookies", h.cookieFile())
	if r.code == 0 || !strings.Contains(r.stderr, "proxy") {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if len(h.daemon.imports) != 0 {
		t.Fatal("imported the session into a daemon that bypasses the proxy")
	}
}

func TestBotBlockPausesSessionRenewals(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.fake.BlockAPI = true
	r := h.run("", "me", "show")
	if r.code != 5 || !strings.Contains(r.stderr, "renewals are paused until") {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if len(h.daemon.pauses) != 1 || h.daemon.pauses[0] != 900 {
		t.Fatalf("pauses %v", h.daemon.pauses)
	}
	// A paused engine starts no renewal, and the CLI says why.
	r = h.run("", "auth", "refresh")
	if r.code != 5 || !strings.Contains(r.stderr, "paused") {
		t.Fatalf("refresh while paused: exit %d\n%s", r.code, r.stderr)
	}
	st := h.must("", "auth", "service", "status")
	if !strings.Contains(st.stdout, `"paused_until"`) {
		t.Fatalf("status does not show the pause:\n%s", st.stdout)
	}
}

// A command that never used the session does not start the engine just to
// pause it, and Wallapop's own JSON 403 is not a bot block.
func TestBlockWithoutSessionDoesNotTouchTheEngine(t *testing.T) {
	h := newHarness(t)
	h.fake.BlockAPI = true
	if r := h.run("", "search", "bici", "--lat", "40", "--lng", "-3"); r.code != 5 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if len(h.daemon.calls) != 0 {
		t.Fatalf("anonymous block reached the engine: %v", h.daemon.calls)
	}
}

func TestLoginReadsBrowserCookieExports(t *testing.T) {
	const device = "d3c456c7-9f01-45ab-a986-634193eb77c0"
	session := "__Secure-next-auth.session-token"
	cases := map[string]string{
		"cookie-editor array": `[{"domain":".wallapop.com","hostOnly":false,"name":"device_id","path":"/","value":"` + device + `"},` +
			`{"domain":"es.wallapop.com","expirationDate":1999999999,"hostOnly":true,"httpOnly":true,"name":"` + session + `","path":"/","sameSite":"lax","secure":true,"value":"` + fakewallapop.ValidCookie + `"}]`,
		"cookie-editor wrapper": `{"url":"https://es.wallapop.com","cookies":[{"name":"` + session + `","value":"` + fakewallapop.ValidCookie + `","path":"/","secure":true}]}`,
		"editthiscookie":        `[{"domain":"es.wallapop.com","expirationDate":1999999999.5,"hostOnly":true,"httpOnly":true,"name":"` + session + `","path":"/","sameSite":"no_restriction","secure":true,"session":false,"storeId":"0","value":"` + fakewallapop.ValidCookie + `","id":1}]`,
		"devtools":              `[{"name":"` + session + `","value":"` + fakewallapop.ValidCookie + `","domain":"es.wallapop.com","path":"/","expires":-1,"httpOnly":true,"secure":true,"sameSite":"Lax"}]`,
		"header":                "Cookie: device_id=" + device + "; " + session + "=" + fakewallapop.ValidCookie,
		"bare value":            fakewallapop.ValidCookie + "\n",
		// A whole-browser export: another NextAuth site's session cookie,
		// listed last, must not be picked up.
		"whole browser": "# Netscape HTTP Cookie File\n" +
			"#HttpOnly_es.wallapop.com\tFALSE\t/\tTRUE\t1999999999\t" + session + "\t" + fakewallapop.ValidCookie + "\n" +
			"#HttpOnly_app.example.com\tFALSE\t/\tTRUE\t1999999999\t" + session + "\tsomeone-elses-session\n" +
			".example.com\tTRUE\t/\tFALSE\t1999999999\tdevice_id\tnot-wallapops\n",
	}
	for name, export := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			path := filepath.Join(h.home, "export")
			if err := os.WriteFile(path, []byte(export), 0o600); err != nil {
				t.Fatal(err)
			}
			h.must("", "auth", "login", "--cookies", path)
			if n := len(h.daemon.imports); n != 1 || h.daemon.imports[0].CookieHeader != session+"="+fakewallapop.ValidCookie {
				t.Fatalf("imports %+v", h.daemon.imports)
			}
			creds := managedCredentials(t, h)
			if got := creds.Profiles["default"].DeviceID; got == "not-wallapops" || (strings.Contains(export, device) && got != device) {
				t.Fatalf("device id %q", got)
			}
		})
	}
}

func TestLoginCookieFormatOverride(t *testing.T) {
	h := newHarness(t)
	r := h.run("", "auth", "login", "--cookies", h.cookieFile(), "--cookies-format", "json")
	if r.code != 2 || len(h.daemon.imports) != 0 {
		t.Fatalf("a Netscape file read as JSON: exit %d\n%s", r.code, r.stderr)
	}
	h.must("", "auth", "login", "--cookies", h.cookieFile(), "--cookies-format", "netscape")
	if r := h.run("", "auth", "login", "--cookies", h.cookieFile(), "--cookies-format", "yaml"); r.code != 2 {
		t.Fatalf("unknown format accepted: exit %d", r.code)
	}
}
