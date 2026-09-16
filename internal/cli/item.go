package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
		a.itemCreateCmd(),
		a.itemEditCmd(),
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

// itemCreateCmd publishes a new listing from the terminal. Required fields
// missing on the command line are a usage error (exit 2), never a partial
// publish: the server rejects imageless creates, so --image is required too.
func (a *App) itemCreateCmd() *cobra.Command {
	var title, description, category, condition string
	var price, lat, lng float64
	var images []string
	var attrs []string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Publish a new listing with one or more images",
		Long: `Publish a new listing as the active profile.

Category accepts a leaf id or name (the upload flow only takes leaves).
Attributes specific to the category go through --attr key=value and are
validated against what Wallapop lists for it; unknown keys fail before
anything is published. Location defaults to the profile's.

Examples:
  wallapop item create --title "MTB" --description "Barely used" --price 120 --category MTB --condition good --image bike1.jpg --image bike2.jpg
  wallapop item create --title "Golf" --description "One owner" --price 8000 --category Cars --condition good --attr brand=Volkswagen --attr year=2019 --image car.jpg`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			var missing []string
			if title == "" {
				missing = append(missing, "--title")
			}
			if description == "" {
				missing = append(missing, "--description")
			}
			if !cmd.Flags().Changed("price") {
				missing = append(missing, "--price")
			}
			if category == "" {
				missing = append(missing, "--category")
			}
			if condition == "" {
				missing = append(missing, "--condition")
			}
			if len(images) == 0 {
				missing = append(missing, "--image FILE")
			}
			if len(missing) > 0 {
				return output.Usagef("missing required %s", strings.Join(missing, ", "))
			}
			loc, err := a.location(lat, lng, 0)
			if err != nil {
				return err
			}
			cats, err := a.Client.CreateCategories(cmd.Context())
			if err != nil {
				return err
			}
			cat, err := wallapop.ResolveCreateCategory(cats, category)
			if err != nil {
				return err
			}
			parsed, err := parseItemAttrs(attrs, cat)
			if err != nil {
				return err
			}
			files, err := loadItemImages(images)
			if err != nil {
				return err
			}
			it, err := a.Client.CreateItem(cmd.Context(), wallapop.CreateInput{
				Title: title, Description: description, Price: price,
				CategoryLeaf: cat.LeafID, CategoryRoot: cat.RootID, Condition: condition,
				Lat: loc.Lat, Lng: loc.Lng, Attrs: parsed, Images: files,
			})
			if err != nil {
				return err
			}
			return a.Printer.Print(itemView(it))
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "listing title")
	cmd.Flags().StringVar(&description, "description", "", "listing description")
	cmd.Flags().Float64Var(&price, "price", 0, "price in euros")
	cmd.Flags().StringVar(&category, "category", "", "leaf category id or name")
	cmd.Flags().StringVar(&condition, "condition", "", "item condition")
	cmd.Flags().Float64Var(&lat, "lat", 0, "listing latitude (default: profile location)")
	cmd.Flags().Float64Var(&lng, "lng", 0, "listing longitude (default: profile location)")
	cmd.Flags().StringArrayVar(&images, "image", nil, "image file to upload (repeatable, at least one)")
	cmd.Flags().StringArrayVar(&attrs, "attr", nil, "category attribute key=value (repeatable)")
	return cmd
}

// itemEditCmd changes fields on one of the account's own listings. Only the
// flags passed change; everything else stays as it was. With no change flags
// it reports usage instead of writing.
func (a *App) itemEditCmd() *cobra.Command {
	var title, description, condition string
	var price float64
	var images []string
	var attrs []string
	cmd := &cobra.Command{
		Use:   "edit ITEM",
		Short: "Change title, description, price, condition or images on your listing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if !fl.Changed("title") && !fl.Changed("description") && !fl.Changed("price") && !fl.Changed("condition") && len(attrs) == 0 && len(images) == 0 {
				return output.Usagef("nothing to change. Pass at least one of --title, --description, --price, --condition, --attr, --image")
			}
			hash, err := a.ownedItemHash(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			in := wallapop.EditInput{Attrs: map[string]string{}}
			if fl.Changed("title") {
				in.Title = &title
			}
			if fl.Changed("description") {
				in.Description = &description
			}
			if fl.Changed("price") {
				in.Price = &price
			}
			if fl.Changed("condition") {
				in.Condition = &condition
			}
			if len(attrs) > 0 {
				it, err := a.Client.Item(cmd.Context(), hash)
				if err != nil {
					return err
				}
				cats, err := a.Client.CreateCategories(cmd.Context())
				if err != nil {
					return err
				}
				cat, err := wallapop.ResolveCreateCategory(cats, it.Category)
				if err != nil && it.CategoryID != 0 {
					// Fall back to the numeric id when the name no longer resolves.
					cat, err = wallapop.ResolveCreateCategory(cats, strconv.Itoa(it.CategoryID))
				}
				if err != nil {
					return err
				}
				parsed, err := parseItemAttrs(attrs, cat)
				if err != nil {
					return err
				}
				in.Attrs = parsed
				// The leaf is already resolved here; hand it over so the
				// write does not have to recover it from the taxonomy path.
				in.CategoryLeaf = cat.LeafID
			}
			if len(images) > 0 {
				files, err := loadItemImages(images)
				if err != nil {
					return err
				}
				in.Images = files
			}
			me, err := a.userHash(cmd.Context())
			if err != nil {
				return err
			}
			it, err := a.Client.EditItem(cmd.Context(), hash, me, in)
			if err != nil {
				return err
			}
			return a.Printer.Print(itemView(it))
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().Float64Var(&price, "price", 0, "new price in euros")
	cmd.Flags().StringVar(&condition, "condition", "", "new condition")
	cmd.Flags().StringArrayVar(&images, "image", nil, "replacement image files (repeatable)")
	cmd.Flags().StringArrayVar(&attrs, "attr", nil, "category attribute key=value (repeatable)")
	return cmd
}

// commonAttrFlags are the payload fields that have their own typed flags.
// Routing them through --attr would let `--attr title=x` quietly beat
// `--title`, and would put price_amount on the wire as a string.
var commonAttrFlags = map[string]string{
	"title":        "--title",
	"description":  "--description",
	"condition":    "--condition",
	"price_amount": "--price",
}

// parseItemAttrs turns --attr key=value pairs into a map, rejecting unknown
// keys against the category's attribute list before anything is published.
// Keys are stored under the category's own spelling: the API drops the
// attribute when the casing does not match.
func parseItemAttrs(pairs []string, cat wallapop.CreateCategory) (map[string]string, error) {
	out := map[string]string{}
	allowed := map[string]string{}
	for _, a := range cat.Attrs {
		allowed[strings.ToLower(a)] = a
	}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, output.Usagef("bad --attr %q. Use key=value", p)
		}
		lower := strings.ToLower(k)
		if flag, ok := commonAttrFlags[lower]; ok {
			return nil, output.Usagef("set %s with %s, not --attr", lower, flag)
		}
		canon, ok := allowed[lower]
		if !ok {
			return nil, output.Usagef("unknown attribute %q for category %s. Valid: %s", k, cat.Name, strings.Join(cat.Attrs, ", "))
		}
		out[canon] = v
	}
	return out, nil
}

// loadItemImages reads image files, refusing non-images before any upload.
func loadItemImages(paths []string) ([]wallapop.UploadImage, error) {
	var out []wallapop.UploadImage
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			return nil, output.Usagef("image %s is empty", p)
		}
		ct := http.DetectContentType(raw[:min(512, len(raw))])
		if !strings.HasPrefix(ct, "image/") {
			return nil, output.Usagef("image %s is not an image (%s)", p, ct)
		}
		out = append(out, wallapop.UploadImage{Name: filepath.Base(p), Data: raw, ContentType: ct})
	}
	return out, nil
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
