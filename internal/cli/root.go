// Package cli wires cobra commands to the wallapop client, the store and the
// output layer. One file per noun. Command handlers stay thin: parse flags,
// call the client, print.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Microck/wallapop-cli/internal/config"
	"github.com/Microck/wallapop-cli/internal/output"
	"github.com/Microck/wallapop-cli/internal/store"
	"github.com/Microck/wallapop-cli/internal/wallapop"
)

// App is the per-invocation state shared by every command.
type App struct {
	Version string
	Paths   config.Paths
	Cfg     config.Config
	Creds   config.Credentials
	Profile string
	Client  *wallapop.Client
	Session *wallapop.Session
	Printer output.Printer

	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	// Interactive is true only when stdin, stdout and stderr are all TTYs and
	// --no-input is absent. Prompts happen only then.
	Interactive bool

	flagProfile     string
	flagFormat      string
	flagNoColor     bool
	flagErrorFormat string
	flagNoInput     bool
	flagDebug       bool

	store *store.Store
}

// Execute runs the CLI and returns the process exit code.
func Execute(version string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	app := &App{Version: version, Stdin: stdin, Stdout: stdout, Stderr: stderr}
	root := app.rootCmd()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		fmt.Fprintln(stderr, "wallapop: interrupted")
		return 130
	}
	if app.store != nil {
		app.store.Close()
	}
	path := "wallapop"
	if cmd != nil {
		path = cmd.CommandPath()
	}
	ef := output.ErrorFormat(app.flagErrorFormat)
	if v := os.Getenv("WALLAPOP_ERROR_FORMAT"); v != "" && app.flagErrorFormat == "text" {
		ef = output.ErrorFormat(v)
	}
	return output.PrintError(stderr, err, ef, version, path)
}

func (a *App) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "wallapop",
		Short: "Search, track and chat on Wallapop from the terminal",
		Long: `Search, track and chat on Wallapop from the terminal, with your own account.

Output is JSON by default so every command pipes into jq. Use --format pretty
for humans. Errors go to stderr; exit codes are documented in docs/cli-spec.md.


Examples:
  wallapop auth login
  wallapop search "thinkpad x1" --max-price 400 --format pretty
  wallapop watch add search "thinkpad x1" --max-price 400 --name x1 --notify phone
  wallapop chat list --unread --format pretty

Agent usage: wallapop skills get wallapop-usage`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       a.Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return a.setup(cmd)
		},
	}
	root.SetVersionTemplate("wallapop {{.Version}}\n")
	root.CompletionOptions.HiddenDefaultCmd = false
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return output.Usagef("%s. See `%s --help`", err.Error(), cmd.CommandPath())
	})

	pf := root.PersistentFlags()
	pf.StringVar(&a.flagProfile, "profile", "", "profile to act as (env WALLAPOP_PROFILE)")
	pf.StringVar(&a.flagFormat, "format", "json", "output format: json, jsonl, pretty, toon")
	pf.BoolVar(&a.flagNoColor, "no-color", false, "disable colour in pretty output (also NO_COLOR)")
	pf.StringVar(&a.flagErrorFormat, "error-format", "text", "error rendering on stderr: text or json")
	pf.BoolVar(&a.flagNoInput, "no-input", false, "never prompt; fail when input is missing")
	pf.BoolVar(&a.flagDebug, "debug", false, "log every http request to stderr (secrets redacted)")
	_ = root.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"json", "jsonl", "pretty", "toon"}, cobra.ShellCompDirectiveNoFileComp))
	_ = root.RegisterFlagCompletionFunc("error-format", cobra.FixedCompletions([]string{"text", "json"}, cobra.ShellCompDirectiveNoFileComp))

	root.AddCommand(
		a.authCmd(), a.profileCmd(),
		a.searchCmd(), a.categoryCmd(), a.alertCmd(),
		a.itemCmd(), a.userCmd(), a.meCmd(),
		a.chatCmd(),
		a.mcpCmd(),
		a.watchCmd(), a.sinkCmd(),
		a.configCmd(), a.doctorCmd(), a.skillsCmd(),
	)
	return root
}

// setup loads config and credentials and builds the client. It runs for every
// command, so it must stay cheap: no network.
func (a *App) setup(cmd *cobra.Command) error {
	if cmd.Name() == "help" || cmd.Name() == "completion" || (cmd.Parent() != nil && cmd.Parent().Name() == "completion") {
		return nil
	}
	format, err := output.ParseFormat(a.flagFormat)
	if err != nil {
		return err
	}
	if a.flagErrorFormat != "text" && a.flagErrorFormat != "json" {
		return output.Usagef("--error-format must be text or json")
	}
	a.Paths = config.DefaultPaths()
	a.Cfg, err = config.Load(a.Paths.ConfigFile)
	if err != nil {
		return err
	}
	a.Creds, err = config.LoadCredentials(a.Paths.CredentialsFile)
	if err != nil {
		return err
	}
	a.Profile = config.ResolveProfile(a.flagProfile, a.Cfg)

	stdoutFile, _ := a.Stdout.(*os.File)
	stdinFile, _ := a.Stdin.(*os.File)
	stderrFile, _ := a.Stderr.(*os.File)
	a.Interactive = !a.flagNoInput && output.IsTerminal(stdinFile) && output.IsTerminal(stdoutFile) && output.IsTerminal(stderrFile)
	a.Printer = output.Printer{W: a.Stdout, Format: format, Color: output.ColorEnabled(a.flagNoColor, stdoutFile)}

	a.Client = wallapop.New(a.Version)
	if a.flagDebug {
		a.Client.Debug = a.Stderr
	}
	a.Client.Notice = func(s string) { fmt.Fprintln(a.Stderr, "wallapop: "+s) }
	a.attachSession()
	return nil
}

// attachSession wires the active profile's session (or WALLAPOP_SESSION_TOKEN)
// into the client. Commands that need auth call requireSession.
func (a *App) attachSession() {
	cookie, deviceID := "", ""
	if env := os.Getenv("WALLAPOP_SESSION_TOKEN"); env != "" {
		cookie = env
	} else if s, ok := a.Creds.Profiles[a.Profile]; ok {
		cookie, deviceID = s.SessionCookie, s.DeviceID
	}
	if cookie == "" {
		return
	}
	a.Session = wallapop.NewSession(a.Client, cookie, deviceID)
	a.Session.OnRotate = func(newCookie string, expires time.Time) {
		s, ok := a.Creds.Profiles[a.Profile]
		if !ok || os.Getenv("WALLAPOP_SESSION_TOKEN") != "" {
			return
		}
		s.SessionCookie = newCookie
		s.SessionExpires = expires.UTC()
		s.UpdatedAt = time.Now().UTC()
		a.Creds.Profiles[a.Profile] = s
		if err := config.SaveCredentials(a.Paths.CredentialsFile, a.Creds); err != nil {
			fmt.Fprintf(a.Stderr, "wallapop: could not persist rotated session: %v\n", err)
		}
	}
	a.Client.Tokens = a.Session
}

func (a *App) requireSession() error {
	if a.Session == nil {
		return &wallapop.Error{Kind: wallapop.KindAuth, Msg: fmt.Sprintf("profile %q has no session. Run `wallapop auth login`", a.Profile)}
	}
	return nil
}

// userHash returns the logged-in account's hash, fetching it once if the
// stored session predates it.
func (a *App) userHash(ctx context.Context) (string, error) {
	if s, ok := a.Creds.Profiles[a.Profile]; ok && s.UserHash != "" {
		return s.UserHash, nil
	}
	me, err := a.Client.Me(ctx)
	if err != nil {
		return "", err
	}
	return me.Hash, nil
}

func (a *App) Store() (*store.Store, error) {
	if a.store != nil {
		return a.store, nil
	}
	st, err := store.Open(a.Paths.StateDB)
	if err != nil {
		return nil, err
	}
	a.store = st
	return st, nil
}

// confirm asks a yes/no question when interactive. Non-interactive callers
// must pass --yes; otherwise this is a usage error, never a silent proceed.
func (a *App) confirm(yes bool, prompt string) error {
	if yes {
		return nil
	}
	if !a.Interactive {
		return output.Usagef("%s Pass --yes to confirm without a prompt", prompt)
	}
	fmt.Fprintf(a.Stderr, "%s [y/N] ", prompt)
	line, _ := bufio.NewReader(a.Stdin).ReadString('\n')
	if s := strings.ToLower(strings.TrimSpace(line)); s != "y" && s != "yes" {
		return output.Usagef("cancelled")
	}
	return nil
}

// readArgOrStdin returns the text argument, or stdin when the argument is "-".
func (a *App) readArgOrStdin(arg string) (string, error) {
	if arg != "-" {
		return arg, nil
	}
	raw, err := io.ReadAll(a.Stdin)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", output.Usagef("stdin was empty")
	}
	return text, nil
}

// location resolves the search centre: flags, then profile config, then the
// account location saved at login. Missing location is a usage error.
func (a *App) location(lat, lng float64, radius int) (config.Location, error) {
	if lat != 0 || lng != 0 {
		return config.Location{Lat: lat, Lng: lng, RadiusKm: radius}, nil
	}
	if p, ok := a.Cfg.Profiles[a.Profile]; ok && p.Location != nil {
		loc := *p.Location
		if radius > 0 {
			loc.RadiusKm = radius
		}
		return loc, nil
	}
	return config.Location{}, output.Usagef("no search location. Pass --lat and --lng, or run `wallapop auth login` (it saves your account location), or `wallapop config set profiles.%s.location.lat 40.4`", a.Profile)
}

// saveConfig writes config.toml back.
func (a *App) saveConfig() error { return config.Save(a.Paths.ConfigFile, a.Cfg) }

func (a *App) saveCreds() error { return config.SaveCredentials(a.Paths.CredentialsFile, a.Creds) }
