package wallapop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SearchParams mirrors the query keys Wallapop's /api/v3/search accepts. Extra
// carries category-specific filters (brand, min_km, ...) under Wallapop's own
// keys: validated against the filters endpoint when a user types them, taken
// as stored when they come from a saved search.
type SearchParams struct {
	Keywords   string
	Lat, Lng   float64
	DistanceKm int
	MinPrice   *int
	MaxPrice   *int
	Conditions []string
	CategoryID int
	Shippable  bool
	TimeFilter string // today | lastWeek | lastMonth
	OrderBy    string // most_relevance | newest | price_low_to_high | price_high_to_low
	Extra      map[string]string
	NextPage   string
}

// Sort names exposed by the CLI mapped to Wallapop's order_by values.
var SortValues = map[string]string{
	"relevance":  "most_relevance",
	"newest":     "newest",
	"price_asc":  "price_low_to_high",
	"price_desc": "price_high_to_low",
}

// SinceValues maps the CLI's --since to Wallapop's time_filter.
var SinceValues = map[string]string{
	"today": "today",
	"week":  "lastWeek",
	"month": "lastMonth",
}

func (p SearchParams) query() url.Values {
	q := url.Values{}
	q.Set("source", "search_box")
	if p.Keywords != "" {
		q.Set("keywords", p.Keywords)
	}
	q.Set("latitude", strconv.FormatFloat(p.Lat, 'f', -1, 64))
	q.Set("longitude", strconv.FormatFloat(p.Lng, 'f', -1, 64))
	if p.DistanceKm > 0 {
		q.Set("distance_in_km", strconv.Itoa(p.DistanceKm))
	}
	if p.MinPrice != nil {
		q.Set("min_sale_price", strconv.Itoa(*p.MinPrice))
	}
	if p.MaxPrice != nil {
		q.Set("max_sale_price", strconv.Itoa(*p.MaxPrice))
	}
	if len(p.Conditions) > 0 {
		q.Set("condition", strings.Join(p.Conditions, ","))
	}
	if p.CategoryID > 0 {
		// category_id is the key the search backend honours; category_ids is ignored.
		q.Set("category_id", strconv.Itoa(p.CategoryID))
	}
	if p.Shippable {
		q.Set("is_shippable", "true")
	}
	if p.TimeFilter != "" {
		q.Set("time_filter", p.TimeFilter)
	}
	if p.OrderBy != "" {
		q.Set("order_by", p.OrderBy)
	}
	for k, v := range p.Extra {
		q.Set(k, v)
	}
	if p.NextPage != "" {
		q.Set("next_page", p.NextPage)
	}
	return q
}

type searchResponse struct {
	Data struct {
		Section struct {
			Payload struct {
				Items []searchItem `json:"items"`
			} `json:"payload"`
		} `json:"section"`
	} `json:"data"`
	Meta struct {
		NextPage string `json:"next_page"`
	} `json:"meta"`
}

type searchItem struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	CategoryID  int    `json:"category_id"`
	Price       money  `json:"price"`
	Images      images `json:"images"`
	Reserved    flag   `json:"reserved"`
	Favorited   flag   `json:"favorited"`
	Location    struct {
		Latitude   float64 `json:"latitude"`
		Longitude  float64 `json:"longitude"`
		PostalCode string  `json:"postal_code"`
		City       string  `json:"city"`
		Country    string  `json:"country_code"`
	} `json:"location"`
	Shipping struct {
		ItemIsShippable bool `json:"item_is_shippable"`
	} `json:"shipping"`
	WebSlug    string `json:"web_slug"`
	CreatedAt  msTime `json:"created_at"`
	ModifiedAt msTime `json:"modified_at"`
	Taxonomy   []struct {
		Name string `json:"name"`
	} `json:"taxonomy"`
}

// Search runs one page. Pass the returned NextPage back in params for the next.
func (c *Client) Search(ctx context.Context, p SearchParams) (SearchPage, error) {
	var raw searchResponse
	resp, err := c.do(ctx, request{method: "GET", base: c.APIBase, path: "/api/v3/search", query: p.query()}, &raw)
	if err != nil {
		return SearchPage{}, err
	}
	page := SearchPage{NextPage: raw.Meta.NextPage, SearchID: resp.Header.Get("x-wallapop-search-id")}
	for _, it := range raw.Data.Section.Payload.Items {
		page.Items = append(page.Items, Item{
			Hash:        it.ID,
			Title:       it.Title,
			Description: it.Description,
			Price:       it.Price.Amount,
			Currency:    it.Price.Currency,
			CategoryID:  it.CategoryID,
			Category:    taxonomyPath(it.Taxonomy),
			SellerHash:  it.UserID,
			Reserved:    it.Reserved.Flag,
			Favorited:   it.Favorited.Flag,
			Shippable:   it.Shipping.ItemIsShippable,
			Location:    Location{Lat: it.Location.Latitude, Lng: it.Location.Longitude, City: it.Location.City, PostalCode: it.Location.PostalCode, Country: it.Location.Country},
			Images:      it.Images.bigURLs(),
			URL:         c.itemURL(it.WebSlug),
			CreatedAt:   it.CreatedAt.Time,
			ModifiedAt:  it.ModifiedAt.Time,
		})
	}
	return page, nil
}

func taxonomyPath(t []struct {
	Name string `json:"name"`
}) string {
	names := make([]string, 0, len(t))
	for _, n := range t {
		names = append(names, n.Name)
	}
	return strings.Join(names, " > ")
}

func (c *Client) itemURL(slug string) string {
	if slug == "" {
		return ""
	}
	return DefaultWebBase + "/item/" + slug
}

type filtersResponse struct {
	FilterSections []struct {
		Filters []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			Type         string `json:"type"`
			SearchParams map[string]struct {
				ParamKey string `json:"param_key"`
			} `json:"search_params"`
			TypeData struct {
				SourceData struct {
					OptionsData struct {
						Options []struct {
							ID    string `json:"id"`
							Title string `json:"title"`
						} `json:"options"`
					} `json:"options_data"`
				} `json:"source_data"`
			} `json:"type_data"`
		} `json:"filters"`
	} `json:"filter_sections"`
}

// Filters lists what Wallapop lets you filter on for a category (0 = general).
// Category-specific filters (cars: brand, min_km...) only appear with the category set.
func (c *Client) Filters(ctx context.Context, keywords string, categoryID int) ([]Filter, error) {
	q := url.Values{"source": {"search_box"}}
	if keywords != "" {
		q.Set("keywords", keywords)
	}
	if categoryID > 0 {
		q.Set("category_id", strconv.Itoa(categoryID))
	}
	var raw filtersResponse
	if err := c.getJSON(ctx, "/api/v3/search/filters/regular-filters", q, false, &raw); err != nil {
		return nil, err
	}
	var out []Filter
	for _, s := range raw.FilterSections {
		for _, f := range s.Filters {
			fl := Filter{ID: f.ID, Title: f.Title, Type: f.Type}
			for _, p := range f.SearchParams {
				if p.ParamKey != "" {
					fl.Params = append(fl.Params, p.ParamKey)
				}
			}
			for _, o := range f.TypeData.SourceData.OptionsData.Options {
				fl.Options = append(fl.Options, FilterOption{ID: o.ID, Title: o.Title})
			}
			out = append(out, fl)
		}
	}
	if len(out) == 0 {
		return nil, &Error{Kind: KindAPIChanged, Endpoint: "GET /api/v3/search/filters/regular-filters", Msg: "wallapop returned no filters"}
	}
	return out, nil
}

// ValidateExtra checks --filter key=value pairs against the filters Wallapop
// offers. It is a usage error, not an API error, when a key or value is unknown.
func ValidateExtra(extra map[string]string, filters []Filter) error {
	byParam := map[string]Filter{}
	for _, f := range filters {
		for _, p := range f.Params {
			byParam[p] = f
		}
	}
	for k, v := range extra {
		f, ok := byParam[k]
		if !ok {
			keys := make([]string, 0, len(byParam))
			for p := range byParam {
				keys = append(keys, p)
			}
			return Usage("unknown filter %q. Valid keys: %s", k, strings.Join(sortedStrings(keys), ", "))
		}
		if len(f.Options) > 0 {
			valid := false
			ids := make([]string, 0, len(f.Options))
			for _, o := range f.Options {
				ids = append(ids, o.ID)
				for _, part := range strings.Split(v, ",") {
					if part == o.ID {
						valid = true
					}
				}
			}
			if !valid {
				return Usage("filter %s=%s is not one of: %s", k, v, strings.Join(ids, ", "))
			}
		}
	}
	return nil
}

func sortedStrings(s []string) []string {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s
}

type categoriesResponse struct {
	Categories []rawCategory `json:"categories"`
}

type rawCategory struct {
	ID            int           `json:"id"`
	Name          string        `json:"name"`
	VerticalID    string        `json:"vertical_id"`
	Subcategories []rawCategory `json:"subcategories"`
}

func (r rawCategory) normalize() Category {
	c := Category{ID: r.ID, Name: r.Name, VerticalID: r.VerticalID}
	for _, s := range r.Subcategories {
		c.Subcategories = append(c.Subcategories, s.normalize())
	}
	return c
}

func (c *Client) Categories(ctx context.Context) ([]Category, error) {
	var raw categoriesResponse
	if err := c.getJSON(ctx, "/api/v3/categories", nil, false, &raw); err != nil {
		return nil, err
	}
	out := make([]Category, 0, len(raw.Categories))
	for _, r := range raw.Categories {
		out = append(out, r.normalize())
	}
	if len(out) == 0 {
		return nil, &Error{Kind: KindAPIChanged, Endpoint: "GET /api/v3/categories", Msg: "wallapop returned no categories"}
	}
	return out, nil
}

// String renders the params for debug output and watch listings.
func (p SearchParams) String() string {
	var parts []string
	if p.Keywords != "" {
		parts = append(parts, fmt.Sprintf("%q", p.Keywords))
	}
	if p.MinPrice != nil || p.MaxPrice != nil {
		lo, hi := "", ""
		if p.MinPrice != nil {
			lo = strconv.Itoa(*p.MinPrice)
		}
		if p.MaxPrice != nil {
			hi = strconv.Itoa(*p.MaxPrice)
		}
		parts = append(parts, lo+".."+hi)
	}
	if p.CategoryID > 0 {
		parts = append(parts, "cat="+strconv.Itoa(p.CategoryID))
	}
	if p.DistanceKm > 0 {
		parts = append(parts, strconv.Itoa(p.DistanceKm)+"km")
	}
	// Sorted so the same target always reads the same way in watch list.
	keys := make([]string, 0, len(p.Extra))
	for k := range p.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+"="+p.Extra[k])
	}
	return strings.Join(parts, " ")
}

// SavedSearch is Wallapop's server-side search alert, "Saved search" in
// CONTEXT.md and the `alert` noun in the CLI. Filters holds the saved query
// minus location and bookkeeping keys, under Wallapop's own parameter names,
// so a script sees exactly what the alert runs.
type SavedSearch struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Keywords    string            `json:"keywords,omitempty"`
	Label       string            `json:"location_label,omitempty"`
	Location    Location          `json:"location"`
	RadiusKm    int               `json:"radius_km,omitempty"`
	Filters     map[string]string `json:"filters"`
	Enabled     bool              `json:"enabled"`
	Hits        int               `json:"hits"`
	CreatedAt   time.Time         `json:"created_at"`
}

// SearchParams replays the saved query the way the web does when the alert is
// opened. Keys the CLI types are mapped onto the typed fields so an imported
// Watch stores the same shape as a hand-made one; anything else rides in
// Extra unvalidated, since Wallapop itself stored the values.
func (s SavedSearch) SearchParams() SearchParams {
	p := SearchParams{Keywords: s.Keywords, Lat: s.Location.Lat, Lng: s.Location.Lng, DistanceKm: s.RadiusKm, Extra: map[string]string{}}
	for k, v := range s.Filters {
		switch k {
		case "min_sale_price":
			n, _ := strconv.Atoi(v)
			p.MinPrice = &n
		case "max_sale_price":
			n, _ := strconv.Atoi(v)
			p.MaxPrice = &n
		case "condition":
			p.Conditions = strings.Split(v, ",")
		case "category_id":
			p.CategoryID, _ = strconv.Atoi(v)
		case "is_shippable":
			p.Shippable = v == "true"
		case "time_filter":
			p.TimeFilter = v
		case "order_by":
			p.OrderBy = v
		default:
			p.Extra[k] = v
		}
	}
	return p
}

// rawSavedSearch is GET /api/v3/searchalerts/savedsearch/ as recorded 2026-09-15.
// query values are untyped: numbers, strings, and lists for category_ids.
type rawSavedSearch struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Label       string         `json:"location_label"`
	Query       map[string]any `json:"query"`
	Alert       struct {
		Enabled bool `json:"enabled"`
		Hits    int  `json:"hits"`
	} `json:"alert"`
	CreatedAt int64 `json:"createdAt"`
}

// savedSearchHeaders: the searchalerts service rejects calls without X-AppVersion.
var savedSearchHeaders = map[string]string{"X-AppVersion": "0"}

// SavedSearches lists the account's saved searches (authenticated).
func (c *Client) SavedSearches(ctx context.Context) ([]SavedSearch, error) {
	var raw []rawSavedSearch
	if _, err := c.do(ctx, request{method: http.MethodGet, base: c.APIBase, path: "/api/v3/searchalerts/savedsearch/", auth: true, rawHeaders: savedSearchHeaders}, &raw); err != nil {
		return nil, err
	}
	out := make([]SavedSearch, 0, len(raw))
	for _, r := range raw {
		out = append(out, normalizeSavedSearch(r))
	}
	return out, nil
}

// SavedSearch fetches one alert by id; an unknown id is KindNotFound (exit 4).
func (c *Client) SavedSearch(ctx context.Context, id string) (SavedSearch, error) {
	var raw rawSavedSearch
	if _, err := c.do(ctx, request{method: http.MethodGet, base: c.APIBase, path: "/api/v3/searchalerts/savedsearch/" + url.PathEscape(id), auth: true, rawHeaders: savedSearchHeaders}, &raw); err != nil {
		var e *Error
		if errors.As(err, &e) && e.Kind == KindNotFound {
			e.Msg = fmt.Sprintf("no saved search %q. See `wallapop alert list`", id)
		}
		return SavedSearch{}, err
	}
	return normalizeSavedSearch(raw), nil
}

// pluralQueryKeys are stored as lists but searched by their first element
// under the singular key, as the web bundle does.
var pluralQueryKeys = map[string]string{"category_ids": "category_id", "subcategory_ids": "subcategory_id"}

func normalizeSavedSearch(r rawSavedSearch) SavedSearch {
	s := SavedSearch{ID: r.ID, Title: r.Title, Description: r.Description, Label: r.Label, Enabled: r.Alert.Enabled, Hits: r.Alert.Hits, Filters: map[string]string{}}
	if r.CreatedAt > 0 {
		s.CreatedAt = time.UnixMilli(r.CreatedAt).UTC()
	}
	for k, v := range r.Query {
		val := queryValue(v)
		if val == "" {
			continue
		}
		switch k {
		case "keywords":
			s.Keywords = val
		case "latitude":
			s.Location.Lat, _ = strconv.ParseFloat(val, 64)
		case "longitude":
			s.Location.Lng, _ = strconv.ParseFloat(val, 64)
		case "distance_in_km":
			s.RadiusKm, _ = strconv.Atoi(val)
		case "country_code", "saved_search_id":
			// Bookkeeping the search endpoint does not take.
		default:
			if singular, ok := pluralQueryKeys[k]; ok {
				k = singular
				val, _, _ = strings.Cut(val, ",")
			}
			s.Filters[k] = val
		}
	}
	return s
}

// queryValue flattens a saved query value to the string the search endpoint
// takes: numbers without a trailing ".0", lists comma-joined like the CLI's
// own multi-value flags, null dropped.
func queryValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			if p := queryValue(e); p != "" {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, ",")
	}
	return fmt.Sprint(v)
}
