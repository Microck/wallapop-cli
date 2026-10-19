package cli

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Microck/wallapop-cli/internal/config"
	"github.com/Microck/wallapop-cli/internal/output"
	"github.com/Microck/wallapop-cli/internal/wallapop"
)

func (a *App) authCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Log in, inspect or remove the session for a profile"}
	cmd.AddCommand(a.authLoginCmd(), a.authStatusCmd(), a.authRefreshCmd(), a.authLogoutCmd(), a.authServiceCmd())
	return cmd
}

func (a *App) authLoginCmd() *cobra.Command {
	var cookiesFile, cookiesFormat string
	var cookiesStdin, withService, noService bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Import a browser session for the active profile",
		Long: `Import a browser session for the active profile.

Wallapop keeps the durable login in an HttpOnly cookie, so the CLI needs a
cookie export from a browser where you are logged in:

  1. Install a cookie export extension (Cookie-Editor works) and open es.wallapop.com.
  2. Export the cookies, or copy the value of the cookie named
     __Secure-next-auth.session-token.
  3. Run this command and paste the file path or the value when asked, or pass
     --cookies FILE / --cookies-stdin.

These exports work, and the format is detected (--cookies-format overrides it):
Netscape cookies.txt, Cookie-Editor or EditThisCookie JSON (an array or a
{"url", "cookies"} wrapper), a DevTools or Playwright cookie list, a Cookie:
header or a=b; c=d pairs, or the bare session cookie value. A whole-browser
export is fine: only wallapop.com cookies are read.

The CLI starts its bundled Galleton session engine automatically. The engine
renews tokens and stores rotated cookies in its encrypted private vault.
Nothing else from the export is kept. Google, Apple and Facebook accounts work the
same way, since the cookie is what the browser holds after any login method.

In a terminal, a successful login then asks whether to keep it alive in the
background, which is the same as running "wallapop auth service enable".
--service answers yes and --no-service answers no without asking; with
--no-input, in CI or with WALLAPOP_SESSION_TOKEN it is not asked.

Examples:
  wallapop auth login
  wallapop auth login --cookies ~/Downloads/es.wallapop.com_cookies.txt --profile work
  wallapop auth login --cookies ~/Downloads/cookies.json
  cat cookies.txt | wallapop auth login --cookies-stdin
  wallapop auth login --cookies cookies.txt --no-input --service`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if withService && noService {
				return output.Usagef("--service and --no-service are exclusive; pass one")
			}
			var src io.Reader
			switch {
			case cookiesFile != "":
				f, err := os.Open(cookiesFile)
				if err != nil {
					return err
				}
				defer f.Close()
				src = f
			case cookiesStdin:
				src = a.Stdin
			case a.Interactive:
				fmt.Fprintf(a.Stderr, "Paste the cookie export file path, or the %s value: ", wallapop.SessionCookieName)
				line, _ := bufio.NewReader(a.Stdin).ReadString('\n')
				line = strings.TrimSpace(line)
				if line == "" {
					return output.Usagef("nothing entered")
				}
				if st, err := os.Stat(line); err == nil && !st.IsDir() {
					f, err := os.Open(line)
					if err != nil {
						return err
					}
					defer f.Close()
					src = f
				} else {
					src = strings.NewReader(line)
				}
			default:
				return output.Usagef("no cookies given. Pass --cookies FILE or --cookies-stdin (prompts need a terminal)")
			}
			cookie, deviceID, expires, err := wallapop.ParseCookieExport(src, cookiesFormat, a.cookieHosts()...)
			if err != nil {
				return output.Usagef("%v. Export cookies from a browser logged in at es.wallapop.com", err)
			}
			if deviceID == "" {
				deviceID = wallapop.NewDeviceID()
			}
			// Validate by minting once and reading the profile.
			// The cookie is recorded as issued before the validating mint, so
			// the renewal that mint makes moves its expiry (noteRenewal).
			imported := time.Now().UTC()
			a.managedLogin(cookie, deviceID)
			me, err := a.Client.Me(cmd.Context())
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			a.Creds.Profiles[a.Profile] = config.Session{
				GalletonID: a.managedID(), DeviceID: deviceID,
				UserHash: me.Hash, Name: me.Name, UpdatedAt: now, SessionIssued: imported, SessionExpires: expires.UTC(),
			}
			if err := a.saveCreds(); err != nil {
				return err
			}
			// First profile becomes the default; seed location once from the account.
			changed := false
			if a.Cfg.DefaultProfile == "" {
				a.Cfg.DefaultProfile = a.Profile
				changed = true
			}
			if a.Cfg.Profiles == nil {
				a.Cfg.Profiles = map[string]config.ProfileConfig{}
			}
			if p := a.Cfg.Profiles[a.Profile]; p.Location == nil && (me.Location.Lat != 0 || me.Location.Lng != 0) {
				p.Location = &config.Location{Lat: me.Location.Lat, Lng: me.Location.Lng}
				a.Cfg.Profiles[a.Profile] = p
				changed = true
			}
			if changed {
				if err := a.saveConfig(); err != nil {
					return err
				}
			}
			want := ""
			if withService {
				want = "on"
			} else if noService {
				want = "off"
			}
			a.noteRenewal(cmd.Context())
			// The status comes after the renewal choice, so it reflects it.
			serr := a.offerRenewalService(cmd.Context(), want)
			if err := a.Printer.Print(a.currentStatus()); err != nil {
				return err
			}
			return serr
		},
	}
	cmd.Flags().StringVar(&cookiesFile, "cookies", "", "path to a cookie export (cookies.txt, Cookie-Editor JSON, Cookie header)")
	cmd.Flags().BoolVar(&cookiesStdin, "cookies-stdin", false, "read the cookie export from stdin")
	cmd.Flags().StringVar(&cookiesFormat, "cookies-format", "auto", "cookie export format: "+strings.Join(wallapop.CookieFormats, ", "))
	_ = cmd.RegisterFlagCompletionFunc("cookies-format", cobra.FixedCompletions(wallapop.CookieFormats, cobra.ShellCompDirectiveNoFileComp))
	cmd.Flags().BoolVar(&withService, "service", false, "enable background renewal after the login without asking")
	cmd.Flags().BoolVar(&noService, "no-service", false, "do not enable background renewal and do not ask")
	cmd.MarkFlagsMutuallyExclusive("cookies", "cookies-stdin")
	return cmd
}

// cookieHosts are the hosts whose cookies an import reads: Wallapop's web and
// API hosts, and the configured ones when tests or a mirror override them.
func (a *App) cookieHosts() []string {
	var hosts []string
	for _, base := range []string{wallapop.DefaultWebBase, wallapop.DefaultAPIBase, a.Client.WebBase, a.Client.APIBase} {
		if u, err := url.Parse(base); err == nil && u.Hostname() != "" && !slices.Contains(hosts, u.Hostname()) {
			hosts = append(hosts, u.Hostname())
		}
	}
	return hosts
}

type authStatus struct {
	Profile        string           `json:"profile"`
	LoggedIn       bool             `json:"logged_in"`
	Account        string           `json:"account,omitempty"`
	UserHash       string           `json:"user_hash,omitempty"`
	Source         string           `json:"source,omitempty"`
	GalletonID     string           `json:"galleton_id,omitempty"`
	UpdatedAt      *time.Time       `json:"updated_at,omitempty"`
	SessionExpires *time.Time       `json:"session_expires,omitempty"`
	Location       *config.Location `json:"location,omitempty"`
	Valid          *bool            `json:"session_valid,omitempty"`
	// Warning is set when the session cookie expires within 48 h and no
	// renewal service runs.
	Warning string `json:"warning,omitempty"`
}

func (s authStatus) Pretty(w io.Writer, color bool) {
	rows := [][]string{{"profile", s.Profile}}
	if !s.LoggedIn {
		rows = append(rows, []string{"session", "none (run `wallapop auth login`)"})
	} else {
		rows = append(rows, []string{"account", s.Account + " " + output.Dim(s.UserHash, color)}, []string{"source", s.Source})
		if s.GalletonID != "" {
			rows = append(rows, []string{"galleton", s.GalletonID})
		}
		if s.UpdatedAt != nil {
			rows = append(rows, []string{"updated", s.UpdatedAt.Local().Format(time.RFC3339)})
		}
		if s.SessionExpires != nil {
			rows = append(rows, []string{"expires", s.SessionExpires.Local().Format(time.RFC3339)})
		}
		if s.Valid != nil {
			rows = append(rows, []string{"valid", fmt.Sprint(*s.Valid)})
		}
		if s.Warning != "" {
			rows = append(rows, []string{"warning", s.Warning})
		}
	}
	if s.Location != nil {
		rows = append(rows, []string{"location", fmt.Sprintf("%.4f, %.4f", s.Location.Lat, s.Location.Lng)})
	}
	output.Table(w, color, nil, rows)
}

func (a *App) authStatusCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the active profile's session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve migration before reading the locally stored session reference.
			var valid *bool
			if check && a.Session != nil {
				_, err := a.Session.AccessToken(cmd.Context())
				valid = new(bool)
				*valid = err == nil
			}
			if valid != nil && *valid {
				a.noteRenewal(cmd.Context())
			}
			st := a.currentStatus()
			st.Valid = valid
			if st.Warning != "" {
				fmt.Fprintln(a.Stderr, "wallapop: warning: "+st.Warning)
			}
			return a.Printer.Print(st)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "mint a token to verify the session still works")
	return cmd
}

// currentStatus describes the active profile's session as stored right now.
// Read it after token acquisition, which may migrate a legacy cookie.
func (a *App) currentStatus() authStatus {
	st := authStatus{Profile: a.Profile}
	if p, ok := a.Cfg.Profiles[a.Profile]; ok {
		st.Location = p.Location
	}
	if os.Getenv("WALLAPOP_SESSION_TOKEN") != "" {
		st.LoggedIn, st.Source = true, "env WALLAPOP_SESSION_TOKEN"
		// Env sessions are never persisted, so the only expiry known is the
		// one seen on this process's rotation.
		st.SessionExpires = timePtr(a.Session.CookieExpires)
	} else if s, ok := a.Creds.Profiles[a.Profile]; ok {
		st.LoggedIn, st.Source, st.Account, st.UserHash = true, "credentials", s.Name, s.UserHash
		st.UpdatedAt, st.SessionExpires = timePtr(s.UpdatedAt), timePtr(s.SessionExpires)
		if s.GalletonID != "" {
			st.Source, st.GalletonID = "galleton", s.GalletonID
			// Galleton does not expose the cookie's current expiry. The stored
			// one is the imported cookie's, as of the login: only the
			// renewal warning uses it.
			st.SessionExpires = nil
			st.Warning = a.renewalWarning(s)
		}
	}
	return st
}

// timePtr turns a zero time into nil so JSON omits unknown timestamps.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (a *App) authRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Renew the session through the managed engine",
		Long: `Renew the session through the managed engine.

Galleton owns credential rotation and persistence. A refresh can still fail when
Wallapop revokes the session or requires a new browser login. Enable unattended
renewal explicitly with "wallapop auth service enable".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			// Explicitly refresh in the daemon, even when it has a cached token.
			if err := a.refreshSession(cmd.Context()); err != nil {
				return err
			}
			a.noteRenewal(cmd.Context())
			return a.Printer.Print(a.currentStatus())
		},
	}
}

func (a *App) authLogoutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Forget the active profile's session (keeps watches and config)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, ok := a.Creds.Profiles[a.Profile]; !ok {
				return a.Printer.Print(authStatus{Profile: a.Profile})
			}
			if err := a.forgetManagedProfile(cmd.Context(), a.Profile); err != nil {
				return err
			}
			delete(a.Creds.Profiles, a.Profile)
			if err := a.saveCreds(); err != nil {
				return err
			}
			return a.Printer.Print(authStatus{Profile: a.Profile})
		},
	}
	cmd.Flags().Bool("yes", false, "accepted for symmetry; logout never prompts")
	return cmd
}

// profile

type profileRow struct {
	Name           string     `json:"name"`
	Account        string     `json:"account,omitempty"`
	UserHash       string     `json:"user_hash,omitempty"`
	Default        bool       `json:"default"`
	UpdatedAt      time.Time  `json:"updated_at,omitempty"`
	SessionExpires *time.Time `json:"session_expires,omitempty"`
}

type profileList []profileRow

func (l profileList) Pretty(w io.Writer, color bool) {
	rows := make([][]string, 0, len(l))
	for _, p := range l {
		mark := ""
		if p.Default {
			mark = "*"
		}
		expires := ""
		if p.SessionExpires != nil {
			expires = p.SessionExpires.Local().Format(time.RFC3339)
		}
		rows = append(rows, []string{mark, p.Name, p.Account, output.Dim(p.UserHash, color), expires})
	}
	output.Table(w, color, []string{"", "PROFILE", "ACCOUNT", "HASH", "EXPIRES"}, rows)
}

func (a *App) profileCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "profile", Short: "Manage named accounts"}
	cmd.AddCommand(
		&cobra.Command{
			Use: "list", Short: "List profiles with a session", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				def := config.ResolveProfile("", a.Cfg)
				list := profileList{}
				for name, s := range a.Creds.Profiles {
					row := profileRow{Name: name, Account: s.Name, UserHash: s.UserHash, Default: name == def, UpdatedAt: s.UpdatedAt, SessionExpires: timePtr(s.SessionExpires)}
					if s.GalletonID != "" {
						// As in auth status: the stored expiry is the imported
						// cookie's, kept for the renewal warning only.
						row.SessionExpires = nil
					}
					list = append(list, row)
				}
				sortProfiles(list)
				return a.Printer.Print(list)
			},
		},
		&cobra.Command{
			Use: "use NAME", Short: "Make a profile the default", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if _, ok := a.Creds.Profiles[args[0]]; !ok {
					return output.Usagef("profile %q has no session. Run `wallapop auth login --profile %s` first", args[0], args[0])
				}
				a.Cfg.DefaultProfile = args[0]
				if err := a.saveConfig(); err != nil {
					return err
				}
				return a.Printer.Print(map[string]string{"default_profile": args[0]})
			},
		},
		a.profileRemoveCmd(),
	)
	return cmd
}

func (a *App) profileRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: "remove NAME", Short: "Delete a profile's session, config and watches", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := a.confirm(yes, fmt.Sprintf("Remove profile %q, its session and its watches?", name)); err != nil {
				return err
			}
			if err := a.forgetManagedProfile(cmd.Context(), name); err != nil {
				return err
			}
			delete(a.Creds.Profiles, name)
			if err := a.saveCreds(); err != nil {
				return err
			}
			delete(a.Cfg.Profiles, name)
			if a.Cfg.DefaultProfile == name {
				a.Cfg.DefaultProfile = ""
			}
			if err := a.saveConfig(); err != nil {
				return err
			}
			st, err := a.Store()
			if err != nil {
				return err
			}
			if err := st.RemoveProfileWatches(cmd.Context(), name); err != nil {
				return err
			}
			return a.Printer.Print(map[string]string{"removed": name})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "do not prompt")
	return cmd
}

func sortProfiles(l profileList) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && l[j].Name < l[j-1].Name; j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}
