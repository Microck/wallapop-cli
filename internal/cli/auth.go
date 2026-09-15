package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Microck/wallapop-cli/internal/config"
	"github.com/Microck/wallapop-cli/internal/output"
	"github.com/Microck/wallapop-cli/internal/wallapop"
)

func (a *App) authCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Log in, inspect or remove the session for a profile"}
	cmd.AddCommand(a.authLoginCmd(), a.authStatusCmd(), a.authRefreshCmd(), a.authLogoutCmd())
	return cmd
}

func (a *App) authLoginCmd() *cobra.Command {
	var cookiesFile string
	var cookiesStdin bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Import a browser session for the active profile",
		Long: `Import a browser session for the active profile.

Wallapop keeps the durable login in an HttpOnly cookie, so the CLI needs a
cookie export from a browser where you are logged in:

  1. Install a cookie export extension (Cookie-Editor works) and open es.wallapop.com.
  2. Export cookies as Netscape/text and save the file, or copy the value of
     the cookie named __Secure-next-auth.session-token.
  3. Run this command and paste the file path or the value when asked, or pass
     --cookies FILE / --cookies-stdin.

The CLI mints short-lived access tokens from that cookie exactly as the web does.
Nothing else from the export is kept. Google, Apple and Facebook accounts work the
same way, since the cookie is what the browser holds after any login method.

Examples:
  wallapop auth login
  wallapop auth login --cookies ~/Downloads/es.wallapop.com_cookies.txt --profile work
  cat cookies.txt | wallapop auth login --cookies-stdin`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
			cookie, deviceID, err := wallapop.ParseCookieExport(src)
			if err != nil {
				return output.Usagef("%v. Export cookies from a browser logged in at es.wallapop.com", err)
			}
			if deviceID == "" {
				deviceID = wallapop.NewDeviceID()
			}
			// Validate by minting once and reading the profile.
			a.Session = wallapop.NewSession(a.Client, cookie, deviceID)
			a.Client.Tokens = a.Session
			me, err := a.Client.Me(cmd.Context())
			if err != nil {
				return err
			}
			a.Creds.Profiles[a.Profile] = config.Session{
				SessionCookie: a.Session.Cookie, SessionExpires: a.Session.CookieExpires.UTC(), DeviceID: deviceID, UserHash: me.Hash, Name: me.Name, UpdatedAt: time.Now().UTC(),
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
			return a.Printer.Print(authStatus{Profile: a.Profile, LoggedIn: true, Account: me.Name, UserHash: me.Hash, Source: "credentials", Location: a.Cfg.Profiles[a.Profile].Location})
		},
	}
	cmd.Flags().StringVar(&cookiesFile, "cookies", "", "path to a Netscape cookie export")
	cmd.Flags().BoolVar(&cookiesStdin, "cookies-stdin", false, "read the cookie export from stdin")
	cmd.MarkFlagsMutuallyExclusive("cookies", "cookies-stdin")
	return cmd
}

type authStatus struct {
	Profile        string           `json:"profile"`
	LoggedIn       bool             `json:"logged_in"`
	Account        string           `json:"account,omitempty"`
	UserHash       string           `json:"user_hash,omitempty"`
	Source         string           `json:"source,omitempty"`
	UpdatedAt      *time.Time       `json:"updated_at,omitempty"`
	SessionExpires *time.Time       `json:"session_expires,omitempty"`
	Location       *config.Location `json:"location,omitempty"`
	Valid          *bool            `json:"session_valid,omitempty"`
}

func (s authStatus) Pretty(w io.Writer, color bool) {
	rows := [][]string{{"profile", s.Profile}}
	if !s.LoggedIn {
		rows = append(rows, []string{"session", "none (run `wallapop auth login`)"})
	} else {
		rows = append(rows, []string{"account", s.Account + " " + output.Dim(s.UserHash, color)}, []string{"source", s.Source})
		if s.UpdatedAt != nil {
			rows = append(rows, []string{"updated", s.UpdatedAt.Local().Format(time.RFC3339)})
		}
		if s.SessionExpires != nil {
			rows = append(rows, []string{"expires", s.SessionExpires.Local().Format(time.RFC3339)})
		}
		if s.Valid != nil {
			rows = append(rows, []string{"valid", fmt.Sprint(*s.Valid)})
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
			// Mint before reading the stored session: the mint rotates the
			// cookie and persists a new expiry, which is what should print.
			var valid *bool
			if check && a.Session != nil {
				_, err := a.Session.AccessToken(cmd.Context())
				valid = new(bool)
				*valid = err == nil
			}
			st := a.currentStatus()
			st.Valid = valid
			return a.Printer.Print(st)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "mint a token to verify the session still works")
	return cmd
}

// currentStatus describes the active profile's session as stored right now.
// Read it after any mint, since a mint rotates the cookie and its expiry.
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
		Short: "Mint once to extend the session and show its new expiry",
		Long: `Mint once to extend the session and show its new expiry.

Wallapop re-issues the session cookie with a fresh 30-day expiry on every mint,
so any authenticated command keeps the session alive. ` + "`watch check`" + ` does this
on its own; this command is for people who schedule with cron instead of
` + "`watch service`" + `, or who want to see how long the session has left.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			// A fresh process has no cached token, so this always mints once.
			if _, err := a.Session.AccessToken(cmd.Context()); err != nil {
				return err
			}
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
					list = append(list, profileRow{Name: name, Account: s.Name, UserHash: s.UserHash, Default: name == def, UpdatedAt: s.UpdatedAt, SessionExpires: timePtr(s.SessionExpires)})
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
