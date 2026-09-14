// Package watch runs one Check for a Watch: fetch the target's current state,
// diff it against what the store already has, and produce Events. It does not
// deliver or persist anything; the CLI layer sequences sinks then CommitCheck.
package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Microck/wallapop-cli/internal/store"
	"github.com/Microck/wallapop-cli/internal/wallapop"
)

// Result is everything a Check produced, ready for delivery and commit.
type Result struct {
	Events  []store.Event
	Upserts []store.Seen
	Removed []string
}

// Target payloads stored as JSON in the watch row.
type SearchTarget struct {
	Params wallapop.SearchParams `json:"params"`
}

type ItemTarget struct {
	Hash string `json:"hash"`
	URL  string `json:"url,omitempty"`
}

type SellerTarget struct {
	Hash string `json:"hash"`
	Name string `json:"name,omitempty"`
}

// Check runs one check. emitInitial makes the first run report every item as
// new instead of silently recording a baseline.
func Check(ctx context.Context, c *wallapop.Client, st *store.Store, w store.Watch, emitInitial bool) (Result, error) {
	seen, err := st.SeenFor(ctx, w.ID)
	if err != nil {
		return Result{}, err
	}
	baseline := !w.Baselined && !emitInitial
	switch w.Kind {
	case store.KindSearch:
		var t SearchTarget
		if err := json.Unmarshal(w.Target, &t); err != nil {
			return Result{}, fmt.Errorf("watch %s has a corrupt target: %w", w.Name, err)
		}
		return checkSearch(ctx, c, w, t, seen, baseline)
	case store.KindItem:
		var t ItemTarget
		if err := json.Unmarshal(w.Target, &t); err != nil {
			return Result{}, fmt.Errorf("watch %s has a corrupt target: %w", w.Name, err)
		}
		return checkItem(ctx, c, w, t, seen, baseline)
	case store.KindSeller:
		var t SellerTarget
		if err := json.Unmarshal(w.Target, &t); err != nil {
			return Result{}, fmt.Errorf("watch %s has a corrupt target: %w", w.Name, err)
		}
		return checkSeller(ctx, c, w, t, seen, baseline)
	}
	return Result{}, fmt.Errorf("watch %s has unknown kind %q", w.Name, w.Kind)
}

func event(w store.Watch, typ string, it wallapop.Item, change any) store.Event {
	ev := store.Event{Type: typ, Watch: w.Name, Profile: w.Profile, At: time.Now().UTC()}
	ev.Item, _ = json.Marshal(it)
	if change != nil {
		ev.Change, _ = json.Marshal(change)
	}
	return ev
}

func seenOf(it wallapop.Item) store.Seen {
	snap, _ := json.Marshal(it)
	return store.Seen{Hash: it.Hash, Price: it.Price, Reserved: it.Reserved, Sold: it.Sold, Title: it.Title, Modified: it.ModifiedAt, Snapshot: snap}
}

type priceChange struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
}

// diffKnown produces the events for an item the watch has seen before.
// Sold is only trusted when the source reports it (item pages do, search does not).
func diffKnown(w store.Watch, prev store.Seen, it wallapop.Item, trustSold bool) []store.Event {
	var evs []store.Event
	if it.Price != prev.Price {
		evs = append(evs, event(w, "item.price_changed", it, priceChange{From: prev.Price, To: it.Price}))
	}
	if it.Reserved && !prev.Reserved {
		evs = append(evs, event(w, "item.reserved", it, nil))
	}
	if !it.Reserved && prev.Reserved {
		evs = append(evs, event(w, "item.unreserved", it, nil))
	}
	if trustSold && it.Sold && !prev.Sold {
		evs = append(evs, event(w, "item.sold", it, nil))
	}
	return evs
}

func checkSearch(ctx context.Context, c *wallapop.Client, w store.Watch, t SearchTarget, seen map[string]store.Seen, baseline bool) (Result, error) {
	var res Result
	pages := w.Pages
	if pages < 1 {
		pages = 1
	}
	params := t.Params
	for page := 0; page < pages; page++ {
		sp, err := c.Search(ctx, params)
		if err != nil {
			return Result{}, err
		}
		for _, it := range sp.Items {
			prev, known := seen[it.Hash]
			switch {
			case !known && !baseline:
				res.Events = append(res.Events, event(w, "item.new", it, nil))
			case known:
				res.Events = append(res.Events, diffKnown(w, prev, it, false)...)
			}
			res.Upserts = append(res.Upserts, seenOf(it))
		}
		if sp.NextPage == "" {
			break
		}
		params.NextPage = sp.NextPage
	}
	// Items that fell off the watched pages are not reported: a search watch
	// tracks arrivals and changes, not departures, since page one churns.
	return res, nil
}

func checkItem(ctx context.Context, c *wallapop.Client, w store.Watch, t ItemTarget, seen map[string]store.Seen, baseline bool) (Result, error) {
	var res Result
	ref := t.Hash
	if t.URL != "" {
		ref = t.URL
	}
	it, err := c.Item(ctx, ref)
	if err != nil {
		var e *wallapop.Error
		if errors.As(err, &e) && e.Kind == wallapop.KindNotFound {
			// Gone entirely. Report once, using the last snapshot for the payload.
			if prev, ok := seen[t.Hash]; ok {
				var last wallapop.Item
				_ = json.Unmarshal(prev.Snapshot, &last)
				res.Events = append(res.Events, event(w, "item.removed", last, nil))
				res.Removed = append(res.Removed, t.Hash)
			}
			return res, nil
		}
		return Result{}, err
	}
	prev, known := seen[it.Hash]
	if known {
		res.Events = append(res.Events, diffKnown(w, prev, it, true)...)
		if it.Expired && !isExpired(prev) {
			res.Events = append(res.Events, event(w, "item.removed", it, nil))
		}
		if !it.ModifiedAt.IsZero() && !prev.Modified.IsZero() && it.ModifiedAt.After(prev.Modified) && len(res.Events) == 0 {
			res.Events = append(res.Events, event(w, "item.edited", it, map[string]any{"from": prev.Modified, "to": it.ModifiedAt}))
		}
	} else if !baseline {
		res.Events = append(res.Events, event(w, "item.new", it, nil))
	}
	res.Upserts = append(res.Upserts, seenOf(it))
	return res, nil
}

func isExpired(prev store.Seen) bool {
	var last struct {
		Expired bool `json:"expired"`
	}
	_ = json.Unmarshal(prev.Snapshot, &last)
	return last.Expired
}

func checkSeller(ctx context.Context, c *wallapop.Client, w store.Watch, t SellerTarget, seen map[string]store.Seen, baseline bool) (Result, error) {
	var res Result
	items, err := c.UserItems(ctx, t.Hash)
	if err != nil {
		return Result{}, err
	}
	current := map[string]bool{}
	for _, it := range items {
		current[it.Hash] = true
		prev, known := seen[it.Hash]
		switch {
		case !known && !baseline:
			res.Events = append(res.Events, event(w, "seller.new_item", it, nil))
		case known:
			res.Events = append(res.Events, diffKnown(w, prev, it, true)...)
		}
		res.Upserts = append(res.Upserts, seenOf(it))
	}
	// The seller's list is complete, so a missing hash really is gone.
	for hash, prev := range seen {
		if !current[hash] {
			var last wallapop.Item
			_ = json.Unmarshal(prev.Snapshot, &last)
			res.Events = append(res.Events, event(w, "item.removed", last, nil))
			res.Removed = append(res.Removed, hash)
		}
	}
	return res, nil
}

// Jitter spreads checks so several watches on one timer do not align. Up to
// 10% of the interval, never negative.
func Jitter(interval time.Duration, seed int64) time.Duration {
	if interval <= 0 {
		return 0
	}
	max := int64(interval / 10)
	if max == 0 {
		return 0
	}
	if seed < 0 {
		seed = -seed
	}
	return time.Duration(seed % max)
}
