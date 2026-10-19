package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Microck/wallapop-cli/internal/cli"
	"github.com/Microck/wallapop-cli/internal/fakewallapop"
)

const renewalPrompt = "keep this login alive in the background? [Y/n]"

// renewalHarness fakes the terminal and the service manager: enabling runs
// the real `auth service enable` code, writing the unit under the temp HOME,
// and records the systemctl calls instead of running them.
func renewalHarness(t *testing.T, tty bool) (*harness, func() []string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the renewal service tests use the systemd layout")
	}
	t.Setenv("CI", "")
	h := newHarness(t)
	var mu sync.Mutex
	var calls []string
	t.Cleanup(cli.UseServiceExec(func(name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}))
	t.Cleanup(cli.UseBundledEngine())
	t.Cleanup(cli.UsePromptTerminal(tty))
	return h, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

func (h *harness) renewalUnits() []string {
	units, _ := filepath.Glob(filepath.Join(h.home, ".config", "systemd", "user", "wallapop-session-*.service"))
	return units
}

func enabled(calls []string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, "systemctl --user enable --now wallapop-session-") {
			return true
		}
	}
	return false
}

func TestLoginPromptEnablesRenewal(t *testing.T) {
	for _, answer := range []string{"\n", "y\n", "YES\n"} {
		h, calls := renewalHarness(t, true)
		r := h.must(answer, "auth", "login", "--cookies", h.cookieFile())
		if !strings.Contains(r.stderr, renewalPrompt) || !strings.Contains(r.stderr, "background renewal on (systemd wallapop-session-") {
			t.Fatalf("answer %q: %s", answer, r.stderr)
		}
		if !enabled(calls()) || len(h.renewalUnits()) != 1 {
			t.Fatalf("answer %q: not enabled: %v %v", answer, calls(), h.renewalUnits())
		}
		var st map[string]any
		decode(t, r.stdout, &st)
		if st["logged_in"] != true {
			t.Fatalf("stdout must stay the login status: %s", r.stdout)
		}
	}
}

func TestLoginPromptNoPrintsHint(t *testing.T) {
	for _, answer := range []string{"n\n", "no\n", ""} {
		h, calls := renewalHarness(t, true)
		r := h.must(answer, "auth", "login", "--cookies", h.cookieFile())
		if !strings.Contains(r.stderr, renewalPrompt) || !strings.Contains(r.stderr, "run `wallapop auth service enable` later to turn it on") {
			t.Fatalf("answer %q: %s", answer, r.stderr)
		}
		if len(calls()) != 0 || len(h.renewalUnits()) != 0 {
			t.Fatalf("answer %q installed the service: %v", answer, calls())
		}
	}
}

func TestLoginDoesNotAskWithoutATerminalOrInCI(t *testing.T) {
	cases := map[string]struct {
		tty   bool
		env   [2]string
		extra []string
	}{
		"no-input":       {tty: true, extra: []string{"--no-input"}},
		"not a terminal": {tty: false},
		"ci":             {tty: true, env: [2]string{"CI", "true"}},
		"no-service":     {tty: true, extra: []string{"--no-service"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h, calls := renewalHarness(t, c.tty)
			if c.env[0] != "" {
				t.Setenv(c.env[0], c.env[1])
			}
			r := h.must("y\n", append([]string{"auth", "login", "--cookies", h.cookieFile()}, c.extra...)...)
			if strings.Contains(r.stderr, renewalPrompt) || len(calls()) != 0 {
				t.Fatalf("asked or installed: %s %v", r.stderr, calls())
			}
		})
	}
}

// An env session is never persisted, so there is nothing to keep alive.
func TestEnvSessionIsNeverOfferedRenewal(t *testing.T) {
	h, calls := renewalHarness(t, true)
	h.must("", "auth", "login", "--cookies", h.cookieFile(), "--no-service")
	t.Setenv("WALLAPOP_SESSION_TOKEN", "env-session-cookie")
	if r := h.must("y\n", "auth", "login", "--cookies", h.cookieFile()); strings.Contains(r.stderr, renewalPrompt) {
		t.Fatalf("asked with an env session: %s", r.stderr)
	}
	h.setSessionExpiry(time.Now().Add(time.Hour))
	var st map[string]any
	decode(t, h.must("", "auth", "status").stdout, &st)
	if st["warning"] != nil || len(calls()) != 0 {
		t.Fatalf("env session: %v %v", st, calls())
	}
}

func TestLoginServiceFlagEnablesWithoutAsking(t *testing.T) {
	h, calls := renewalHarness(t, false)
	r := h.must("", "auth", "login", "--cookies", h.cookieFile(), "--no-input", "--service")
	if strings.Contains(r.stderr, renewalPrompt) || !enabled(calls()) || len(h.renewalUnits()) != 1 {
		t.Fatalf("%s %v", r.stderr, calls())
	}
	if r := h.run("", "auth", "login", "--cookies", h.cookieFile(), "--service", "--no-service"); r.code != 2 || !strings.Contains(r.stderr, "exclusive") {
		t.Fatalf("both flags: %d %s", r.code, r.stderr)
	}
}

func TestLoginSkipsThePromptWhenRenewalIsInstalled(t *testing.T) {
	h, calls := renewalHarness(t, true)
	h.must("\n", "auth", "login", "--cookies", h.cookieFile())
	before := len(calls())
	r := h.must("\n", "auth", "login", "--cookies", h.cookieFile())
	if strings.Contains(r.stderr, renewalPrompt) || len(calls()) != before {
		t.Fatalf("asked again: %s %v", r.stderr, calls())
	}
	// --service always enables again: that retries an enable that failed
	// after writing the unit file.
	r = h.must("", "auth", "login", "--cookies", h.cookieFile(), "--service")
	if !strings.Contains(r.stderr, "background renewal on") || len(calls()) == before {
		t.Fatalf("--service did not enable again: %s %v", r.stderr, calls())
	}
}

// setSessionExpiry rewrites the stored session cookie expiry, recorded as
// issued now, after any renewal the engine has made.
func (h *harness) setSessionExpiry(at time.Time) {
	h.t.Helper()
	h.setSession(time.Now(), at)
}

func (h *harness) setSession(issued, expires time.Time) {
	h.t.Helper()
	raw, err := os.ReadFile(h.credentialsPath())
	if err != nil {
		h.t.Fatal(err)
	}
	for key, at := range map[string]time.Time{"session_expires": expires, "session_issued": issued} {
		re := regexp.MustCompile(`(?m)^` + key + ` = .*$`)
		if !re.Match(raw) {
			h.t.Fatalf("no %s in credentials.toml:\n%s", key, raw)
		}
		raw = re.ReplaceAll(raw, []byte(key+" = "+at.UTC().Format(time.RFC3339Nano)))
	}
	if err := os.WriteFile(h.credentialsPath(), raw, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func renewalCheck(t *testing.T, h *harness) map[string]any {
	t.Helper()
	var rep struct {
		OK     bool             `json:"ok"`
		Checks []map[string]any `json:"checks"`
	}
	decode(t, h.must("", "doctor").stdout, &rep)
	if !rep.OK {
		t.Fatalf("a renewal warning must not fail doctor: %+v", rep)
	}
	for _, c := range rep.Checks {
		if c["name"] == "renewal" {
			return c
		}
	}
	return nil
}

func TestRenewalWarningThresholds(t *testing.T) {
	h, _ := renewalHarness(t, false)
	r := h.must("", "auth", "login", "--cookies", h.cookieFile(), "--no-service")
	// The export's cookie expired already, but the login's own mint
	// re-issued it: no warning right after the login.
	if strings.Contains(r.stdout, `"warning"`) {
		t.Fatalf("warned right after a login that renewed the cookie: %s", r.stdout)
	}
	h.setSessionExpiry(time.Now().Add(72 * time.Hour))
	if r := h.must("", "profile", "list"); strings.Contains(r.stdout, "session_expires") {
		t.Fatalf("profile list shows the imported expiry of a managed session: %s", r.stdout)
	}
	for _, c := range []struct {
		in   time.Duration
		warn bool
	}{{72 * time.Hour, false}, {49 * time.Hour, false}, {47 * time.Hour, true}, {time.Hour, true}, {-time.Hour, true}} {
		h.setSessionExpiry(time.Now().Add(c.in))
		r := h.must("", "auth", "status")
		var st map[string]any
		decode(t, r.stdout, &st)
		if got := st["warning"] != nil; got != c.warn || strings.Contains(r.stderr, "wallapop: warning:") != c.warn {
			t.Fatalf("expiry in %s: warning %v, want %v: %v %s", c.in, got, c.warn, st, r.stderr)
		}
		check := renewalCheck(t, h)
		if (check != nil) != c.warn || (check != nil && (check["ok"] != true || check["warning"] != true)) {
			t.Fatalf("doctor, expiry in %s: %v", c.in, check)
		}
	}
	// An installed service keeps the login alive: no warning.
	h.must("", "auth", "service", "enable")
	h.setSessionExpiry(time.Now().Add(time.Hour))
	var st map[string]any
	decode(t, h.must("", "auth", "status").stdout, &st)
	if st["warning"] != nil {
		t.Fatalf("warned with the service installed: %v", st)
	}
	if c := renewalCheck(t, h); c != nil {
		t.Fatalf("doctor warned with the service installed: %v", c)
	}
}

// A renewal by the engine re-issues the session cookie: the recorded expiry
// moves forward by the recorded lifetime and the warning clears.
func TestEngineRenewalMovesTheSessionExpiry(t *testing.T) {
	h, _ := renewalHarness(t, false)
	h.must("", "auth", "login", "--cookies", h.cookieFile(), "--no-service")
	h.setSession(time.Now().Add(-29*24*time.Hour), time.Now().Add(time.Hour))
	var st map[string]any
	decode(t, h.must("", "auth", "status").stdout, &st)
	if st["warning"] == nil {
		t.Fatalf("no warning an hour before expiry: %v", st)
	}
	h.must("", "auth", "refresh")
	st = nil
	decode(t, h.must("", "auth", "status").stdout, &st)
	if st["warning"] != nil {
		t.Fatalf("renewal did not move the expiry: %v", st)
	}
	raw, _ := os.ReadFile(h.credentialsPath())
	m := regexp.MustCompile(`(?m)^session_expires = (.*)$`).FindSubmatch(raw)
	exp, err := time.Parse(time.RFC3339, string(m[1]))
	if err != nil || time.Until(exp) < 29*24*time.Hour {
		t.Fatalf("expiry not moved: %s", m[1])
	}
}

// A failed enable from the login leaves no unit file behind, so the next
// login asks again instead of reading a half-installed service as on.
func TestFailedEnableFromLoginIsNotInstalled(t *testing.T) {
	h, _ := renewalHarness(t, true)
	t.Cleanup(cli.UseServiceExec(func(name string, args ...string) error {
		if name == "systemctl" && len(args) > 1 && args[1] == "enable" {
			return errors.New("exit status 1")
		}
		return nil
	}))
	r := h.must("\n", "auth", "login", "--cookies", h.cookieFile())
	if !strings.Contains(r.stderr, "background renewal could not be enabled") || len(h.renewalUnits()) != 0 {
		t.Fatalf("%s %v", r.stderr, h.renewalUnits())
	}
	if r := h.run("", "auth", "login", "--cookies", h.cookieFile(), "--service"); r.code == 0 || len(h.renewalUnits()) != 0 {
		t.Fatalf("--service: %d %s %v", r.code, r.stderr, h.renewalUnits())
	}
	if r := h.must("n\n", "auth", "login", "--cookies", h.cookieFile()); !strings.Contains(r.stderr, renewalPrompt) {
		t.Fatalf("not asked again: %s", r.stderr)
	}
}

// A bare session token has no expiry; the login's own mint gives it one.
func TestBareTokenLoginRecordsTheRenewedExpiry(t *testing.T) {
	h, _ := renewalHarness(t, false)
	h.must(fakewallapop.ValidCookie, "auth", "login", "--cookies-stdin", "--no-service")
	raw, _ := os.ReadFile(h.credentialsPath())
	m := regexp.MustCompile(`(?m)^session_expires = (.*)$`).FindSubmatch(raw)
	if m == nil {
		t.Fatalf("no expiry recorded:\n%s", raw)
	}
	if exp, err := time.Parse(time.RFC3339, string(m[1])); err != nil || time.Until(exp) < 29*24*time.Hour {
		t.Fatalf("expiry %s", m[1])
	}
}

// A profile saved before expiry tracking gets an expiry from the first
// renewal a foreground command sees.
func TestRenewalSeedsExpiryForOlderProfiles(t *testing.T) {
	h, _ := renewalHarness(t, false)
	h.must("", "auth", "login", "--cookies", h.cookieFile(), "--no-service")
	raw, _ := os.ReadFile(h.credentialsPath())
	raw = regexp.MustCompile(`(?m)^session_(expires|issued) = .*\n`).ReplaceAll(raw, nil)
	if err := os.WriteFile(h.credentialsPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	h.must("", "auth", "refresh")
	raw, _ = os.ReadFile(h.credentialsPath())
	m := regexp.MustCompile(`(?m)^session_expires = (.*)$`).FindSubmatch(raw)
	if m == nil {
		t.Fatalf("no expiry seeded:\n%s", raw)
	}
	if exp, err := time.Parse(time.RFC3339, string(m[1])); err != nil || time.Until(exp) < 29*24*time.Hour {
		t.Fatalf("expiry %s", m[1])
	}
}
