package cli_test

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Microck/wallapop-cli/internal/fakewallapop"
)

const (
	firstOfferID = "4e11b01a-22cd-4aaa-8888-123456789abc"
	lastOfferID  = "5e11b01a-22cd-4aaa-8888-123456789abc"
)

type offerMessage struct {
	ID         string          `json:"id"`
	Text       string          `json:"text"`
	Type       string          `json:"type"`
	Kind       string          `json:"kind"`
	OfferID    string          `json:"offer_id"`
	TimeToken  string          `json:"time_token"`
	Payload    json.RawMessage `json:"payload"`
	OfferError string          `json:"offer_error"`
	Offer      *struct {
		ID       string  `json:"id"`
		Amount   float64 `json:"amount"`
		Currency string  `json:"currency"`
		Status   string  `json:"status"`
		ItemHash string  `json:"item_hash"`
		Buyer    string  `json:"buyer"`
		Headline string  `json:"headline"`
	} `json:"offer"`
}

func assertOffer(t *testing.T, m offerMessage, id, status string, amount float64, buyer string) {
	t.Helper()
	if m.OfferID != id || m.Offer == nil {
		t.Fatalf("missing linked offer %s: %+v", id, m)
	}
	o := m.Offer
	if o.ID != id || o.Amount != amount || o.Currency != "EUR" || o.Status != status || o.Buyer != buyer || o.ItemHash != "offeritem001" || o.Headline == "" {
		t.Fatalf("offer details = %+v", o)
	}
	if m.Kind == "" || len(m.Payload) == 0 || m.OfferError != "" {
		t.Fatalf("offer message lost classification/payload or failed enrichment: %+v", m)
	}
}

func offerConversation(h *harness, mine bool, price float64) *fakewallapop.Conversation {
	seller := fakewallapop.OtherHash
	if mine {
		seller = fakewallapop.UserHash
	}
	h.fake.AddItem(fakewallapop.Item{Hash: "offeritem001", Title: "Offer bike", Price: price, Seller: seller})
	return h.fake.AddConversation(fakewallapop.Conversation{Hash: "convhash0001", Item: "offeritem001", Other: fakewallapop.OtherHash})
}

func offerWrites(h *harness) int {
	n := 0
	for _, r := range h.fake.RequestsUnder("/api/v3/delivery/") {
		if r.Method == "POST" || r.Method == "PATCH" {
			n++
		}
	}
	return n
}

func TestChatOfferSendAppearsWithCurrentDetails(t *testing.T) {
	h := newHarness(t)
	h.login()
	offerConversation(h, false, 1.01)
	// 70% of 1.01 rounds to 0.71, rather than truncating to 0.70.
	h.must("", "chat", "offer", "convhash0001", "0.71")
	var conv struct{ Messages []offerMessage }
	decode(t, h.must("", "chat", "show", "convhash0001", "--no-mark-read").stdout, &conv)
	if len(conv.Messages) != 1 {
		t.Fatalf("sent offer not present in conversation: %+v", conv.Messages)
	}
	m := conv.Messages[0]
	if m.OfferID == "" {
		t.Fatalf("sent offer has no identity: %+v", m)
	}
	assertOffer(t, m, m.OfferID, "PENDING", 0.71, fakewallapop.UserHash)
	if m.Type != "delivery_generic" || h.fake.OfferRestrictions.RemainingOffersPerDay != 7 {
		t.Fatalf("send did not create delivery message and consume allowance: %+v", m)
	}
	var inbox struct {
		Conversations []struct{ Messages []offerMessage }
	}
	decode(t, h.must("", "chat", "list").stdout, &inbox)
	if len(inbox.Conversations) != 1 || len(inbox.Conversations[0].Messages) != 1 {
		t.Fatalf("offer missing from inbox: %+v", inbox)
	}
	assertOffer(t, inbox.Conversations[0].Messages[0], m.OfferID, "PENDING", 0.71, fakewallapop.UserHash)
}

func TestChatOfferRefusesInvalidSendBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name      string
		amount    string
		mine      bool
		remaining int
	}{
		{name: "daily cap", amount: "0.80", remaining: 0},
		{name: "rounded floor", amount: "0.70", remaining: 8},
		{name: "above price", amount: "1.02", remaining: 8},
		{name: "own listing", amount: "0.80", mine: true, remaining: 8},
		{name: "zero", amount: "0", remaining: 8},
		{name: "negative", amount: "-1", remaining: 8},
		{name: "fractional cent", amount: "0.801", remaining: 8},
		{name: "not money", amount: "eighty", remaining: 8},
		{name: "nonfinite", amount: "NaN", remaining: 8},
		{name: "infinite", amount: "Inf", remaining: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.login()
			conv := offerConversation(h, tc.mine, 1.01)
			h.fake.OfferRestrictions.RemainingOffersPerDay = tc.remaining
			r := h.run("", "chat", "offer", "convhash0001", "--", tc.amount)
			if r.code == 0 {
				t.Fatalf("invalid offer accepted: %s", r.stdout)
			}
			if offerWrites(h) != 0 || len(h.fake.Offers) != 0 || len(conv.Messages) != 0 {
				t.Fatalf("invalid offer reached mutation endpoint: writes=%d offers=%v messages=%v", offerWrites(h), h.fake.Offers, conv.Messages)
			}
		})
	}
}

func TestChatOfferDeclineFindsLatestLinkedOfferAcrossHistory(t *testing.T) {
	h := newHarness(t)
	h.login()
	conv := offerConversation(h, true, 100)
	conv.MessagePageSize = 1
	first := h.fake.AddOffer(fakewallapop.Offer{ID: firstOfferID, ItemHash: conv.Item, Amount: 70})
	latest := h.fake.AddOffer(fakewallapop.Offer{ID: lastOfferID, ItemHash: conv.Item, Amount: 80})
	conv.Messages = append(conv.Messages, fakewallapop.Message{ID: "new-text", Text: "Still available?", At: time.Now(), TimeToken: "17895562791081226"})
	h.must("", "chat", "offer", "decline", conv.Hash)
	if first.Status != "PENDING" || latest.Status != "DECLINED" {
		t.Fatalf("decline selected wrong offer: first=%+v latest=%+v", first, latest)
	}
	var shown struct{ Messages []offerMessage }
	decode(t, h.must("", "chat", "show", conv.Hash, "--no-mark-read").stdout, &shown)
	if len(shown.Messages) != 4 {
		t.Fatalf("paged history lost messages: %+v", shown.Messages)
	}
	assertOffer(t, shown.Messages[0], firstOfferID, "PENDING", 70, fakewallapop.OtherHash)
	// Both the original offer message and the new decline message show current state.
	assertOffer(t, shown.Messages[1], lastOfferID, "DECLINED", 80, fakewallapop.OtherHash)
	assertOffer(t, shown.Messages[3], lastOfferID, "DECLINED", 80, fakewallapop.OtherHash)
	before := offerWrites(h)
	if r := h.run("", "chat", "offer", "decline", conv.Hash); r.code == 0 {
		t.Fatal("declining again must not select an older pending offer")
	}
	if offerWrites(h) != before || first.Status != "PENDING" {
		t.Fatal("terminal latest offer allowed an older offer to be declined")
	}
}

func TestChatOfferDeclineRefusesBuyerAndUnknownOfferState(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mine        bool
		unavailable bool
	}{
		{name: "buyer", mine: false},
		{name: "detail unavailable", mine: true, unavailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.login()
			conv := offerConversation(h, tc.mine, 100)
			o := h.fake.AddOffer(fakewallapop.Offer{ID: lastOfferID, ItemHash: conv.Item, Amount: 80})
			if tc.unavailable {
				h.fake.OfferDetailsStatus = map[string]int{lastOfferID: 404}
			}
			if r := h.run("", "chat", "offer", "decline", conv.Hash); r.code == 0 {
				t.Fatal("unsafe decline succeeded")
			}
			if offerWrites(h) != 0 || o.Status != "PENDING" {
				t.Fatal("unsafe decline reached mutation endpoint")
			}
		})
	}
}

func TestChatOfferReadKeepsMessageWhenDetailsFailAndRefreshesLater(t *testing.T) {
	h := newHarness(t)
	h.login()
	conv := offerConversation(h, true, 100)
	o := h.fake.AddOffer(fakewallapop.Offer{ID: lastOfferID, ItemHash: conv.Item, Amount: 80})
	h.fake.OfferDetailsStatus = map[string]int{lastOfferID: 404}
	var failed struct{ Messages []offerMessage }
	decode(t, h.must("", "chat", "show", conv.Hash, "--no-mark-read").stdout, &failed)
	if len(failed.Messages) != 1 {
		t.Fatalf("failed detail lookup dropped original message: %+v", failed)
	}
	m := failed.Messages[0]
	if m.ID != conv.Messages[0].ID || m.TimeToken != conv.Messages[0].TimeToken || m.OfferID != lastOfferID || m.OfferError == "" || len(m.Payload) == 0 || m.Offer != nil {
		t.Fatalf("failed lookup lost message identity or hid the error: %+v", m)
	}
	delete(h.fake.OfferDetailsStatus, lastOfferID)
	o.Status, o.Headline = "DECLINED", "Oferta rechazada"
	var refreshed struct{ Messages []offerMessage }
	decode(t, h.must("", "chat", "show", conv.Hash, "--no-mark-read").stdout, &refreshed)
	if len(refreshed.Messages) != 1 {
		t.Fatalf("refresh changed history length: %+v", refreshed)
	}
	assertOffer(t, refreshed.Messages[0], lastOfferID, "DECLINED", 80, fakewallapop.OtherHash)
	if refreshed.Messages[0].ID != m.ID || refreshed.Messages[0].TimeToken != m.TimeToken {
		t.Fatal("detail refresh changed original message identity")
	}
}

func TestChatOpenShowsNestedServerOffersAndUnknownMessages(t *testing.T) {
	h := newHarness(t)
	h.login()
	conv := offerConversation(h, true, 100)
	h.fake.AddOffer(fakewallapop.Offer{ID: lastOfferID, ItemHash: conv.Item, Amount: 80, Status: "DECLINED", Headline: "Oferta rechazada"})
	conv.Messages = nil // The offer exists, but its notification has not reached REST history yet.
	makeEnvelope := func(id, kind, token string, payload any) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"c": "inbox." + fakewallapop.UserHash,
			"p": map[string]any{"t": fakewallapop.InboxForwardTT, "r": 41},
			"u": map[string]any{
				"conversation_hash": conv.Hash, "from_user_hash": fakewallapop.OtherHash,
				"to_user_hash": fakewallapop.UserHash, "type": kind,
				"original_time_token": token, "original_channel": fakewallapop.InboxChannel,
			},
			"d": map[string]any{"id": id, "payload": payload},
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	unknownToken := "17895562791081226"
	h.fake.SubscribeMessages = []json.RawMessage{
		makeEnvelope("live-offer", "server-message", fakewallapop.InboxOriginalTT, map[string]any{
			"type": "delivery_generic", "payload": string(fakewallapop.OfferPayload(lastOfferID, "Oferta pendiente")),
		}),
		makeEnvelope("live-unknown", "future-message", unknownToken, map[string]any{"text": "future notification", "extension": map[string]any{"value": 7}}),
	}
	r := h.runUntil("live-unknown", "chat", "open", conv.Hash, "--format", "jsonl")
	if r.code != 0 {
		t.Fatalf("chat open failed: %s", r.stderr)
	}
	messages := map[string]offerMessage{}
	for _, line := range strings.Split(strings.TrimSpace(r.stdout), "\n") {
		var m offerMessage
		decode(t, line, &m)
		if m.ID != "" {
			messages[m.ID] = m
		}
	}
	if len(messages) != 2 {
		t.Fatalf("live messages lost or action envelope leaked: %s", r.stdout)
	}
	m := messages["live-offer"]
	assertOffer(t, m, lastOfferID, "DECLINED", 80, fakewallapop.OtherHash)
	if m.TimeToken != fakewallapop.InboxOriginalTT {
		t.Fatalf("offer lost original token: %+v", m)
	}
	unknown := messages["live-unknown"]
	if unknown.Type != "future-message" || unknown.TimeToken != unknownToken || !strings.Contains(string(unknown.Payload), "extension") {
		t.Fatalf("unknown message lost type, identity or payload: %+v", unknown)
	}
	for _, token := range []string{fakewallapop.InboxOriginalTT, unknownToken} {
		seen := false
		for _, action := range h.fake.Actions {
			path, _ := action["path"].(string)
			if action["type"] == "seen" && strings.HasSuffix(path, "/message/"+token) && strings.Contains(path, url.PathEscape(fakewallapop.InboxChannel)) {
				seen = true
			}
		}
		if !seen {
			t.Fatalf("missing original-channel read receipt for %s: %v", token, h.fake.Actions)
		}
	}
}

func TestChatOpenShowsRecordedOfferEventsWithoutInventingReceipts(t *testing.T) {
	h := newHarness(t)
	h.login()
	conv := offerConversation(h, true, 1)
	const id = "11111111-1111-4111-8111-111111111111"
	h.fake.AddOffer(fakewallapop.Offer{ID: id, ItemHash: conv.Item, Amount: 0.85, Status: "DECLINED", Headline: "Oferta rechazada"})
	conv.Messages = nil
	if err := json.Unmarshal(fakewallapop.RecordedOfferEvents, &h.fake.SubscribeMessages); err != nil {
		t.Fatal(err)
	}
	r := h.runUntil("3639f29d-e208-46f8-ada4-9e734a1e9f66", "chat", "open", conv.Hash, "--format", "jsonl")
	if r.code != 0 {
		t.Fatalf("chat open failed: %s", r.stderr)
	}
	var messages []offerMessage
	for _, line := range strings.Split(strings.TrimSpace(r.stdout), "\n") {
		var m offerMessage
		decode(t, line, &m)
		messages = append(messages, m)
	}
	if len(messages) != 2 {
		t.Fatalf("recorded messages lost or action envelope leaked: %s", r.stdout)
	}
	for i, token := range []string{"17895932255515902", "17895932456735108"} {
		m := messages[i]
		assertOffer(t, m, id, "DECLINED", 0.85, fakewallapop.OtherHash)
		if m.Type != "server-message" || m.TimeToken != token || m.Text == "" {
			t.Fatalf("recorded server message lost type, envelope token or text: %+v", m)
		}
	}
	if len(h.fake.Actions) != 0 {
		t.Fatalf("server messages without original channels must not invent read receipts: %v", h.fake.Actions)
	}
}
