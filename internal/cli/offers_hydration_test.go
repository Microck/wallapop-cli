package cli_test

import (
	"testing"

	"github.com/Microck/wallapop-cli/internal/fakewallapop"
)

// Offer status cards are read per offer, so only the commands that display
// them may fetch them. A write command (send, decline) resolving its
// conversation must not pay for, or depend on, those reads.
func TestChatWriteCommandsDoNotHydrateOffers(t *testing.T) {
	h := newHarness(t)
	h.login()
	offerConversation(h, false, 1.01)
	h.fake.AddOffer(fakewallapop.Offer{ID: lastOfferID, ItemHash: "offeritem001", Amount: 0.9})

	h.must("", "chat", "send", "convhash0001", "hola")
	if got := len(h.fake.RequestsUnder("/bff/delivery/offer-details")); got != 0 {
		t.Fatalf("chat send fetched %d offer status cards", got)
	}
}
