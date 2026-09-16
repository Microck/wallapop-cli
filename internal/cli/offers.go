package cli

import (
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/Microck/wallapop-cli/internal/output"
	"github.com/Microck/wallapop-cli/internal/wallapop"
	"github.com/spf13/cobra"
)

var offerAmountRE = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,2})?$`)

type offerView struct {
	Conversation string         `json:"conversation"`
	Offer        wallapop.Offer `json:"offer"`
}

func (v offerView) Pretty(w io.Writer, color bool) {
	fmt.Fprintf(w, "%s: offer %.2f %s (%s)\n", v.Conversation, v.Offer.Amount, v.Offer.Currency, strings.ToLower(v.Offer.Status))
}

func (a *App) chatOfferCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "offer CONV AMOUNT",
		Short: "Send a price offer, or decline an incoming offer",
		Long: `Send a buyer offer in the item's currency. AMOUNT is a positive decimal
with at most two fractional digits. Wallapop's daily cap and minimum price
are checked before sending. Decline the latest incoming offer with:
  wallapop chat offer decline CONV
Accepting is not supported: it belongs to Wallapop's delivery checkout flow.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !offerAmountRE.MatchString(args[1]) {
				return output.Usagef("AMOUNT must be a positive decimal with at most two fractional digits")
			}
			amount, err := strconv.ParseFloat(args[1], 64)
			if err != nil || math.IsInf(amount, 0) || amount <= 0 {
				return output.Usagef("AMOUNT must be a positive decimal with at most two fractional digits")
			}
			if err := a.requireSession(); err != nil {
				return err
			}
			ctx := cmd.Context()
			conv, err := a.resolveConversation(ctx, args[0])
			if err != nil {
				return err
			}
			if conv.Item.IsMine {
				return output.Usagef("you cannot send a buyer offer on your own item")
			}
			terms, err := a.Client.OfferTerms(ctx, conv.Item.Hash)
			if err != nil {
				return err
			}
			if terms.Remaining <= 0 {
				return output.Usagef("you have used today's %d offers; try again tomorrow", terms.MaxPerDay)
			}
			if amount < terms.MinAmount {
				return output.Usagef("the minimum offer on this item is %.2f %s", terms.MinAmount, terms.Currency)
			}
			if amount > terms.Price {
				return output.Usagef("an offer cannot exceed the asking price of %.2f %s", terms.Price, terms.Currency)
			}
			id, err := a.Client.SendOffer(ctx, conv.Item.Hash, amount, terms.Currency)
			if err != nil {
				return err
			}
			return a.Printer.Print(offerView{Conversation: conv.Hash, Offer: wallapop.Offer{ID: id, Amount: amount, Currency: terms.Currency, Status: "PENDING", ItemHash: conv.Item.Hash}})
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use: "decline CONV", Short: "Decline the latest incoming price offer", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireSession(); err != nil {
				return err
			}
			ctx := cmd.Context()
			conv, err := a.resolveConversation(ctx, args[0])
			if err != nil {
				return err
			}
			if !conv.Item.IsMine {
				return output.Usagef("only the seller can decline an incoming buyer offer")
			}
			messages, from := conv.Messages, conv.NextFrom
			seenPages := make(map[string]bool)
			for {
				for i := len(messages) - 1; i >= 0; i-- {
					if messages[i].OfferID == "" {
						continue
					}
					// Refresh immediately before the write; never decline an older
					// pending offer when the newest offer has already been resolved.
					offer, err := a.Client.Offer(ctx, messages[i].OfferID)
					if err != nil {
						return err
					}
					if offer.ItemHash != conv.Item.Hash || offer.Buyer != conv.WithUser.Hash {
						return output.Usagef("the latest offer does not belong to this item and buyer; nothing was declined")
					}
					if offer.Status != "PENDING" {
						return output.Usagef("the latest offer is %s, not pending", strings.ToLower(offer.Status))
					}
					if err := a.Client.DeclineOffer(ctx, offer.ID); err != nil {
						return err
					}
					offer.Status = "DECLINED"
					offer.Headline = ""
					return a.Printer.Print(offerView{Conversation: conv.Hash, Offer: offer})
				}
				if from == "" || seenPages[from] {
					return output.Usagef("this conversation has no offer to decline")
				}
				seenPages[from] = true
				messages, from, err = a.Client.OlderMessages(ctx, conv.Hash, from, 30)
				if err != nil {
					return err
				}
			}
		},
	})
	return cmd
}
