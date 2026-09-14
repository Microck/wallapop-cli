package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Microck/wallapop-cli/internal/output"
	"github.com/Microck/wallapop-cli/internal/wallapop"
)

func (a *App) chatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Read and send Wallapop messages",
		Long: `Read and send Wallapop messages.

CONV is a conversation hash, a unique prefix of one, or the hash of an item you
already have a conversation about.

Examples:
  wallapop chat list --unread --format pretty
  wallapop chat show 8f1c2 --format pretty
  wallapop chat send 8f1c2 "Sigue disponible?"
  wallapop chat start k2j3h4g5f6d7 "Hola, lo recogería hoy"
  echo "Te lo dejo en 200" | wallapop chat send 8f1c2 -
  wallapop chat open 8f1c2`,
	}
	cmd.AddCommand(a.chatListCmd(), a.chatShowCmd(), a.chatSendCmd(), a.chatStartCmd(), a.chatOpenCmd(), a.chatArchiveCmd())
	return cmd
}

// resolveConversation finds a conversation by hash, prefix or item hash. Full
// hashes are tried directly first; everything else scans the first inbox pages.
func (a *App) resolveConversation(ctx context.Context, ref string) (wallapop.Conversation, error) {
	if wallapop.IsHash(ref) {
		conv, err := a.Client.Conversation(ctx, ref)
		if err == nil {
			return conv, nil
		}
		var e *wallapop.Error
		if !errors.As(err, &e) || e.Kind != wallapop.KindNotFound {
			return wallapop.Conversation{}, err
		}
	}
	var matches []wallapop.Conversation
	from := ""
	for page := 0; page < 3; page++ {
		inbox, err := a.Client.Inbox(ctx, false, 30, 1, from)
		if err != nil {
			return wallapop.Conversation{}, err
		}
		for _, c := range inbox.Conversations {
			if strings.HasPrefix(c.Hash, ref) || c.Item.Hash == ref {
				matches = append(matches, c)
			}
		}
		if inbox.NextFrom == "" || len(matches) > 0 {
			break
		}
		from = inbox.NextFrom
	}
	switch len(matches) {
	case 0:
		return wallapop.Conversation{}, wallapop.NotFound("no conversation matches %q. See `wallapop chat list`", ref)
	case 1:
		return a.Client.Conversation(ctx, matches[0].Hash)
	}
	hashes := make([]string, 0, len(matches))
	for _, m := range matches {
		hashes = append(hashes, m.Hash)
	}
	return wallapop.Conversation{}, output.Usagef("%q matches several conversations: %s", ref, strings.Join(hashes, ", "))
}

type inboxView wallapop.Inbox

func (v inboxView) Pretty(w io.Writer, color bool) {
	rows := make([][]string, 0, len(v.Conversations))
	for _, c := range v.Conversations {
		last, when := "", ""
		if n := len(c.Messages); n > 0 {
			m := c.Messages[n-1]
			last = output.Truncate(m.Text, 40)
			if m.FromSelf {
				last = "me: " + last
			}
			when = relTime(m.At)
		}
		unread := ""
		if c.Unread > 0 {
			unread = fmt.Sprintf("%d", c.Unread)
		}
		rows = append(rows, []string{c.Hash, unread, output.Truncate(c.WithUser.Name, 16), output.Truncate(c.Item.Title, 28), fmt.Sprintf("%.0f", c.Item.Price), last, output.Dim(when, color)})
	}
	output.Table(w, color, []string{"CONV", "NEW", "WITH", "ITEM", "PRICE", "LAST", "WHEN"}, rows)
}

func relTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return t.Format("2006-01-02")
}

func (a *App) chatListCmd() *cobra.Command {
	var unread, archived bool
	var limit int
	cmd := &cobra.Command{
		Use: "list", Short: "List conversations, most recent first", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			inbox, err := a.Client.Inbox(cmd.Context(), archived, limit, 1, "")
			if err != nil {
				return err
			}
			if unread {
				kept := inbox.Conversations[:0]
				for _, c := range inbox.Conversations {
					if c.Unread > 0 {
						kept = append(kept, c)
					}
				}
				inbox.Conversations = kept
			}
			return a.Printer.Print(inboxView(inbox))
		},
	}
	cmd.Flags().BoolVar(&unread, "unread", false, "only conversations with unread messages")
	cmd.Flags().BoolVar(&archived, "archived", false, "list archived conversations instead")
	cmd.Flags().IntVar(&limit, "limit", 30, "how many conversations")
	return cmd
}

type convView wallapop.Conversation

func (v convView) Pretty(w io.Writer, color bool) {
	c := wallapop.Conversation(v)
	fmt.Fprintf(w, "%s  with %s  about %s (%.0f %s)\n", c.Hash, c.WithUser.Name, c.Item.Title, c.Item.Price, c.Item.Currency)
	if c.Item.URL != "" {
		fmt.Fprintln(w, output.Dim(c.Item.URL, color))
	}
	fmt.Fprintln(w)
	for _, m := range c.Messages {
		printMessage(w, color, m, c.WithUser.Name)
	}
}

func printMessage(w io.Writer, color bool, m wallapop.Message, other string) {
	who := other
	if m.FromSelf {
		who = "me"
	}
	if m.Type == "server-message" {
		who = "wallapop"
	}
	fmt.Fprintf(w, "%s %s: %s\n", output.Dim(m.At.Local().Format("01-02 15:04"), color), who, m.Text)
}

func (a *App) chatShowCmd() *cobra.Command {
	var limit int
	var noMarkRead bool
	cmd := &cobra.Command{
		Use: "show CONV", Short: "Show a conversation's messages, oldest first", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			ctx := cmd.Context()
			conv, err := a.resolveConversation(ctx, args[0])
			if err != nil {
				return err
			}
			// The BFF embeds the newest 30; page backwards until limit is met.
			from := conv.NextFrom
			for len(conv.Messages) < limit && from != "" {
				older, next, err := a.Client.OlderMessages(ctx, conv.Hash, from, 30)
				if err != nil {
					return err
				}
				if len(older) == 0 {
					break
				}
				conv.Messages = append(older, conv.Messages...)
				from = next
			}
			if len(conv.Messages) > limit {
				conv.Messages = conv.Messages[len(conv.Messages)-limit:]
			}
			if conv.Unread > 0 && !noMarkRead {
				if err := a.markRead(ctx, conv); err != nil {
					fmt.Fprintf(a.Stderr, "wallapop: could not mark as read: %v\n", err)
				}
			}
			return a.Printer.Print(convView(conv))
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 30, "how many messages to show")
	cmd.Flags().BoolVar(&noMarkRead, "no-mark-read", false, "do not mark the conversation as read")
	return cmd
}

func (a *App) chat(ctx context.Context) (*wallapop.Chat, error) {
	hash, err := a.userHash(ctx)
	if err != nil {
		return nil, err
	}
	return a.Client.NewChat(ctx, hash)
}

func (a *App) markRead(ctx context.Context, conv wallapop.Conversation) error {
	ch, err := a.chat(ctx)
	if err != nil {
		return err
	}
	return ch.MarkRead(ctx, conv)
}

type sentView struct {
	Conversation string `json:"conversation"`
	MessageID    string `json:"message_id"`
	Text         string `json:"text"`
	To           string `json:"to"`
}

func (s sentView) Pretty(w io.Writer, color bool) {
	fmt.Fprintf(w, "sent to %s in %s\n", s.To, s.Conversation)
}

func (a *App) chatSendCmd() *cobra.Command {
	return &cobra.Command{
		Use: "send CONV TEXT", Short: "Send a message into an existing conversation (TEXT - reads stdin)", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			text, err := a.readArgOrStdin(args[1])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			conv, err := a.resolveConversation(ctx, args[0])
			if err != nil {
				return err
			}
			ch, err := a.chat(ctx)
			if err != nil {
				return err
			}
			id, err := ch.Send(ctx, conv, text)
			if err != nil {
				return err
			}
			return a.Printer.Print(sentView{Conversation: conv.Hash, MessageID: id, Text: text, To: conv.WithUser.Name})
		},
	}
}

func (a *App) chatStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start ITEM TEXT",
		Short: "Message an item's seller, opening the conversation if needed",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			text, err := a.readArgOrStdin(args[1])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			itemHash, err := a.Client.ResolveItemHash(ctx, args[0])
			if err != nil {
				return err
			}
			// Reuse an existing conversation about this item rather than opening a second one.
			conv, err := a.resolveConversation(ctx, itemHash)
			if err != nil {
				var e *wallapop.Error
				if !errors.As(err, &e) || e.Kind != wallapop.KindNotFound {
					return err
				}
				created, err := a.Client.CreateConversation(ctx, itemHash)
				if err != nil {
					return err
				}
				conv, err = a.Client.Conversation(ctx, created.Hash)
				if err != nil {
					return err
				}
			}
			ch, err := a.chat(ctx)
			if err != nil {
				return err
			}
			id, err := ch.Send(ctx, conv, text)
			if err != nil {
				return err
			}
			return a.Printer.Print(sentView{Conversation: conv.Hash, MessageID: id, Text: text, To: conv.WithUser.Name})
		},
	}
}

func (a *App) chatOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open CONV",
		Short: "Line-mode chat: streams incoming messages, sends what you type",
		Long: `Line-mode chat for one conversation. Prints the recent history, then streams
incoming messages while each line you type is sent. /quit or Ctrl-D exits.
With --format jsonl, incoming messages are printed as JSON objects instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			conv, err := a.resolveConversation(ctx, args[0])
			if err != nil {
				return err
			}
			ch, err := a.chat(ctx)
			if err != nil {
				return err
			}
			jsonl := a.Printer.Format == output.JSONL
			if !jsonl {
				convView(conv).Pretty(a.Stdout, a.Printer.Color)
				fmt.Fprintln(a.Stderr, output.Dim("type a message and press enter; /quit to leave", a.Printer.Color))
			}
			if conv.Unread > 0 {
				_ = ch.MarkRead(ctx, conv)
			}
			subErr := make(chan error, 1)
			go func() {
				subErr <- ch.Subscribe(ctx, func(in wallapop.Incoming) {
					if in.Conversation != conv.Hash || in.FromSelf {
						return
					}
					if jsonl {
						_ = a.Printer.Print(in.Message)
						return
					}
					printMessage(a.Stdout, a.Printer.Color, in.Message, conv.WithUser.Name)
				})
			}()
			lines := make(chan string)
			go func() {
				sc := bufio.NewScanner(a.Stdin)
				for sc.Scan() {
					lines <- sc.Text()
				}
				close(lines)
			}()
			for {
				select {
				case err := <-subErr:
					if ctx.Err() != nil {
						return nil
					}
					return err
				case line, ok := <-lines:
					if !ok || strings.TrimSpace(line) == "/quit" {
						return nil
					}
					text := strings.TrimSpace(line)
					if text == "" {
						continue
					}
					if _, err := ch.Send(ctx, conv, text); err != nil {
						fmt.Fprintf(a.Stderr, "wallapop: send failed: %v\n", err)
						continue
					}
					if !jsonl {
						printMessage(a.Stdout, a.Printer.Color, wallapop.Message{FromSelf: true, Text: text, At: time.Now()}, conv.WithUser.Name)
					}
				}
			}
		},
	}
}

func (a *App) chatArchiveCmd() *cobra.Command {
	var undo bool
	cmd := &cobra.Command{
		Use: "archive CONV", Short: "Archive a conversation (or --undo)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			ctx := cmd.Context()
			hash := args[0]
			if !wallapop.IsHash(hash) {
				conv, err := a.resolveConversation(ctx, hash)
				if err != nil {
					return err
				}
				hash = conv.Hash
			}
			if err := a.Client.SetArchived(ctx, []string{hash}, !undo); err != nil {
				return err
			}
			action := "archived"
			if undo {
				action = "unarchived"
			}
			return a.Printer.Print(actionResult{Action: action, Item: hash, OK: true})
		},
	}
	cmd.Flags().BoolVar(&undo, "undo", false, "unarchive instead")
	return cmd
}
