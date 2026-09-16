package wallapop

import (
	"encoding/json"
	"time"
)

// The types in this file are the CLI's stable output contract. Wallapop's raw
// shapes (three different item representations, {"flag":bool} wrappers,
// millisecond epochs) are normalized into these once, in this package.

type Location struct {
	Lat        float64 `json:"lat,omitempty"`
	Lng        float64 `json:"lng,omitempty"`
	City       string  `json:"city,omitempty"`
	PostalCode string  `json:"postal_code,omitempty"`
	Country    string  `json:"country,omitempty"`
}

type Item struct {
	Hash        string  `json:"hash"`
	Title       string  `json:"title"`
	Description string  `json:"description,omitempty"`
	Price       float64 `json:"price"`
	Currency    string  `json:"currency"`
	CategoryID  int     `json:"category_id,omitempty"`
	Category    string  `json:"category,omitempty"`
	SellerHash  string  `json:"seller_hash,omitempty"`
	Condition   string  `json:"condition,omitempty"`
	// Attributes is the category attribute table (a car's brand/model/year,
	// a garment's size). Only `item edit` reads it, to resend what it is not
	// changing, so it stays out of the CLI's own output.
	Attributes map[string]any `json:"-"`
	Reserved   bool           `json:"reserved"`
	Sold       bool           `json:"sold"`
	Expired    bool           `json:"expired"`
	Shippable  bool           `json:"shippable"`
	Favorited  bool           `json:"favorited"`
	Location   Location       `json:"location"`
	Views      int            `json:"views,omitempty"`
	Favorites  int            `json:"favorites,omitempty"`
	Images     []string       `json:"images,omitempty"`
	URL        string         `json:"url"`
	CreatedAt  time.Time      `json:"created_at,omitempty"`
	ModifiedAt time.Time      `json:"modified_at,omitempty"`
}

type SearchPage struct {
	Items    []Item `json:"items"`
	NextPage string `json:"next_page,omitempty"`
	SearchID string `json:"search_id,omitempty"`
}

// Filter is one constraint Wallapop offers for a search, as reported by the
// filters endpoint. Params are the query keys it maps to; Options the allowed
// values for list filters.
type Filter struct {
	ID      string         `json:"id"`
	Title   string         `json:"title"`
	Type    string         `json:"type"`
	Params  []string       `json:"params"`
	Options []FilterOption `json:"options,omitempty"`
}

type FilterOption struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type Category struct {
	ID            int        `json:"id"`
	Name          string     `json:"name"`
	VerticalID    string     `json:"vertical_id,omitempty"`
	Subcategories []Category `json:"subcategories,omitempty"`
}

type User struct {
	Hash         string     `json:"hash"`
	Name         string     `json:"name"`
	Slug         string     `json:"slug,omitempty"`
	URL          string     `json:"url,omitempty"`
	Type         string     `json:"type,omitempty"`
	SellerType   string     `json:"seller_type,omitempty"`
	Verified     bool       `json:"verified"`
	Location     Location   `json:"location"`
	RegisteredAt time.Time  `json:"registered_at,omitempty"`
	Stats        *UserStats `json:"stats,omitempty"`
}

type UserStats struct {
	RatingAverage float64 `json:"rating_average"`
	Reviews       int     `json:"reviews"`
	Sold          int     `json:"sold"`
	Published     int     `json:"published"`
}

type Review struct {
	ID        string    `json:"id"`
	Score     int       `json:"score"`
	Comment   string    `json:"comment,omitempty"`
	Date      time.Time `json:"date"`
	ItemHash  string    `json:"item_hash,omitempty"`
	ItemTitle string    `json:"item_title,omitempty"`
	ByHash    string    `json:"by_hash,omitempty"`
	ByName    string    `json:"by_name,omitempty"`
	Type      string    `json:"type,omitempty"`
}

type Conversation struct {
	Hash     string    `json:"hash"`
	WithUser ChatUser  `json:"with_user"`
	Item     ChatItem  `json:"item"`
	Messages []Message `json:"messages"`
	NextFrom string    `json:"next_from,omitempty"`
	Unread   int       `json:"unread"`
	Channel  string    `json:"channel"`
	Archived bool      `json:"archived,omitempty"`
}

type ChatUser struct {
	Hash      string  `json:"hash"`
	Name      string  `json:"name"`
	Slug      string  `json:"slug,omitempty"`
	Available bool    `json:"available"`
	Blocked   bool    `json:"blocked"`
	Rating    float64 `json:"rating,omitempty"`
	Reviews   int     `json:"reviews,omitempty"`
}

type ChatItem struct {
	Hash     string  `json:"hash"`
	Title    string  `json:"title"`
	Status   string  `json:"status,omitempty"`
	Price    float64 `json:"price"`
	Currency string  `json:"currency"`
	IsMine   bool    `json:"is_mine"`
	URL      string  `json:"url,omitempty"`
}

type Message struct {
	ID         string          `json:"id"`
	FromSelf   bool            `json:"from_self"`
	Text       string          `json:"text"`
	At         time.Time       `json:"at"`
	Status     string          `json:"status,omitempty"`
	Type       string          `json:"type,omitempty"`
	TimeToken  string          `json:"time_token,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	OfferID    string          `json:"offer_id,omitempty"`
	Offer      *Offer          `json:"offer,omitempty"`
	Buyer      string          `json:"buyer,omitempty"`
	OfferError string          `json:"offer_error,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	// Conversation is set on messages received live, where the caller has to
	// route by conversation itself.
	Conversation string `json:"conversation,omitempty"`
}

// flag decodes Wallapop's {"flag": true} wrapper.
type flag struct {
	Flag bool `json:"flag"`
}

// msTime decodes an epoch in milliseconds (Wallapop's convention). Seconds are
// accepted too, since a few older fields use them.
type msTime struct{ time.Time }

func (t *msTime) UnmarshalJSON(b []byte) error {
	var n int64
	if err := json.Unmarshal(b, &n); err != nil || n == 0 {
		return nil
	}
	if n > 1e12 {
		t.Time = time.UnixMilli(n).UTC()
	} else {
		t.Time = time.Unix(n, 0).UTC()
	}
	return nil
}

type money struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type images []struct {
	URLs struct {
		Big    string `json:"big"`
		Medium string `json:"medium"`
		Small  string `json:"small"`
	} `json:"urls"`
}

func (im images) bigURLs() []string {
	out := make([]string, 0, len(im))
	for _, i := range im {
		u := i.URLs.Big
		if u == "" {
			u = i.URLs.Medium
		}
		if u != "" {
			out = append(out, u)
		}
	}
	return out
}
