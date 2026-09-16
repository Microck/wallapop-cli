package wallapop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	hashRe     = regexp.MustCompile(`^[a-z0-9]{12}$`)
	nextDataRe = regexp.MustCompile(`<script id="__NEXT_DATA__" type="application/json">(.*?)</script>`)
)

// IsHash reports whether ref looks like a Wallapop 12-character hash.
func IsHash(ref string) bool { return hashRe.MatchString(ref) }

// ItemRef accepts a hash or an item URL. Bare numeric ids cannot be resolved
// (the item page needs the slug), so they are rejected with a hint.
func itemSlugFromRef(ref string) (slug string, ok bool) {
	ref = strings.TrimSpace(ref)
	if u, err := url.Parse(ref); err == nil && u.Host != "" && strings.Contains(u.Path, "/item/") {
		_, slug, _ = strings.Cut(u.Path, "/item/")
		return strings.Trim(slug, "/"), slug != ""
	}
	return "", false
}

// Item returns the full detail for a hash or URL. Hashes need two requests
// (the API detail has the slug; the page has the reserved/sold flags); URLs
// need one because the page carries everything.
func (c *Client) Item(ctx context.Context, ref string) (Item, error) {
	if slug, ok := itemSlugFromRef(ref); ok {
		return c.itemFromPage(ctx, slug)
	}
	if !IsHash(ref) {
		return Item{}, Usage("%q is not an item hash or item URL. Pass the 12-character hash or the full es.wallapop.com/item/... link", ref)
	}
	detail, err := c.itemFromAPI(ctx, ref)
	if err != nil {
		return Item{}, err
	}
	page, err := c.itemFromPage(ctx, pathTail(detail.URL))
	if err != nil {
		// The API knows the item but the page does not: keep the detail and
		// flag it as expired, which is how Wallapop hides removed items.
		var e *Error
		if asError(err, &e) && e.Kind == KindNotFound {
			detail.Expired = true
			return detail, nil
		}
		return Item{}, err
	}
	// The page is a rendered snapshot and can lag, so the writable fields come
	// from the detail endpoint, the API of record. `item edit` depends on it:
	// it resends the fields it is not changing, and a stale title read back
	// from the page would revert the listing.
	page.Title = firstNonEmpty(detail.Title, page.Title)
	page.Description = firstNonEmpty(detail.Description, page.Description)
	page.Condition = firstNonEmpty(detail.Condition, page.Condition)
	if detail.Price > 0 {
		page.Price = detail.Price
		page.Currency = firstNonEmpty(detail.Currency, page.Currency)
	}
	// Category is the exception: the page carries the whole taxonomy path,
	// detail only its root, and editLeafID needs the path.
	page.Category = firstNonEmpty(page.Category, detail.Category)
	// Only the detail endpoint exposes the category attribute table, and
	// `item edit` has to resend it or the write clears it.
	page.Attributes = detail.Attributes
	return page, nil
}

// ResolveItemHash turns any accepted item reference into its hash.
func (c *Client) ResolveItemHash(ctx context.Context, ref string) (string, error) {
	if IsHash(ref) {
		return ref, nil
	}
	it, err := c.Item(ctx, ref)
	if err != nil {
		return "", err
	}
	return it.Hash, nil
}

type itemDetail struct {
	ID    string `json:"id"`
	Title struct {
		Original string `json:"original"`
	} `json:"title"`
	Description struct {
		Original string `json:"original"`
	} `json:"description"`
	Taxonomy []struct {
		Name string `json:"name"`
	} `json:"taxonomy"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Slug  string `json:"slug"`
	Price struct {
		Cash money `json:"cash"`
	} `json:"price"`
	Images   images `json:"images"`
	Location struct {
		Latitude   float64 `json:"latitude"`
		Longitude  float64 `json:"longitude"`
		City       string  `json:"city"`
		PostalCode string  `json:"postal_code"`
		Country    string  `json:"country_code"`
	} `json:"location"`
	TypeAttributes typeAttrs `json:"type_attributes"`
	Shipping       struct {
		ItemIsShippable bool `json:"item_is_shippable"`
	} `json:"shipping"`
	Favorited flag `json:"favorited"`
	Counters  struct {
		Views     int `json:"views"`
		Favorites int `json:"favorites"`
	} `json:"counters"`
	ModifiedDate msTime `json:"modified_date"`
}

// typeAttrs decodes the category attribute table. Each entry arrives as a
// {"value": ...} object (recorded live: condition, and on vertical listings
// brand/model/year); entries shaped otherwise are skipped, since the item
// write payload takes flat scalars.
type typeAttrs map[string]any

func (m *typeAttrs) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := typeAttrs{}
	for k, v := range raw {
		var d struct {
			Value any `json:"value"`
		}
		if err := json.Unmarshal(v, &d); err == nil && d.Value != nil {
			out[k] = d.Value
		}
	}
	*m = out
	return nil
}

func (m typeAttrs) str(key string) string {
	s, _ := m[key].(string)
	return s
}

func (c *Client) itemFromAPI(ctx context.Context, hash string) (Item, error) {
	var d itemDetail
	if err := c.getJSON(ctx, "/api/v3/items/"+hash, nil, false, &d); err != nil {
		return Item{}, err
	}
	if d.ID == "" {
		return Item{}, &Error{Kind: KindAPIChanged, Endpoint: "GET /api/v3/items/{hash}", Msg: "item detail came back without an id"}
	}
	return Item{
		Hash:        d.ID,
		Title:       d.Title.Original,
		Description: d.Description.Original,
		Price:       d.Price.Cash.Amount,
		Currency:    d.Price.Cash.Currency,
		Category:    taxonomyPath(d.Taxonomy),
		SellerHash:  d.User.ID,
		Condition:   d.TypeAttributes.str("condition"),
		Attributes:  d.TypeAttributes,
		Shippable:   d.Shipping.ItemIsShippable,
		Favorited:   d.Favorited.Flag,
		Location:    Location{Lat: d.Location.Latitude, Lng: d.Location.Longitude, City: d.Location.City, PostalCode: d.Location.PostalCode, Country: d.Location.Country},
		Views:       d.Counters.Views,
		Favorites:   d.Counters.Favorites,
		Images:      d.Images.bigURLs(),
		URL:         c.itemURL(d.Slug),
		ModifiedAt:  d.ModifiedDate.Time,
	}, nil
}

// itemPage is the subset of the item page's __NEXT_DATA__ the CLI reads.
type itemPage struct {
	Props struct {
		PageProps struct {
			Item struct {
				ID     string `json:"id"`
				UserID string `json:"userId"`
				Title  struct {
					Original string `json:"original"`
				} `json:"title"`
				Description struct {
					Original string `json:"original"`
				} `json:"description"`
				Slug  string `json:"slug"`
				Price struct {
					Cash money `json:"cash"`
				} `json:"price"`
				Flags struct {
					Reserved bool `json:"reserved"`
					Sold     bool `json:"sold"`
					Expired  bool `json:"expired"`
					OnHold   bool `json:"onHold"`
				} `json:"flags"`
				ModifiedDate msTime `json:"modifiedDate"`
				Views        int    `json:"views"`
				Favorites    int    `json:"favorites"`
				Images       images `json:"images"`
				Location     struct {
					Latitude   float64 `json:"latitude"`
					Longitude  float64 `json:"longitude"`
					City       string  `json:"city"`
					PostalCode string  `json:"postalCode"`
					Country    string  `json:"countryCode"`
				} `json:"location"`
				Shipping struct {
					IsItemShippable bool `json:"isItemShippable"`
				} `json:"shipping"`
				Condition struct {
					Value string `json:"value"`
				} `json:"condition"`
				Taxonomies []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"taxonomies"`
			} `json:"item"`
		} `json:"pageProps"`
	} `json:"props"`
}

func (c *Client) itemFromPage(ctx context.Context, slug string) (Item, error) {
	var page itemPage
	if err := c.nextData(ctx, "/item/"+slug, &page); err != nil {
		return Item{}, err
	}
	it := page.Props.PageProps.Item
	if it.ID == "" {
		return Item{}, &Error{Kind: KindAPIChanged, Endpoint: "GET /item/{slug}", Msg: "item page has no item data"}
	}
	out := Item{
		Hash:        it.ID,
		Title:       it.Title.Original,
		Description: it.Description.Original,
		Price:       it.Price.Cash.Amount,
		Currency:    it.Price.Cash.Currency,
		SellerHash:  it.UserID,
		Condition:   it.Condition.Value,
		Reserved:    it.Flags.Reserved,
		Sold:        it.Flags.Sold,
		Expired:     it.Flags.Expired || it.Flags.OnHold,
		Shippable:   it.Shipping.IsItemShippable,
		Location:    Location{Lat: it.Location.Latitude, Lng: it.Location.Longitude, City: it.Location.City, PostalCode: it.Location.PostalCode, Country: it.Location.Country},
		Views:       it.Views,
		Favorites:   it.Favorites,
		Images:      it.Images.bigURLs(),
		URL:         c.itemURL(firstNonEmpty(it.Slug, slug)),
		ModifiedAt:  it.ModifiedDate.Time,
	}
	if n := len(it.Taxonomies); n > 0 {
		out.CategoryID, _ = strconv.Atoi(it.Taxonomies[0].ID)
		names := make([]string, 0, n)
		for _, t := range it.Taxonomies {
			names = append(names, t.Name)
		}
		out.Category = strings.Join(names, " > ")
	}
	return out, nil
}

// nextData fetches a web page and decodes its __NEXT_DATA__ blob. This is how
// the CLI reads state the JSON API does not expose (item flags, user hashes
// behind slugs).
func (c *Client) nextData(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.WebBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.currentUA())
	req.Header.Set("Accept", "text/html")
	endpoint := "GET " + path
	c.debugf("> GET %s", c.WebBase+path)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return c.netError(endpoint, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return c.netError(endpoint, err)
	}
	c.debugf("< %d %s (%d bytes)", resp.StatusCode, endpoint, len(raw))
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return &Error{Kind: KindNotFound, Status: resp.StatusCode, Endpoint: endpoint, Msg: "wallapop has no page at " + path}
	}
	if resp.StatusCode != http.StatusOK {
		return c.statusError(endpoint, resp.StatusCode, raw)
	}
	m := nextDataRe.FindSubmatch(raw)
	if m == nil {
		return &Error{Kind: KindAPIChanged, Status: resp.StatusCode, Endpoint: endpoint, Msg: "the wallapop page has no __NEXT_DATA__ block the cli can read"}
	}
	if err := json.Unmarshal(m[1], out); err != nil {
		return &Error{Kind: KindAPIChanged, Status: resp.StatusCode, Endpoint: endpoint, Body: "decode: " + err.Error(), Msg: "the wallapop page data has an unexpected shape"}
	}
	return nil
}

// SetFavorite adds or removes the item from the account's favorites.
func (c *Client) SetFavorite(ctx context.Context, hash string, on bool) error {
	_, err := c.do(ctx, request{method: http.MethodPut, base: c.APIBase, path: "/api/v3/items/" + hash + "/favorite", body: map[string]bool{"favorited": on}, auth: true}, nil)
	return err
}

// SetReserved toggles the reserved flag on one of the account's own items.
func (c *Client) SetReserved(ctx context.Context, hash string, on bool) error {
	_, err := c.do(ctx, request{method: http.MethodPut, base: c.APIBase, path: "/api/v3/items/" + hash + "/reserve", body: map[string]bool{"reserved": on}, auth: true}, nil)
	return ownWriteError(err)
}

// ownWriteError names what a bare 401/403 on an item write means. Wallapop
// answers writes on someone else's item that way, with no body (verified
// 2026-09-15: 401 for reserve and delete, 403 for sold), which the generic
// mapping would report as a dead session or a rate limit. Callers check
// ownership before writing; this covers the race when they lose it. A 401 or
// 403 that carries a body (Wallapop's auth envelope, CloudFront's HTML) keeps
// its generic classification.
func ownWriteError(err error) error {
	var e *Error
	if errors.As(err, &e) && e.Body == "" && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden) {
		e.Kind = KindGeneric
		e.Retryable = false
		e.Msg = "wallapop refused the write: the item is not listed by this account, or the session lapsed. Check `wallapop item show` and `wallapop auth status --check`"
	}
	return err
}

// MarkSold marks one of the account's own items as sold. Irreversible on Wallapop.
func (c *Client) MarkSold(ctx context.Context, hash string) error {
	_, err := c.do(ctx, request{method: http.MethodPut, base: c.APIBase, path: "/api/v3/items/" + hash + "/sold", auth: true}, nil)
	return ownWriteError(err)
}

// DeleteItem removes one of the account's own items. Irreversible on Wallapop.
func (c *Client) DeleteItem(ctx context.Context, hash string) error {
	_, err := c.do(ctx, request{method: http.MethodDelete, base: c.APIBase, path: "/api/v3/items/" + hash, auth: true}, nil)
	return ownWriteError(err)
}

type userItemsResponse struct {
	Data []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Slug        string `json:"slug"`
		Images      images `json:"images"`
		Price       struct {
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
			Cash     *money  `json:"cash"`
		} `json:"price"`
		Shipping struct {
			ItemIsShippable bool `json:"item_is_shippable"`
		} `json:"shipping"`
		Reserved flag   `json:"reserved"`
		Sold     flag   `json:"sold"`
		UserID   string `json:"user_id"`
	} `json:"data"`
	Meta struct {
		Next string `json:"next"`
	} `json:"meta"`
}

func (c *Client) userItems(ctx context.Context, path string, auth bool, sellerHash string) ([]Item, error) {
	var raw userItemsResponse
	if err := c.getJSON(ctx, path, nil, auth, &raw); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(raw.Data))
	for _, d := range raw.Data {
		price, cur := d.Price.Amount, d.Price.Currency
		if d.Price.Cash != nil {
			price, cur = d.Price.Cash.Amount, d.Price.Cash.Currency
		}
		out = append(out, Item{
			Hash:        d.ID,
			Title:       d.Title,
			Description: d.Description,
			Price:       price,
			Currency:    cur,
			SellerHash:  firstNonEmpty(d.UserID, sellerHash),
			Reserved:    d.Reserved.Flag,
			Sold:        d.Sold.Flag,
			Shippable:   d.Shipping.ItemIsShippable,
			Images:      d.Images.bigURLs(),
			URL:         c.itemURL(d.Slug),
		})
	}
	return out, nil
}

// MyItems lists the account's active (or sold) listings.
func (c *Client) MyItems(ctx context.Context, sold bool) ([]Item, error) {
	path := "/api/v3/user/items"
	if sold {
		path += "/sold"
	}
	return c.userItems(ctx, path, true, "")
}

// MyFavorites lists the items the account has favorited.
func (c *Client) MyFavorites(ctx context.Context) ([]Item, error) {
	items, err := c.userItems(ctx, "/api/v3/user/items/favorited", true, "")
	for i := range items {
		items[i].Favorited = true
	}
	return items, err
}

func pathTail(u string) string {
	i := strings.LastIndex(u, "/")
	if i < 0 {
		return u
	}
	return u[i+1:]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
