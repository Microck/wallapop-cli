package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Microck/wallapop-cli/internal/config"
	"github.com/Microck/wallapop-cli/internal/galleton"
	"github.com/Microck/wallapop-cli/internal/output"
)

// renewalWarnWindow is how close the session cookie's expiry gets before
// `auth status` and `doctor` suggest the renewal service.
const renewalWarnWindow = 48 * time.Hour

// Seams for tests: whether the login may ask (stdin and stderr are
// terminals) and whether an external daemon (WALLAPOP_GALLETON_*) owns the
// engine's startup.
var (
	promptTerminal = func(a *App) bool {
		in, _ := a.Stdin.(*os.File)
		errf, _ := a.Stderr.(*os.File)
		return output.IsTerminal(in) && output.IsTerminal(errf)
	}
	externalEngine = galleton.Configured
)

const serviceHint = "run `wallapop auth service enable` later to turn it on"

// ciMode is true on CI runners, where nothing should be installed unasked.
func ciMode() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("CI")))
	return v == "true" || v == "1"
}

// renewalInstalled reports whether the renewal service is installed for
// this state directory. An unsupported OS counts as not installed.
func (a *App) renewalInstalled() bool {
	u, _, err := a.authServiceUnit()
	return err == nil && u.installed()
}

// offerRenewalService runs after a successful login. want is "on" for
// --service, "off" for --no-service and "" to ask in a terminal.
func (a *App) offerRenewalService(ctx context.Context, want string) error {
	switch want {
	case "off":
		return nil
	case "on":
		// Always (re)enable: a unit file left by an enable that failed
		// half-way counts as installed, and enabling again retries it.
		if err := a.enableRenewal(ctx); err != nil {
			return fmt.Errorf("logged in, but background renewal could not be enabled: %w", err)
		}
		return nil
	}
	if a.flagNoInput || ciMode() || os.Getenv("WALLAPOP_SESSION_TOKEN") != "" || externalEngine() || !promptTerminal(a) || a.renewalInstalled() {
		return nil
	}
	fmt.Fprint(a.Stderr, "keep this login alive in the background? [Y/n] ")
	line, err := bufio.NewReader(a.Stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if err != nil && answer == "" {
		// End of input (Ctrl-D) or a read error is no answer, so not a yes.
		answer = "n"
	}
	switch answer {
	case "", "y", "yes":
		if err := a.enableRenewal(ctx); err != nil {
			fmt.Fprintf(a.Stderr, "wallapop: background renewal could not be enabled: %v\n%s\n", err, serviceHint)
		}
	default:
		fmt.Fprintln(a.Stderr, serviceHint)
	}
	return nil
}

// enableRenewal is `wallapop auth service enable`, reported on stderr so
// stdout keeps the login's status.
func (a *App) enableRenewal(ctx context.Context) error {
	u, _, err := a.authServiceUnit()
	if err != nil {
		return err
	}
	fresh := !u.installed()
	res, err := a.authServiceApply(ctx, true)
	if err != nil {
		// An enable that fails half-way keeps the unit file for a retry. One
		// this login wrote would read as installed while nothing renews, so
		// undo it the way `auth service disable` does (which unregisters a
		// Windows task before its marker, and keeps the file if that fails):
		// the next login asks again and the warnings stay.
		if fresh && u.installed() {
			if _, derr := a.authServiceApply(ctx, false); derr != nil {
				// The unit stays (a Windows marker is what lets disable find
				// its task), so say it is half-installed, not on.
				return fmt.Errorf("%w; undoing it failed too (%v), so run `wallapop auth service disable` to clean up", err, derr)
			}
		}
		return err
	}
	fmt.Fprintf(a.Stderr, "background renewal on (%v %v)\n", res["platform"], res["unit"])
	return nil
}

// renewalWarning is set when a stored login's session cookie expires within
// renewalWarnWindow and no renewal service keeps it alive between commands.
func (a *App) renewalWarning(s config.Session) string {
	if os.Getenv("WALLAPOP_SESSION_TOKEN") != "" || externalEngine() || s.GalletonID == "" || s.SessionExpires.IsZero() || time.Until(s.SessionExpires) > renewalWarnWindow || a.renewalInstalled() {
		return ""
	}
	verb := "expires"
	if !time.Now().Before(s.SessionExpires) {
		verb = "expired"
	}
	return fmt.Sprintf("the session cookie from the login %s %s and no background renewal is installed; run `wallapop auth service enable`", verb, s.SessionExpires.Local().Format("2006-01-02 15:04 MST"))
}

// sessionLifetime is how long Wallapop's session cookie lives from a mint:
// each one re-issues it with a fresh 30-day Expires.
const sessionLifetime = 30 * 24 * time.Hour

// noteRenewal moves the recorded session cookie expiry forward when the
// engine reports a renewal after the one it was recorded for: each renewal
// re-issues the cookie for at least sessionLifetime.
func (a *App) noteRenewal(ctx context.Context) {
	s, ok := a.Creds.Profiles[a.Profile]
	// A bare token or Cookie header carries no expiry, and a profile saved
	// before expiry tracking has neither: the first renewal seen seeds both.
	if a.managed == nil || !ok || s.GalletonID == "" {
		return
	}
	last, err := a.managed.LastRefresh(ctx)
	if err != nil || !last.After(s.SessionIssued) {
		return
	}
	life := sessionLifetime
	if !s.SessionExpires.IsZero() && !s.SessionIssued.IsZero() {
		life = max(s.SessionExpires.Sub(s.SessionIssued), sessionLifetime)
	}
	s.SessionIssued, s.SessionExpires = last.UTC(), last.Add(life).UTC()
	a.Creds.Profiles[a.Profile] = s
	if err := a.saveCreds(); err != nil {
		fmt.Fprintf(a.Stderr, "wallapop: could not record the session renewal: %v\n", err)
	}
}
