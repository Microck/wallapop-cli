package wallapop

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

func asError(err error, target **Error) bool { return errors.As(err, target) }

type rawUser struct {
	ID        string `json:"id"`
	MicroName string `json:"micro_name"`
	Type      string `json:"type"`
	Location  struct {
		Latitude  float64 `json:"approximated_latitude"`
		Longitude float64 `json:"approximated_longitude"`
		City      string  `json:"city"`
		Zip       string  `json:"zip"`
		Country   string  `json:"country_code"`
	} `json:"location"`
	WebSlug      string `json:"web_slug"`
	URLShare     string `json:"url_share"`
	RegisterDate msTime `json:"register_date"`
	SellerType   struct {
		Type     string `json:"type"`
		Verified bool   `json:"verified"`
	} `json:"seller_type"`
}

func (c *Client) normalizeUser(r rawUser) User {
	return User{
		Hash:         r.ID,
		Name:         r.MicroName,
		Slug:         r.WebSlug,
		URL:          firstNonEmpty(r.URLShare, DefaultWebBase+"/user/"+r.WebSlug),
		Type:         r.Type,
		SellerType:   r.SellerType.Type,
		Verified:     r.SellerType.Verified,
		Location:     Location{Lat: r.Location.Latitude, Lng: r.Location.Longitude, City: r.Location.City, PostalCode: r.Location.Zip, Country: r.Location.Country},
		RegisteredAt: r.RegisterDate.Time,
	}
}

// ResolveUserHash accepts a user hash, a profile URL, or a web slug
// (name-12345678) and returns the hash. Slugs go through the profile page.
func (c *Client) ResolveUserHash(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if IsHash(ref) {
		return ref, nil
	}
	slug := ref
	if u, err := url.Parse(ref); err == nil && u.Host != "" {
		_, tail, ok := strings.Cut(u.Path, "/user/")
		if !ok {
			return "", Usage("%q is not a wallapop profile URL", ref)
		}
		slug = strings.Trim(tail, "/")
	}
	var page struct {
		Props struct {
			PageProps struct {
				User struct {
					ID string `json:"id"`
				} `json:"user"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := c.nextData(ctx, "/user/"+slug, &page); err != nil {
		return "", err
	}
	if page.Props.PageProps.User.ID == "" {
		return "", &Error{Kind: KindAPIChanged, Endpoint: "GET /user/{slug}", Msg: "the profile page has no user id the cli can read"}
	}
	return page.Props.PageProps.User.ID, nil
}

// User returns a public profile with its stats.
func (c *Client) User(ctx context.Context, hash string) (User, error) {
	var r rawUser
	if err := c.getJSON(ctx, "/api/v3/users/"+hash, nil, false, &r); err != nil {
		return User{}, err
	}
	if r.ID == "" {
		return User{}, &Error{Kind: KindAPIChanged, Endpoint: "GET /api/v3/users/{hash}", Msg: "user came back without an id"}
	}
	u := c.normalizeUser(r)
	stats, err := c.userStats(ctx, hash)
	if err == nil {
		u.Stats = &stats
	}
	return u, nil
}

// Me returns the logged-in account's profile.
func (c *Client) Me(ctx context.Context) (User, error) {
	var r rawUser
	if err := c.getJSON(ctx, "/api/v3/users/me", nil, true, &r); err != nil {
		return User{}, err
	}
	if r.ID == "" {
		return User{}, &Error{Kind: KindAPIChanged, Endpoint: "GET /api/v3/users/me", Msg: "profile came back without an id"}
	}
	return c.normalizeUser(r), nil
}

func (c *Client) userStats(ctx context.Context, hash string) (UserStats, error) {
	var raw struct {
		Ratings []struct {
			Type  string `json:"type"`
			Value int    `json:"value"`
		} `json:"ratings"`
		Counters []struct {
			Type  string `json:"type"`
			Value int    `json:"value"`
		} `json:"counters"`
		RatingAverage float64 `json:"rating_average"`
	}
	if err := c.getJSON(ctx, "/api/v3/users/"+hash+"/stats", nil, false, &raw); err != nil {
		return UserStats{}, err
	}
	s := UserStats{RatingAverage: raw.RatingAverage}
	for _, r := range raw.Ratings {
		if r.Type == "reviews" {
			s.Reviews = r.Value
		}
	}
	for _, ct := range raw.Counters {
		switch ct.Type {
		case "sold":
			s.Sold = ct.Value
		case "publish":
			s.Published = ct.Value
		}
	}
	return s, nil
}

// UserItems lists a seller's active items.
func (c *Client) UserItems(ctx context.Context, hash string) ([]Item, error) {
	return c.userItems(ctx, "/api/v3/users/"+hash+"/items", false, hash)
}

// UserReviews lists the reviews a user has received.
func (c *Client) UserReviews(ctx context.Context, hash string) ([]Review, error) {
	var raw []struct {
		Item struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"item"`
		Review struct {
			ID             string `json:"id"`
			Date           msTime `json:"date"`
			RatingOverFive int    `json:"rating_over_five"`
			Comments       string `json:"comments"`
		} `json:"review"`
		User struct {
			ID        string `json:"id"`
			MicroName string `json:"micro_name"`
		} `json:"user"`
		Type string `json:"type"`
	}
	if err := c.getJSON(ctx, "/api/v3/users/"+hash+"/reviews", url.Values{"init": {"0"}}, false, &raw); err != nil {
		return nil, err
	}
	out := make([]Review, 0, len(raw))
	for _, r := range raw {
		out = append(out, Review{
			ID: r.Review.ID, Score: r.Review.RatingOverFive, Comment: r.Review.Comments, Date: r.Review.Date.Time,
			ItemHash: r.Item.ID, ItemTitle: r.Item.Title, ByHash: r.User.ID, ByName: r.User.MicroName, Type: r.Type,
		})
	}
	return out, nil
}
