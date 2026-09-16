package wallapop

import (
	"context"
	"math"
	"net/http"
	"net/url"
)

// Offers use the delivery API. Accepting and checkout are deliberately absent.
var deliveryHeaders = map[string]string{"X-AppVersion": "0"}

type OfferTerms struct {
	ItemHash  string  `json:"item_hash"`
	Price     float64 `json:"price"`
	Currency  string  `json:"currency"`
	MinAmount float64 `json:"min_amount"`
	Remaining int     `json:"remaining_offers_per_day"`
	MaxPerDay int     `json:"max_offers_per_day"`
}

type Offer struct {
	ID       string  `json:"id"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Status   string  `json:"status"`
	Headline string  `json:"headline,omitempty"`
	ItemHash string  `json:"item_hash,omitempty"`
	Buyer    string  `json:"buyer,omitempty"`
}

func (c *Client) OfferTerms(ctx context.Context, itemHash string) (OfferTerms, error) {
	var raw struct {
		Restriction *struct {
			MaxPerDay  *int     `json:"max_offers_per_day"`
			Remaining  *int     `json:"remaining_offers_per_day"`
			MaxPercent *float64 `json:"max_offer_percentage"`
		} `json:"restriction"`
		ItemsPrice money `json:"items_price"`
	}
	_, err := c.do(ctx, request{method: http.MethodGet, base: c.APIBase, path: "/bff/delivery/make-an-offer", query: url.Values{"item_id": {itemHash}}, auth: true, rawHeaders: deliveryHeaders}, &raw)
	if err != nil {
		return OfferTerms{}, err
	}
	r := raw.Restriction
	if r == nil || r.MaxPerDay == nil || r.Remaining == nil || r.MaxPercent == nil || *r.MaxPercent < 0 || *r.MaxPercent > 100 || raw.ItemsPrice.Amount <= 0 || raw.ItemsPrice.Currency == "" {
		return OfferTerms{}, &Error{Kind: KindAPIChanged, Endpoint: "GET /bff/delivery/make-an-offer", Msg: "offer restrictions or price are missing or invalid; no offer was sent"}
	}
	return OfferTerms{ItemHash: itemHash, Price: raw.ItemsPrice.Amount, Currency: raw.ItemsPrice.Currency, MinAmount: math.Round(raw.ItemsPrice.Amount*(100-*r.MaxPercent)) / 100, Remaining: *r.Remaining, MaxPerDay: *r.MaxPerDay}, nil
}

func (c *Client) SendOffer(ctx context.Context, itemHash string, amount float64, currency string) (string, error) {
	id := newUUID()
	_, err := c.do(ctx, request{method: http.MethodPost, base: c.APIBase, path: "/api/v3/delivery/buyer/offers", auth: true, rawHeaders: deliveryHeaders, body: map[string]any{"offer_id": id, "offer_price_amount": amount, "offer_price_currency": currency, "item_ids": []string{itemHash}}}, nil)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (c *Client) DeclineOffer(ctx context.Context, id string) error {
	_, err := c.do(ctx, request{method: http.MethodPatch, base: c.APIBase, path: "/api/v3/delivery/offers/" + url.PathEscape(id) + "/status", auth: true, rawHeaders: deliveryHeaders, body: map[string]string{"status": "DECLINED"}}, nil)
	return err
}

func (c *Client) Offer(ctx context.Context, id string) (Offer, error) {
	var raw struct {
		Status struct {
			Title string `json:"title"`
		} `json:"offer_status"`
		Analytics struct {
			ItemID string `json:"item_id"`
			ID     string `json:"offer_id"`
			Price  money  `json:"offer_price"`
			Status string `json:"offer_status"`
			Buyer  string `json:"buyer_user_id"`
		} `json:"offer_analytics"`
	}
	_, err := c.do(ctx, request{method: http.MethodGet, base: c.APIBase, path: "/bff/delivery/offer-details", query: url.Values{"offer_id": {id}}, auth: true, rawHeaders: deliveryHeaders}, &raw)
	if err != nil {
		return Offer{}, err
	}
	if raw.Analytics.Status == "" || raw.Analytics.Price.Currency == "" || raw.Analytics.ID != id {
		return Offer{}, &Error{Kind: KindAPIChanged, Endpoint: "GET /bff/delivery/offer-details", Msg: "offer details are missing the requested identity, price or state"}
	}
	return Offer{ID: id, Amount: raw.Analytics.Price.Amount, Currency: raw.Analytics.Price.Currency, Status: raw.Analytics.Status, Headline: raw.Status.Title, ItemHash: raw.Analytics.ItemID, Buyer: raw.Analytics.Buyer}, nil
}

// HydrateOffers keeps the original message even when a status card is unavailable.
// Within a page, repeated links share one current status-card read.
func (c *Client) HydrateOffers(ctx context.Context, messages []Message) {
	type result struct {
		offer Offer
		err   error
	}
	cache := make(map[string]result)
	for i := range messages {
		m := &messages[i]
		if m.OfferID == "" {
			continue
		}
		r, ok := cache[m.OfferID]
		if !ok {
			r.offer, r.err = c.Offer(ctx, m.OfferID)
			cache[m.OfferID] = r
		}
		if r.err != nil {
			m.OfferError = r.err.Error()
			continue
		}
		m.Offer = &r.offer
	}
}
