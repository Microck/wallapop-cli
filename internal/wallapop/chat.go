package wallapop

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Chat REST surface. Reads are the messaging BFF; writes are the
// instant-messaging service; message delivery itself is PubNub (pubnub.go).

type rawConversation struct {
	Hash     string `json:"hash"`
	WithUser struct {
		Hash          string  `json:"hash"`
		Name          string  `json:"name"`
		Slug          string  `json:"slug"`
		Available     bool    `json:"available"`
		Blocked       bool    `json:"blocked"`
		RatingAverage float64 `json:"rating_average"`
		ReviewsCount  int     `json:"reviews_count"`
	} `json:"with_user"`
	Item struct {
		Hash   string `json:"hash"`
		Title  string `json:"title"`
		Status string `json:"status"`
		Price  money  `json:"price"`
		Slug   string `json:"slug"`
		IsMine bool   `json:"is_mine"`
	} `json:"item"`
	Messages struct {
		Messages []rawMessage `json:"messages"`
		NextFrom string       `json:"next_from"`
	} `json:"messages"`
	UnreadMessages int    `json:"unread_messages"`
	Channel        string `json:"channel"`
}

type rawMessage struct {
	ID        string `json:"id"`
	FromSelf  bool   `json:"from_self"`
	Text      string `json:"text"`
	Timestamp msTime `json:"timestamp"`
	Status    string `json:"status"`
	Type      string `json:"type"`
	TimeToken string `json:"time_token"`
}

func (m rawMessage) normalize() Message {
	return Message{ID: m.ID, FromSelf: m.FromSelf, Text: m.Text, At: m.Timestamp.Time, Status: m.Status, Type: m.Type, TimeToken: m.TimeToken}
}

func (c *Client) normalizeConversation(r rawConversation) Conversation {
	conv := Conversation{
		Hash: r.Hash,
		WithUser: ChatUser{Hash: r.WithUser.Hash, Name: r.WithUser.Name, Slug: r.WithUser.Slug, Available: r.WithUser.Available,
			Blocked: r.WithUser.Blocked, Rating: r.WithUser.RatingAverage, Reviews: r.WithUser.ReviewsCount},
		Item: ChatItem{Hash: r.Item.Hash, Title: r.Item.Title, Status: r.Item.Status, Price: r.Item.Price.Amount,
			Currency: r.Item.Price.Currency, IsMine: r.Item.IsMine, URL: c.itemURL(r.Item.Slug)},
		NextFrom: r.Messages.NextFrom,
		Unread:   r.UnreadMessages,
		Channel:  r.Channel,
	}
	// The BFF returns newest first; the CLI shows oldest first everywhere.
	for i := len(r.Messages.Messages) - 1; i >= 0; i-- {
		conv.Messages = append(conv.Messages, r.Messages.Messages[i].normalize())
	}
	return conv
}

type inboxResponse struct {
	UserHash       string            `json:"user_hash"`
	UnreadMessages int               `json:"unread_messages"`
	Conversations  []rawConversation `json:"conversations"`
	NextFrom       string            `json:"next_from"`
}

// Inbox is one page of conversations plus the caller's own user hash, which
// PubNub needs as the publisher uuid.
type Inbox struct {
	UserHash      string         `json:"user_hash"`
	Unread        int            `json:"unread"`
	Conversations []Conversation `json:"conversations"`
	NextFrom      string         `json:"next_from,omitempty"`
}

// Inbox lists conversations, newest activity first. maxMessages controls how
// many recent messages come embedded per conversation (the web uses 30).
func (c *Client) Inbox(ctx context.Context, archived bool, pageSize, maxMessages int, from string) (Inbox, error) {
	path := "/bff/messaging/inbox"
	if archived {
		path = "/bff/messaging/archived"
	}
	q := url.Values{"page_size": {strconv.Itoa(pageSize)}, "max_messages": {strconv.Itoa(maxMessages)}}
	if from != "" {
		q.Set("from", from)
	}
	var raw inboxResponse
	if err := c.getJSON(ctx, path, q, true, &raw); err != nil {
		return Inbox{}, err
	}
	if raw.UserHash == "" {
		return Inbox{}, &Error{Kind: KindAPIChanged, Endpoint: "GET " + path, Msg: "inbox came back without the user hash"}
	}
	out := Inbox{UserHash: raw.UserHash, Unread: raw.UnreadMessages, NextFrom: raw.NextFrom}
	for _, r := range raw.Conversations {
		conv := c.normalizeConversation(r)
		conv.Archived = archived
		out.Conversations = append(out.Conversations, conv)
	}
	return out, nil
}

// Conversation fetches one conversation with its 30 most recent messages.
func (c *Client) Conversation(ctx context.Context, hash string) (Conversation, error) {
	var raw rawConversation
	if err := c.getJSON(ctx, "/bff/messaging/conversation/"+hash, nil, true, &raw); err != nil {
		return Conversation{}, err
	}
	if raw.Hash == "" {
		return Conversation{}, &Error{Kind: KindAPIChanged, Endpoint: "GET /bff/messaging/conversation/{hash}", Msg: "conversation came back without a hash"}
	}
	return c.normalizeConversation(raw), nil
}

// OlderMessages pages backwards from a conversation's next_from cursor.
// Returned oldest first, with the cursor for the page before it.
func (c *Client) OlderMessages(ctx context.Context, hash, from string, max int) ([]Message, string, error) {
	q := url.Values{"max_messages": {strconv.Itoa(max)}}
	if from != "" {
		q.Set("from", from)
	}
	var raw struct {
		Messages []rawMessage `json:"messages"`
		NextFrom string       `json:"next_from"`
	}
	if err := c.getJSON(ctx, "/api/v3/instant-messaging/archive/conversation/"+hash+"/messages", q, true, &raw); err != nil {
		return nil, "", err
	}
	out := make([]Message, 0, len(raw.Messages))
	for i := len(raw.Messages) - 1; i >= 0; i-- {
		out = append(out, raw.Messages[i].normalize())
	}
	return out, raw.NextFrom, nil
}

// NewConversation is what the web does when you message a seller for the first
// time. Wallapop caps how many new conversations an account can open; that
// comes back as API code 100.
type NewConversation struct {
	Hash    string `json:"conversation_id"`
	Item    string `json:"item_id"`
	Other   string `json:"other_user_id"`
	Channel string `json:"channel"`
}

func (c *Client) CreateConversation(ctx context.Context, itemHash string) (NewConversation, error) {
	var out NewConversation
	_, err := c.do(ctx, request{method: http.MethodPost, base: c.APIBase, path: "/api/v3/instant-messaging/conversation",
		body: map[string]string{"item_hash_id": itemHash}, auth: true}, &out)
	if err != nil {
		var e *Error
		if asError(err, &e) && e.APICode == 100 {
			e.Kind = KindBlocked
			e.Msg = "wallapop is not letting this account open more new conversations right now. Try again later"
		}
		return NewConversation{}, err
	}
	if out.Hash == "" {
		return NewConversation{}, &Error{Kind: KindAPIChanged, Endpoint: "POST /api/v3/instant-messaging/conversation", Msg: "conversation was created without an id"}
	}
	return out, nil
}

// SetArchived archives or unarchives conversations. Wallapop answers 409 when
// the conversation is already in that state; that counts as done.
func (c *Client) SetArchived(ctx context.Context, hashes []string, archived bool) error {
	path := "/api/v3/instant-messaging/conversations/archive"
	if !archived {
		path = "/api/v3/instant-messaging/conversations/unarchive"
	}
	_, err := c.do(ctx, request{method: http.MethodPut, base: c.APIBase, path: path,
		body: map[string][]string{"conversation_ids": hashes}, auth: true, acceptStatus: []int{http.StatusConflict}}, nil)
	return err
}

// UnreadCount is the badge number the web shows.
func (c *Client) UnreadCount(ctx context.Context) (int, error) {
	var raw struct {
		UnreadCounter int `json:"unread_counter"`
	}
	err := c.getJSON(ctx, "/api/v3/instant-messaging/messages/unread", nil, true, &raw)
	return raw.UnreadCounter, err
}

// ChatToken is a PubNub Access Manager token scoped to the account's channels.
type ChatToken struct {
	Token    string
	Channels []string
	// Expires is a conservative local guess; the PAM token carries its own TTL
	// but decoding CBOR just for that is not worth a dependency.
	Expires time.Time
}

func (c *Client) ChatToken(ctx context.Context) (ChatToken, error) {
	var raw struct {
		Token    string   `json:"token"`
		Channels []string `json:"channels"`
	}
	if err := c.getJSON(ctx, "/api/v3/instant-messaging/token", nil, true, &raw); err != nil {
		return ChatToken{}, err
	}
	if raw.Token == "" {
		return ChatToken{}, &Error{Kind: KindAPIChanged, Endpoint: "GET /api/v3/instant-messaging/token", Msg: "chat token came back empty"}
	}
	c.Redact(raw.Token)
	return ChatToken{Token: raw.Token, Channels: raw.Channels, Expires: time.Now().Add(10 * time.Minute)}, nil
}
