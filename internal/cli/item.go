package cli

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Microck/wallapop-cli/internal/output"
	"github.com/Microck/wallapop-cli/internal/wallapop"
)

type itemView wallapop.Item

func (v itemView) Pretty(w io.Writer, color bool) {
	it := wallapop.Item(v)
	state := "available"
	switch {
	case it.Sold:
		state = "sold"
	case it.Expired:
		state = "expired"
	case it.Reserved:
		state = "reserved"
	}
	rows := [][]string{
		{"hash", it.Hash},
		{"title", it.Title},
		{"price", fmt.Sprintf("%.2f %s", it.Price, it.Currency)},
		{"state", state},
	}
	if it.Condition != "" {
		rows = append(rows, []string{"condition", it.Condition})
	}
	if it.Category != "" {
		rows = append(rows, []string{"category", it.Category})
	}
	rows = append(rows, []string{"shipping", fmt.Sprint(it.Shippable)})
	if it.Location.City != "" {
		rows = append(rows, []string{"location", strings.TrimSpace(it.Location.City + " " + it.Location.PostalCode)})
	}
	if it.SellerHash != "" {
		rows = append(rows, []string{"seller", it.SellerHash})
	}
	if it.Views > 0 || it.Favorites > 0 {
		rows = append(rows, []string{"views/favs", fmt.Sprintf("%d / %d", it.Views, it.Favorites)})
	}
	if !it.ModifiedAt.IsZero() {
		rows = append(rows, []string{"modified", it.ModifiedAt.Local().Format(time.RFC3339)})
	}
	rows = append(rows, []string{"url", it.URL})
	output.Table(w, color, nil, rows)
	if it.Description != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, it.Description)
	}
}

func (a *App) itemCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "item",
		Short: "Inspect and act on a listing",
		Long: `Inspect and act on a listing. ITEM is the 12-character hash or the full
es.wallapop.com/item/... URL.`,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "show ITEM", Short: "Full detail including reserved/sold flags", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				it, err := a.Client.Item(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				return a.Printer.Print(itemView(it))
			},
		},
		&cobra.Command{
			Use: "open ITEM", Short: "Open the listing in the browser", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				url := args[0]
				if wallapop.IsHash(url) {
					it, err := a.Client.Item(cmd.Context(), url)
					if err != nil {
						return err
					}
					url = it.URL
				}
				return openBrowser(url)
			},
		},
		a.favoriteCmd("favorite", "Add the listing to your favorites", true),
		a.favoriteCmd("unfavorite", "Remove the listing from your favorites", false),
		a.reserveCmd(),
		a.destructiveItemCmd("sold", "sold", "Mark one of your own listings as sold", "Mark %s as sold? This cannot be undone on Wallapop.",
			// Closures, not method values: a.Client is built in setup, after this runs.
			func(ctx context.Context, hash string) error { return a.Client.MarkSold(ctx, hash) }),
		a.destructiveItemCmd("delete", "deleted", "Delete one of your own listings", "Delete %s from Wallapop? This cannot be undone.",
			func(ctx context.Context, hash string) error { return a.Client.DeleteItem(ctx, hash) }),
	)
	return cmd
}

type actionResult struct {
	Action string `json:"action"`
	Item   string `json:"item"`
	OK     bool   `json:"ok"`
}

func (r actionResult) Pretty(w io.Writer, color bool) { fmt.Fprintf(w, "%s %s\n", r.Action, r.Item) }

func (a *App) favoriteCmd(name, short string, on bool) *cobra.Command {
	return &cobra.Command{
		Use: name + " ITEM", Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			hash, err := a.Client.ResolveItemHash(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := a.Client.SetFavorite(cmd.Context(), hash, on); err != nil {
				return err
			}
			return a.Printer.Print(actionResult{Action: name + "d", Item: hash, OK: true})
		},
	}
}

func (a *App) reserveCmd() *cobra.Command {
	var off bool
	cmd := &cobra.Command{
		Use: "reserve ITEM", Short: "Mark one of your own listings as reserved (or --off)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			hash, err := a.ownedItemHash(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := a.Client.SetReserved(cmd.Context(), hash, !off); err != nil {
				return err
			}
			action := "reserved"
			if off {
				action = "unreserved"
			}
			return a.Printer.Print(actionResult{Action: action, Item: hash, OK: true})
		},
	}
	cmd.Flags().BoolVar(&off, "off", false, "clear the reserved flag")
	return cmd
}

// ownedItemHash resolves ITEM and refuses it unless the active account listed
// it. The check lives here because only the CLI knows the Profile's user hash;
// Wallapop's own answer to a foreign write carries no explanation (see
// wallapop.ownWriteError).
func (a *App) ownedItemHash(ctx context.Context, ref string) (string, error) {
	it, err := a.Client.Item(ctx, ref)
	if err != nil {
		return "", err
	}
	me, err := a.userHash(ctx)
	if err != nil {
		return "", err
	}
	if it.SellerHash != me {
		return "", output.Usagef("%s belongs to another seller (%s). Only your own items can be reserved, sold or deleted", it.Hash, it.SellerHash)
	}
	return it.Hash, nil
}

// destructiveItemCmd wraps the two irreversible item actions with a prompt or
// --yes. done is the past-tense verb printed on success.
func (a *App) destructiveItemCmd(name, done, short, prompt string, run func(context.Context, string) error) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: name + " ITEM", Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			hash, err := a.ownedItemHash(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(yes, fmt.Sprintf(prompt, hash)); err != nil {
				return err
			}
			if err := run(cmd.Context(), hash); err != nil {
				return err
			}
			return a.Printer.Print(actionResult{Action: done, Item: hash, OK: true})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "do not prompt")
	return cmd
}

func openBrowser(url string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	return c.Start()
}

// user / me

type userView wallapop.User

func (v userView) Pretty(w io.Writer, color bool) {
	u := wallapop.User(v)
	rows := [][]string{{"hash", u.Hash}, {"name", u.Name}}
	if u.SellerType != "" {
		rows = append(rows, []string{"type", strings.TrimSpace(u.SellerType + " " + map[bool]string{true: "(verified)", false: ""}[u.Verified])})
	}
	if u.Location.City != "" {
		rows = append(rows, []string{"location", u.Location.City})
	}
	if !u.RegisteredAt.IsZero() {
		rows = append(rows, []string{"since", u.RegisteredAt.Format("2006-01")})
	}
	if u.Stats != nil {
		rows = append(rows, []string{"rating", fmt.Sprintf("%.1f (%d reviews)", u.Stats.RatingAverage, u.Stats.Reviews)}, []string{"sold/listed", fmt.Sprintf("%d / %d", u.Stats.Sold, u.Stats.Published)})
	}
	rows = append(rows, []string{"url", u.URL})
	output.Table(w, color, nil, rows)
}

type reviewsView []wallapop.Review

func (v reviewsView) Pretty(w io.Writer, color bool) {
	rows := make([][]string, 0, len(v))
	for _, r := range v {
		rows = append(rows, []string{r.Date.Format("2006-01-02"), strings.Repeat("★", r.Score), r.ByName, output.Truncate(r.ItemTitle, 30), output.Truncate(r.Comment, 60)})
	}
	output.Table(w, color, []string{"DATE", "SCORE", "BY", "ITEM", "COMMENT"}, rows)
}

func (a *App) userCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Look at a seller",
		Long:  "Look at a seller. USER is a user hash, a profile URL, or the web slug (name-12345678).",
	}
	resolve := func(cmd *cobra.Command, ref string) (string, error) {
		return a.Client.ResolveUserHash(cmd.Context(), ref)
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "show USER", Short: "Profile and stats", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				hash, err := resolve(cmd, args[0])
				if err != nil {
					return err
				}
				u, err := a.Client.User(cmd.Context(), hash)
				if err != nil {
					return err
				}
				return a.Printer.Print(userView(u))
			},
		},
		&cobra.Command{
			Use: "items USER", Short: "Active listings", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				hash, err := resolve(cmd, args[0])
				if err != nil {
					return err
				}
				items, err := a.Client.UserItems(cmd.Context(), hash)
				if err != nil {
					return err
				}
				return a.Printer.Print(itemsView{Items: items})
			},
		},
		&cobra.Command{
			Use: "reviews USER", Short: "Reviews received", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				hash, err := resolve(cmd, args[0])
				if err != nil {
					return err
				}
				reviews, err := a.Client.UserReviews(cmd.Context(), hash)
				if err != nil {
					return err
				}
				return a.Printer.Print(reviewsView(reviews))
			},
		},
	)
	return cmd
}

func (a *App) meCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "me", Short: "Your own account: profile, listings, favorites"}
	var sold bool
	items := &cobra.Command{
		Use: "items", Short: "Your listings (active, or --sold)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			list, err := a.Client.MyItems(cmd.Context(), sold)
			if err != nil {
				return err
			}
			return a.Printer.Print(itemsView{Items: list})
		},
	}
	items.Flags().BoolVar(&sold, "sold", false, "list sold items instead")
	cmd.AddCommand(
		&cobra.Command{
			Use: "show", Short: "Your profile", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := a.requireSession(); err != nil {
					return err
				}
				u, err := a.Client.Me(cmd.Context())
				if err != nil {
					return err
				}
				return a.Printer.Print(userView(u))
			},
		},
		items,
		&cobra.Command{
			Use: "favorites", Short: "Items you favorited", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := a.requireSession(); err != nil {
					return err
				}
				list, err := a.Client.MyFavorites(cmd.Context())
				if err != nil {
					return err
				}
				return a.Printer.Print(itemsView{Items: list})
			},
		},
	)
	return cmd
}
