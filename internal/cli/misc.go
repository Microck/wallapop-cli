package cli

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"

	"github.com/Microck/wallapop-cli/internal/config"
	"github.com/Microck/wallapop-cli/internal/output"
	"github.com/Microck/wallapop-cli/internal/sink"
)

// config

func (a *App) configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and write config.toml",
		Long: `Read and write config.toml. Keys are dotted paths.

Examples:
  wallapop config path
  wallapop config set watch.interval 10m
  wallapop config set profiles.default.location.lat 41.39
  wallapop config set sinks.phone.type ntfy
  wallapop config set sinks.phone.url https://ntfy.sh
  wallapop config set sinks.phone.topic wallapop-deals
  wallapop config get sinks.phone`,
	}
	cmd.AddCommand(
		&cobra.Command{Use: "path", Short: "Print the config file path", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			return a.Printer.Print(map[string]string{"config": a.Paths.ConfigFile, "credentials": a.Paths.CredentialsFile, "state": a.Paths.StateDB})
		}},
		&cobra.Command{Use: "list", Short: "Print the whole config", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			return a.Printer.Print(a.Cfg)
		}},
		&cobra.Command{Use: "get KEY", Short: "Print one key", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := a.configDoc()
			if err != nil {
				return err
			}
			v, ok := lookup(doc, strings.Split(args[0], "."))
			if !ok {
				return output.Usagef("config has no key %q", args[0])
			}
			return a.Printer.Print(v)
		}},
		&cobra.Command{Use: "set KEY VALUE", Short: "Set one key (numbers and booleans are typed automatically)", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := a.configDoc()
			if err != nil {
				return err
			}
			assign(doc, strings.Split(args[0], "."), typed(args[1]))
			raw, err := toml.Marshal(doc)
			if err != nil {
				return err
			}
			// Validate strictly before writing so a bad key never lands on disk.
			dec := toml.NewDecoder(strings.NewReader(string(raw)))
			dec.DisallowUnknownFields()
			var cfg config.Config
			if err := dec.Decode(&cfg); err != nil {
				return output.Usagef("%q is not a valid config key: %v", args[0], err)
			}
			a.Cfg = cfg
			if err := a.saveConfig(); err != nil {
				return err
			}
			return a.Printer.Print(map[string]any{args[0]: typed(args[1])})
		}},
	)
	return cmd
}

func (a *App) configDoc() (map[string]any, error) {
	doc := map[string]any{}
	raw, err := os.ReadFile(a.Paths.ConfigFile)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(raw) > 0 {
		if err := toml.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
	}
	return doc, nil
}

func lookup(doc map[string]any, path []string) (any, bool) {
	var cur any = doc
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func assign(doc map[string]any, path []string, v any) {
	cur := doc
	for _, p := range path[:len(path)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[path[len(path)-1]] = v
}

func typed(s string) any {
	if s == "true" || s == "false" {
		return s == "true"
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// doctor

type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type doctorReport struct {
	Checks []doctorCheck `json:"checks"`
	OK     bool          `json:"ok"`
}

func (r doctorReport) Pretty(w io.Writer, color bool) {
	rows := make([][]string, 0, len(r.Checks))
	for _, c := range r.Checks {
		mark := "ok"
		if !c.OK {
			mark = "FAIL"
		}
		rows = append(rows, []string{mark, c.Name, c.Detail})
	}
	output.Table(w, color, nil, rows)
}

func (a *App) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use: "doctor", Short: "Check config, session, api reachability, chat token and schedule", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			rep := doctorReport{OK: true}
			add := func(name string, err error, detail string) {
				c := doctorCheck{Name: name, OK: err == nil, Detail: detail}
				if err != nil {
					c.Detail = err.Error()
					rep.OK = false
				}
				rep.Checks = append(rep.Checks, c)
			}
			add("config", nil, a.Paths.ConfigFile)
			for name, s := range a.Cfg.Sinks {
				add("sink "+name, sink.Validate(name, s), s.Type)
			}
			if _, err := a.Client.Categories(ctx); err != nil {
				add("api reachable", err, "")
			} else {
				add("api reachable", nil, a.Client.APIBase)
			}
			if a.Session == nil {
				add("session", fmt.Errorf("profile %q has no session", a.Profile), "")
			} else if _, err := a.Session.AccessToken(ctx); err != nil {
				add("session", err, "")
			} else {
				add("session", nil, "mints access tokens")
				if _, err := a.Client.ChatToken(ctx); err != nil {
					add("chat token", err, "")
				} else {
					add("chat token", nil, "pubnub token issued")
				}
			}
			if u := a.serviceUnit(); u != nil {
				state, _ := u.state()
				add("schedule", nil, state)
			}
			if err := a.Printer.Print(rep); err != nil {
				return err
			}
			if !rep.OK {
				return &output.UsageError{Msg: "doctor found problems"}
			}
			return nil
		},
	}
}

// skills: embedded agent-facing docs

//go:embed skills/*.md
var skillFiles embed.FS

func (a *App) skillsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "skills", Short: "Embedded usage docs for humans and agents"}
	cmd.AddCommand(
		&cobra.Command{Use: "list", Short: "List available docs", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			names, err := skillNames()
			if err != nil {
				return err
			}
			return a.Printer.Print(names)
		}},
		&cobra.Command{Use: "get NAME", Short: "Print one doc as markdown", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := skillFiles.ReadFile("skills/" + args[0] + ".md")
			if err != nil {
				names, _ := skillNames()
				return output.Usagef("no skill %q. Available: %s", args[0], strings.Join(names, ", "))
			}
			_, err = a.Stdout.Write(raw)
			return err
		}},
	)
	return cmd
}

func skillNames() ([]string, error) {
	entries, err := fs.ReadDir(skillFiles, "skills")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)
	return names, nil
}
