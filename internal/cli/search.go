package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Microck/wallapop-cli/internal/output"
	"github.com/Microck/wallapop-cli/internal/wallapop"
)

// searchFlags are shared by `search` and `watch add search`.
type searchFlags struct {
	lat, lng   float64
	distance   int
	minPrice   int
	maxPrice   int
	conditions []string
	category   int
	shipping   bool
	since      string
	sort       string
	filter     map[string]string
}

func (a *App) bindSearchFlags(cmd *cobra.Command, f *searchFlags) {
	fl := cmd.Flags()
	fl.Float64Var(&f.lat, "lat", 0, "latitude of the search centre (default: profile location)")
	fl.Float64Var(&f.lng, "lng", 0, "longitude of the search centre")
	fl.IntVar(&f.distance, "distance", 0, "radius in km")
	fl.IntVar(&f.minPrice, "min-price", -1, "minimum price")
	fl.IntVar(&f.maxPrice, "max-price", -1, "maximum price")
	fl.StringSliceVar(&f.conditions, "condition", nil, "item condition, repeatable: new, as_good_as_new, good, fair, has_given_it_all")
	fl.IntVar(&f.category, "category", 0, "category id (see `wallapop category list`)")
	fl.BoolVar(&f.shipping, "shipping", false, "only items that can be shipped")
	fl.StringVar(&f.since, "since", "", "listing age: today, week, month")
	fl.StringVar(&f.sort, "sort", "", "order: relevance, newest, price_asc, price_desc")
	fl.StringToStringVar(&f.filter, "filter", nil, "category-specific filter key=value, repeatable (see `wallapop search filters`)")
	_ = cmd.RegisterFlagCompletionFunc("condition", cobra.FixedCompletions([]string{"new", "as_good_as_new", "good", "fair", "has_given_it_all"}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("since", cobra.FixedCompletions([]string{"today", "week", "month"}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("sort", cobra.FixedCompletions([]string{"relevance", "newest", "price_asc", "price_desc"}, cobra.ShellCompDirectiveNoFileComp))
}

var validConditions = map[string]bool{"new": true, "as_good_as_new": true, "good": true, "fair": true, "has_given_it_all": true}

// toParams validates the flags and resolves the location. Vertical filters are
// checked against Wallapop's own filter list when any are given.
func (a *App) toParams(cmd *cobra.Command, keywords []string, f searchFlags) (wallapop.SearchParams, error) {
	loc, err := a.location(f.lat, f.lng, f.distance)
	if err != nil {
		return wallapop.SearchParams{}, err
	}
	p := wallapop.SearchParams{Keywords: strings.Join(keywords, " "), Lat: loc.Lat, Lng: loc.Lng, DistanceKm: loc.RadiusKm, CategoryID: f.category, Shippable: f.shipping, Extra: f.filter}
	if f.minPrice >= 0 {
		v := f.minPrice
		p.MinPrice = &v
	}
	if f.maxPrice >= 0 {
		v := f.maxPrice
		p.MaxPrice = &v
	}
	for _, c := range f.conditions {
		if !validConditions[c] {
			return p, output.Usagef("--condition %q is not one of new, as_good_as_new, good, fair, has_given_it_all", c)
		}
	}
	p.Conditions = f.conditions
	if f.since != "" {
		tf, ok := wallapop.SinceValues[f.since]
		if !ok {
			return p, output.Usagef("--since must be today, week or month")
		}
		p.TimeFilter = tf
	}
	if f.sort != "" {
		ob, ok := wallapop.SortValues[f.sort]
		if !ok {
			return p, output.Usagef("--sort must be relevance, newest, price_asc or price_desc")
		}
		p.OrderBy = ob
	}
	if len(f.filter) > 0 {
		filters, err := a.Client.Filters(cmd.Context(), p.Keywords, p.CategoryID)
		if err != nil {
			return p, err
		}
		if err := wallapop.ValidateExtra(f.filter, filters); err != nil {
			return p, err
		}
	}
	return p, nil
}

type itemsView struct {
	Items    []wallapop.Item `json:"items"`
	NextPage string          `json:"next_page,omitempty"`
}

func (v itemsView) Pretty(w io.Writer, color bool) {
	rows := make([][]string, 0, len(v.Items))
	for _, it := range v.Items {
		flags := ""
		if it.Reserved {
			flags += "R"
		}
		if it.Sold {
			flags += "S"
		}
		if it.Shippable {
			flags += "+"
		}
		rows = append(rows, []string{it.Hash, fmt.Sprintf("%.0f %s", it.Price, it.Currency), flags, output.Truncate(it.Title, 48), output.Truncate(it.Location.City, 16), output.Dim(it.URL, color)})
	}
	output.Table(w, color, []string{"HASH", "PRICE", "", "TITLE", "CITY", "URL"}, rows)
	if v.NextPage != "" {
		fmt.Fprintln(w, output.Dim("more: --next "+output.Truncate(v.NextPage, 24), color))
	}
}

func (a *App) searchCmd() *cobra.Command {
	var f searchFlags
	var limit, pages int
	var next string
	cmd := &cobra.Command{
		Use:   "search [keywords...]",
		Short: "Search listings around a location",
		Long: `Search listings around a location.

The centre defaults to the active profile's location (saved at login, or set
with config). Common filters are flags; category-specific ones (cars: brand,
min_km, gearbox...) go through --filter key=value and are validated against
what Wallapop offers for that category.

Examples:
  wallapop search "thinkpad x1" --max-price 400 --sort newest --format pretty
  wallapop search --category 100 --filter brand=Toyota --filter max_km=120000
  wallapop search bici --lat 41.39 --lng 2.17 --distance 10 --since week
  wallapop search bici --next "$(wallapop search bici | jq -r .next_page)"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.toParams(cmd, args, f)
			if err != nil {
				return err
			}
			p.NextPage = next
			view := itemsView{Items: []wallapop.Item{}}
			for page := 0; page < pages; page++ {
				sp, err := a.Client.Search(cmd.Context(), p)
				if err != nil {
					return err
				}
				view.Items = append(view.Items, sp.Items...)
				view.NextPage = sp.NextPage
				if sp.NextPage == "" || (limit > 0 && len(view.Items) >= limit) {
					break
				}
				p.NextPage = sp.NextPage
			}
			if limit > 0 && len(view.Items) > limit {
				view.Items = view.Items[:limit]
			}
			return a.Printer.Print(view)
		},
	}
	a.bindSearchFlags(cmd, &f)
	cmd.Flags().IntVar(&limit, "limit", 0, "stop after this many items (0 = whole pages)")
	cmd.Flags().IntVar(&pages, "pages", 1, "how many result pages to fetch")
	cmd.Flags().StringVar(&next, "next", "", "continue from a previous next_page token")
	cmd.AddCommand(a.searchFiltersCmd())
	return cmd
}

type filtersView []wallapop.Filter

func (v filtersView) Pretty(w io.Writer, color bool) {
	rows := make([][]string, 0, len(v))
	for _, f := range v {
		opts := ""
		if len(f.Options) > 0 {
			ids := make([]string, 0, len(f.Options))
			for _, o := range f.Options {
				ids = append(ids, o.ID)
			}
			opts = output.Truncate(strings.Join(ids, ","), 60)
		}
		rows = append(rows, []string{f.ID, f.Type, strings.Join(f.Params, ","), opts})
	}
	output.Table(w, color, []string{"FILTER", "TYPE", "PARAMS", "VALUES"}, rows)
}

func (a *App) searchFiltersCmd() *cobra.Command {
	var category int
	cmd := &cobra.Command{
		Use:   "filters [keywords...]",
		Short: "List the filters Wallapop offers, per category",
		Long: `List the filters Wallapop offers for a search. PARAMS are the keys accepted by
--filter; VALUES are the allowed ids for list filters.

Examples:
  wallapop search filters --format pretty
  wallapop search filters --category 100 --format pretty`,
		RunE: func(cmd *cobra.Command, args []string) error {
			filters, err := a.Client.Filters(cmd.Context(), strings.Join(args, " "), category)
			if err != nil {
				return err
			}
			return a.Printer.Print(filtersView(filters))
		},
	}
	cmd.Flags().IntVar(&category, "category", 0, "category id")
	return cmd
}

type categoriesView []wallapop.Category

func (v categoriesView) Pretty(w io.Writer, color bool) {
	var rows [][]string
	var walk func(cs []wallapop.Category, depth int)
	walk = func(cs []wallapop.Category, depth int) {
		for _, c := range cs {
			rows = append(rows, []string{fmt.Sprint(c.ID), strings.Repeat("  ", depth) + c.Name})
			walk(c.Subcategories, depth+1)
		}
	}
	walk(v, 0)
	output.Table(w, color, []string{"ID", "CATEGORY"}, rows)
}

func (a *App) categoryCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "category", Short: "Browse Wallapop's category tree"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List categories and subcategories with their ids", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cats, err := a.Client.Categories(cmd.Context())
			if err != nil {
				return err
			}
			return a.Printer.Print(categoriesView(cats))
		},
	})
	return cmd
}
