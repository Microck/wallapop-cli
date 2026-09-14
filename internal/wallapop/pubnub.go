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
	"time"
)

// PubNub REST, the three calls the web chat makes. The channel strings come
// from Wallapop and are opaque; the CLI never constructs one.

// Chat is a live messaging handle for one account.
type Chat struct {
	client   *Client
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

func (ch *Chat) refreshToken(ctx context.Context) error {
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
	return url.Values{"uuid": {ch.UserHash}, "auth": {ch.token.Token}, "pnsdk": {"wallapop-cli"}}
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
	if err := ch.refreshToken(ctx); err != nil {
		return err
	}
	path := fmt.Sprintf("/v1/message-actions/%s/channel/%s/message/%s", pubNubSubscribeKey, url.PathEscape(conv.Channel), last.TimeToken)
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
}

// Subscribe long-polls the account's inbox channel and calls fn for each text
// message until ctx is cancelled. Messages the account sent from another
// device also arrive here; fn decides what to show.
func (ch *Chat) Subscribe(ctx context.Context, fn func(Incoming)) error {
	channel := "inbox." + ch.UserHash
	timetoken, region := "0", ""
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
				C string          `json:"c"`
				D json.RawMessage `json:"d"`
				U json.RawMessage `json:"u"`
				P struct {
					T string `json:"t"`
				} `json:"p"`
			} `json:"m"`
		}
		// PubNub holds the request up to ~280 s; the client timeout must allow that.
		sub := *ch.client.HTTP
		sub.Timeout = 5 * time.Minute
		saved := ch.client.HTTP
		ch.client.HTTP = &sub
		_, err := ch.client.do(ctx, request{method: http.MethodGet, base: ch.client.PubNubBase,
			path: fmt.Sprintf("/v2/subscribe/%s/%s/0", pubNubSubscribeKey, url.PathEscape(channel)), query: q}, &out)
		ch.client.HTTP = saved
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
				ID      string `json:"id"`
				Payload struct {
					Text string `json:"text"`
				} `json:"payload"`
			}
			var u struct {
				Type   string `json:"type"`
				From   string `json:"from_user_hash"`
				To     string `json:"to_user_hash"`
				Conv   string `json:"conversation_hash"`
				Status string `json:"status"`
			}
			_ = json.Unmarshal(m.D, &d)
			_ = json.Unmarshal(m.U, &u)
			if u.Type != "text" && u.Type != "server-message" {
				continue
			}
			at := time.Now()
			if len(m.P.T) > 7 {
				if ns, err := parseTimetoken(m.P.T); err == nil {
					at = ns
				}
			}
			fn(Incoming{
				Message:  Message{ID: d.ID, FromSelf: u.From == ch.UserHash, Text: d.Payload.Text, At: at, Type: u.Type, TimeToken: m.P.T, Conversation: u.Conv},
				FromUser: u.From,
				ToUser:   u.To,
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
