package wallapop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// PubNub REST, the three calls the web chat makes. The channel strings come
// from Wallapop and are opaque; the CLI never constructs one.

// Chat is a live messaging handle for one account. It is safe to use from
// several goroutines: `chat open` subscribes on one, publishes typed lines on
// another and posts receipts on a third, and all three refresh the same token.
type Chat struct {
	client *Client
	// mu guards token, which every call refreshes and reads.
	mu       sync.Mutex
	token    ChatToken
	UserHash string
}

// NewChat fetches a PubNub token for the logged-in account.
func (c *Client) NewChat(ctx context.Context, userHash string) (*Chat, error) {
	tok, err := c.ChatToken(ctx)
	if err != nil {
		return nil, err
	}
	return &Chat{client: c, token: tok, UserHash: userHash}, nil
}

// refreshToken fetches a new token when the current one is close to expiring.
// The lock is held across the fetch so two callers cannot both decide the token
// is stale and race each other to replace it.
func (ch *Chat) refreshToken(ctx context.Context) error {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if time.Until(ch.token.Expires) > time.Minute {
		return nil
	}
	tok, err := ch.client.ChatToken(ctx)
	if err != nil {
		return err
	}
	ch.token = tok
	return nil
}

func (ch *Chat) baseQuery() url.Values {
	ch.mu.Lock()
	token := ch.token.Token
	ch.mu.Unlock()
	return url.Values{"uuid": {ch.UserHash}, "auth": {token}, "pnsdk": {"wallapop-cli"}}
}

// Send publishes a text message. Payload and meta match the web client
// byte-for-byte in structure, since the receiving apps parse meta.type.
func (ch *Chat) Send(ctx context.Context, conv Conversation, text string) (string, error) {
	if conv.Channel == "" {
		return "", &Error{Kind: KindAPIChanged, Endpoint: "pubnub publish", Msg: "conversation has no channel to publish on"}
	}
	if err := ch.refreshToken(ctx); err != nil {
		return "", err
	}
	id := newUUID()
	message := map[string]any{"id": id, "payload": map[string]string{"text": text}}
	meta := map[string]any{
		"type":              "text",
		"sender":            map[string]any{"platform": map[string]string{"app_version": "web", "os_version": "0"}},
		"to_user_hash":      conv.WithUser.Hash,
		"from_user_hash":    ch.UserHash,
		"conversation_hash": conv.Hash,
	}
	msgJSON, _ := json.Marshal(message)
	metaJSON, _ := json.Marshal(meta)
	q := ch.baseQuery()
	q.Set("meta", string(metaJSON))
	path := fmt.Sprintf("/publish/%s/%s/0/%s/0/%s", pubNubPublishKey, pubNubSubscribeKey, url.PathEscape(conv.Channel), url.PathEscape(string(msgJSON)))
	var out []any
	if _, err := ch.client.do(ctx, request{method: http.MethodGet, base: ch.client.PubNubBase, path: path, query: q}, &out); err != nil {
		return "", pubnubError(err)
	}
	// PubNub answers [1, "Sent", "<timetoken>"].
	if len(out) < 3 {
		return "", &Error{Kind: KindAPIChanged, Endpoint: "GET /publish", Msg: "pubnub publish answered with an unexpected shape"}
	}
	if status, ok := out[0].(float64); !ok || status != 1 {
		return "", &Error{Kind: KindGeneric, Endpoint: "GET /publish", Msg: fmt.Sprintf("pubnub did not accept the message: %v", out[1])}
	}
	return id, nil
}

// MarkRead signals "seen" on the last message, as the web does when you open a
// conversation with unread messages. Only messages from the other side carry a
// time token worth marking.
func (ch *Chat) MarkRead(ctx context.Context, conv Conversation) error {
	var last *Message
	for i := len(conv.Messages) - 1; i >= 0; i-- {
		if !conv.Messages[i].FromSelf && conv.Messages[i].TimeToken != "" {
			last = &conv.Messages[i]
			break
		}
	}
	if last == nil || conv.Channel == "" {
		return nil
	}
	return ch.MarkSeen(ctx, conv.Channel, last.TimeToken)
}

// MarkSeen signals "seen" on one message. Live messages need this as well as
// the conversation-level MarkRead: a session left open receives messages the
// open-time receipt could not have covered, and without it the sender is left
// looking at "received" for as long as the session lasts.
func (ch *Chat) MarkSeen(ctx context.Context, channel, timeToken string) error {
	if channel == "" || timeToken == "" {
		return nil
	}
	if err := ch.refreshToken(ctx); err != nil {
		return err
	}
	path := fmt.Sprintf("/v1/message-actions/%s/channel/%s/message/%s", pubNubSubscribeKey, url.PathEscape(channel), timeToken)
	_, err := ch.client.do(ctx, request{method: http.MethodPost, base: ch.client.PubNubBase, path: path, query: ch.baseQuery(),
		body: map[string]string{"type": "seen", "value": "{}"}, acceptStatus: []int{http.StatusConflict}}, nil)
	return pubnubError(err)
}

// Incoming is one live message as delivered on the inbox channel. The
// conversation hash lives in Message.Conversation.
type Incoming struct {
	Message
	FromUser string
	ToUser   string
	// Channel is the conversation channel the message was published on,
	// which is where a read receipt for it has to go. The inbox channel it
	// arrived on is a forwarding copy and marking that one does nothing.
	Channel string
}

// Subscribe long-polls the account's inbox channel and calls fn for every
// message until ctx is cancelled. Message actions are not messages. Unknown
// message types are retained; fn decides which conversations to show.
func (ch *Chat) Subscribe(ctx context.Context, fn func(Incoming)) error {
	channel := "inbox." + ch.UserHash
	timetoken, region := "0", ""
	// PubNub holds the request up to ~280 s, which no other call wants. This
	// is a separate client rather than a swap of the shared one: `chat open`
	// publishes typed lines while a poll is in flight, and mutating the
	// client under it is a data race.
	poller := *ch.client.HTTP
	poller.Timeout = 5 * time.Minute
	for {
		if err := ch.refreshToken(ctx); err != nil {
			return err
		}
		q := ch.baseQuery()
		q.Set("tt", timetoken)
		if region != "" {
			q.Set("tr", region)
		}
		var out struct {
			T struct {
				T string `json:"t"`
				R int    `json:"r"`
			} `json:"t"`
			M []struct {
				E int             `json:"e"`
				C string          `json:"c"`
				D json.RawMessage `json:"d"`
				U json.RawMessage `json:"u"`
				P struct {
					T string `json:"t"`
				} `json:"p"`
			} `json:"m"`
		}
		_, err := ch.client.do(ctx, request{method: http.MethodGet, base: ch.client.PubNubBase,
			path:  fmt.Sprintf("/v2/subscribe/%s/%s/0", pubNubSubscribeKey, url.PathEscape(channel)),
			query: q, httpClient: &poller}, &out)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return pubnubError(err)
		}
		timetoken = out.T.T
		region = fmt.Sprint(out.T.R)
		for _, m := range out.M {
			var d struct {
				ID      string          `json:"id"`
				Payload json.RawMessage `json:"payload"`
			}
			var u struct {
				Type   string `json:"type"`
				From   string `json:"from_user_hash"`
				To     string `json:"to_user_hash"`
				Conv   string `json:"conversation_hash"`
				Status string `json:"status"`
				// Recorded live 2026-09-16: the inbox copy carries the
				// message's identity on its own conversation channel. Every
				// other part of Wallapop, the REST message list and the read
				// receipts included, keys off these and not off the inbox
				// envelope's own timetoken.
				OriginalTimeToken string `json:"original_time_token"`
				OriginalChannel   string `json:"original_channel"`
			}
			_ = json.Unmarshal(m.D, &d)
			_ = json.Unmarshal(m.U, &u)
			if m.E != 0 || u.Conv == "" {
				continue
			}
			// The inbox envelope's timetoken is when Wallapop forwarded the
			// copy, a few milliseconds after the message was published. The
			// original is the one that identifies the message everywhere else.
			timeToken := firstNonEmpty(u.OriginalTimeToken, m.P.T)
			at := time.Now()
			if len(timeToken) > 7 {
				if ns, err := parseTimetoken(timeToken); err == nil {
					at = ns
				}
			}
			msg := Message{ID: d.ID, FromSelf: u.From == ch.UserHash, At: at, Type: u.Type, Status: u.Status, TimeToken: timeToken, Conversation: u.Conv}
			msg.decodePayload(d.Payload)
			fn(Incoming{
				Message:  msg,
				FromUser: u.From,
				ToUser:   u.To,
				Channel:  u.OriginalChannel,
			})
		}
	}
}

// parseTimetoken converts PubNub's 17-digit 100 ns timetoken to a time.
func parseTimetoken(tt string) (time.Time, error) {
	var n int64
	if _, err := fmt.Sscan(tt, &n); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, n*100).UTC(), nil
}

// pubnubError rewrites PubNub's 403 (token rejected) into an auth error rather
// than the "blocked" the API classifier assumes for the Wallapop host.
func pubnubError(err error) error {
	var e *Error
	if asError(err, &e) && e.Status == http.StatusForbidden {
		e.Kind = KindAuth
		e.Msg = "pubnub rejected the chat token. Run the command again; if it keeps failing, log in again"
	}
	return err
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return strings.Join([]string{h[0:8], h[8:12], h[12:16], h[16:20], h[20:]}, "-")
}
