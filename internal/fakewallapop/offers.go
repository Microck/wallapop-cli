package fakewallapop

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RecordedOfferEvents contains the send and decline envelopes captured on
// 2026-09-16, with only names and account/conversation/offer identities replaced.
// These server messages have no original channel or original timetoken.
//
//go:embed testdata/offer-events.json
var RecordedOfferEvents []byte

type OfferRestrictions struct {
	MaxOffersPerDay       int     `json:"max_offers_per_day"`
	RemainingOffersPerDay int     `json:"remaining_offers_per_day"`
	MaxOfferPercentage    float64 `json:"max_offer_percentage"`
}

// Offer is current server state, not a snapshot embedded in a chat message.
type Offer struct {
	ID       string
	Amount   float64
	Currency string
	Status   string
	ItemHash string
	Buyer    string
	Headline string
}

// AddOffer seeds an offer and appends its delivery message to matching conversations.
func (s *Server) AddOffer(o Offer) *Offer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.Status == "" {
		o.Status = "PENDING"
	}
	if o.Currency == "" {
		o.Currency = "EUR"
	}
	if o.Buyer == "" {
		o.Buyer = OtherHash
	}
	if o.Headline == "" {
		o.Headline = "Oferta pendiente"
	}
	s.Offers[o.ID] = &o
	s.appendOfferMessage(&o)
	return &o
}

// OfferPayload returns the recorded JSON string content of a delivery_generic message.
func OfferPayload(id, text string) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{
		"text": text, "type": "delivery", "actionURL": "/delivery/conv",
		"buttons": []map[string]string{{
			"loc-key": "chat_buyer_all_all_third_voice_button", "loc-value": "Ver",
			"action": "https://wallapop.com/app/chat/offer/" + id,
		}},
		"enrichedPayload": nil,
	})
	return payload
}

// Called with s.mu held. Messages retain their original text when offer state changes.
func (s *Server) appendOfferMessage(o *Offer) {
	for _, c := range s.Conversations {
		if c.Item != o.ItemHash {
			continue
		}
		if o.Buyer != UserHash && c.Other != o.Buyer {
			continue
		}
		at := time.Now()
		c.Messages = append(c.Messages, Message{
			ID: fmt.Sprintf("offer-%s-%d", o.ID, len(c.Messages)), FromSelf: o.Buyer == UserHash,
			At: at, TimeToken: strconv.FormatInt(at.UnixNano()/100, 10),
			Type: "delivery_generic", Payload: OfferPayload(o.ID, o.Headline),
		})
	}
}

func (s *Server) deliveryOffers(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	if r.Header.Get("X-AppVersion") != "0" {
		writeJSON(w, 400, map[string]any{"message": "X-AppVersion must be 0"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.URL.Path == "/bff/delivery/make-an-offer" && r.Method == http.MethodGet:
		it := s.Items[r.URL.Query().Get("item_id")]
		if it == nil {
			w.WriteHeader(404)
			return
		}
		price := map[string]any{"amount": it.Price, "currency": "EUR"}
		writeJSON(w, 200, map[string]any{
			"restriction": s.OfferRestrictions,
			"item":        map[string]any{"title": it.Title, "price": price},
			"items_price": price, "offer": nil,
		})
	case r.URL.Path == "/bff/delivery/offer-details" && r.Method == http.MethodGet:
		id := r.URL.Query().Get("offer_id")
		if status := s.OfferDetailsStatus[id]; status != 0 {
			w.WriteHeader(status)
			return
		}
		o := s.Offers[id]
		if o == nil {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, 200, map[string]any{
			"offer_status": map[string]any{"title": o.Headline},
			"offer_analytics": map[string]any{
				"item_id": o.ItemHash, "offer_id": o.ID,
				"offer_price":  map[string]any{"amount": o.Amount, "currency": o.Currency},
				"offer_status": o.Status, "buyer_user_id": o.Buyer,
			},
		})
	case r.URL.Path == "/api/v3/delivery/buyer/offers" && r.Method == http.MethodPost:
		var body struct {
			ID       string   `json:"offer_id"`
			Amount   float64  `json:"offer_price_amount"`
			Currency string   `json:"offer_price_currency"`
			Items    []string `json:"item_ids"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Items) != 1 || body.ID == "" || body.Currency != "EUR" {
			w.WriteHeader(400)
			return
		}
		it := s.Items[body.Items[0]]
		if it == nil {
			w.WriteHeader(404)
			return
		}
		if it.Seller == UserHash {
			w.WriteHeader(403)
			return
		}
		limits := s.OfferRestrictions
		if limits.MaxOffersPerDay <= 0 || limits.RemainingOffersPerDay <= 0 {
			w.WriteHeader(429)
			return
		}
		floor := math.Round(it.Price*(100-limits.MaxOfferPercentage)) / 100
		if body.Amount <= 0 || math.Abs(body.Amount*100-math.Round(body.Amount*100)) > 1e-8 || body.Amount < floor || body.Amount > it.Price {
			w.WriteHeader(400)
			return
		}
		if s.Offers[body.ID] != nil {
			w.WriteHeader(409)
			return
		}
		o := &Offer{ID: body.ID, Amount: body.Amount, Currency: body.Currency, Status: "PENDING", ItemHash: it.Hash, Buyer: UserHash, Headline: "Oferta pendiente"}
		s.Offers[o.ID] = o
		s.OfferRestrictions.RemainingOffersPerDay--
		s.appendOfferMessage(o)
		w.WriteHeader(201)
	case strings.HasPrefix(r.URL.Path, "/api/v3/delivery/offers/") && strings.HasSuffix(r.URL.Path, "/status") && r.Method == http.MethodPatch:
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v3/delivery/offers/"), "/status")
		o := s.Offers[id]
		if o == nil {
			w.WriteHeader(404)
			return
		}
		var body struct {
			Status string `json:"status"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Status != "DECLINED" {
			w.WriteHeader(400)
			return
		}
		it := s.Items[o.ItemHash]
		if it == nil || it.Seller != UserHash {
			w.WriteHeader(403)
			return
		}
		if o.Status != "PENDING" {
			w.WriteHeader(409)
			return
		}
		o.Status, o.Headline = "DECLINED", "Oferta rechazada"
		s.appendOfferMessage(o)
		w.WriteHeader(200)
	default:
		w.WriteHeader(404)
	}
}

func messagePage(c *Conversation, end, size int) ([]map[string]any, string) {
	start := 0
	if size > 0 && end > size {
		start = end - size
	}
	messages := make([]map[string]any, 0, end-start)
	for i := end - 1; i >= start; i-- {
		m := c.Messages[i]
		kind := m.Type
		if kind == "" {
			kind = "text"
		}
		raw := map[string]any{"id": m.ID, "from_self": m.FromSelf, "text": m.Text, "timestamp": m.At.UnixMilli(), "status": "read", "type": kind, "time_token": m.TimeToken}
		if m.Payload != nil {
			raw["payload"] = string(m.Payload)
		}
		messages = append(messages, raw)
	}
	next := ""
	if start > 0 {
		next = strconv.Itoa(start)
	}
	return messages, next
}

func (s *Server) olderMessages(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	hash := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v3/instant-messaging/archive/conversation/"), "/messages")
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.Conversations[hash]
	if c == nil {
		w.WriteHeader(404)
		return
	}
	end, err := strconv.Atoi(r.URL.Query().Get("from"))
	if err != nil || end < 0 || end > len(c.Messages) {
		w.WriteHeader(400)
		return
	}
	size := c.MessagePageSize
	if size <= 0 {
		size, _ = strconv.Atoi(r.URL.Query().Get("max_messages"))
	}
	messages, next := messagePage(c, end, size)
	writeJSON(w, 200, map[string]any{"messages": messages, "next_from": next})
}
